package parity

import (
	"regexp"
	"sort"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// The pin's capability and pricing tables are keyed by provider id or alias but
// are not the registry. A key there that is neither an active provider, a
// hidden one nor an alias of either is a table-only entry; it must be named
// here, with the reason, or the test fails. A table key is never a provider to
// seed (task #76 P01).
var tableOnlyKeys = map[string]string{
	"qoder-cn": "open-sse/providers/capabilities.js assigns PROVIDER_CAPABILITIES[\"qoder-cn\"] = qoder's row; no registry entry has that id or alias",
	"devin":    "open-sse/providers/capabilities.js:353 assigns PROVIDER_CAPABILITIES.devin = the hidden devin-cli row; the registry alias is \"dv\"",
}

// tableKeys returns the keys of one exported table plus later assignments to it.
func tableKeys(source, name string) []string {
	body := regexp.MustCompile(`(?ms)^export const ` + name + ` = \{\n(.*?)^\};`).FindStringSubmatch(source)
	var keys []string
	if body != nil {
		for _, m := range regexp.MustCompile(`(?m)^  "?([A-Za-z0-9._-]+)"?: \{`).FindAllStringSubmatch(body[1], -1) {
			keys = append(keys, m[1])
		}
	}
	for _, m := range regexp.MustCompile(`(?m)^`+name+`(?:\["([^"]+)"\]|\.(\w+)) =`).FindAllStringSubmatch(source, -1) {
		keys = append(keys, m[1]+m[2])
	}
	return keys
}

// registryAliases maps each registry file's alias to its provider id.
func registryAliases(t *testing.T, o oracle) map[string]string {
	t.Helper()
	aliases := map[string]string{}
	for _, name := range o.files(t, registryDir) {
		if name == "index.js" {
			continue
		}
		source := o.file(t, registryDir+"/"+name)
		id := regexp.MustCompile(`(?m)^  id: "([^"]+)"`).FindStringSubmatch(source)
		alias := regexp.MustCompile(`(?m)^  alias: "([^"]+)"`).FindStringSubmatch(source)
		if id != nil && alias != nil {
			aliases[alias[1]] = id[1]
		}
	}
	return aliases
}

// The set a user can select is the built-in catalog, not the Builtins slice, so
// the guard looks every pinned active provider up the way the product does.
func TestEveryOracleProviderIsSelectable(t *testing.T) {
	o := pinnedOracle(t)
	active, _ := oracleProviders(t, o)
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for id := range active {
		if _, ok := catalog.Lookup(id); !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("pinned active providers absent from the selectable catalog: %v", missing)
	}
	if catalog.Len() != len(active) {
		t.Errorf("selectable catalog has %d providers, pin serves %d", catalog.Len(), len(active))
	}
}

// muse is an API-key provider with one credential path: its registry entry has
// category "apikey" and a single Bearer transport, with no OAuth block. The
// "two contributors" are its two models.
func TestMuseMatchesPinnedEntry(t *testing.T) {
	o := pinnedOracle(t)
	source := o.file(t, registryDir+"/muse.js")
	var pinned []string
	for _, m := range regexp.MustCompile(`(?m)^    \{ id: "([^"]+)"`).FindAllStringSubmatch(source, -1) {
		pinned = append(pinned, m[1])
	}
	if len(pinned) != 2 {
		t.Fatalf("pinned muse declares %d models, expected the two contributors: %v", len(pinned), pinned)
	}
	if !regexp.MustCompile(`(?m)^  category: "apikey"`).MatchString(source) || regexp.MustCompile(`(?m)^\s+oauth`).MatchString(source) {
		t.Fatal("pinned muse is no longer a plain API-key provider; its credential flow needs a new decision")
	}
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := catalog.Lookup("muse")
	if !ok {
		t.Fatal("muse is not selectable")
	}
	if spec.Auth != registry.AuthAPIKey || len(spec.Transports) != 1 || spec.Transports[0] != registry.TransportOpenAIResponses {
		t.Errorf("muse auth/transports = %v %v, want API key over OpenAI Responses", spec.Auth, spec.Transports)
	}
	var seeded []string
	for _, m := range spec.SeedModels {
		seeded = append(seeded, m.ID)
	}
	sort.Strings(pinned)
	sort.Strings(seeded)
	if len(seeded) != len(pinned) || (len(pinned) == 2 && (seeded[0] != pinned[0] || seeded[1] != pinned[1])) {
		t.Errorf("muse seed models %v, pin declares %v", seeded, pinned)
	}
}

// Table keys that are not providers must stay classified: appearing only in a
// capability or pricing table is not a reason to add a provider.
func TestPinnedTableKeysAreClassified(t *testing.T) {
	o := pinnedOracle(t)
	active, hidden := oracleProviders(t, o)
	aliases := registryAliases(t, o)
	var unclassified []string
	seen := map[string]bool{}
	for table, file := range map[string]string{"PROVIDER_CAPABILITIES": "capabilities.js", "PROVIDER_PRICING": "pricing.js"} {
		for _, key := range tableKeys(o.file(t, "open-sse/providers/"+file), table) {
			_, isActive := active[key]
			_, isHidden := hidden[key]
			_, isAlias := aliases[key]
			if isActive || isHidden || isAlias {
				continue
			}
			seen[key] = true
			if _, ok := tableOnlyKeys[key]; !ok {
				unclassified = append(unclassified, table+"."+key)
			}
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("pinned table keys that are neither providers nor aliases and are not classified: %v", unclassified)
	}
	for key := range tableOnlyKeys {
		if !seen[key] {
			t.Errorf("tableOnlyKeys lists %q but the pin no longer has it (or it became a provider); remove it", key)
		}
	}
}
