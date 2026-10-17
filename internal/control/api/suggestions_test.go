package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
)

func TestSuggestionFiltersPinnedFixtures(t *testing.T) {
	for _, tc := range []struct{ filter, body, want string }{
		{"kenari", `{"models":[{"id":"chat","endpoints":["chat"],"context_length":8192},{"id":"media","endpoints":["image"]}]}`, `[{"id":"chat","name":"chat","contextLength":8192}]`},
		{"openrouter-free", `{"data":[{"id":"small","pricing":{"prompt":"0","completion":"0"},"context_length":199999},{"id":"free","name":"Free","pricing":{"prompt":"0","completion":"0"},"context_length":200000},{"id":"larger","pricing":{"prompt":"0","completion":"0"},"context_length":300000},{"id":"paid","pricing":{"prompt":"1","completion":"0"},"context_length":300000}]}`, `[{"id":"larger","name":"larger","contextLength":300000},{"id":"free","name":"Free","contextLength":200000}]`},
		{"opencode-free", `[{"id":"big-pickle"},{"id":"model-free"},{"id":"deepseek-v4-flash-free"},{"id":"paid"}]`, `[{"id":"big-pickle","name":"big-pickle"},{"id":"model-free","name":"model-free"}]`},
		{"mimo-free", `[{"id":"mimo-model"},{"id":"named","name":"MiMo experimental"},{"id":"other"}]`, `[{"id":"mimo-model","name":"mimo-model"},{"id":"named","name":"MiMo experimental"}]`},
		{"airforce-free", `[{"id":"z:free","supports_chat":true},{"id":"a","name":"A","tier":"free","supports_chat":true,"media_type":"text"},{"id":"media:free","supports_chat":true,"media_type":"image"},{"id":"no-chat:free"}]`, `[{"id":"a","name":"A"},{"id":"z:free","name":"z:free"}]`},
		{"kilo-free", `{"data":[{"id":"free","name":"Free","isFree":true,"context_length":1000,"pricing":{"prompt":0}},{"id":"paid","isFree":false}]}`, `[{"id":"free","name":"Free","contextLength":1000,"isFree":true}]`},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			got, err := filterSuggestions([]byte(tc.body), tc.filter)
			encoded, _ := json.Marshal(got)
			if err != nil || string(encoded) != tc.want {
				t.Fatalf("got %s, %v; want %s", encoded, err, tc.want)
			}
		})
	}
	if _, err := filterSuggestions([]byte(`{"data":[`), "kenari"); err == nil {
		t.Fatal("malformed catalog accepted")
	}
}

type workflowHTTPFunc func(*http.Request) (*http.Response, error)

func (f workflowHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type catalogWaitContext struct {
	context.Context
	entered chan struct{}
}

func (c catalogWaitContext) Done() <-chan struct{} {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func TestKiloRefreshCoalescesWaitersAndAllowsCancellation(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	var release chan struct{}
	started := make(chan struct{}, 1)
	client := workflowHTTPFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		if fail.Load() {
			return nil, errors.New("synthetic failure")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"free","isFree":true}]}`))}, nil
	})
	h, mux, store, _, cookie := newProvidersAPI(t, false, &discovery.Client{HTTP: client})
	defer store.Close()
	for _, tc := range []struct {
		name       string
		warm, fail bool
	}{
		{"cold", false, false}, {"expired", true, false}, {"expired failure", true, true}, {"cold failure", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release = make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			fail.Store(tc.fail)
			h.workflowMu.Lock()
			h.kiloCache = nil
			if tc.warm {
				h.kiloCache = []suggestedModel{{ID: "free"}}
			}
			h.kiloAt = time.Now().Add(-2 * time.Hour)
			h.workflowMu.Unlock()
			before := calls.Load()
			codes := make(chan int, 9)
			path := "/admin/v1/providers/kilo/free-models"
			go func() { codes <- doJSON(t, mux, cookie, "GET", path, "").Code }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("refresh did not start")
			}
			for i := 0; i < 8; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entered := make(chan struct{}, 1)
				r := httptest.NewRequest("GET", path, nil).WithContext(catalogWaitContext{Context: ctx, entered: entered})
				r.AddCookie(cookie)
				go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, r); codes <- w.Code }()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("waiter did not enter refresh gate")
				}
				if i == 0 {
					cancel()
					select {
					case code := <-codes:
						if code != 408 {
							t.Fatalf("canceled waiter: %d", code)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("canceled waiter blocked")
					}
				}
			}
			close(release)
			want := 200
			if tc.fail && !tc.warm {
				want = 502
			}
			for i := 0; i < 8; i++ {
				select {
				case code := <-codes:
					if code != want {
						t.Errorf("coalesced response: %d, want %d", code, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("refresh waiter blocked")
				}
			}
			if got := calls.Load() - before; got != 1 {
				t.Fatalf("refresh fan-out: %d upstream calls, want 1", got)
			}
		})
	}
}

func TestKiloCacheFreshExpiredFailureAndConcurrency(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	client := workflowHTTPFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://api.kilo.ai/api/gateway/models" || r.Header.Get("Authorization") != "" {
			t.Error("wrong catalog URL or leaked auth")
		}
		if fail.Load() {
			return nil, errors.New("synthetic secret must not reach browser")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"free","name":"Free","isFree":true},{"id":"paid"}]}`))}, nil
	})
	h, mux, store, _, cookie := newProvidersAPI(t, false, &discovery.Client{HTTP: client})
	defer store.Close()
	path := "/admin/v1/providers/kilo/free-models"
	w := doJSON(t, mux, cookie, "GET", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cached":false`) || strings.Contains(w.Body.String(), "paid") {
		t.Fatalf("initial catalog: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, mux, cookie, "GET", path, "")
	if !strings.Contains(w.Body.String(), `"cached":true`) || calls.Load() != 1 {
		t.Fatalf("fresh cache: %s calls=%d", w.Body, calls.Load())
	}
	h.workflowMu.Lock()
	h.kiloAt = time.Now().Add(-2 * time.Hour)
	h.workflowMu.Unlock()
	fail.Store(true)
	w = doJSON(t, mux, cookie, "GET", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"warning"`) || !strings.Contains(w.Body.String(), `"id":"free"`) || strings.Contains(w.Body.String(), "synthetic") {
		t.Fatalf("stale cache: %d %s", w.Code, w.Body)
	}
	h.workflowMu.Lock()
	h.kiloCache = nil
	h.workflowMu.Unlock()
	w = doJSON(t, mux, cookie, "GET", path, "")
	if w.Code != 502 {
		t.Fatalf("cold cache failure: %d", w.Code)
	}
	fail.Store(false)
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			w := doJSON(t, mux, cookie, "GET", path, "")
			if w.Code != 200 {
				t.Errorf("concurrent catalog: %d", w.Code)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestSuggestionsFailureAndRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			io.WriteString(w, strings.Repeat(" ", (4<<20)+1))
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, target.URL, 302)
			return
		}
		w.WriteHeader(500)
	}))
	defer upstream.Close()
	_, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	for _, suffix := range []string{"/failure", "/redirect", "/large"} {
		w := doJSON(t, mux, cookie, "GET", "/admin/v1/providers/suggested-models?type=kenari&url="+upstream.URL+suffix, "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"data":[]}` {
			t.Fatalf("advisory failure: %d %s", w.Code, w.Body)
		}
	}
	if targetCalls.Load() != 0 {
		t.Fatal("catalog followed redirect")
	}
}
