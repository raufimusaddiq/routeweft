package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/cloudflare"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

func systemOneApp(t *testing.T, provider, upstream string, accounts int) (*App, string, string) {
	t.Helper()
	a := New(Config{DataDir: storetest.DataDir(t), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true}, nil)
	ctx := context.Background()
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() })
	_, key, err := a.runtime.CreateAPIKey(ctx, "typed-client")
	if err != nil {
		t.Fatal(err)
	}
	node := credentials.Node{ID: "typed", Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider, BaseURL: upstream}
	model := "jev-latest"
	if provider == "custom" {
		node.Kind, node.Prefix, node.Transports = credentials.NodeGeneric, provider, []string{"systemone"}
		if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: provider, ID: model}); err != nil {
			t.Fatal(err)
		}
	} else if provider == "cloudflare-ai" {
		model = cloudflare.Clef
	}
	if _, err := a.creds.PutNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := a.runtime.PutAlias(ctx, "typed-fast", runtime.ModelRef{ProviderID: provider, ModelID: model}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < accounts; i++ {
		_, err := a.creds.PutConnection(ctx, credentials.Connection{ID: fmt.Sprintf("typed-%d", i), NodeID: node.ID, Name: fmt.Sprintf("Account %d", i), Identity: fmt.Sprintf("identity-%d", i), AccountID: fmt.Sprintf("account-%d", i), AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{AccessToken: fmt.Sprintf("synthetic-%d", i)}, Enabled: true, Priority: i})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.runtime.SetSettings(ctx, map[string]string{"enableObservability": "true", "rtkEnabled": "true", "cavemanEnabled": "true", "ponytailEnabled": "true", "headroomEnabled": "true", "pxpipeEnabled": "true"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return a, key, model
}

func systemOneFixture(t *testing.T, id string) fixtures.Exchange {
	t.Helper()
	all, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("fixture %s not found", id)
	return fixtures.Exchange{}
}

func TestConfiguredSystemOneWireAndAccounting(t *testing.T) {
	for _, provider := range []string{"typesafe", "custom", "cloudflare-ai"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, stream), func(t *testing.T) {
				id := "systemone-nonstreaming"
				if stream {
					id = "systemone-streaming"
				}
				if provider == "cloudflare-ai" {
					id = "systemone-cloudflare"
					if stream {
						id += "-streaming"
					}
				}
				f := systemOneFixture(t, id)
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					wantPath := "/typed/full-endpoint"
					if provider == "cloudflare-ai" {
						model := cloudflare.Clef
						if stream {
							model = cloudflare.ClefFlash
						}
						wantPath += "/account-0/ai/run/" + model
					}
					body, _ := io.ReadAll(r.Body)
					if r.URL.Path != wantPath || string(body) != f.Request.Body {
						t.Errorf("upstream path=%s body=%s", r.URL.Path, body)
					}
					if r.Header.Get("Authorization") != "Bearer synthetic-0" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Secret") != "" {
						t.Error("credential isolation failed")
					}
					for name, value := range f.Response.Headers {
						w.Header().Set(name, value)
					}
					w.WriteHeader(f.Response.Status)
					fmt.Fprint(w, f.Response.Body)
				}))
				defer upstream.Close()
				a, key, _ := systemOneApp(t, provider, upstream.URL+"/typed/full-endpoint", 1)
				ctx, cancel := context.WithCancel(context.Background())
				go a.usage.Run(ctx)
				defer func() { a.usage.Close(); cancel(); a.usage.Wait() }()
				w := serveRequest(a.Handler(), key, "/v1/systemone", f.Request.Body)
				if w.Code != f.Response.Status || w.Body.String() != f.Response.Body || calls.Load() != 1 {
					t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
				}
				for name, value := range f.Response.Headers {
					if !slices.Contains(w.Header().Values(name), value) {
						t.Errorf("header %s=%q want %q", name, w.Header().Get(name), value)
					}
				}
				flushCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				defer stop()
				if err := a.usage.FlushNow(flushCtx); err != nil {
					t.Fatal(err)
				}
				var count, input, output, read, write int
				var gotProvider, connection string
				err := a.store.DB().QueryRow(`SELECT count(*),sum(input_tokens),sum(output_tokens),sum(cache_read_tokens),sum(cache_write_tokens),provider_id,connection_id FROM usage_events`).Scan(&count, &input, &output, &read, &write, &gotProvider, &connection)
				if err != nil || count != 1 || input != f.Usage.InputTokens || output != f.Usage.OutputTokens || read != f.Usage.CacheReadTokens || write != f.Usage.CacheCreateTokens || gotProvider != provider || connection != "typed-0" {
					t.Fatalf("accounting count=%d tokens=%d/%d cache=%d/%d provider=%s account=%s err=%v", count, input, output, read, write, gotProvider, connection, err)
				}
				if err := a.store.DB().QueryRow(`SELECT sum(requests) FROM usage_daily`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("daily requests=%d err=%v", count, err)
				}
				a.usage.Close()
				cancel()
				a.usage.Wait()
				if err := a.store.Close(); err != nil {
					t.Fatal(err)
				}
				w = serveRequest(a.Handler(), key, "/v1/systemone", f.Request.Body)
				if w.Code != f.Response.Status || w.Body.String() != f.Response.Body || calls.Load() != 2 {
					t.Fatalf("DB-free serving status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
				}
			})
		}
	}
}

func TestConfiguredSystemOneValidationAndOpaqueErrorWithoutDatabase(t *testing.T) {
	f := systemOneFixture(t, "systemone-error")
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var got, want map[string]json.RawMessage
		if json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(f.Request.Body), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("opaque questions/model mapping: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(f.Response.Status)
		fmt.Fprint(w, f.Response.Body)
	}))
	defer upstream.Close()
	a, key, _ := systemOneApp(t, "custom", upstream.URL+"/exact", 2)
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, questions := range []string{"null", "{}", "[]", `"text"`, "true", "7"} {
		w := serveRequest(a.Handler(), key, "/v1/systemone", `{"model":"jev-latest","state":null,"questions":`+questions+`}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "questions must be a non-empty object") {
			t.Fatalf("questions=%s status=%d body=%s", questions, w.Code, w.Body)
		}
	}
	w := serveRequest(a.Handler(), "wrong", "/v1/systemone", f.Request.Body)
	if w.Code != 401 || calls.Load() != 0 {
		t.Fatalf("auth/validation status=%d calls=%d", w.Code, calls.Load())
	}
	for _, model := range []string{"jev-latest", "custom/jev-latest", "  custom/jev-latest  ", "typed-fast"} {
		body := strings.Replace(f.Request.Body, `"jev-latest"`, fmt.Sprintf("%q", model), 1)
		w = serveRequest(a.Handler(), key, "/v1/systemone", body)
		if w.Code != f.Response.Status || w.Body.String() != f.Response.Body || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("opaque error status=%d body=%s", w.Code, w.Body)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("nonretryable error replayed: %d", calls.Load())
	}
}

func TestConfiguredSystemOneCloudflareFallback(t *testing.T) {
	for _, status := range []int{401, 429, 529} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if strings.Contains(r.URL.Path, "/account-0/") {
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":"first account failed"}`)
					return
				}
				if !strings.Contains(r.URL.Path, "/account-1/ai/run/"+cloudflare.Clef) || r.Header.Get("Authorization") != "Bearer synthetic-1" {
					t.Error("fallback account metadata lost")
				}
				fmt.Fprint(w, `{"result":{"answers":{"ok":true}}}`)
			}))
			defer upstream.Close()
			a, key, model := systemOneApp(t, "cloudflare-ai", upstream.URL, 2)
			if err := a.store.Close(); err != nil {
				t.Fatal(err)
			}
			w := serveRequest(a.Handler(), key, "/v1/systemone", fmt.Sprintf(`{"model":%q,"state":null,"questions":{"ok":null}}`, model))
			if w.Code != 200 || calls.Load() != 2 || w.Body.String() != `{"result":{"answers":{"ok":true}}}` {
				t.Fatalf("fallback status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
			}
		})
	}
}

func TestConfiguredSystemOneCancellation(t *testing.T) {
	for _, provider := range []string{"typesafe", "custom", "cloudflare-ai"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, stream), func(t *testing.T) {
				started, cancelled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					io.Copy(io.Discard, r.Body)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"usage\":{\"input_tokens\":18,\"output_tokens\":2}}\n\n")
						w.(http.Flusher).Flush()
					}
					close(started)
					<-r.Context().Done()
					close(cancelled)
				}))
				defer upstream.Close()
				defer upstream.CloseClientConnections()
				a, key, model := systemOneApp(t, provider, upstream.URL, 2)
				events, unsubscribe := a.events.Subscribe()
				defer unsubscribe()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(finished)
					a.Handler().ServeHTTP(w, r)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				body, _ := json.Marshal(map[string]any{"model": model, "state": nil, "questions": map[string]any{"ok": nil}})
				r, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/systemone", strings.NewReader(string(body)))
				r.Header.Set("Authorization", "Bearer "+key)
				clientDone := make(chan error, 1)
				go func() {
					response, err := server.Client().Do(r)
					if err == nil {
						defer response.Body.Close()
						first := make([]byte, 1)
						_, err = io.ReadFull(response.Body, first)
					}
					clientDone <- err
				}()
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("upstream not started")
				}
				if stream {
					select {
					case err := <-clientDone:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal("stream buffered until EOF")
					}
				}
				cancel()
				for _, done := range []chan struct{}{cancelled, finished} {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("disconnect did not cancel upstream/serving handler")
					}
				}
				if !stream {
					if err := <-clientDone; err == nil {
						t.Fatal("cancelled nonstream request succeeded")
					}
				}
				if calls.Load() != 1 {
					t.Fatalf("cancelled request replayed: %d", calls.Load())
				}
				select {
				case event := <-events:
					t.Fatalf("cancelled request emitted completion: %+v", event)
				default:
				}
			})
		}
	}
}
