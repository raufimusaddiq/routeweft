package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// Gemini and Ollama clients are served by providers that speak their own protocol
// (there is no translation between these and the others), so these tests give
// every model its own provider with that transport and an upstream that answers in
// that protocol.
type nativeProtocol struct {
	name       string
	transport  string
	baseSuffix string
	request    func(model, extra string, turns []string) (path, body string)
	answer     func(text string) string
	final      string
	streamed   func(streaming bool) string // fragment that marks a streaming body or path
}

var (
	geminiProtocol = nativeProtocol{
		name: "gemini", transport: "gemini",
		answer: func(text string) string {
			return fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":%q}]}}]}`, text)
		},
	}
	ollamaProtocol = nativeProtocol{
		name: "ollama", transport: "ollama", baseSuffix: "/v1",
		answer: func(text string) string {
			return fmt.Sprintf(`{"model":"m","message":{"role":"assistant","content":%q},"done":true}`, text)
		},
	}
)

// nativeApp builds an app whose providers speak one native protocol. caps gives a
// model the capabilities it declares.
func nativeApp(t *testing.T, upstream *upstreamLog, protocol nativeProtocol, models []string, caps map[string][]string) (*App, string) {
	t.Helper()
	server := httptest.NewServer(upstream.handler())
	t.Cleanup(server.Close)
	a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true}, nil)
	ctx := context.Background()
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() })
	_, key, err := a.runtime.CreateAPIKey(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		provider := "p" + model
		node, err := a.creds.PutNode(ctx, credentials.Node{ID: "node-" + provider, Kind: credentials.NodeGeneric, ProviderID: provider, Name: provider, Prefix: provider, BaseURL: server.URL + protocol.baseSuffix, Transports: []string{protocol.transport}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.creds.PutConnection(ctx, credentials.Connection{ID: "account-" + provider, NodeID: node.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{AccessToken: "synthetic-" + provider}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: provider, ID: model, Capabilities: caps[model]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return a, key
}

func geminiTurns(turns int, last string) string {
	var contents []string
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		contents = append(contents, fmt.Sprintf(`{"role":%q,"parts":[{"text":"turn %d"}]}`, role, i))
	}
	return `{"contents":[` + strings.Join(append(contents, last), ",") + `]}`
}

func ollamaTurns(turns int, last, extra string) string {
	var messages []string
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, fmt.Sprintf(`{"role":%q,"content":"turn %d"}`, role, i))
	}
	return `{"model":"panel",` + extra + `"messages":[` + strings.Join(append(messages, last), ",") + `]}`
}

func nativeScript(protocol nativeProtocol) func(string, int) (time.Duration, int, string) {
	return func(model string, call int) (time.Duration, int, string) {
		if model == "j" {
			return 0, 200, protocol.answer("FINAL")
		}
		return 0, 200, protocol.answer("answer from " + model)
	}
}

func setFusion(t *testing.T, a *App, models ...string) {
	t.Helper()
	var members []runtime.ComboMember
	for position, model := range models {
		members = append(members, runtime.ComboMember{ProviderID: "p" + model, ModelID: model, Position: position, Selected: true})
	}
	ctx := context.Background()
	if _, err := a.runtime.SetCombos(ctx, []runtime.Combo{{ID: "f", Name: "panel", Members: members}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.SetSettings(ctx, map[string]string{"comboStrategies": `{"panel":{"fallbackStrategy":"fusion","judgeModel":"pj/j"}}`}, nil); err != nil {
		t.Fatal(err)
	}
}

func bodyOf(t *testing.T, call upstreamCall) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(call.body), &body); err != nil {
		t.Fatalf("%s: upstream body is not JSON: %v", call.model, err)
	}
	return body
}

// A Gemini client's Fusion request: the panel is asked with generateContent and no
// tools, the judge with the method the client used, and the request body names no
// model (Gemini names it in the path).
func TestFusionThroughTheGeminiEntryPoint(t *testing.T) {
	for _, method := range []string{"generateContent", "streamGenerateContent"} {
		t.Run(method, func(t *testing.T) {
			upstream := &upstreamLog{script: nativeScript(geminiProtocol)}
			a, key := nativeApp(t, upstream, geminiProtocol, []string{"a", "b", "j"}, nil)
			setFusion(t, a, "a", "b")
			body := `{"tools":[{"functionDeclarations":[{"name":"f"}]}],"toolConfig":{},"contents":[{"role":"user","parts":[{"text":"question"}]}]}`
			w := serveRequest(a.Handler(), key, "/v1beta/models/panel:"+method, body)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "FINAL") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			upstream.mu.Lock()
			defer upstream.mu.Unlock()
			if len(upstream.calls) != 3 {
				t.Fatalf("calls %v", callNames(upstream.calls))
			}
			for index, call := range upstream.calls {
				sent := bodyOf(t, call)
				judge := call.model == "j"
				wantMethod := "generateContent"
				if judge {
					wantMethod = method
				}
				if !strings.HasSuffix(upstream.paths[index], "/models/"+call.model+":"+wantMethod) {
					t.Errorf("%s was called at %s, want .../models/%s:%s", call.model, upstream.paths[index], call.model, wantMethod)
				}
				_, hasTools := sent["tools"]
				_, hasToolConfig := sent["toolConfig"]
				if judge == false && (hasTools || hasToolConfig) {
					t.Errorf("%s: panel call carries tools", call.model)
				}
				if judge && (!hasTools || !hasToolConfig) {
					t.Errorf("judge call lost the client's tools")
				}
				contents := sent["contents"].([]any)
				last := contents[len(contents)-1].(map[string]any)
				text := last["parts"].([]any)[0].(map[string]any)["text"].(string)
				if judge != strings.Contains(text, "=== PANEL RESPONSES ===") {
					t.Errorf("%s: judge prompt present=%v in %q", call.model, !judge, text)
				}
			}
		})
	}
}

// An Ollama client's Fusion request: panel calls are not streamed, the judge call
// keeps the client's stream flag.
func TestFusionThroughTheOllamaEntryPoint(t *testing.T) {
	upstream := &upstreamLog{script: nativeScript(ollamaProtocol)}
	a, key := nativeApp(t, upstream, ollamaProtocol, []string{"a", "b", "j"}, nil)
	setFusion(t, a, "a", "b")
	w := serveRequest(a.Handler(), key, "/v1/api/chat", ollamaTurns(2, `{"role":"user","content":"question"}`, `"stream":true,"tools":[{"type":"function","function":{"name":"f"}}],`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "FINAL") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.calls) != 3 {
		t.Fatalf("calls %v", callNames(upstream.calls))
	}
	for _, call := range upstream.calls {
		sent := bodyOf(t, call)
		streamed, _ := sent["stream"].(bool)
		_, hasTools := sent["tools"]
		if judge := call.model == "j"; streamed != judge || hasTools != judge {
			t.Errorf("%s: stream=%v tools=%v (the judge call keeps both, panel calls have neither)", call.model, streamed, hasTools)
		}
	}
}

// A model the gateway added has its history fitted on the Gemini and Ollama entry
// points too: the handler's swap of the parsed request works for them.
func TestAdapterHistoryFittingThroughTheGeminiAndOllamaEntryPoints(t *testing.T) {
	for _, tc := range []struct {
		protocol nativeProtocol
		path     string
		body     string
		count    func(map[string]any) int
	}{
		{geminiProtocol, "/v1beta/models/texty:generateContent", geminiTurns(10, `{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}`), func(b map[string]any) int { return len(b["contents"].([]any)) }},
		{ollamaProtocol, "/v1/api/chat", ollamaTurns(10, `{"role":"user","content":"look","images":["AAAA"]}`, `"stream":false,`), func(b map[string]any) int { return len(b["messages"].([]any)) }},
	} {
		t.Run(tc.protocol.name, func(t *testing.T) {
			upstream := &upstreamLog{script: nativeScript(tc.protocol)}
			a, key := nativeApp(t, upstream, tc.protocol, []string{"text-a", "also-sees"}, map[string][]string{"also-sees": {"vision"}})
			ctx := context.Background()
			if _, err := a.runtime.SetCombos(ctx, []runtime.Combo{{ID: "c", Name: "texty", Members: []runtime.ComboMember{{ProviderID: "ptext-a", ModelID: "text-a", Selected: true}}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.runtime.SetSettings(ctx, map[string]string{"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"palso-sees","modelId":"also-sees"}]}`}, nil); err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(tc.body, `"model":"panel"`, `"model":"texty"`, 1)
			w := serveRequest(a.Handler(), key, tc.path, body)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			upstream.mu.Lock()
			defer upstream.mu.Unlock()
			if len(upstream.calls) != 1 || upstream.calls[0].model != "also-sees" {
				t.Fatalf("calls %v, want only the adapter model", callNames(upstream.calls))
			}
			if got := tc.count(bodyOf(t, upstream.calls[0])); got != 7 {
				t.Fatalf("adapter model received %d history items, want 7 (six older turns and the current one)", got)
			}
		})
	}
}

// anthropicProtocol is a provider that speaks Anthropic Messages natively, whose
// usage excludes the cache counts.
var anthropicProtocol = nativeProtocol{
	name: "anthropic", transport: "anthropic-messages", baseSuffix: "/v1",
	answer: func(text string) string {
		return fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":400,"cache_creation_input_tokens":0}}`, text)
	},
}
