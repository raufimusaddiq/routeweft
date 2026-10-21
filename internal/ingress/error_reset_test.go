package ingress

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestCodexBodyResetReachesDispatchCooldown(t *testing.T) {
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	body := fmt.Sprintf(`{"error":{"type":"usage_limit_reached","resets_at":%d,"resets_in_seconds":10}}`, reset.Unix())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer synthetic" || !strings.Contains(string(raw), `"input":"ping"`) {
			t.Errorf("request: %s %v %s", r.URL, r.Header, raw)
		}
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
		fmt.Fprint(w, body)
	}))
	defer upstream.Close()
	state := runtime.NewState()
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, State: state,
		ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "codex", ConnectionID: "acct", Protocol: "openai-responses", BaseURL: upstream.URL, UpstreamModel: "gpt-5", APIToken: "synthetic"}, true
		},
	})
	out := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"ping"}`)))
	if out.Code != 429 || out.Body.String() != body {
		t.Fatalf("error changed: %d %s", out.Code, out.Body)
	}
	until, cooling := state.CooldownUntil("acct", time.Now())
	if !cooling || !until.Equal(reset) {
		t.Fatalf("cooldown %v, %v; want %v", until, cooling, reset)
	}
}

func TestBodyResetRefinesWithoutErasingHeaderHints(t *testing.T) {
	now := time.Now()
	original := routing.Classification{Outcome: routing.OutcomeQuotaLock, RetryAfterSeconds: 45, ResetAt: now.Add(time.Hour).UTC().Format(time.RFC3339)}
	for _, id := range []string{"codex", "gemini-cli"} {
		for _, body := range []string{`{}`, `null`, `{"error":`, `{"error":{"type":"invalid_api_key","details":[]}}`} {
			got := providerClassification(routing.ProviderRef{ProviderID: id}, 429, []byte(body), original, now)
			if got != original {
				t.Fatalf("%s %s erased known hints: %+v", id, body, got)
			}
		}
	}
	body := []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"120.5s"}]}}`)
	got := providerClassification(routing.ProviderRef{ProviderID: "gemini-cli"}, 429, body, original, now)
	if got.ResetAt != now.Add(120500*time.Millisecond).UTC().Format(time.RFC3339Nano) || got.RetryAfterSeconds != 45 {
		t.Fatalf("Google hint not applied: %+v", got)
	}
}
