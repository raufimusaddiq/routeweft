package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func listingRef(provider, token string, settings routing.ProviderSettings) routing.ProviderRef {
	return routing.ProviderRef{ProviderID: provider, APIToken: token, Connection: routing.ConnectionMetadata{Settings: settings, AuthKind: "oauth"}}
}

func mustListing(t *testing.T, ref routing.ProviderRef) Listing {
	t.Helper()
	listing, ok, err := ListingFor(ref)
	if !ok || err != nil {
		t.Fatalf("%s listing: ok=%v err=%v", ref.ProviderID, ok, err)
	}
	return listing
}

// Each listing request is the pin's (route.js unless noted), not a bearer GET
// {base}/models.
func TestListingRequestsMatchThePin(t *testing.T) {
	cases := []struct {
		name     string
		ref      routing.ProviderRef
		method   string
		url      string
		path     string
		query    string
		body     string
		headers  map[string]string
		shape    Shape
		ownAuth  bool
		override map[string]string
	}{
		{name: "perplexity lists /v1/models (route.js:247)", ref: listingRef("perplexity", "k", routing.ProviderSettings{}), path: "v1/models"},
		{name: "codex gates entries by client_version (route.js:19-24,149-164)", ref: listingRef("codex", "k", routing.ProviderSettings{}),
			query: "client_version=" + url.QueryEscape(codexprovider.CLIVersion), shape: ShapeCodex},
		{name: "gemini-cli POSTs the project (route.js:440-460)", ref: listingRef("gemini-cli", "k", routing.ProviderSettings{ProjectID: "proj-1"}),
			method: "POST", url: "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", body: `{"project":"proj-1"}`,
			headers: map[string]string{"User-Agent": "google-api-nodejs-client/9.15.1", "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1"}, shape: ShapeGoogleModels},
		{name: "gemini-cli without a project POSTs {}", ref: listingRef("gemini-cli", "k", routing.ProviderSettings{}),
			method: "POST", url: "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", body: `{}`,
			headers: map[string]string{"User-Agent": "google-api-nodejs-client/9.15.1", "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1"}, shape: ShapeGoogleModels},
		{name: "antigravity POSTs {} to the sandbox host (route.js:165-173)", ref: listingRef("antigravity", "k", routing.ProviderSettings{}),
			method: "POST", url: "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:models", body: `{}`, shape: ShapeGoogleModels},
		{name: "github reads chat models only (route.js:186-199)", ref: listingRef("github", "k", routing.ProviderSettings{}), shape: ShapeGitHub},
		{name: "kimchi lists the CLI metadata catalog (kimchiModels.js:22-24)", ref: listingRef("kimchi", "k", routing.ProviderSettings{}),
			url: "https://llm.kimchi.dev/v1/models/metadata", query: "include_in_cli=true", headers: map[string]string{"User-Agent": "kimchi/0.1.40"}, shape: ShapeKimchi},
		{name: "grok-cli lists /v1/models as grok-shell (grokCliModels.js:11,56-70)", ref: listingRef("grok-cli", "k", routing.ProviderSettings{Email: "a@example.test", UserID: "u-1"}),
			path: "v1/models", shape: ShapeGrokCLI,
			headers: map[string]string{"x-xai-token-auth": "xai-grok-cli", "x-grok-client-version": "1.0.44", "x-grok-client-identifier": "grok-shell", "x-grok-client-mode": "headless",
				"User-Agent": "grok-shell/1.0.44 (linux; x86_64)", "x-email": "a@example.test", "x-userid": "u-1"}},
		{name: "cursor POSTs an empty proto body (cursorModels.js:146-170)", ref: listingRef("cursor", "k", routing.ProviderSettings{MachineID: "m"}),
			method: "POST", url: "https://agent.api5.cursor.sh/agent.v1.AgentService/GetUsableModels", shape: ShapeCursor, ownAuth: true,
			override: map[string]string{"Accept": "application/proto", "Content-Type": "application/proto", "Connect-Accept-Encoding": "", "Connect-Protocol-Version": ""}},
		{name: "qoder is a signed GET on api3 (qoderModels.js:195-262)", ref: listingRef("qoder", "dt-1", routing.ProviderSettings{}),
			url: "https://api3.qoder.sh/algo/api/v2/model/list", shape: ShapeQoder, ownAuth: true, headers: map[string]string{"Accept": "application/json", "Accept-Encoding": "identity"}},
		{name: "qoder job tokens are served from api2 (qoderModels.js:200-203)", ref: listingRef("qoder", "jt-1", routing.ProviderSettings{}),
			url: "https://api2.qoder.sh/algo/api/v2/model/list", shape: ShapeQoder, ownAuth: true, headers: map[string]string{"Accept": "application/json", "Accept-Encoding": "identity"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			listing := mustListing(t, c.ref)
			if listing.Method != c.method || listing.URL != c.url || listing.Path != c.path || listing.Query != c.query || string(listing.Body) != c.body || listing.Shape != c.shape || listing.OwnAuth != c.ownAuth {
				t.Fatalf("listing %+v", listing)
			}
			for name, want := range c.headers {
				if listing.Headers[name] != want {
					t.Errorf("header %s = %q, want %q", name, listing.Headers[name], want)
				}
			}
			if c.override != nil && !reflect.DeepEqual(listing.Override, c.override) {
				t.Errorf("override %v, want %v", listing.Override, c.override)
			}
		})
	}
}

// A newer client_version only widens the endpoint's minimal_client_version gate,
// so Routeweft's own identity is fine as long as it is not older than the pin's.
func TestCodexListingVersionIsNotOlderThanThePins(t *testing.T) {
	parse := func(v string) (out [3]int) {
		for i, part := range strings.Split(v, ".") {
			for _, r := range part {
				out[i] = out[i]*10 + int(r-'0')
			}
		}
		return out
	}
	got, pin := parse(codexprovider.CLIVersion), parse("0.144.6")
	for i := range got {
		if got[i] != pin[i] {
			if got[i] < pin[i] {
				t.Fatalf("codex client_version %s is older than the pin's 0.144.6: newer models would be silently missing", codexprovider.CLIVersion)
			}
			return
		}
	}
}

func TestKiroListingFollowsTheProfileRegion(t *testing.T) {
	arn := "arn:aws:codewhisperer:eu-west-1:123456789012:profile/ABC"
	listing := mustListing(t, listingRef("kiro", "tok", routing.ProviderSettings{ProfileARN: arn}))
	parsed, err := url.Parse(listing.URL)
	if err != nil || parsed.Host != "q.eu-west-1.amazonaws.com" || parsed.Path != "/ListAvailableModels" {
		t.Fatalf("url %s", listing.URL)
	}
	query, _ := url.ParseQuery(listing.Query)
	if query.Get("origin") != "AI_EDITOR" || query.Get("profileArn") != arn {
		t.Fatalf("query %q", listing.Query)
	}
	if listing.Headers["x-amzn-kiro-agent-mode"] != "vibe" || !strings.Contains(listing.Headers["User-Agent"], "KiroIDE-0.10.32-") || listing.Headers["amz-sdk-invocation-id"] == "" || listing.Shape != ShapeKiro {
		t.Fatalf("headers %v", listing.Headers)
	}
	// No profile: us-east-1, and no profileArn parameter.
	listing = mustListing(t, listingRef("kiro", "tok", routing.ProviderSettings{}))
	if listing.URL != "https://q.us-east-1.amazonaws.com/ListAvailableModels" || listing.Query != "origin=AI_EDITOR" {
		t.Fatalf("default listing %+v", listing)
	}
	// The region becomes a host name; a profile that smuggles another host is refused.
	for _, bad := range []string{"arn:aws:codewhisperer:evil.example.com/x:1:profile/A", "arn:aws:codewhisperer:us-east-1.evil.test:1:profile/A", "arn:aws:codewhisperer:US EAST:1:profile/A"} {
		_, _, err := ListingFor(listingRef("kiro", "tok", routing.ProviderSettings{ProfileARN: bad}))
		var refused *ListingError
		if !errors.As(err, &refused) {
			t.Errorf("profile %q was not refused: %v", bad, err)
		}
	}
}

// An API key is a plain bearer; an OAuth token is a WorkOS JWT and carries the
// workos: prefix (clinepassModels.js:11-20). The shared bearer would send the JWT bare.
func TestClineListingPrefixesOAuthTokens(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig"
	oauth := mustListing(t, listingRef("cline", jwt, routing.ProviderSettings{}))
	if !oauth.OwnAuth || oauth.Headers["Authorization"] != "Bearer workos:"+jwt || oauth.Headers["Accept"] != "application/json" || oauth.Headers["X-CLIENT-TYPE"] == "" || oauth.Shape != ShapeCline {
		t.Fatalf("oauth listing %+v", oauth)
	}
	key := listingRef("clinepass", "clp_key", routing.ProviderSettings{})
	key.Connection.AuthKind = "api-key"
	apiKey := mustListing(t, key)
	if apiKey.Headers["Authorization"] != "Bearer clp_key" || len(apiKey.Headers) != 2 || apiKey.Shape != ShapeClinePass {
		t.Fatalf("api key listing %+v", apiKey)
	}
	// Even a key that looks like a JWT is sent verbatim for an API-key connection.
	key.APIToken = jwt
	if got := mustListing(t, key).Headers["Authorization"]; got != "Bearer "+jwt {
		t.Fatalf("api-key jwt authorization %q", got)
	}
}

// Tracked gap, stated rather than hidden: a PAT needs the job-token exchange.
func TestQoderPersonalTokenListingIsAnExplicitGap(t *testing.T) {
	_, ok, err := ListingFor(listingRef("qoder", "pt-secret", routing.ProviderSettings{}))
	var refused *ListingError
	if !ok || !errors.As(err, &refused) || strings.Contains(err.Error(), "pt-secret") {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestSharedListingProvidersHaveNoListing(t *testing.T) {
	for _, id := range []string{"openai", "groq", "deepseek", "openrouter", "mistral", ""} {
		if _, ok, _ := ListingFor(listingRef(id, "k", routing.ProviderSettings{})); ok {
			t.Errorf("%q has its own listing", id)
		}
	}
}

func TestListingURLAssemblyDropsForeignQueries(t *testing.T) {
	policy := func(raw string) (*url.URL, error) { return url.Parse(raw) }
	cases := []struct{ base, absolute, path, query, want string }{
		{"https://api.perplexity.ai", "", "v1/models", "", "https://api.perplexity.ai/v1/models"},
		{"https://gw.example.com/v1", "", "v1/models", "", "https://gw.example.com/v1/models"},
		{"https://gw.example.com/v1/", "", "", "", "https://gw.example.com/v1/models"},
		{"https://gw.example.com/v1/models", "", "", "", "https://gw.example.com/v1/models"},
		{"https://gw.example.com/api?injected=1#frag", "", "api/tags", "", "https://gw.example.com/api/api/tags"},
		{"https://chatgpt.com/backend-api/codex?x=1", "", "", "client_version=0.154.0", "https://chatgpt.com/backend-api/codex/models?client_version=0.154.0"},
		{"https://ignored.example.com", "https://llm.kimchi.dev/v1/models/metadata?injected=1", "", "include_in_cli=true", "https://llm.kimchi.dev/v1/models/metadata?include_in_cli=true"},
	}
	for _, c := range cases {
		got, err := ListingURL(policy, c.base, c.absolute, c.path, c.query)
		if err != nil || got != c.want {
			t.Errorf("ListingURL(%q,%q,%q,%q) = %q, %v; want %q", c.base, c.absolute, c.path, c.query, got, err, c.want)
		}
	}
	if _, err := ListingURL(policy, "https://x.example.com", "", "../secret", ""); err == nil {
		t.Error("a path that traverses was accepted")
	}
}

type listedWant struct {
	id, name string
	window   int
	caps     []string
}

func checkListed(t *testing.T, shape Shape, body string, want []listedWant) {
	t.Helper()
	got, err := ParseListing(shape, []byte(body))
	if err != nil {
		t.Fatalf("%s: %v", shape, err)
	}
	var actual []listedWant
	for _, m := range got {
		actual = append(actual, listedWant{m.ID, m.Name, m.ContextWindow, m.Capabilities})
	}
	if len(actual) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("%s:\n got %+v\nwant %+v", shape, actual, want)
	}
}

// The slug-keyed shape and the -review twins (route.js:54-73).
func TestParseCodexListing(t *testing.T) {
	checkListed(t, ShapeCodex, `{"models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","context_window":272000},
		{"slug":"gpt-5.5-review","display_name":"GPT-5.5 Review"},
		{"id":"image-gen","type":"image"},
		{"slug":"text-embed-3"},
		{"display_name":"no id"}
	]}`, []listedWant{
		{"gpt-5.5", "GPT-5.5", 272000, nil}, {"gpt-5.5-review", "GPT-5.5 Review", 272000, nil},
		{"image-gen", "image-gen", 0, nil}, {"text-embed-3", "text-embed-3", 0, nil},
	})
}

// models as an array, or an object keyed by id with internal entries dropped.
func TestParseGoogleModelsListing(t *testing.T) {
	checkListed(t, ShapeGoogleModels, `{"models":[{"id":"gemini-3-pro","displayName":"Gemini 3 Pro"},{"model":"m2"},{"name":"n3"},{}]}`,
		[]listedWant{{"gemini-3-pro", "Gemini 3 Pro", 0, nil}, {"m2", "m2", 0, nil}, {"n3", "n3", 0, nil}})
	// isInternal drops an entry in the array form of the payload as well.
	checkListed(t, ShapeGoogleModels, `{"models":[{"name":"models/gemini-3-pro","isInternal":true},{"name":"public"}]}`,
		[]listedWant{{"public", "public", 0, nil}})
	checkListed(t, ShapeGoogleModels, `{"models":{"claude-opus-4-6":{"displayName":"Claude Opus"},"internal-x":{"isInternal":true},"plain":{}}}`,
		[]listedWant{{"claude-opus-4-6", "Claude Opus", 0, nil}, {"plain", "plain", 0, nil}})
	checkListed(t, ShapeGoogleModels, `{}`, []listedWant{})
}

// Embeddings and policy-disabled models are not offered (route.js:186-199).
func TestParseGitHubListing(t *testing.T) {
	checkListed(t, ShapeGitHub, `{"data":[
		{"id":"gpt-4.1","name":"GPT-4.1","capabilities":{"type":"chat","limits":{"max_context_window_tokens":128000}},"policy":{"state":"enabled"}},
		{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
		{"id":"o-disabled","capabilities":{"type":"chat"},"policy":{"state":"disabled"}},
		{"id":"no-policy","capabilities":{"type":"chat"}},
		{"id":"no-caps"}
	]}`, []listedWant{{"gpt-4.1", "GPT-4.1", 128000, nil}, {"no-policy", "no-policy", 0, nil}})
}

func TestParseKimchiListing(t *testing.T) {
	checkListed(t, ShapeKimchi, `{"models":[
		{"slug":"claude-sonnet","display_name":"Claude Sonnet","provider":"anthropic","input_modalities":["text","image"],"reasoning":true,"limits":{"context_window":200000,"max_output_tokens":64000}},
		{"id":"glm-5","name":"GLM 5","contextLength":131072}
	]}`, []listedWant{
		{"claude-sonnet", "Claude Sonnet", 200000, []string{"vision", "reasoning"}},
		{"glm-5", "GLM 5", 131072, nil},
	})
}

// Four ids per upstream model, none of the agentic ones for auto (kiroModels.js:106-142).
func TestParseKiroListing(t *testing.T) {
	checkListed(t, ShapeKiro, `{"models":[
		{"modelId":"claude-sonnet-4.5","modelName":"Claude Sonnet 4.5","rateMultiplier":1.3,"tokenLimits":{"maxInputTokens":200000}},
		{"modelId":"auto","modelName":"Auto","rateMultiplier":1},
		{"modelId":"already-thinking-agentic"}
	]}`, []listedWant{
		// An upstream id that already looks synthetic is reduced to its base (kiroModels.js:44-53).
		{"already", "Kiro already-thinking-agentic", 0, nil},
		{"already-agentic", "Kiro already-thinking-agentic (Agentic)", 0, nil},
		{"already-thinking", "Kiro already-thinking-agentic (Thinking)", 0, []string{"reasoning"}},
		{"already-thinking-agentic", "Kiro already-thinking-agentic (Thinking + Agentic)", 0, []string{"reasoning"}},
		{"auto", "Kiro Auto", 0, nil},
		{"auto-thinking", "Kiro Auto (Thinking)", 0, []string{"reasoning"}},
		{"claude-sonnet-4.5", "Kiro Claude Sonnet 4.5 (1.3x credit)", 200000, nil},
		{"claude-sonnet-4.5-agentic", "Kiro Claude Sonnet 4.5 (1.3x credit) (Agentic)", 200000, nil},
		{"claude-sonnet-4.5-thinking", "Kiro Claude Sonnet 4.5 (1.3x credit) (Thinking)", 200000, []string{"reasoning"}},
		{"claude-sonnet-4.5-thinking-agentic", "Kiro Claude Sonnet 4.5 (1.3x credit) (Thinking + Agentic)", 200000, []string{"reasoning"}},
	})
}

// ClinePass keeps cline-pass/ only; Cline keeps everything with an id (clinepassModels.js:61-94).
func TestParseClineListings(t *testing.T) {
	body := `{"data":[{"id":"cline-pass/glm-5","name":"GLM 5 Pass"},{"id":"z-ai/glm-5.3-flash"},{"id":" "},{"name":"idless"}]}`
	checkListed(t, ShapeClinePass, body, []listedWant{{"cline-pass/glm-5", "GLM 5 Pass", 0, nil}})
	checkListed(t, ShapeCline, body, []listedWant{{"cline-pass/glm-5", "GLM 5 Pass", 0, nil}, {"z-ai/glm-5.3-flash", "z-ai/glm-5.3-flash", 0, nil}})
	checkListed(t, ShapeCline, `[{"id":"bare/array"}]`, []listedWant{{"bare/array", "bare/array", 0, nil}})
}

// Array, keyed object and bare strings, with the pin's seven id spellings
// (grokCliModels.js:13-48).
func TestParseGrokCLIListing(t *testing.T) {
	checkListed(t, ShapeGrokCLI, `{"data":[
		{"id":"grok-build","display_name":"Grok Build"},
		{"model_id":"grok-4.5","context_window":256000},
		"grok-plain",
		{"slug":"grok-slug"},
		{"modelId":"grok-camel","displayName":"Camel"}
	]}`, []listedWant{
		{"grok-4.5", "grok-4.5", 256000, nil}, {"grok-build", "Grok Build", 0, nil}, {"grok-camel", "Camel", 0, nil},
		{"grok-plain", "grok-plain", 0, nil}, {"grok-slug", "grok-slug", 0, nil},
	})
	checkListed(t, ShapeGrokCLI, `{"models":{"keyed-model":{"name":"Keyed"},"other":{"id":"explicit-id"}}}`,
		[]listedWant{{"explicit-id", "explicit-id", 0, nil}, {"keyed-model", "Keyed", 0, nil}})
}

// Hidden (enable:false) entries are not offered by the provider listing; the
// ids are the bare keys the catalog seeds (qoderModels.js:244-278).
func TestParseQoderListing(t *testing.T) {
	checkListed(t, ShapeQoder, `{"chat":[
		{"key":"ultimate","display_name":"Ultimate","max_input_tokens":200000,"is_reasoning":true},
		{"key":"hidden","enable":false},
		{"key":"vision","display_name":"Vision","is_vl":true}
	]}`, []listedWant{
		{"ultimate", "Ultimate", 200000, []string{"reasoning"}},
		{"vision", "Vision", 0, []string{"vision"}},
	})
	if _, err := ParseListing(ShapeQoder, []byte(`{"models":[]}`)); err == nil {
		t.Error("a qoder body with no chat catalog was accepted")
	}
}

func TestParseCursorListing(t *testing.T) {
	detail := func(id, short, display, name string) []byte {
		return pbBytes(1, pbJoin(pbString(1, id), pbString(3, display), pbString(4, name), pbString(5, short)))
	}
	body := pbJoin(
		detail("composer-2", "C2", "composer-2-display", "Composer 2"),
		detail("gpt-5", "G5", "", ""),
		detail("claude-sonnet", "", "Claude Display", ""),
		detail("composer-2", "dup", "", "Duplicate"),
		pbBytes(2, pbString(1, "composer-2")), // AvailableModels flags are not part of the list
	)
	got, err := ParseListing(ShapeCursor, body)
	if err != nil {
		t.Fatal(err)
	}
	want := []ListedModel{{ID: "claude-sonnet", Name: "Claude Display"}, {ID: "composer-2", Name: "Composer 2"}, {ID: "gpt-5", Name: "G5"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if _, err := ParseListing(ShapeCursor, []byte(`<html>not proto</html>`)); err == nil {
		t.Error("a non-protobuf cursor body was accepted")
	}
}

func TestParseListingRejectsMalformedBodiesAndShapes(t *testing.T) {
	for _, shape := range []Shape{ShapeCodex, ShapeGoogleModels, ShapeGitHub, ShapeKimchi, ShapeKiro, ShapeCline, ShapeClinePass, ShapeGrokCLI, ShapeQoder} {
		if _, err := ParseListing(shape, []byte(`{"models":[`)); err == nil {
			t.Errorf("%s accepted truncated JSON", shape)
		}
	}
	if _, err := ParseListing("nope", []byte(`{}`)); err == nil {
		t.Error("unknown shape accepted")
	}
}

// The header is a stable uuid per account, not a fresh one per call.
func TestKiroListingInvocationIDIsDeterministic(t *testing.T) {
	a := mustListing(t, listingRef("kiro", "tok", routing.ProviderSettings{}))
	b := mustListing(t, listingRef("kiro", "tok", routing.ProviderSettings{}))
	other := mustListing(t, listingRef("kiro", "tok2", routing.ProviderSettings{}))
	sum := sha256.Sum256([]byte("tok"))
	want := kiroInvocationID(hex.EncodeToString(sum[:]))
	if a.Headers["amz-sdk-invocation-id"] != want || b.Headers["amz-sdk-invocation-id"] != want || other.Headers["amz-sdk-invocation-id"] == want {
		t.Fatalf("invocation ids %q %q %q want %q", a.Headers["amz-sdk-invocation-id"], b.Headers["amz-sdk-invocation-id"], other.Headers["amz-sdk-invocation-id"], want)
	}
}

// An unknown window is 0 (as in the seeds); a guess would drive combo budgets.
func TestUnknownContextWindowsAreNotInvented(t *testing.T) {
	checkListed(t, ShapeKiro, `{"models":[{"modelId":"m"}]}`, []listedWant{
		{"m", "Kiro m", 0, nil}, {"m-agentic", "Kiro m (Agentic)", 0, nil},
		{"m-thinking", "Kiro m (Thinking)", 0, []string{"reasoning"}}, {"m-thinking-agentic", "Kiro m (Thinking + Agentic)", 0, []string{"reasoning"}},
	})
}
