package ingress

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// vertexADCWithoutProject is an ADC credential naming no quota project: with no
// projectId either, vertex-partner refuses it (executors/vertex.js:78).
const vertexADCWithoutProject = `{"type":"authorized_user","client_id":"cid","client_secret":"csecret","refresh_token":"rtok"}`

// A Target hook refusing one candidate (vertex-partner with no projectId) must not
// end the Combo: the next candidate serves.
func TestTargetRefusalFallsThroughToTheNextCandidate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"j","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(upstream.Close)
	accounts := map[string]routing.ProviderRef{
		"a": {ProviderID: "vertex-partner", ConnectionID: "a", BaseURL: upstream.URL, Protocol: "openai-chat", UpstreamModel: "m", APIToken: vertexADCWithoutProject},
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
		"a": {ProviderID: "vertex-partner", ConnectionID: "a", BaseURL: "http://127.0.0.1:1", Protocol: "openai-chat", UpstreamModel: "m", APIToken: vertexADCWithoutProject},
	}
	mux, key := targetRefusalRig(t, accounts, "a")
	w := postChat(mux, key)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "request_translation_failed") || !strings.Contains(w.Body.String(), "projectId") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

// vertexPartnerKeys numbers the keys the probe test uses. The probed project is
// cached for the life of the process by key alone (wire/vertex.go
// vertexProbeProject), so a key used again, as -count does, is never probed.
var vertexPartnerKeys atomic.Int64

// executors/vertex.js:158-164 end to end: a vertex-partner raw key with no
// projectId is probed for its project at the connection's base, then served there
// with the key in X-Goog-Api-Key and never in a URL, buffered and streamed.
func TestVertexPartnerRawKeyIsProbedAndServed(t *testing.T) {
	for _, stream := range []bool{false, true} {
		var probes, served int
		token := fmt.Sprintf("AIza-ingress-fixture-%d", vertexPartnerKeys.Add(1))
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" || r.Header.Get("X-Goog-Api-Key") != token || r.Header.Get("Authorization") != "" {
				http.Error(w, "credential misplaced", http.StatusTeapot)
				return
			}
			switch r.URL.Path {
			case "/v1/publishers/google/models/__probe__:generateContent":
				probes++
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `[{"error":{"code":404,"message":"Publisher Model `+"`projects/probed-project/locations/us-central1/publishers/google/models/__probe__`"+` was not found."}}]`)
			case "/v1/projects/probed-project/locations/global/endpoints/openapi/chat/completions":
				served++
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"id\":\"j\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"j","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		accounts := map[string]routing.ProviderRef{
			"a": {ProviderID: "vertex-partner", ConnectionID: "a", BaseURL: upstream.URL, Protocol: "openai-chat", UpstreamModel: "zai-org/glm-5-maas", APIToken: token},
		}
		mux, key := targetRefusalRig(t, accounts, "a")
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"x","stream":%v,"messages":[{"role":"user","content":"ping"}]}`, stream)))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		upstream.Close()
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") || stream != strings.Contains(w.Body.String(), "[DONE]") {
			t.Fatalf("stream=%v: got %d %s", stream, w.Code, w.Body.String())
		}
		if probes != 1 || served != 1 {
			t.Fatalf("stream=%v: probes %d served %d", stream, probes, served)
		}
	}
}
