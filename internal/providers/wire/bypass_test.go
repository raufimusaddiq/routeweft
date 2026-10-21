package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// How a function that reads a provider base URL field (`.BaseURL`,
// `.DefaultBaseURL`) relates to the wire seam.
const (
	// dials: it resolves or uses an upstream base, so its body must call
	// wire.NodeBase, wire.ResolveTarget or wire.DiscoveryBase.
	dials = "dials"
	// sink: it dials the base its caller handed it (the caller is a "dials").
	sink = "sink"
	// config: it stores, validates or shows the value and never dials it.
	config = "config"
)

type baseReader struct{ kind, reason string }

// upstreamBaseReaders are the only non-test functions allowed to read a provider
// base URL field. Every request that reaches an upstream (serving, probe, model
// discovery, route listing) takes its base from wire.NodeBase and its URL from
// wire.ResolveTarget / Target.URL, so a connection-supplied base, a region host, a
// deployment path and a query appear on all of them. A function that reads one of
// these fields and is not listed here is a new place that builds an upstream
// base, and this test fails it; a "dials" entry that stops calling wire fails too.
var upstreamBaseReaders = map[string]baseReader{
	"runtime/providers.go:AccountProvider":           {dials, "serving route: wire.NodeBase"},
	"control/api/providers.go:probeRef":              {dials, "probe/listing route: wire.NodeBase"},
	"control/api/providers.go:testProviderModel":     {dials, "model probe: wire.ResolveTarget then Target.URL"},
	"control/api/workflows.go:handleNodeRoutes":      {dials, "route listing: wire.NodeBase and wire.ResolveTarget"},
	"providers/discovery/lifecycle.go:SyncSpec":      {dials, "catalog sync: wire.NodeBase"},
	"credentials/resolver.go:ResolveForAccount":      {dials, "builds a ProviderRef: wire.NodeBase (no caller outside tests)"},
	"providers/discovery/discovery.go:ModelEntries":  {sink, "dials the base its callers resolved with wire.NodeBase / wire.DiscoveryBase"},
	"routing/plan.go:BuildPlan":                      {config, "requires a base to exist; does not build one"},
	"runtime/providers.go:compileProviders":          {config, "fills a built-in node's stored base from its spec"},
	"credentials/store.go:PutNode":                   {config, "stores the node"},
	"credentials/store.go:GetNode":                   {config, "loads the node"},
	"credentials/store.go:ListNodes":                 {config, "loads nodes"},
	"control/api/providers.go:handlePutProviderNode": {config, "operator edits the node's own base URL"},
	"control/api/providers.go:nodeJSON":              {config, "shows the node"},
	"credentials/resolver.go:node":                   {config, "loads the node row"},
	"control/api/providers.go:builtinNode":           {config, "seeds a built-in node from the catalog"},
	"control/api/read_models.go:providers":           {config, "shows the catalog's default base URL"},
	"control/api/read_models.go:providerNodes":       {config, "shows the node's base URL"},
	"control/api/workflows.go:handleValidateNode":    {config, "validates an operator-supplied base URL against policy"},
	"providers/generic/generic.go:Validate":          {config, "generic node validation"},
	"providers/generic/generic.go:Endpoint":          {config, "generic node's own endpoint; no caller outside its package"},
	"providers/generic/generic.go:DiscoverModels":    {config, "generic node; no caller outside its package"},
}

func callsWire(fn *ast.FuncDecl) bool {
	calls := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "wire" {
				calls = true
			}
		}
		return !calls
	})
	return calls
}

func TestEveryUpstreamBaseComesFromTheWireSeam(t *testing.T) {
	root := filepath.Join("..", "..") // internal/
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "providers/wire/") {
			return nil // the seam itself
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			reads := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "BaseURL" || sel.Sel.Name == "DefaultBaseURL") {
					reads = true
				}
				return !reads
			})
			if reads {
				name := rel + ":" + fn.Name.Name
				found[name] = true
				if entry, listed := upstreamBaseReaders[name]; listed && entry.kind == dials && !callsWire(fn) {
					t.Errorf("%s is listed as dialling but no longer calls the wire seam", name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, allowed := upstreamBaseReaders[name]; !allowed {
			t.Errorf("%s reads a provider base URL but is not on the allowlist: build the upstream URL with wire.NodeBase and wire.ResolveTarget instead, or list it with the reason it never dials", name)
		}
	}
	for name := range upstreamBaseReaders {
		if !found[name] {
			t.Errorf("allowlist entry %s no longer reads a base URL; remove it", name)
		}
	}
}

// The functions that dial an upstream must reach it through the seam even when
// they never name a base URL field themselves, so they are pinned by name too.
var mustCallWire = map[string][]string{
	"ingress/dispatch.go:dispatchAttempts":                 {"ResolveTarget"},
	"ingress/ingress.go:newUpstreamRequest":                {"ResolveTarget", "FinalizeBody"},
	"control/api/providers.go:testProviderModel":           {"ResolveTarget", "FinalizeBody"},
	"control/api/providers.go:fetchConnectionModelEntries": {"DiscoveryBase"},
	"control/api/workflows.go:handleNodeRoutes":            {"ResolveTarget", "NodeBase"},
	"runtime/providers.go:AccountProvider":                 {"NodeBase"},
}

func TestDialingFunctionsCallTheWireSeam(t *testing.T) {
	root := filepath.Join("..", "..")
	for name, wanted := range mustCallWire {
		file, function, _ := strings.Cut(name, ":")
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var fn *ast.FuncDecl
		for _, decl := range parsed.Decls {
			if candidate, ok := decl.(*ast.FuncDecl); ok && candidate.Name.Name == function {
				fn = candidate
			}
		}
		if fn == nil {
			t.Errorf("%s: function not found", name)
			continue
		}
		for _, want := range wanted {
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == want {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "wire" {
						found = true
					}
				}
				return !found
			})
			if !found {
				t.Errorf("%s no longer calls wire.%s: an upstream URL must be built with the seam", name, want)
			}
		}
	}
}

// An inference body is encoded and POSTed in exactly two places, serving and the
// model probe, and both apply wire.FinalizeBody. This finds any other function in
// the packages that talk to providers (ingress, control/api) that POSTs a request,
// so a third site cannot ship without choosing: go through the seam, or be listed
// here with the reason it is not an inference body.
var providerPostSites = map[string]string{
	"ingress/ingress.go:newUpstreamRequest":      "serving",
	"control/api/providers.go:testProviderModel": "model probe",
}

func TestEveryInferenceBodyPostGoesThroughFinalizeBody(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, dir := range []string{"ingress", "control/api"} {
		entries, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range entries {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			rel := dir + "/" + filepath.Base(path)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				posts, finalizes := false, false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.CallExpr:
						if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
							if sel.Sel.Name == "NewRequestWithContext" || sel.Sel.Name == "NewRequest" {
								for _, arg := range node.Args {
									if a, ok := arg.(*ast.SelectorExpr); ok && a.Sel.Name == "MethodPost" {
										posts = true
									}
								}
							}
							if id, ok := sel.X.(*ast.Ident); ok && id.Name == "wire" && sel.Sel.Name == "FinalizeBody" {
								finalizes = true
							}
						}
					}
					return true
				})
				if !posts {
					continue
				}
				name := rel + ":" + fn.Name.Name
				found[name] = true
				if _, listed := providerPostSites[name]; !listed {
					t.Errorf("%s POSTs a request but is not a listed inference site: pass the body through wire.FinalizeBody, or list it with the reason it is not an inference body", name)
				} else if !finalizes {
					t.Errorf("%s POSTs an inference body without wire.FinalizeBody", name)
				}
			}
		}
	}
	for name := range providerPostSites {
		if !found[name] {
			t.Errorf("listed site %s no longer POSTs a request; remove it", name)
		}
	}
}

// The base guard above sees a function that reads a base URL field. An upstream
// URL can also be assembled from a base the function was handed, by appending a
// path or setting a query on a *url.URL. Target.URL is the one place that does it
// for a request: a provider's path and settings-derived segments (an opencode
// /zen/v1 prefix, a Vertex projects/{projectId}/ path) are validated there, and a
// refusal for a missing setting is raised by the Target hook, not a caller. This
// finds any other non-test function that assigns .Path or .RawQuery, so a third
// place cannot start building an endpoint without being listed with the reason it
// does not.
var urlAssemblers = map[string]string{
	"control/api/providers.go:testProviderModel":  "model probe: adds alt=sse to the URL Target.URL returned (Gemini)",
	"control/api/settings.go:safeSettings":        "redacts a stored URL for display; never dials",
	"control/api/read_models.go:safeURL":          "redacts a stored URL for display; never dials",
	"ingress/catalog.go:handleModelKind":          "rewrites the inbound request's own query",
	"providers/generic/generic.go:Endpoint":       "generic node's own endpoint; no caller outside its package",
	"providers/generic/generic.go:DiscoverModels": "generic node; no caller outside its package",
	"providers/onboarding/flow.go:appendQuery":    "OAuth URL query, not an inference upstream",
	"transforms/headroom/headroom.go:Health":      "transform sidecar health probe, not a provider",
	"transforms/pxpipe/pxpipe.go:Health":          "transform sidecar health probe, not a provider",
}

func TestUpstreamURLsAreAssembledOnlyInTheSeam(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "providers/wire/") {
			return nil // the seam itself
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			assembles := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Path" || sel.Sel.Name == "RawQuery" || sel.Sel.Name == "RawPath") {
							assembles = true
						}
					}
				case *ast.CallExpr:
					if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "JoinPath" {
						assembles = true
					}
				}
				return !assembles
			})
			if assembles {
				found[rel+":"+fn.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, allowed := urlAssemblers[name]; !allowed {
			t.Errorf("%s sets a URL path or query outside internal/providers/wire: return a Target from the provider's hook and let Target.URL build it, or list the function with the reason it never builds an upstream URL", name)
		}
	}
	for name := range urlAssemblers {
		if !found[name] {
			t.Errorf("allowlist entry %s no longer assembles a URL; remove it", name)
		}
	}
}

// Cloudflare Workers AI puts the account id in the path
// ({base}/{accountId}/ai/v1/chat/completions, .../ai/run/{model}). That path is
// produced by wire.cloudflareTarget alone, which refuses a missing account id; a
// function elsewhere that spells either path would build the URL without the
// fail-closed check.
func TestCloudflarePathsAreSpelledOnlyInTheSeam(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "providers/wire/") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && (strings.Contains(lit.Value, "/ai/run/") || strings.Contains(lit.Value, "/ai/v1/")) {
				t.Errorf("%s spells a Cloudflare Workers AI path (%s): only wire.cloudflareTarget builds it", rel, lit.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKiroInferenceHostsAreSpelledOnlyInTheSeam(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// Catalog defaults, onboarding validation and quota URLs are not inference dispatch.
		if strings.HasPrefix(filepath.ToSlash(rel), "providers/") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				for _, host := range []string{"https://q.", "https://codewhisperer.", "https://runtime."} {
					if strings.Contains(lit.Value, host) {
						t.Errorf("%s spells a Kiro host outside wire.ResolveTarget: %s", rel, lit.Value)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
