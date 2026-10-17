package ingress

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestTranslationEligibilityResolvesEachAccountOnce(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		compact, wired, failNative bool
		status, attempts           int
	}{
		{"native", false, true, false, 200, 1},
		{"translated-fallback", false, true, true, 200, 2},
		{"compact-skips-chat", true, true, true, 503, 1},
		{"missing-hook-skips-chat", false, false, true, 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				if r.URL.Path == "/chat/completions" {
					fmt.Fprint(w, `{"id":"synthetic","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				} else if tc.failNative {
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":"original upstream failure"}`)
				} else {
					fmt.Fprint(w, `{"output":[]}`)
				}
			}))
			defer upstream.Close()
			manager := newManager(t)
			_, key, err := manager.CreateAPIKey(context.Background(), "test")
			if err != nil {
				t.Fatal(err)
			}
			resolved := map[string]int{}
			opts := Options{
				AllowPrivateUpstreams: true,
				Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
					return []routing.Account{{ID: "chat", Enabled: true}, {ID: "native", Enabled: true}, {ID: "unsupported", Enabled: true}}, true
				},
				AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
					resolved[account]++
					protocol := openai.ChatProtocol
					if account == "native" {
						protocol = openai.ResponsesProtocol
					}
					if account == "unsupported" {
						protocol = "unsupported-protocol"
					}
					return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: upstream.URL, Protocol: protocol}, true
				},
			}
			if tc.wired {
				opts.TranslateResponses = TranslateResponses
			}
			mux := http.NewServeMux()
			New(manager, opts).Attach(mux)
			path := "/v1/responses"
			if tc.compact {
				path += "/compact"
			}
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"test/model","input":"hi"}`))
			r.Header.Set("Authorization", "Bearer "+key)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status || attempts.Load() != int32(tc.attempts) {
				t.Fatalf("status=%d attempts=%d body=%s", w.Code, attempts.Load(), w.Body)
			}
			for _, account := range []string{"chat", "native", "unsupported"} {
				if resolved[account] != 1 {
					t.Fatalf("%s resolved %d times", account, resolved[account])
				}
			}
		})
	}
}
