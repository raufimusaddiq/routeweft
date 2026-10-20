package ingress

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func targetRefusalRig(t *testing.T, accounts map[string]routing.ProviderRef, order ...string) (*http.ServeMux, string) {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true,
		State:                 runtime.NewState(),
		TranslateChat:         TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			var out []routing.Account
			for _, id := range order {
				out = append(out, routing.Account{ID: id, Enabled: true})
			}
			return out, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			ref, ok := accounts[account]
			return ref, ok
		},
	}).Attach(mux)
	return mux, key
}

func postChat(mux *http.ServeMux, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"ping"}]}`))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// A Target hook refusing one candidate (vertex-partner with no projectId) must not
// end the Combo: the next candidate serves.
func TestTargetRefusalFallsThroughToTheNextCandidate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"j","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	accounts := map[string]routing.ProviderRef{
		"a": {ProviderID: "vertex-partner", ConnectionID: "a", BaseURL: upstream.URL, Protocol: "openai-chat", UpstreamModel: "m", APIToken: "t"},
		"b": {ProviderID: "openrouter", ConnectionID: "b", BaseURL: upstream.URL, Protocol: "openai-chat", UpstreamModel: "m", APIToken: "t"},
	}
	mux, key := targetRefusalRig(t, accounts, "a", "b")
	if w := postChat(mux, key); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("combo did not fall back past the refused candidate: %d %s", w.Code, w.Body.String())
	}
}

// When every candidate is refused the client gets the hook's own message, not a
// generic configuration error.
func TestTargetRefusalMessageReachesTheClient(t *testing.T) {
	accounts := map[string]routing.ProviderRef{
		"a": {ProviderID: "vertex-partner", ConnectionID: "a", BaseURL: "http://127.0.0.1:1", Protocol: "openai-chat", UpstreamModel: "m", APIToken: "t"},
	}
	mux, key := targetRefusalRig(t, accounts, "a")
	w := postChat(mux, key)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "request_translation_failed") || !strings.Contains(w.Body.String(), "projectId") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
