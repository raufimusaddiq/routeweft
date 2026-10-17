package api

import (
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
