package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// A probe, a model listing and a route listing take the base, target, query and
// headers serving would use, from the same seam (wire.NodeBase / ResolveTarget),
// not from their own reading of the node and the catalog default.

type recordedCall struct {
	URL    string
	Header http.Header
	Body   string
}

func recordingHandler(t *testing.T) (*Handler, func() []recordedCall) {
	t.Helper()
	h := New(Options{Providers: registry.Builtins()})
	var mu sync.Mutex
	var calls []recordedCall
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		mu.Lock()
		calls = append(calls, recordedCall{r.URL.String(), r.Header.Clone(), string(raw)})
		mu.Unlock()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"m1"}]}`))}, nil
	})
	return h, func() []recordedCall { mu.Lock(); defer mu.Unlock(); return append([]recordedCall(nil), calls...) }
}

func builtinConnection(provider string, settings routing.ProviderSettings, auth credentials.AuthKind) (credentials.Node, credentials.Connection) {
	catalog, _ := registry.NewBuiltinCatalog()
	spec, _ := catalog.Lookup(provider)
	transports := make([]string, 0, len(spec.Transports))
	for _, transport := range spec.Transports {
		transports = append(transports, string(transport))
	}
	node := credentials.Node{ID: provider, Kind: credentials.NodeBuiltin, ProviderID: provider, Transports: transports}
	connection := credentials.Connection{ID: provider + "-c", NodeID: provider, ProviderID: provider, AuthKind: auth, Enabled: true, Settings: settings,
		Secret: credentials.Secret{AccessToken: "tok"}}
	return node, connection
}

func TestProbesTakeServingsTargetAndHeaders(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	cases := []struct {
		name, provider, transport string
		settings                  routing.ProviderSettings
		wantURL                   string
		wantHeaders               map[string]string
		wantNoHeaders             []string
	}{
		{"azure deployment url and api-key", "azure", "openai-chat",
			routing.ProviderSettings{AzureEndpoint: "https://acme.openai.azure.com/", Deployment: "d1", APIVersion: "2025-01-01", Organization: "org-1"},
			"https://acme.openai.azure.com/openai/deployments/d1/chat/completions?api-version=2025-01-01",
			map[string]string{"Api-Key": "tok", "OpenAI-Organization": "org-1"}, []string{"Authorization"}},
		{"kimi messages carries ?beta=true and the device", "kimi", "anthropic-messages",
			routing.ProviderSettings{DeviceID: "dev-9"},
			"https://api.kimi.com/coding/v1/messages?beta=true",
			map[string]string{"X-Msh-Device-Id": "dev-9", "X-Api-Key": "tok"}, nil},
		{"kimi chat has no query", "kimi", "openai-chat", routing.ProviderSettings{},
			"https://api.kimi.com/coding/v1/chat/completions", map[string]string{"X-Msh-Device-Id": "routeweft"}, nil},
		{"tokenplan region host and messages path", "xiaomi-tokenplan", "anthropic-messages",
			routing.ProviderSettings{Region: " cn "},
			"https://token-plan-cn.xiaomimimo.com/anthropic/v1/messages", map[string]string{"X-Api-Key": "tok"}, nil},
		{"tokenplan chat", "xiaomi-tokenplan", "openai-chat", routing.ProviderSettings{Region: "ams"},
			"https://token-plan-ams.xiaomimimo.com/v1/chat/completions", nil, nil},
		{"kilocode organization header", "kilocode", "openai-chat", routing.ProviderSettings{OrgID: "org-7"},
			"https://api.kilo.ai/api/openrouter/chat/completions", map[string]string{"X-Kilocode-Organizationid": "org-7"}, nil},
		{"glm messages", "glm", "anthropic-messages", routing.ProviderSettings{},
			"https://api.z.ai/api/anthropic/v1/messages?beta=true", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, calls := recordingHandler(t)
			spec, _ := catalog.Lookup(c.provider)
			node, connection := builtinConnection(c.provider, c.settings, credentials.AuthAPIKey)
			result := h.testProviderModel(context.Background(), connection, node, spec, c.transport, "model-x", time.Second)
			got := calls()
			if len(got) != 1 {
				t.Fatalf("probe made %d requests (result %+v)", len(got), result)
			}
			if got[0].URL != c.wantURL {
				t.Fatalf("probe URL %s\nwant      %s", got[0].URL, c.wantURL)
			}
			for name, want := range c.wantHeaders {
				if got[0].Header.Get(name) != want {
					t.Errorf("header %s = %q want %q (%v)", name, got[0].Header.Get(name), want, got[0].Header)
				}
			}
			for _, name := range c.wantNoHeaders {
				if got[0].Header.Get(name) != "" {
					t.Errorf("header %s must not be sent: %v", name, got[0].Header)
				}
			}
		})
	}
}

// Azure declares no base URL: with none on the connection the probe says so and
// sends nothing; it must not fail with a generic outbound-policy error on "".
func TestAzureProbeWithoutAnEndpointSendsNothing(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	spec, _ := catalog.Lookup("azure")
	h, calls := recordingHandler(t)
	node, connection := builtinConnection("azure", routing.ProviderSettings{Deployment: "d1"}, credentials.AuthAPIKey)
	result := h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", "model-x", time.Second)
	if result.OK || len(calls()) != 0 || strings.Contains(result.Error, "outbound network policy") || result.Error == "" {
		t.Fatalf("result %+v, calls %v", result, calls())
	}
	// A bad deployment is refused the same way.
	node, connection = builtinConnection("azure", routing.ProviderSettings{AzureEndpoint: "https://acme.openai.azure.com", Deployment: "../x"}, credentials.AuthAPIKey)
	if result := h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", "model-x", time.Second); result.OK || len(calls()) != 0 {
		t.Fatalf("bad deployment probed: %+v", result)
	}
}

func TestModelListingsTakeServingsBase(t *testing.T) {
	cases := []struct {
		provider string
		settings routing.ProviderSettings
		wantHost string
	}{
		{"xiaomi-tokenplan", routing.ProviderSettings{Region: "cn"}, "token-plan-cn.xiaomimimo.com"},
		{"xiaomi-tokenplan", routing.ProviderSettings{}, "token-plan-sgp.xiaomimimo.com"},
		{"kimi", routing.ProviderSettings{DeviceID: "dev-9"}, "api.kimi.com"},
	}
	for _, c := range cases {
		t.Run(c.provider+"/"+c.wantHost, func(t *testing.T) {
			h, calls := recordingHandler(t)
			node, connection := builtinConnection(c.provider, c.settings, credentials.AuthAPIKey)
			if _, err := h.fetchConnectionModelEntries(context.Background(), connection, node); err != nil {
				t.Fatal(err)
			}
			got := calls()
			if len(got) != 1 || !strings.Contains(got[0].URL, "//"+c.wantHost+"/") {
				t.Fatalf("listing went to %+v, want host %s", got, c.wantHost)
			}
			if c.provider == "kimi" && got[0].Header.Get("X-Msh-Device-Id") != "dev-9" {
				t.Errorf("listing lacked the connection's device: %v", got[0].Header)
			}
		})
	}
}

func TestRouteListingUsesServingsTargets(t *testing.T) {
	h, mux, _, creds, cookie := newProvidersAPI(t, false, nil)
	h.opts.Providers = registry.Builtins()
	for _, id := range []string{"kimi", "xiaomi-tokenplan", "azure"} {
		node, _ := builtinConnection(id, routing.ProviderSettings{}, credentials.AuthAPIKey)
		if _, err := creds.PutNode(context.Background(), credentials.Node{ID: id, Kind: credentials.NodeBuiltin, ProviderID: id, Name: id, Transports: node.Transports}); err != nil {
			t.Fatal(err)
		}
	}
	body := func(id string) string {
		w := doJSON(t, mux, cookie, "GET", "/admin/v1/provider-nodes/"+id+"/routes", "")
		if w.Code != 200 {
			t.Fatalf("%s routes: %d %s", id, w.Code, w.Body)
		}
		return w.Body.String()
	}
	if got := body("kimi"); !strings.Contains(got, `"anthropic-messages":"https://api.kimi.com/coding/v1/messages?beta=true"`) || !strings.Contains(got, `"openai-responses":"https://api.kimi.com/coding/v1/responses"`) {
		t.Errorf("kimi routes %s", got)
	}
	if got := body("xiaomi-tokenplan"); !strings.Contains(got, `"anthropic-messages":"https://token-plan-sgp.xiaomimimo.com/anthropic/v1/messages"`) || !strings.Contains(got, `"openai-chat":"https://token-plan-sgp.xiaomimimo.com/v1/chat/completions"`) {
		t.Errorf("tokenplan routes %s", got)
	}
	// An Azure route depends on the connection's endpoint and deployment: none to list.
	if got := body("azure"); !strings.Contains(got, `"routes":{}`) {
		t.Errorf("azure routes %s", got)
	}
}

// Azure has no model listing at the pin (registry/azure.js declares no models
// fetcher; validate/route.js validates by a deployment chat request), so listing
// is a clean no-op: no credential read, no request, no error.
func TestAzureModelListingIsANoOp(t *testing.T) {
	h, calls := recordingHandler(t)
	node, connection := builtinConnection("azure", routing.ProviderSettings{AzureEndpoint: "https://acme.openai.azure.com", Deployment: "d1"}, credentials.AuthAPIKey)
	models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
	if err != nil || len(models) != 0 || len(calls()) != 0 {
		t.Fatalf("models=%v err=%v calls=%v", models, err, calls())
	}
}

// The probe sends the body serving would send: wire.FinalizeBody applies to it.
func TestProbeBodyGoesThroughTheRegistryQuirks(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	// github: the o-series and gpt-5 take max_completion_tokens, never max_tokens.
	for model, want := range map[string]bool{"gpt-5": true, "o1": true, "o3-mini": true, "o4": true, "gpt-4o": false, "deepseek-o3-mini": false} {
		h, calls := recordingHandler(t)
		spec, _ := catalog.Lookup("github")
		node, connection := builtinConnection("github", routing.ProviderSettings{}, credentials.AuthOAuth)
		h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", model, time.Second)
		got := calls()
		if len(got) != 1 {
			t.Fatalf("%s: %d requests", model, len(got))
		}
		moved := strings.Contains(got[0].Body, `"max_completion_tokens"`) && !strings.Contains(got[0].Body, `"max_tokens"`)
		if moved != want {
			t.Errorf("github %s: completion tokens moved=%v want %v: %s", model, moved, want, got[0].Body)
		}
	}
}

// For every built-in provider and transport the body a probe sends is a fixed
// point of the seam: running it through wire.FinalizeBody again changes nothing,
// so the probe cannot have skipped a quirk that applies to it.
func TestEveryProbeBodyIsFinalized(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	checked := 0
	for _, spec := range registry.Builtins() {
		for _, transport := range spec.Transports {
			protocol := string(transport)
			switch protocol {
			case "openai-chat", "openai-responses", "anthropic-messages":
			default:
				continue
			}
			h, calls := recordingHandler(t)
			spec, _ := catalog.Lookup(spec.ID)
			node, connection := builtinConnection(spec.ID, routing.ProviderSettings{AzureEndpoint: "https://acme.openai.azure.com"}, credentials.AuthAPIKey)
			for _, model := range []string{"gpt-5", "o3", "claude-sonnet-4.5", "kimi-k2"} {
				h.testProviderModel(context.Background(), connection, node, spec, protocol, model, time.Second)
			}
			for _, call := range calls() {
				route := routing.ProviderRef{ProviderID: spec.ID, Protocol: protocol}
				if again := string(wire.FinalizeBody(route, []byte(call.Body))); again != call.Body {
					t.Errorf("%s/%s probe body is not final:\n sent %s\n final %s", spec.ID, protocol, call.Body, again)
				}
				checked++
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d probe bodies checked; the table is not exercising the catalog", checked)
	}
}

func TestQoderProbeIsUnavailableUntilConversionLegExists(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	spec, _ := catalog.Lookup("qoder")
	h, calls := recordingHandler(t)
	node, connection := builtinConnection("qoder", routing.ProviderSettings{UserID: "fixture-user", MachineID: "stored-machine"}, credentials.AuthOAuth)
	if len(node.Transports) != 1 || node.Transports[0] != string(registry.TransportQoder) {
		t.Fatalf("qoder transports changed: %v", node.Transports)
	}
	result := h.testProviderModel(context.Background(), connection, node, spec, node.Transports[0], "model-x", time.Second)
	if result.OK || result.Error != "model probe is not implemented for this specialized provider transport" || len(calls()) != 0 {
		t.Fatalf("qoder probe unexpectedly reached dispatch: %+v, calls %v", result, calls())
	}
}

func TestForcedStreamProbesUseTheServingShape(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	for _, tc := range []struct{ provider, model, protocol, path string }{
		{"opencode-go", "gpt-5.6-luna", "openai-chat", "/responses"},
		{"opencode-go", "grok-4.6", "anthropic-messages", "/responses"},
		{"opencode-go", "muse-spark-1.2-contributor", "openai-responses", "/responses"},
		{"opencode-go", "muse-spark-1.3-contributor", "openai-chat", "/responses"},
	} {
		h, calls := recordingHandler(t)
		spec, _ := catalog.Lookup(tc.provider)
		node, connection := builtinConnection(tc.provider, routing.ProviderSettings{}, credentials.AuthAPIKey)
		h.testProviderModel(context.Background(), connection, node, spec, tc.protocol, tc.model, time.Second)
		got := calls()
		if len(got) != 1 || !strings.HasSuffix(got[0].URL, tc.path) || !strings.Contains(got[0].Body, `"stream":true`) {
			t.Fatalf("%s/%s: %+v", tc.provider, tc.model, got)
		}
		if tc.path == "/responses" && (!strings.Contains(got[0].Body, `"input"`) || strings.Contains(got[0].Body, `"messages"`)) {
			t.Fatalf("wrong protocol: %s", got[0].Body)
		}
	}
	h, calls := recordingHandler(t)
	spec, _ := catalog.Lookup("opencode-go")
	node, connection := builtinConnection("opencode-go", routing.ProviderSettings{}, credentials.AuthAPIKey)
	node.Transports = []string{"openai-chat"}
	result := h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", "gpt-5.6-luna", time.Second)
	if result.OK || len(calls()) != 0 || result.UpstreamModelID != "" || result.Error != "model requires the Responses transport" {
		t.Fatalf("%+v %+v", result, calls())
	}
}

// A Zed probe tests the request serving would send: the LLM-token exchange, then
// Zed's envelope at /completions, and the NDJSON stream read back as Chat. Before
// the exchange existed it posted the raw access token to /chat/completions.
func TestZedProbeTakesTheExchangeAndTheEnvelope(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	spec, _ := catalog.Lookup("zed")
	h, _ := recordingHandler(t)
	var mu sync.Mutex
	var calls []recordedCall
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		mu.Lock()
		calls = append(calls, recordedCall{r.URL.String(), r.Header.Clone(), string(raw)})
		mu.Unlock()
		reply := func(contentType, body string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		switch r.URL.Path {
		case "/client/llm_tokens":
			return reply("application/json", `{"token":"llm-fixture"}`)
		case "/models":
			return reply("application/json", `{"models":[{"id":"claude-sonnet-4.5","provider":"anthropic"}]}`)
		}
		return reply("application/x-ndjson", `{"event":{"type":"message_start","message":{"id":"m","role":"assistant","model":"claude-sonnet-4.5","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}}
{"event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}
{"event":{"type":"content_block_stop","index":0}}
{"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}
{"event":{"type":"message_stop"}}
{"status":"stream_ended"}
`)
	})
	node, connection := builtinConnection("zed", routing.ProviderSettings{UserID: "user-1", OrganizationID: "org-1"}, credentials.AuthOAuth)
	result := h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", "claude-sonnet-4.5", 5*time.Second)
	if !result.OK {
		t.Fatalf("%+v", result)
	}
	var paths []string
	for _, call := range calls {
		paths = append(paths, strings.TrimPrefix(call.URL, "https://cloud.zed.dev"))
	}
	if strings.Join(paths, " ") != "/client/llm_tokens /models /completions" {
		t.Fatalf("calls %v", paths)
	}
	last := calls[len(calls)-1]
	if last.Header.Get("Authorization") != "Bearer llm-fixture" || !strings.Contains(last.Body, `"provider":"anthropic"`) || !strings.Contains(last.Body, `"provider_request"`) {
		t.Errorf("completions %v %s", last.Header, last.Body)
	}
	// Without the user id the exchange needs, the probe says so and sends nothing.
	calls = nil
	node, connection = builtinConnection("zed", routing.ProviderSettings{}, credentials.AuthOAuth)
	result = h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", "claude-sonnet-4.5", 5*time.Second)
	if result.OK || len(calls) != 0 || !strings.Contains(result.Error, "user id") {
		t.Errorf("%+v after %d calls", result, len(calls))
	}
}

// Only a credential exchange changes how a probe is sent. The providers whose Do
// reshapes sending (Cursor, Codex, CommandCode, Grok CLI) are probed as before: one
// plain request, with no retry and no synthesized answer in between.
func TestProbesOfProvidersWithAReshapingDoStayPlain(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	for _, id := range []string{"cursor", "codex", "commandcode", "grok-cli"} {
		spec, _ := catalog.Lookup(id)
		for _, transport := range spec.Transports {
			protocol := string(transport)
			switch protocol {
			case "openai-chat", "openai-responses", "anthropic-messages":
			default:
				continue
			}
			t.Run(id+"/"+protocol, func(t *testing.T) {
				h, calls := recordingHandler(t)
				node, connection := builtinConnection(id, routing.ProviderSettings{}, credentials.AuthOAuth)
				h.testProviderModel(context.Background(), connection, node, spec, protocol, "model-x", time.Second)
				got := calls()
				if len(got) != 1 {
					t.Fatalf("%d calls: %+v", len(got), got)
				}
			})
		}
	}
}

// MiMo Free's probe takes the bootstrap, and sends the marker and the JWT.
func TestMimoFreeProbeTakesTheBootstrap(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	spec, _ := catalog.Lookup("mimo-free")
	h, _ := recordingHandler(t)
	var mu sync.Mutex
	var calls []recordedCall
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		mu.Lock()
		calls = append(calls, recordedCall{r.URL.String(), r.Header.Clone(), string(raw)})
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/bootstrap") {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jwt":"probe.jwt.fixture"}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))}, nil
	})
	node, connection := builtinConnection("mimo-free", routing.ProviderSettings{}, credentials.AuthNone)
	result := h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", "mimo-auto", 5*time.Second)
	if !result.OK || len(calls) != 2 || !strings.HasSuffix(calls[0].URL, "/api/free-ai/bootstrap") || !strings.HasSuffix(calls[1].URL, "/api/free-ai/openai/chat") {
		t.Fatalf("%+v calls %+v", result, calls)
	}
	if calls[1].Header.Get("Authorization") != "Bearer probe.jwt.fixture" || !strings.Contains(calls[1].Body, "You are MiMoCode") {
		t.Errorf("chat call %v %s", calls[1].Header, calls[1].Body)
	}
}

type probeRuntime struct{ state *runtime.RuntimeState }

func (r probeRuntime) Load() (*runtime.RuntimeSnapshot, error) {
	return &runtime.RuntimeSnapshot{}, nil
}
func (r probeRuntime) State() *runtime.RuntimeState { return r.state }

// A Zed account that answers a failed status is not a reachable model: the probe
// reports the failure with the upstream's message and cools the account, as serving
// does.
func TestZedProbeOfAFailedAccountIsNotReachableAndCoolsIt(t *testing.T) {
	catalog, _ := registry.NewBuiltinCatalog()
	spec, _ := catalog.Lookup("zed")
	for _, tc := range []struct {
		message string
		cooled  time.Duration
	}{
		{"monthly quota exhausted", 5 * time.Minute},
		{"rate limit exceeded", routing.DefaultFallbackBackoff},
	} {
		t.Run(tc.message, func(t *testing.T) {
			h, _ := recordingHandler(t)
			state := runtime.NewState()
			h.opts.Runtime = probeRuntime{state}
			h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
				reply := func(contentType, body string) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				switch r.URL.Path {
				case "/client/llm_tokens":
					return reply("application/json", `{"token":"llm-fixture"}`)
				case "/models":
					return reply("application/json", `{"models":[{"id":"claude-sonnet-4.5","provider":"anthropic"}]}`)
				}
				return reply("application/x-ndjson", fmt.Sprintf(`{"status":{"failed":{"message":%q}}}`+"\n", tc.message))
			})
			node, connection := builtinConnection("zed", routing.ProviderSettings{UserID: "user-1", OrganizationID: "org-1"}, credentials.AuthOAuth)
			result := h.testProviderModel(context.Background(), connection, node, spec, "openai-chat", "claude-sonnet-4.5", 5*time.Second)
			if result.OK || result.Classification == "reachable" || result.ServedModelID != "" || result.Status != http.StatusBadGateway ||
				!strings.Contains(result.Error, tc.message) {
				t.Fatalf("a failed account probed as healthy: %+v", result)
			}
			until, cooling := state.CooldownUntil(connection.ID, time.Now())
			if !cooling || until.Sub(time.Now()) < tc.cooled-5*time.Second || until.Sub(time.Now()) > tc.cooled+5*time.Second {
				t.Errorf("cooled until %v (cooling=%v), want about %v", until, cooling, tc.cooled)
			}
		})
	}
}
