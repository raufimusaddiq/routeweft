package ingress

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// P08: streaming and non-streaming on every native transport a served provider
// declares that TestRegistryChatWires (Chat only) does not already walk. The
// expected upstream path is the catalog's own base plus the transport endpoint
// (parity.TestEveryOracleTransportURLMatches compares that catalog URL to the
// pin); this test proves the dispatcher actually posts there with the
// credential header the transport uses, forwards unknown fields, and relays the
// stream and the JSON body unchanged. It is per-provider wiring evidence on the
// shared path, not evidence of any provider-specific behaviour.

// nativeWireSkips are served providers whose request URL or body depends on the
// connection or the model, so a table row cannot stand for them. Each has its
// own test.
var nativeWireSkips = map[string]string{
	"zed":              "LLM-token exchange, thread envelope and NDJSON frames (TestZedIsServedThroughItsExchangeAndEnvelope)",
	"mimo-free":        "bootstrap JWT, headers and the MiMoCode marker (TestMimoFreeAndMmfAreServedWithABootstrappedJWT)",
	"mmf":              "the same executor under its alias (TestMimoFreeAndMmfAreServedWithABootstrappedJWT)",
	"azure":            "deployment path and api-version from connection settings (TestAzureRequestShape)",
	"cloudflare-ai":    "account-scoped URL from connection settings (wire.TestCloudflareTargetURLs)",
	"vertex-partner":   "project-scoped URL from the credential or connection settings, a probed project for a raw key (wire.TestVertexPartnerProjectPrecedence, TestVertexPartnerRawKeyIsProbedAndServed)",
	"xiaomi-tokenplan": "region host from connection settings (TestTokenplanRoutesPerWire)",
	"opencode":         "model-dependent Zen target and forced stream (TestOpenCodeForcedStreamFoldsToOneJSONBody)",
	"api-airforce":     "forced stream (TestAirforceForcedStreamAcrossClientProtocols)",
	"codebuddy-cn":     "forced stream and envelope quirks (codebuddy_test.go)",
	"codebuddy-intl":   "forced stream and envelope quirks (codebuddy_test.go)",
	"cline":            "response envelope (TestClineEnvelopeFixtureIsUnwrappedAndAccounted)",
	"clinepass":        "response envelope (TestClineEnvelopeFixtureIsUnwrappedAndAccounted)",
	"kenari":           "Responses function-tool rewrite on all three wires (internal/app TestConfiguredKenariNativeWire)",
	"codex":            "forced stream, store=false and the field allowlist rewrite the body (responses_forced_stream_test.go)",
	"grok-cli":         "forced stream, store=false and the field allowlist rewrite the body (responses_forced_stream_test.go)",
}

type nativeWire struct {
	transport      registry.Protocol
	clientPath     string
	protocol       string
	request        string
	jsonReply      string
	streamReply    string
	defaultSegment string
}

var nativeWires = []nativeWire{
	{registry.TransportOpenAIChat, "/v1/chat/completions", "openai-chat", `{"model":"m","messages":[{"role":"user","content":"hi"}],"future_field":"keep"%s}`,
		`{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n", "chat/completions"},
	{registry.TransportOpenAIResponses, "/v1/responses", "openai-responses", `{"model":"m","input":"hi","future_field":"keep"%s}`,
		`{"id":"r1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`,
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n", "responses"},
	{registry.TransportAnthropic, "/v1/messages", "anthropic-messages", `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"future_field":"keep"%s}`,
		`{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "messages"},
}

// chatWiresWalked are the providers TestRegistryChatWires already covers on Chat.
func chatWiresWalked() map[string]bool {
	walked := map[string]bool{}
	for _, wire := range registryChatWires {
		walked[wire.id] = true
	}
	return walked
}

func TestRegistryNativeTransportWires(t *testing.T) {
	walked := chatWiresWalked()
	covered := 0
	for _, spec := range registry.Builtins() {
		if !spec.Readiness.Served() {
			continue
		}
		if _, skip := nativeWireSkips[spec.ID]; skip {
			continue
		}
		for _, wire := range nativeWires {
			if !hasTransport(spec, wire.transport) {
				continue
			}
			if wire.transport == registry.TransportOpenAIChat && walked[spec.ID] {
				continue
			}
			for _, stream := range []bool{false, true} {
				covered++
				t.Run(fmt.Sprintf("%s/%s/stream=%v", spec.ID, wire.transport, stream), func(t *testing.T) {
					runNativeWire(t, spec, wire, stream)
				})
			}
		}
	}
	if covered == 0 {
		t.Fatal("no provider/transport pair was walked")
	}
}

func hasTransport(spec registry.Spec, transport registry.Protocol) bool {
	for _, candidate := range spec.Transports {
		if candidate == transport {
			return true
		}
	}
	return false
}

func runNativeWire(t *testing.T, spec registry.Spec, wire nativeWire, stream bool) {
	t.Helper()
	endpoint, ok := spec.EndpointFor(wire.transport)
	if !ok {
		endpoint = wire.defaultSegment
	}
	catalogBase, err := url.Parse(spec.DefaultBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := strings.TrimRight(catalogBase.Path, "/") + "/" + strings.TrimLeft(endpoint, "/")

	var mu sync.Mutex
	var seenPath, seenBody string
	var seenHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		seenPath, seenBody, seenHeader = r.URL.Path, string(raw), r.Header.Clone()
		mu.Unlock()
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, wire.streamReply)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, wire.jsonReply)
	}))
	defer upstream.Close()

	ref := routing.ProviderRef{ProviderID: spec.ID, Protocol: wire.protocol, APIToken: "fixture-key", BaseURL: upstream.URL + catalogBase.Path, Endpoint: endpoint, UpstreamModel: "wire-model"}
	flag := ""
	if stream {
		flag = `,"stream":true`
	}
	out := send(t, ref, wire.clientPath, strings.Replace(wire.request, "%s", flag, 1))
	if out.Code != http.StatusOK {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if seenPath != wantPath {
		t.Errorf("posted to %s, want %s", seenPath, wantPath)
	}
	if !strings.Contains(seenBody, `"future_field":"keep"`) || !strings.Contains(seenBody, `"model":"wire-model"`) {
		t.Errorf("upstream body %s lost a field or the upstream model", seenBody)
	}
	bearerWire := wire.transport != registry.TransportAnthropic || spec.ID == "claude" || spec.ID == "github"
	switch {
	case bearerWire && seenHeader.Get("Authorization") != "Bearer fixture-key":
		t.Errorf("headers %v: want bearer credential", seenHeader)
	case !bearerWire && (seenHeader.Get("X-Api-Key") != "fixture-key" || seenHeader.Get("Authorization") != ""):
		t.Errorf("headers %v: want x-api-key credential only", seenHeader)
	}
	if stream {
		if got := out.Body.String(); got != wire.streamReply {
			t.Errorf("stream altered:\n got %q\nwant %q", got, wire.streamReply)
		}
		return
	}
	if got := out.Body.String(); got != wire.jsonReply {
		t.Errorf("body altered:\n got %q\nwant %q", got, wire.jsonReply)
	}
}

// OpenCode answers only streams (registry/opencode.js:20 forceStream). A client
// that did not ask to stream gets one JSON body folded from the SSE, a client
// that did gets the stream, and the upstream always sees stream:true.
func TestOpenCodeForcedStreamFoldsToOneJSONBody(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			rig := newCodeBuddyRig(t, "opencode", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/acct/zen/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("wire %s %v", r.URL.Path, r.Header)
				}
				sseUpstream(codebuddySSE)(w, r)
			})
			out := rig.post("/v1/chat/completions", fmt.Sprintf(`{"model":"x","messages":[{"role":"user","content":"ping"}],"stream":%v}`, stream))
			if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") {
				t.Fatalf("%d %s", out.Code, out.Body)
			}
			if stream != strings.HasPrefix(strings.TrimSpace(out.Body.String()), "data:") {
				t.Fatalf("client asked stream=%v and got %q", stream, out.Body)
			}
			rig.mu.Lock()
			defer rig.mu.Unlock()
			if len(rig.seen) != 1 || rig.seen[0]["stream"] != true {
				t.Fatalf("upstream body %v", rig.seen)
			}
		})
	}
}

// Azure and the Xiaomi Token Plan resolve their URL from the connection, so the
// table above cannot walk them. They post to the connection's own host, which
// lets the same stream and JSON checks run against a local upstream. (Cloudflare
// AI and Vertex partner build an absolute vendor host and are covered at the
// URL seam only; see docs/P08_TEST_MATRIX.md.)
func TestConnectionScopedTargetsStreamAndNonStream(t *testing.T) {
	azureSettings := routing.ProviderSettings{Deployment: "dep1", APIVersion: "2025-01-01"}
	for _, tc := range []struct {
		name, provider string
		wire           nativeWire
		settings       routing.ProviderSettings
		basePath       string
		wantPath       string
		credential     func(http.Header) bool
	}{
		{"azure chat", "azure", nativeWires[0], azureSettings, "/azure", "/azure/openai/deployments/dep1/chat/completions",
			func(h http.Header) bool { return h.Get("Api-Key") == "fixture-key" && h.Get("Authorization") == "" }},
		{"tokenplan chat", "xiaomi-tokenplan", nativeWires[0], routing.ProviderSettings{}, "/v1", "/v1/chat/completions",
			func(h http.Header) bool { return h.Get("Authorization") == "Bearer fixture-key" }},
		{"tokenplan messages", "xiaomi-tokenplan", nativeWires[2], routing.ProviderSettings{}, "/v1", "/anthropic/v1/messages",
			func(h http.Header) bool { return h.Get("X-Api-Key") == "fixture-key" }},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				var mu sync.Mutex
				var path string
				var header http.Header
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					path, header = r.URL.Path, r.Header.Clone()
					mu.Unlock()
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, tc.wire.streamReply)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, tc.wire.jsonReply)
				}))
				defer upstream.Close()
				ref := routing.ProviderRef{ProviderID: tc.provider, Protocol: tc.wire.protocol, APIToken: "fixture-key", BaseURL: upstream.URL + tc.basePath, UpstreamModel: "gpt-4o",
					Connection: routing.ConnectionMetadata{Settings: tc.settings}}
				flag, want := "", tc.wire.jsonReply
				if stream {
					flag, want = `,"stream":true`, tc.wire.streamReply
				}
				out := send(t, ref, tc.wire.clientPath, strings.Replace(tc.wire.request, "%s", flag, 1))
				mu.Lock()
				defer mu.Unlock()
				if out.Code != http.StatusOK || out.Body.String() != want {
					t.Fatalf("status %d body %q, want the upstream reply unchanged", out.Code, out.Body)
				}
				if path != tc.wantPath || !tc.credential(header) {
					t.Fatalf("posted %s with %v, want %s", path, header, tc.wantPath)
				}
			})
		}
	}
}
