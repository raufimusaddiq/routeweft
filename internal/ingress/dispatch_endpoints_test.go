package ingress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

type endpointTransport func(*http.Request) (*http.Response, error)

func (f endpointTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type endpointBody struct {
	io.Reader
	closed bool
}

func (b *endpointBody) Close() error { b.closed = true; return nil }

func TestDispatchEndpointFallback(t *testing.T) {
	for _, status := range []int{401, 403, 404, 400, 429, 500} {
		for _, configured := range []bool{false, true} {
			t.Run(strings.Join([]string{http.StatusText(status), map[bool]string{true: "configured", false: "builtin"}[configured]}, "/"), func(t *testing.T) {
				manager := newManager(t)
				_, key, err := manager.CreateAPIKey(context.Background(), "endpoint test")
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := manager.Load()
				if err != nil {
					t.Fatal(err)
				}
				provider := routing.ProviderRef{ProviderID: "kiro", Protocol: "kiro", ConnectionID: "a", APIToken: "fixture-token", BaseURL: "https://runtime.us-east-1.kiro.dev", Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{AuthMethod: "api_key", Region: "eu-west-1"}}}
				want := []string{"q.eu-west-1.amazonaws.com", "codewhisperer.eu-west-1.amazonaws.com", "runtime.us-east-1.kiro.dev"}
				if configured {
					provider.Connection.Settings.BaseURL = "https://proxy.example/kiro"
					want = append([]string{"proxy.example"}, want...)
				}
				fallback := status == 401 || status == 403 || status == 404
				if !fallback {
					want = want[:1]
				}
				for _, exhaust := range []bool{false, true} {
					state := runtime.NewState()
					var hosts []string
					var bodies []*endpointBody
					h := New(manager, Options{State: state, ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) { return provider, true }})
					h.client = &http.Client{Transport: endpointTransport(func(r *http.Request) (*http.Response, error) {
						if len(bodies) > 0 && !bodies[len(bodies)-1].closed {
							t.Fatal("previous response body left open")
						}
						if _, cooling := state.CooldownUntil("a", time.Now()); cooling {
							t.Fatal("intermediate endpoint rejection cooled credential")
						}
						if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Tokentype") != "API_KEY" {
							t.Fatal("endpoint fallback changed credential")
						}
						if strings.HasPrefix(r.URL.Hostname(), "codewhisperer.") != (r.Header.Get("X-Amz-Target") != "") {
							t.Fatal("target header does not match actual host")
						}
						if !strings.HasSuffix(r.URL.Path, "/generateAssistantResponse") {
							t.Fatalf("wrong operation: %s", r.URL)
						}
						body, _ := io.ReadAll(r.Body)
						r.Body.Close()
						if string(body) != `{"conversationState":{"future":true}}` {
							t.Fatalf("body changed: %s", body)
						}
						hosts = append(hosts, r.URL.Hostname())
						code := status
						if fallback && !exhaust && len(hosts) == len(want) {
							code = 200
						}
						b := &endpointBody{Reader: strings.NewReader(`{"error":"fixture","host":"` + r.URL.Hostname() + `"}`)}
						bodies = append(bodies, b)
						return &http.Response{StatusCode: code, Header: http.Header{"X-Endpoint": []string{r.URL.Hostname()}}, Body: b}, nil
					})}
					r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					r.Header.Set("Authorization", "Bearer "+key)
					response, _, err := h.dispatchAttempts(r, snapshot, "kiro/test", "openai-chat", false, false, func(p routing.ProviderRef) (ChatTranslation, error) {
						return ChatTranslation{Endpoint: "ignored", Body: []byte(`{"conversationState":{"future":true}}`), Headers: openAIProviderHeaders(p)}, nil
					})
					if err != nil || response == nil {
						t.Fatalf("response %v error %v", response, err)
					}
					wantStatus := status
					if fallback && !exhaust {
						wantStatus = 200
					}
					if response.StatusCode != wantStatus {
						t.Fatalf("status %d, want %d", response.StatusCode, wantStatus)
					}
					body, readErr := io.ReadAll(response.Body)
					response.Body.Close()
					if readErr != nil || string(body) != `{"error":"fixture","host":"`+want[len(want)-1]+`"}` || response.Header.Get("X-Endpoint") != want[len(want)-1] {
						t.Fatalf("body %q: %v", body, readErr)
					}

					if !reflect.DeepEqual(hosts, want) {
						t.Fatalf("hosts %v, want %v", hosts, want)
					}
					if len(hosts) > 4 {
						t.Fatalf("exceeded four attempts: %d", len(hosts))
					}
					for _, b := range bodies {
						if !b.closed {
							t.Fatal("response body leaked")
						}
					}
				}
			})
		}
	}
}

func TestDispatchEndpointGlobalBoundAndCancellation(t *testing.T) {
	for _, scenario := range []string{"global bound", "cancellation", "same-account retry", "later account refused"} {
		t.Run(scenario, func(t *testing.T) {
			cancelAfterFirst := scenario == "cancellation"
			retryFirst := scenario == "same-account retry"
			refuseLater := scenario == "later account refused"
			manager := newManager(t)
			_, key, err := manager.CreateAPIKey(context.Background(), "bound test")
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempts := 0
			attemptsByAccount := map[string]int{}
			var bodies []*endpointBody
			h := New(manager, Options{
				Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
					return []routing.Account{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c", Enabled: true}, {ID: "d", Enabled: true}}, true
				},
				AccountProvider: func(_ *runtime.RuntimeSnapshot, _, id, _ string) (routing.ProviderRef, bool) {
					auth := "api_key"
					if refuseLater && id != "a" {
						// A missing authMethod is refused before any upstream call; the
						// scenario is that a later account cannot be dialled at all.
						auth = ""
					}
					return routing.ProviderRef{ProviderID: "kiro", Protocol: "kiro", ConnectionID: id, APIToken: id, Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{AuthMethod: auth, BaseURL: "https://proxy.example"}}}, true
				},
			})
			hosts := []string{"proxy.example", "q.us-east-1.amazonaws.com", "codewhisperer.us-east-1.amazonaws.com", "runtime.us-east-1.kiro.dev"}
			h.client = &http.Client{Transport: endpointTransport(func(r *http.Request) (*http.Response, error) {
				if attempts >= 10 {
					t.Fatal("global attempt cap exceeded")
				}
				step := attempts
				if retryFirst && attempts > 0 {
					step--
				}
				if r.Header.Get("Authorization") != "Bearer "+string(rune('a'+step/4)) || r.URL.Hostname() != hosts[step%4] {
					t.Fatalf("account or host revisited: attempt %d %s", attempts, r.URL)
				}
				attemptsByAccount[r.Header.Get("Authorization")]++
				attempts++
				if cancelAfterFirst {
					cancel()
				}
				body := &endpointBody{Reader: strings.NewReader("not found")}
				bodies = append(bodies, body)
				status := 404
				if retryFirst && attempts == 1 {
					status = 408
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
			})}
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+key)
			response, _, err := h.dispatchAttempts(r, snapshot, "kiro/test", "kiro", false, false, func(p routing.ProviderRef) (ChatTranslation, error) {
				return ChatTranslation{Body: []byte(`{}`), Headers: openAIProviderHeaders(p)}, nil
			})
			want := 10
			if refuseLater {
				want = 4
			}
			if cancelAfterFirst {
				want = 1
				if response != nil {
					response.Body.Close()
					t.Fatal("cancelled request returned a response")
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			} else {
				if err != nil || response == nil {
					t.Fatalf("exhaustion: response %v error %v", response, err)
				}
				if response.StatusCode != 404 {
					t.Fatalf("exhaustion status %d, want 404", response.StatusCode)
				}
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if readErr != nil || string(body) != "not found" {
					t.Fatalf("exhaustion body %q: %v", body, readErr)
				}
			}

			if retryFirst && attemptsByAccount["Bearer a"] != 5 {
				t.Fatalf("same-account retry bound: %v", attemptsByAccount)
			}
			if attempts != want {
				t.Fatalf("attempts %d, want %d", attempts, want)
			}
			for _, body := range bodies {
				if !body.closed {
					t.Fatal("response body leaked")
				}
			}
		})
	}
}
