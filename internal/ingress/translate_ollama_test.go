package ingress

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func serveOllamaClient(t *testing.T, upstream string, protocols map[string]string, accounts []string, request struct{ path, body string }) (*httptest.ResponseRecorder, []RequestOutcome) {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []RequestOutcome
	var mu sync.Mutex
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true,
		TranslateChat:         TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			list := make([]routing.Account, 0, len(accounts))
			for _, id := range accounts {
				list = append(list, routing.Account{ID: id, Enabled: true})
			}
			return list, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, source string) (routing.ProviderRef, bool) {
			protocol := protocols[account]
			if source != protocol && !CanTranslate(source, protocol) {
				return routing.ProviderRef{}, false
			}
			return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: upstream, Protocol: protocol, UpstreamModel: "vendor/m", APIToken: "secret"}, true
		},
		OnRequestComplete: func(outcome RequestOutcome) { mu.Lock(); outcomes = append(outcomes, outcome); mu.Unlock() },
	}).Attach(mux)
	r := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(request.body))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	mu.Lock()
	defer mu.Unlock()
	return w, append([]RequestOutcome(nil), outcomes...)
}

// A request the Ollama wire cannot express is skipped, not served wrongly: the
// next candidate answers.
func TestOllamaRefusalMovesToTheNextCandidate(t *testing.T) {
	var contacted []string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		contacted = append(contacted, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, upstreamBodies[openai.ChatProtocol][false])
	}))
	defer upstream.Close()
	w, _ := serveOllamaClient(t, upstream.URL, map[string]string{"local": ollama.Protocol, "chat": openai.ChatProtocol}, []string{"local", "chat"},
		struct{ path, body string }{"/v1/chat/completions", `{"model":"test/model","n":2,"messages":[{"role":"user","content":"ping"}]}`})
	mu.Lock()
	defer mu.Unlock()
	if w.Code != 200 || len(contacted) != 1 || contacted[0] != "/chat/completions" {
		t.Fatalf("status %d contacted %v body %s", w.Code, contacted, w.Body)
	}
}

// A stream that ends before done:true is a failure the client is told about, and
// it is not recorded as a success with the usage it had not yet reported.
func TestTruncatedOllamaStreamIsAFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, `{"model":"vendor/m","message":{"role":"assistant","content":"po"},"done":false}`+"\n")
	}))
	defer upstream.Close()
	for _, client := range []string{openai.ChatProtocol, anthropic.MessagesProtocol, openai.ResponsesProtocol} {
		t.Run(client, func(t *testing.T) {
			w, outcomes := serveOllamaClient(t, upstream.URL, map[string]string{"local": ollama.Protocol}, []string{"local"},
				struct{ path, body string }{clientRequests[client].path, fmt.Sprintf(clientRequests[client].body, true)})
			body := w.Body.String()
			if !strings.Contains(body, `"error"`) && !strings.Contains(body, "response.failed") {
				t.Errorf("client was not told the stream failed:\n%s", body)
			}
			if len(outcomes) != 1 || outcomes[0].Status != http.StatusBadGateway || outcomes[0].InputTokens != 0 || outcomes[0].OutputTokens != 0 {
				t.Errorf("outcomes %+v", outcomes)
			}
		})
	}
}
