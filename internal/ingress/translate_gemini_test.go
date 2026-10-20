package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	geminiwire "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// The same exchange as upstreamBodies, as Gemini's generateContent reports it:
// the model says "pong", calls lookup once with id call_1, and stops. The prompt
// is 130 tokens of which 100 were cached, the output 42.
const (
	geminiBuffered = `{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"},{"functionCall":{"id":"call_1","name":"lookup","args":{"city":"Oslo"}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":130,"cachedContentTokenCount":100,"candidatesTokenCount":42,"totalTokenCount":172},"modelVersion":"gemini-x","responseId":"up0001"}`
	geminiStreamed = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]}}],"modelVersion":"gemini-x","responseId":"up0001"}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{"city":"Oslo"}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":130,"cachedContentTokenCount":100,"candidatesTokenCount":42,"totalTokenCount":172},"modelVersion":"gemini-x","responseId":"up0001"}

`
)

type geminiExchange struct {
	sync.Mutex
	path, auth, googKey string
	query               string
	body                map[string]any
}

func serveGemini(t *testing.T, client string, stream bool, translate Options, accounts ...string) (*httptest.ResponseRecorder, *geminiExchange, []RequestOutcome, string) {
	t.Helper()
	seen := &geminiExchange{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Lock()
		defer seen.Unlock()
		seen.path, seen.auth, seen.googKey, seen.query = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Goog-Api-Key"), r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		seen.body = map[string]any{}
		_ = json.Unmarshal(raw, &seen.body)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, geminiStreamed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, geminiBuffered)
	}))
	t.Cleanup(upstream.Close)
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []RequestOutcome
	var mu sync.Mutex
	if len(accounts) == 0 {
		accounts = []string{"a"}
	}
	translate.AllowPrivateUpstreams = true
	translate.Candidates = func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
		var list []routing.Account
		for _, id := range accounts {
			list = append(list, routing.Account{ID: id, Enabled: true})
		}
		return list, true
	}
	translate.AccountProvider = func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: "gemini", ConnectionID: account, BaseURL: upstream.URL, Protocol: geminiwire.Protocol, UpstreamModel: "gemini-x", APIToken: "secret"}, true
	}
	translate.OnRequestComplete = func(outcome RequestOutcome) { mu.Lock(); outcomes = append(outcomes, outcome); mu.Unlock() }
	mux := http.NewServeMux()
	New(manager, translate).Attach(mux)
	request := clientRequests[client]
	r := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(fmt.Sprintf(request.body, stream)))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	mu.Lock()
	defer mu.Unlock()
	return w, seen, append([]RequestOutcome(nil), outcomes...), key
}

func allTranslators() Options {
	return Options{TranslateChat: TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages}
}

// Every client wire reaches a Gemini provider, buffered and streamed, with the
// provider's own credential in its own header, the model in the path, a Gemini
// body, the client's tool id, text, finish reason and usage intact, and exactly
// one usage record carrying what the upstream reported.
func TestGeminiServesEveryConvertedClient(t *testing.T) {
	for _, client := range []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				w, seen, outcomes, _ := serveGemini(t, client, stream, allTranslators())
				seen.Lock()
				defer seen.Unlock()
				if w.Code != http.StatusOK {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				wantPath := "/v1beta/models/gemini-x:generateContent"
				if stream {
					wantPath = "/v1beta/models/gemini-x:streamGenerateContent"
				}
				if seen.path != wantPath {
					t.Errorf("upstream path %q want %q", seen.path, wantPath)
				}
				if seen.googKey != "secret" || seen.auth != "" {
					t.Errorf("credential: x-goog-api-key=%q authorization=%q", seen.googKey, seen.auth)
				}
				if seen.body["stream"] != nil || seen.body["messages"] != nil || seen.body["model"] != "gemini-x" {
					t.Errorf("not a Gemini body: %v", seen.body)
				}
				if got := asJSONText(concerns.Items(seen.body["contents"])); !strings.Contains(got, `"text":"ping"`) {
					t.Errorf("contents %s", got)
				}
				assertClientSees(t, client, stream, w.Body.String())
				if len(outcomes) != 1 {
					t.Fatalf("want exactly one usage record, got %d", len(outcomes))
				}
				got := outcomes[0]
				if got.UsageUnobserved || got.Termination != "" || got.InputTokens != 130 || got.CacheRead != 100 || got.OutputTokens != 42 {
					t.Errorf("usage record %+v", got)
				}
			})
		}
	}
}

func asJSONText(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// A request the Gemini wire cannot express is skipped, and the next candidate
// serves it: a model that could end the path never reaches the network.
func TestGeminiTranslationFailureMovesOn(t *testing.T) {
	for _, model := range []string{"", "a/b", "a:b", "a?alt=x", "a#b", "a b", "../x"} {
		if endpoint, err := geminiEndpoint(model, false); err == nil {
			t.Errorf("model %q produced path %q", model, endpoint)
		}
	}
	for model, want := range map[string]string{
		"gemini-2.5-flash":       "v1beta/models/gemini-2.5-flash:generateContent",
		"gemma-4-31b-it":         "v1beta/models/gemma-4-31b-it:generateContent",
		"gemini-3.1-pro-preview": "v1beta/models/gemini-3.1-pro-preview:generateContent",
	} {
		if got, err := geminiEndpoint(model, false); err != nil || got != want {
			t.Errorf("%s: %q %v", model, got, err)
		}
	}
	if got, _ := geminiEndpoint("m", true); got != "v1beta/models/m:streamGenerateContent" {
		t.Errorf("stream endpoint %q", got)
	}
}

// Gemini keeps a thought signature per conversation, and a signature is a
// client's model state. The scope that keys it names the authenticated client
// key and the provider connection, so two clients, or one client on two
// connections, never share an entry.
func TestTranslationScopeSeparatesClientsAndConnections(t *testing.T) {
	scopes := map[string]string{}
	var mu sync.Mutex
	capture := Options{
		TranslateChat: func(request *openai.ChatRequest, target string) (ChatTranslation, error) {
			mu.Lock()
			scopes[request.Model+"|"+fmt.Sprint(len(scopes))] = request.Scope
			mu.Unlock()
			return TranslateChat(request, target)
		},
	}
	_, _, _, firstKey := serveGemini(t, openai.ChatProtocol, false, capture, "conn-1", "conn-2")
	_, _, _, secondKey := serveGemini(t, openai.ChatProtocol, false, capture, "conn-1")
	if firstKey == secondKey {
		t.Fatal("test setup: two clients need two keys")
	}
	mu.Lock()
	defer mu.Unlock()
	distinct := map[string]bool{}
	for _, scope := range scopes {
		if scope == "" || !strings.Contains(scope, "conn-") {
			t.Errorf("scope %q does not name a connection", scope)
		}
		distinct[scope] = true
	}
	if len(scopes) != 2 {
		t.Fatalf("expected two translated attempts, got %d: %v", len(scopes), scopes)
	}
	if len(distinct) != 2 {
		t.Errorf("two clients on one connection share a scope: %v", scopes)
	}
}

func TestTranslationScope(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	holder := &attribution{}
	r = r.WithContext(context.WithValue(r.Context(), attributionKey{}, holder))
	provider := routing.ProviderRef{ConnectionID: "c1"}
	if got := translationScope(r, routing.ProviderRef{}); got != "" {
		t.Errorf("neither key nor connection: %q", got)
	}
	noKey := translationScope(r, provider)
	id := "key-1"
	holder.keyID.Store(&id)
	withKey := translationScope(r, provider)
	other := "key-2"
	holder.keyID.Store(&other)
	secondKey := translationScope(r, provider)
	secondConnection := translationScope(r, routing.ProviderRef{ConnectionID: "c2"})
	scopes := map[string]bool{noKey: true, withKey: true, secondKey: true, secondConnection: true}
	if len(scopes) != 4 || noKey == "" {
		t.Errorf("scopes collide: %q %q %q %q", noKey, withKey, secondKey, secondConnection)
	}
	// A key id is not a connection id, whichever way the two are joined.
	idA, idB := "a", "a\x00b"
	holder.keyID.Store(&idA)
	left := translationScope(r, routing.ProviderRef{ConnectionID: "b\x00c"})
	holder.keyID.Store(&idB)
	right := translationScope(r, routing.ProviderRef{ConnectionID: "c"})
	if left == right {
		t.Errorf("(%q, %q) and (%q, %q) share a scope", idA, "b\x00c", idB, "c")
	}
	if translationScope(nil, provider) == "" {
		t.Error("a request with no attribution still has a connection to scope by")
	}
}

// Vertex has conversion legs and no way to be reached: its URL is project-scoped
// and its credential is a service-account token or a key in the query, both of
// which #115 PR-C's target hook supplies (executors/vertex.js:66-123). Until it
// does, a client routed to Vertex would fail at the last hop, so no client wire
// may translate to it. When PR-C binds it, this test is the one to change, along
// with ingress.unwired and TranslateChat's vertex arm.
func TestVertexIsNotTranslatableUntilItsURLAndAuthLand(t *testing.T) {
	if !hub.Has(openai.ChatProtocol, geminiwire.VertexProtocol, hub.Request) || !hub.Has(geminiwire.VertexProtocol, openai.ChatProtocol, hub.Response) {
		t.Fatal("the vertex legs are expected to exist; the gate is the only thing keeping Vertex unrouted")
	}
	if !unwired[geminiwire.VertexProtocol] {
		t.Fatal("vertex left ingress.unwired before #115 PR-C supplied its URL and auth: a client could route to Vertex and fail at the last hop")
	}
	for _, client := range []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol} {
		if CanTranslate(client, geminiwire.VertexProtocol) {
			t.Errorf("%s client claims to reach vertex", client)
		}
		if reaches(client, []registry.Protocol{registry.TransportVertex}) {
			t.Errorf("%s client reaches the vertex provider", client)
		}
	}
	if _, err := TranslateChat(mustChat(t), geminiwire.VertexProtocol); err == nil {
		t.Error("TranslateChat has a vertex arm before its endpoint exists")
	}
}

// Gemini is routable from the three converted client wires and nothing else
// changed with it.
func TestGeminiIsTranslatableFromConvertedClients(t *testing.T) {
	for _, client := range []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol} {
		if !CanTranslate(client, geminiwire.Protocol) {
			t.Errorf("%s client cannot reach gemini", client)
		}
	}
	// A Gemini client has the request leg to Chat and no response leg back (the
	// pin registers no openai>gemini response leg), so it is not routable.
	if CanTranslate(geminiwire.Protocol, openai.ChatProtocol) {
		t.Error("a Gemini client claims to reach a Chat provider with no response leg")
	}
	if !hub.Has(geminiwire.Protocol, openai.ChatProtocol, hub.Request) {
		t.Error("the gemini>chat request leg is missing")
	}
}

func mustChat(t *testing.T) *openai.ChatRequest {
	t.Helper()
	request, err := openai.ParseChatRequest([]byte(`{"model":"vertex-model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// Without ?alt=sse Gemini streams a JSON array. The client is still served, and
// the usage record says the scanner saw none rather than recording zero tokens
// (PARITY 6.7 G11); #115 PR-C's target hook is what sets the query.
func TestGeminiArrayFramedStreamIsServedWithUnobservedUsage(t *testing.T) {
	array := "[" + strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(geminiStreamed, "data: ", ""), "\n\n", ",")) + "]"
	array = strings.Replace(array, "},]", "}]", 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, array)
	}))
	defer upstream.Close()
	manager := newManager(t)
	_, key, _ := manager.CreateAPIKey(context.Background(), "test")
	var outcomes []RequestOutcome
	var mu sync.Mutex
	mux := http.NewServeMux()
	options := allTranslators()
	options.AllowPrivateUpstreams = true
	options.Candidates = func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
		return []routing.Account{{ID: "a", Enabled: true}}, true
	}
	options.AccountProvider = func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: "gemini", ConnectionID: account, BaseURL: upstream.URL, Protocol: geminiwire.Protocol, UpstreamModel: "gemini-x", APIToken: "secret"}, true
	}
	options.OnRequestComplete = func(outcome RequestOutcome) { mu.Lock(); outcomes = append(outcomes, outcome); mu.Unlock() }
	New(manager, options).Attach(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(clientRequests[openai.ChatProtocol].body, true)))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	mu.Lock()
	defer mu.Unlock()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	assertClientSees(t, openai.ChatProtocol, true, w.Body.String())
	if len(outcomes) != 1 || !outcomes[0].UsageUnobserved || outcomes[0].InputTokens != 0 {
		t.Errorf("usage record %+v", outcomes)
	}
}

// A translated request whose upstream reports no usage is not recorded as zero
// tokens: usage_observed is false (SPEC 21.1, PARITY V8). The same Chat exchange
// with its usage chunk is observed.
func TestTranslatedRequestWithoutUsageIsUnobserved(t *testing.T) {
	withoutUsage := map[bool]string{
		false: `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`,
		true: "data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"pong\"}}]}\n\n" +
			"data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
	}
	for _, withUsage := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("usage=%v/stream=%v", withUsage, stream), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if withUsage {
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
						}
						fmt.Fprint(w, upstreamBodies[openai.ChatProtocol][stream])
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					fmt.Fprint(w, withoutUsage[stream])
				}))
				defer upstream.Close()
				manager := newManager(t)
				_, key, _ := manager.CreateAPIKey(context.Background(), "test")
				var outcomes []RequestOutcome
				var mu sync.Mutex
				mux := http.NewServeMux()
				New(manager, Options{
					AllowPrivateUpstreams: true, TranslateMessages: TranslateMessages,
					Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
						return []routing.Account{{ID: "a", Enabled: true}}, true
					},
					AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
						return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: upstream.URL, Protocol: openai.ChatProtocol}, true
					},
					OnRequestComplete: func(o RequestOutcome) { mu.Lock(); outcomes = append(outcomes, o); mu.Unlock() },
				}).Attach(mux)
				r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(clientRequests[anthropic.MessagesProtocol].body, stream)))
				r.Header.Set("Authorization", "Bearer "+key)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				mu.Lock()
				defer mu.Unlock()
				if w.Code != http.StatusOK || len(outcomes) != 1 {
					t.Fatalf("status %d outcomes %+v", w.Code, outcomes)
				}
				if outcomes[0].UsageUnobserved == withUsage {
					t.Errorf("UsageUnobserved = %v with usage=%v: %+v", outcomes[0].UsageUnobserved, withUsage, outcomes[0])
				}
				if withUsage && outcomes[0].InputTokens != 130 {
					t.Errorf("observed usage lost: %+v", outcomes[0])
				}
			})
		}
	}
}
