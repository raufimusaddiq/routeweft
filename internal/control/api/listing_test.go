package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// These tests run the real listing request for a connection through the admin
// seam and record what reaches the upstream. A provider with a registry row is
// not covered by that row: the request, and the parse of its actual shape, are.

type listingCall struct {
	Method, URL, Body string
	Header            http.Header
}

func listingHandler(t *testing.T, body string) (*Handler, func() []listingCall) {
	t.Helper()
	h := New(Options{Providers: registry.Builtins()})
	var mu sync.Mutex
	var calls []listingCall
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		mu.Lock()
		calls = append(calls, listingCall{r.Method, r.URL.String(), string(raw), r.Header.Clone()})
		mu.Unlock()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return h, func() []listingCall { mu.Lock(); defer mu.Unlock(); return append([]listingCall(nil), calls...) }
}

func listProvider(t *testing.T, provider string, auth credentials.AuthKind, settings routing.ProviderSettings, token, upstream string) ([]discovery.Model, listingCall, error) {
	t.Helper()
	h, calls := listingHandler(t, upstream)
	node, connection := builtinConnection(provider, settings, auth)
	connection.Secret.AccessToken = token
	models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
	got := calls()
	if err == nil && len(got) != 1 {
		t.Fatalf("%s sent %d requests, want 1: %+v", provider, len(got), got)
	}
	if len(got) == 0 {
		return models, listingCall{}, err
	}
	return models, got[0], err
}

func ids(models []discovery.Model) string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return strings.Join(out, ",")
}

func TestPerplexityListsUnderV1(t *testing.T) {
	models, call, err := listProvider(t, "perplexity", credentials.AuthAPIKey, routing.ProviderSettings{}, "tok", `{"data":[{"id":"sonar"}]}`)
	if err != nil || call.URL != "https://api.perplexity.ai/v1/models" || call.Header.Get("Authorization") != "Bearer tok" || ids(models) != "sonar" {
		t.Fatalf("%+v %v %v", call, models, err)
	}
	// perplexity-agent already serves under /v1 and is not doubled.
	_, call, _ = listProvider(t, "perplexity-agent", credentials.AuthAPIKey, routing.ProviderSettings{}, "tok", `{"data":[]}`)
	if call.URL != "https://api.perplexity.ai/v1/models" {
		t.Fatalf("perplexity-agent URL %s", call.URL)
	}
}

func TestCodexListingSendsTheGateAndReadsSlugs(t *testing.T) {
	models, call, err := listProvider(t, "codex", credentials.AuthOAuth, routing.ProviderSettings{ChatGPTAccountID: "acct-1"}, "tok",
		`{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","context_window":272000}]}`)
	if err != nil || call.Method != "GET" {
		t.Fatalf("%+v %v", call, err)
	}
	parsed, _ := url.Parse(call.URL)
	if parsed.Host != "chatgpt.com" || parsed.Path != "/backend-api/codex/models" || parsed.Query().Get("client_version") == "" || len(parsed.Query()) != 1 {
		t.Fatalf("codex URL %s", call.URL)
	}
	if call.Header.Get("Authorization") != "Bearer tok" || call.Header.Get("Originator") != "codex_cli_rs" {
		t.Fatalf("codex headers %v", call.Header)
	}
	if ids(models) != "gpt-5.5,gpt-5.5-review" || models[0].ContextWindow != 272000 {
		t.Fatalf("codex models %+v", models)
	}
}

func TestGoogleInternalListingsArePosts(t *testing.T) {
	models, call, err := listProvider(t, "gemini-cli", credentials.AuthOAuth, routing.ProviderSettings{ProjectID: "proj-1"}, "tok",
		`{"models":{"gemini-3-pro":{"displayName":"Gemini 3 Pro"},"hidden":{"isInternal":true}}}`)
	if err != nil || call.Method != "POST" || call.URL != "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels" || call.Body != `{"project":"proj-1"}` ||
		call.Header.Get("Authorization") != "Bearer tok" || call.Header.Get("Content-Type") != "application/json" || call.Header.Get("User-Agent") != "google-api-nodejs-client/9.15.1" {
		t.Fatalf("gemini-cli %+v %v", call, err)
	}
	if ids(models) != "gemini-3-pro" || models[0].Name != "Gemini 3 Pro" {
		t.Fatalf("gemini-cli models %+v", models)
	}
	models, call, err = listProvider(t, "antigravity", credentials.AuthOAuth, routing.ProviderSettings{}, "tok", `{"models":[{"id":"gemini-3-flash"}]}`)
	if err != nil || call.Method != "POST" || call.URL != "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:models" || call.Body != `{}` || call.Header.Get("Authorization") != "Bearer tok" || ids(models) != "gemini-3-flash" {
		t.Fatalf("antigravity %+v %v %v", call, models, err)
	}
}

func TestGitHubListingDropsEmbeddingsAndDisabledModels(t *testing.T) {
	models, call, err := listProvider(t, "github", credentials.AuthOAuth, routing.ProviderSettings{}, "tok", `{"data":[
		{"id":"gpt-4.1","capabilities":{"type":"chat"},"policy":{"state":"enabled"}},
		{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
		{"id":"retired","capabilities":{"type":"chat"},"policy":{"state":"disabled"}}]}`)
	if err != nil || call.URL != "https://api.githubcopilot.com/models" || call.Header.Get("Authorization") != "Bearer tok" || ids(models) != "gpt-4.1" {
		t.Fatalf("%+v %v %v", call, models, err)
	}
}

func TestKimchiListingUsesTheMetadataCatalogWithItsFilter(t *testing.T) {
	models, call, err := listProvider(t, "kimchi", credentials.AuthAPIKey, routing.ProviderSettings{}, "tok",
		`{"models":[{"slug":"claude-sonnet","display_name":"Sonnet","input_modalities":["text","image"],"limits":{"context_window":200000}}]}`)
	if err != nil || call.URL != "https://llm.kimchi.dev/v1/models/metadata?include_in_cli=true" || call.Header.Get("Authorization") != "Bearer tok" || call.Header.Get("User-Agent") != "kimchi/0.1.40" {
		t.Fatalf("%+v %v", call, err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet" || models[0].ContextWindow != 200000 || strings.Join(models[0].Capabilities, ",") != "vision" {
		t.Fatalf("kimchi models %+v", models)
	}
}

func TestKiroListingIsListAvailableModels(t *testing.T) {
	arn := "arn:aws:codewhisperer:eu-central-1:123456789012:profile/ABC"
	models, call, err := listProvider(t, "kiro", credentials.AuthOAuth, routing.ProviderSettings{ProfileARN: arn}, "tok",
		`{"models":[{"modelId":"claude-sonnet-4.5","modelName":"Claude Sonnet 4.5","rateMultiplier":1.3,"tokenLimits":{"maxInputTokens":200000}},{"modelId":"auto"}]}`)
	if err != nil || call.Method != "GET" || call.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("%+v %v", call, err)
	}
	parsed, _ := url.Parse(call.URL)
	if parsed.Host != "q.eu-central-1.amazonaws.com" || parsed.Path != "/ListAvailableModels" || parsed.Query().Get("origin") != "AI_EDITOR" || parsed.Query().Get("profileArn") != arn {
		t.Fatalf("kiro URL %s", call.URL)
	}
	if got := ids(models); got != "auto,auto-thinking,claude-sonnet-4.5,claude-sonnet-4.5-agentic,claude-sonnet-4.5-thinking,claude-sonnet-4.5-thinking-agentic" {
		t.Fatalf("kiro ids %s", got)
	}
}

// A profile naming another host never reaches the network.
func TestKiroListingRefusesAHostInTheRegion(t *testing.T) {
	h, calls := listingHandler(t, `{}`)
	node, connection := builtinConnection("kiro", routing.ProviderSettings{ProfileARN: "arn:aws:codewhisperer:evil.example.com:1:profile/A"}, credentials.AuthOAuth)
	if _, err := h.fetchConnectionModelEntries(context.Background(), connection, node); err == nil || len(calls()) != 0 {
		t.Fatalf("err=%v calls=%v", err, calls())
	}
}

// The shared bearer would send a Cline OAuth JWT bare; Cline wants workos:.
func TestClineListingCarriesTheWorkOSToken(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig"
	body := `{"data":[{"id":"cline-pass/glm-5","name":"GLM 5"},{"id":"z-ai/free-model"}]}`
	models, call, err := listProvider(t, "cline", credentials.AuthOAuth, routing.ProviderSettings{}, jwt, body)
	if err != nil || call.URL != "https://api.cline.bot/api/v1/models" || ids(models) != "cline-pass/glm-5,z-ai/free-model" {
		t.Fatalf("%+v %v %v", call, models, err)
	}
	if values := call.Header.Values("Authorization"); len(values) != 1 || values[0] != "Bearer workos:"+jwt {
		t.Fatalf("cline authorization %v", values)
	}
	models, call, err = listProvider(t, "clinepass", credentials.AuthAPIKey, routing.ProviderSettings{}, "clp_key", body)
	if err != nil || ids(models) != "cline-pass/glm-5" {
		t.Fatalf("clinepass %v %v", models, err)
	}
	if values := call.Header.Values("Authorization"); len(values) != 1 || values[0] != "Bearer clp_key" {
		t.Fatalf("clinepass authorization %v", values)
	}
}

func TestGrokCLIListingUsesTheGrokShellIdentity(t *testing.T) {
	models, call, err := listProvider(t, "grok-cli", credentials.AuthOAuth, routing.ProviderSettings{Email: "a@example.test", UserID: "u-1"}, "tok",
		`{"data":[{"id":"grok-build"},"grok-4.5"]}`)
	if err != nil || call.URL != "https://cli-chat-proxy.grok.com/v1/models" || call.Header.Get("Authorization") != "Bearer tok" ||
		call.Header.Get("x-xai-token-auth") != "xai-grok-cli" || call.Header.Get("x-grok-client-identifier") != "grok-shell" || call.Header.Get("x-email") != "a@example.test" || call.Header.Get("x-userid") != "u-1" {
		t.Fatalf("%+v %v", call, err)
	}
	if ids(models) != "grok-4.5,grok-build" {
		t.Fatalf("grok models %+v", models)
	}
}

func TestQoderListingIsSignedAndReadsTheChatCatalog(t *testing.T) {
	settings := routing.ProviderSettings{UserID: "user-1", MachineID: "machine-1", Email: "a@example.test"}
	models, call, err := listProvider(t, "qoder", credentials.AuthOAuth, settings, "dt-token",
		`{"chat":[{"key":"ultimate","display_name":"Ultimate","max_input_tokens":200000},{"key":"hidden","enable":false}]}`)
	if err != nil || call.Method != "GET" || call.URL != "https://api3.qoder.sh/algo/api/v2/model/list" {
		t.Fatalf("%+v %v", call, err)
	}
	if !strings.HasPrefix(call.Header.Get("Authorization"), "Bearer COSY.") || call.Header.Get("Cosy-User") != "user-1" || call.Header.Get("Cosy-Sigpath") != "/api/v2/model/list" ||
		call.Header.Get("Accept-Encoding") != "identity" || strings.Contains(call.Header.Get("Authorization"), "dt-token") {
		t.Fatalf("qoder headers %v", call.Header)
	}
	if len(models) != 1 || models[0].ID != "ultimate" || models[0].ContextWindow != 200000 {
		t.Fatalf("qoder models %+v", models)
	}
	// A job token is served from api2.
	_, call, err = listProvider(t, "qoder", credentials.AuthOAuth, settings, "jt-token", `{"chat":[]}`)
	if err != nil || call.URL != "https://api2.qoder.sh/algo/api/v2/model/list" {
		t.Fatalf("job token %+v %v", call, err)
	}
	// No machine id: nothing is sent, and the refusal names the cause.
	h, calls := listingHandler(t, `{}`)
	node, connection := builtinConnection("qoder", routing.ProviderSettings{UserID: "user-1"}, credentials.AuthOAuth)
	if _, err := h.fetchConnectionModelEntries(context.Background(), connection, node); err == nil || len(calls()) != 0 {
		t.Fatalf("unsigned listing: err=%v calls=%v", err, calls())
	}
	// A PAT is the tracked gap: refused by name, not sent bare.
	h, calls = listingHandler(t, `{}`)
	node, connection = builtinConnection("qoder", settings, credentials.AuthAPIKey)
	connection.Secret.AccessToken = "pt-secret"
	if _, err := h.fetchConnectionModelEntries(context.Background(), connection, node); err == nil || len(calls()) != 0 || strings.Contains(err.Error(), "pt-secret") {
		t.Fatalf("pat listing: err=%v calls=%v", err, calls())
	}
}

func TestCursorListingIsAUnaryProtoPost(t *testing.T) {
	detail := func(id, name string) []byte {
		field := append([]byte{0x0a, byte(len(id))}, id...)
		field = append(field, append([]byte{0x22, byte(len(name))}, name...)...)
		return append([]byte{0x0a, byte(len(field))}, field...)
	}
	proto := string(append(detail("composer-2", "Composer 2"), detail("gpt-5", "GPT-5")...))
	models, call, err := listProvider(t, "cursor", credentials.AuthOAuth, routing.ProviderSettings{MachineID: "machine-1"}, "tok", proto)
	if err != nil || call.Method != "POST" || call.URL != "https://agent.api5.cursor.sh/agent.v1.AgentService/GetUsableModels" || call.Body != "" {
		t.Fatalf("%+v %v", call, err)
	}
	if call.Header.Get("Content-Type") != "application/proto" || call.Header.Get("Accept") != "application/proto" ||
		call.Header.Get("Connect-Protocol-Version") != "" || call.Header.Get("Connect-Accept-Encoding") != "" ||
		call.Header.Get("Authorization") != "Bearer tok" || call.Header.Get("X-Cursor-Checksum") == "" || call.Header.Get("X-Ghost-Mode") != "true" {
		t.Fatalf("cursor headers %v", call.Header)
	}
	if ids(models) != "composer-2,gpt-5" || models[0].Name != "Composer 2" {
		t.Fatalf("cursor models %+v", models)
	}
}

// An Anthropic-compatible node is listed from its base minus a trailing
// /messages, with the key as x-api-key and as a bearer (route.js:554-593).
func TestAnthropicCompatibleNodeListing(t *testing.T) {
	for _, base := range []string{"https://gw.example.com/v1/messages", "https://gw.example.com/v1/messages/", "https://gw.example.com/v1"} {
		h, calls := listingHandler(t, `{"data":[{"id":"claude-x"}]}`)
		node := credentials.Node{ID: "n", Kind: credentials.NodeGeneric, ProviderID: "gw", Name: "gw", BaseURL: base, Transports: []string{"anthropic-messages"}}
		connection := credentials.Connection{ID: "c", NodeID: "n", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "key"}}
		models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
		got := calls()
		if err != nil || len(got) != 1 || got[0].URL != "https://gw.example.com/v1/models" || ids(models) != "claude-x" {
			t.Fatalf("%s: %+v %v %v", base, got, models, err)
		}
		if got[0].Header.Get("x-api-key") != "key" || got[0].Header.Get("Authorization") != "Bearer key" || got[0].Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("%s headers %v", base, got[0].Header)
		}
	}
}

// Every provider that lists differently is a real built-in, so a typo in the
// listing table cannot silently leave the shared request in place.
func TestEveryListingEntryIsABuiltInProvider(t *testing.T) {
	known := map[string]bool{}
	for _, spec := range registry.Builtins() {
		known[spec.ID] = true
	}
	for _, id := range []string{"perplexity", "codex", "gemini-cli", "antigravity", "github", "kimchi", "kiro", "cline", "clinepass", "grok-cli", "qoder", "cursor"} {
		if !known[id] {
			t.Errorf("%s is not a built-in provider", id)
		}
		if _, ok, _ := wire.ListingFor(routing.ProviderRef{ProviderID: id}); !ok {
			t.Errorf("%s has no listing", id)
		}
	}
}

// The pin's listing route takes no client query (route.js:512 reads only the
// connection id) and re-filters nothing beyond each provider's own parse. So a
// client's query is not forwarded to the upstream, a provider's fixed query is
// sent as its own, and a shared-shape catalog is stored whole.
func TestAdminDiscoveryForwardsNoClientQueryAndFiltersNothingExtra(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	bodyFor := func(host string) string {
		if strings.Contains(host, "kimchi") {
			return `{"models":[{"slug":"kimchi-a","display_name":"Kimchi A","limits":{"context_window":64000}}]}`
		}
		return `{"data":[{"id":"chat-1"},{"id":"text-embedding-3-small"},{"id":"omni-moderation"}]}`
	}
	upstream := probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bodyFor(r.URL.Host)))}, nil
	})
	h, mux, store, creds, cookie := newProvidersAPI(t, false, &discovery.Client{HTTP: upstream})
	defer store.Close()
	h.opts.Providers = registry.Builtins()
	for _, provider := range []string{"kimchi", "openai"} {
		node, _ := builtinConnection(provider, routing.ProviderSettings{}, credentials.AuthAPIKey)
		stored, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider, Transports: node.Transports})
		if err != nil {
			t.Fatal(err)
		}
		connection, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: stored.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "tok"}})
		if err != nil {
			t.Fatal(err)
		}
		w := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+connection.ID+"/discover?include_in_cli=false&client_version=0.0.1&limit=1&foo=bar", "")
		if w.Code != 200 {
			t.Fatalf("%s discover: %d %s", provider, w.Code, w.Body)
		}
		if provider == "openai" && !strings.Contains(w.Body.String(), `"count":3`) {
			t.Fatalf("a shared-shape catalog was re-filtered: %s", w.Body)
		}
		if provider == "kimchi" {
			if !strings.Contains(w.Body.String(), "kimchi-a") {
				t.Fatalf("kimchi catalog: %s", w.Body)
			}
			// The listing's own name and window are stored, not just the id.
			stored := doJSON(t, mux, cookie, "GET", "/admin/v1/models?providerId=kimchi", "")
			if !strings.Contains(stored.Body.String(), `"name":"Kimchi A"`) || !strings.Contains(stored.Body.String(), `64000`) {
				t.Fatalf("discovered metadata was not kept: %d %s", stored.Code, stored.Body)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"https://llm.kimchi.dev/v1/models/metadata?include_in_cli=true": true, "https://api.openai.com/v1/models": true}
	if len(seen) != 2 || !want[seen[0]] || !want[seen[1]] {
		t.Fatalf("upstream saw %v, want exactly %v", seen, want)
	}
}

// listing.go mirrors the credential kind by hand (an import cycle); a drift would
// silently send Cline API keys through the OAuth workos: path.
func TestListingAPIKeyKindMatchesCredentials(t *testing.T) {
	ref := routing.ProviderRef{ProviderID: "clinepass", APIToken: "eyJa.eyJb.sig", Connection: routing.ConnectionMetadata{AuthKind: string(credentials.AuthAPIKey)}}
	listing, _, err := wire.ListingFor(ref)
	if err != nil || listing.Headers["Authorization"] != "Bearer eyJa.eyJb.sig" {
		t.Fatalf("api-key connection took the OAuth path: %v %v", listing.Headers, err)
	}
}

// A tracked, known-unsupported listing is not reported as a transient upstream failure.
func TestTrackedListingGapIsDistinguishableFromUpstreamFailure(t *testing.T) {
	var upstream int
	client := &discovery.Client{HTTP: probeHTTPFunc(func(*http.Request) (*http.Response, error) {
		upstream++
		return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	h, mux, store, creds, cookie := newProvidersAPI(t, false, client)
	defer store.Close()
	h.opts.Providers = registry.Builtins()
	for provider, want := range map[string]struct {
		code int
		err  string
	}{"qoder": {501, "model_discovery_unsupported"}, "openai": {502, "model_discovery_failed"}} {
		node, _ := builtinConnection(provider, routing.ProviderSettings{UserID: "u", MachineID: "m"}, credentials.AuthAPIKey)
		stored, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider, Transports: node.Transports})
		if err != nil {
			t.Fatal(err)
		}
		token := "tok"
		if provider == "qoder" {
			token = "pt-secret"
		}
		connection, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: stored.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: token}})
		if err != nil {
			t.Fatal(err)
		}
		w := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+connection.ID+"/discover", "")
		if w.Code != want.code || !strings.Contains(w.Body.String(), want.err) || strings.Contains(w.Body.String(), "pt-secret") {
			t.Fatalf("%s: %d %s", provider, w.Code, w.Body)
		}
	}
	if upstream != 1 {
		t.Fatalf("upstream calls %d: the tracked gap must send nothing", upstream)
	}
}

// kenari's registry row declares its catalog endpoint as GET /v1/models
// (open-sse/providers/registry/kenari.js:23 validateUrl, with the bearer that
// row's openai transport sends, kenari.js:44). The serving base carries no /v1, so before
// #370 Discover requested https://kenari.id/models, which answers the marketing
// page rather than the catalog.
func TestKenariListingIsThePinsValidateURL(t *testing.T) {
	models, call, err := listProvider(t, "kenari", credentials.AuthAPIKey, routing.ProviderSettings{}, "tok",
		`{"data":[{"id":"gpt-5-5","endpoints":["chat"]}]}`)
	if err != nil || ids(models) != "gpt-5-5" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if call.URL != "https://kenari.id/v1/models" || call.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("kenari listing went to %s with %v", call.URL, call.Header)
	}
}
