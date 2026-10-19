package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/transforms"
)

type saverPayload struct {
	System   []string             `json:"system"`
	Messages []transforms.Message `json:"messages"`
}

func setSavers(t *testing.T, a *App, settings map[string]string) {
	t.Helper()
	if _, err := a.runtime.SetSettings(context.Background(), settings, nil); err != nil {
		t.Fatal(err)
	}
}

func setServingProtocol(t *testing.T, a *App, protocol string) {
	t.Helper()
	node, err := a.creds.GetNode(context.Background(), "node")
	if err != nil {
		t.Fatal(err)
	}
	node.Transports = []string{protocol}
	if _, err := a.creds.PutNode(context.Background(), node); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredSaverOrderRetryFallbackAndFinalCache(t *testing.T) {
	for _, failure := range []int{429, 408} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			var mu sync.Mutex
			var order []string
			helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload saverPayload
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				mu.Lock()
				order = append(order, r.URL.Path)
				mu.Unlock()
				if r.URL.Path == "/headroom" {
					if len(payload.System) != 0 || len(payload.Messages) != 2 || payload.Messages[1].Content != `{"x":1}` {
						t.Errorf("RTK must precede Headroom; policies must follow: %+v", payload)
					}
					payload.Messages[1].Content = "headroom result"
				} else {
					if len(payload.System) != 2 || !strings.HasPrefix(payload.System[0], "[routeweft:caveman]") || !strings.HasPrefix(payload.System[1], "[routeweft:ponytail]") || payload.Messages[1].Content != "headroom result" {
						t.Errorf("PXPIPE must follow all four savers: %+v", payload)
					}
					payload.System[1] += " final-pxpipe"
					payload.Messages[1].Content = "pxpipe result"
				}
				json.NewEncoder(w).Encode(payload)
			}))
			defer helper.Close()
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					System []struct {
						Text         string
						CacheControl json.RawMessage `json:"cache_control"`
					}
					Messages []struct{ Content json.RawMessage }
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if len(payload.System) != 2 || !strings.HasSuffix(payload.System[1].Text, "final-pxpipe") || string(payload.System[1].CacheControl) != `{"type":"ephemeral"}` || !strings.Contains(string(payload.Messages[1].Content), "pxpipe result") {
					t.Errorf("cache must anchor the final saver body: %+v", payload)
				}
				if calls.Add(1) == 1 {
					w.WriteHeader(failure)
					fmt.Fprint(w, `{"error":{"message":"synthetic failure"}}`)
					return
				}
				fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 2)
			setSavers(t, a, map[string]string{"headroomEnabled": "true", "headroomUrl": helper.URL + "/headroom", "cavemanEnabled": "true", "ponytailEnabled": "true", "pxpipeEnabled": "true", "pxpipeUrl": helper.URL + "/pxpipe", "pxpipeMinChars": "1"})
			if err := a.store.Close(); err != nil {
				t.Fatal(err)
			}
			w := serveRequest(a.Handler(), key, "/v1/messages", `{"model":"fast","messages":[{"role":"user","content":"hi"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"{ \"x\" : 1 }"}]}]}`)
			mu.Lock()
			defer mu.Unlock()
			if w.Code != 200 || calls.Load() != 2 || !reflect.DeepEqual(order, []string{"/headroom", "/pxpipe", "/headroom", "/pxpipe"}) {
				t.Fatalf("status=%d calls=%d order=%v body=%s", w.Code, calls.Load(), order, w.Body)
			}
		})
	}
}

func TestConfiguredSaversEveryNativePathAndFallback(t *testing.T) {
	for _, tc := range []struct{ protocol, path, body string }{
		{"openai-chat", "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hello"}],"future":true}`},
		{"openai-responses", "/v1/responses", `{"model":"fast","input":"hello","future":true}`},
		{"openai-responses", "/v1/responses/compact", `{"model":"fast","input":"hello","future":true}`},
		{"anthropic-messages", "/v1/messages", `{"model":"fast","messages":[{"role":"user","content":"hello"}],"future":true}`},
		{"gemini", "/v1beta/models/fast:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"future":true}`},
		{"gemini", "/v1beta/models/fast:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"future":true}`},
		{"ollama", "/v1/api/chat", `{"model":"fast","messages":[{"role":"user","content":"hello"}],"future":true}`},
		{"systemone", "/v1/systemone", `{"model":"fast","state":{"opaque":"hello"},"questions":{"answer":"string"},"future":true}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var helperCalls, upstreamCalls atomic.Int32
			helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				helperCalls.Add(1)
				var payload saverPayload
				json.NewDecoder(r.Body).Decode(&payload)
				if r.URL.Path == "/headroom" {
					payload.Messages[0].Content = "headroom"
				} else {
					if payload.Messages[0].Content != "headroom" || len(payload.System) != 2 {
						t.Errorf("wrong saver order: %+v", payload)
					}
					payload.Messages[0].Content = "pxpipe"
				}
				json.NewEncoder(w).Encode(payload)
			}))
			defer helper.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"future":true`) {
					t.Errorf("unknown field lost: %s", body)
				}
				if tc.protocol == "systemone" {
					if !strings.Contains(string(body), `"state":{"opaque":"hello"}`) || strings.Contains(string(body), "[routeweft:") {
						t.Errorf("typed state corrupted: %s", body)
					}
				} else if !strings.Contains(string(body), "pxpipe") || strings.Count(string(body), "[routeweft:caveman]") != 1 || strings.Count(string(body), "[routeweft:ponytail]") != 1 {
					t.Errorf("savers skipped/duplicated: %s", body)
				}
				if upstreamCalls.Add(1) == 1 {
					w.WriteHeader(429)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 2)
			setServingProtocol(t, a, tc.protocol)
			setSavers(t, a, map[string]string{"headroomEnabled": "true", "headroomCompressUserMessages": "true", "headroomUrl": helper.URL + "/headroom", "cavemanEnabled": "true", "ponytailEnabled": "true", "pxpipeEnabled": "true", "pxpipeUrl": helper.URL + "/pxpipe", "pxpipeMinChars": "1"})
			a.store.Close()
			w := serveRequest(a.Handler(), key, tc.path, tc.body)
			wantHelpers := int32(4)
			if tc.protocol == "systemone" {
				wantHelpers = 0
			}
			if w.Code != 200 || upstreamCalls.Load() != 2 || helperCalls.Load() != wantHelpers {
				t.Fatalf("status=%d upstream=%d helpers=%d body=%s", w.Code, upstreamCalls.Load(), helperCalls.Load(), w.Body)
			}
		})
	}
}

func TestConfiguredSaversFailOpenBypassAndCountTokens(t *testing.T) {
	var helperCalls atomic.Int32
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		helperCalls.Add(1)
		fmt.Fprint(w, `{"system":[],"messages":[{"role":"system","content":"corrupt"}]}`)
	}))
	defer broken.Close()
	var bodyMu sync.Mutex
	var recordedBody string
	lastBody := func() string { bodyMu.Lock(); defer bodyMu.Unlock(); return recordedBody }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyMu.Lock()
		recordedBody = string(body)
		bodyMu.Unlock()
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	setSavers(t, a, map[string]string{"headroomEnabled": "true", "headroomUrl": broken.URL, "cavemanEnabled": "true", "ponytailEnabled": "true", "pxpipeEnabled": "true", "pxpipeUrl": broken.URL, "pxpipeMinChars": "1"})
	body := `{"model":"fast","system":"base","messages":[{"role":"user","content":"hello"}]}`
	w := serveRequest(a.Handler(), key, "/v1/messages", body)
	if w.Code != 200 || !strings.Contains(lastBody(), "hello") || strings.Contains(lastBody(), "corrupt") || !strings.Contains(lastBody(), "[routeweft:ponytail]") || helperCalls.Load() != 2 {
		t.Fatalf("fail-open status=%d calls=%d body=%s", w.Code, helperCalls.Load(), lastBody())
	}
	for _, header := range []string{"X-9router-Token-Saver", "X-Literouter-Token-Saver"} {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set(header, "OFF")
		w = httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(lastBody(), "[routeweft:") || !strings.Contains(lastBody(), "cache_control") || helperCalls.Load() != 2 {
			t.Fatalf("bypass %s: status=%d calls=%d body=%s", header, w.Code, helperCalls.Load(), lastBody())
		}
	}
	count := func() int {
		t.Helper()
		w := serveRequest(a.Handler(), key, "/v1/messages/count_tokens", body)
		var result struct {
			Tokens int `json:"input_tokens"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("count %d %s", w.Code, w.Body)
		}
		return result.Tokens
	}
	withPolicies := count()
	setSavers(t, a, map[string]string{"headroomEnabled": "false", "cavemanEnabled": "false", "ponytailEnabled": "false", "pxpipeEnabled": "false"})
	if without := count(); without >= withPolicies {
		t.Fatalf("count ignored savers/settings publication: with=%d without=%d", withPolicies, without)
	}
}

func TestConfiguredSaversAfterTranslationWithFallback(t *testing.T) {
	for _, fixture := range servingTranslationFixtures(t) {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", fixture.Source, stream), func(t *testing.T) {
				target, path, endpoint := openai.ChatProtocol, "/v1/responses", "/chat/completions"
				if fixture.Source == openai.ChatProtocol {
					target, path, endpoint = openai.ResponsesProtocol, "/v1/chat/completions", "/responses"
				}
				var calls, helpers atomic.Int32
				helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					helpers.Add(1)
					var payload saverPayload
					json.NewDecoder(r.Body).Decode(&payload)
					json.NewEncoder(w).Encode(payload)
				}))
				defer helper.Close()
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if r.URL.Path != endpoint || strings.Count(string(body), "[routeweft:caveman]") != 1 || strings.Count(string(body), "[routeweft:ponytail]") != 1 {
						t.Errorf("missing/duplicate translated savers: %s %s", r.URL.Path, body)
					}
					if calls.Add(1) == 1 {
						w.WriteHeader(429)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						w.Write(translationStreamFixture(t, fixture.Source))
					} else {
						w.Write(fixture.UpstreamResponse)
					}
				}))
				defer upstream.Close()
				a, key := servingApp(t, upstream.URL, 2)
				setServingProtocol(t, a, target)
				setSavers(t, a, map[string]string{"cavemanEnabled": "true", "ponytailEnabled": "true", "headroomEnabled": "true", "headroomUrl": helper.URL, "pxpipeEnabled": "true", "pxpipeUrl": helper.URL, "pxpipeMinChars": "1"})
				a.store.Close()
				var request map[string]any
				json.Unmarshal(fixture.Request, &request)
				request["stream"] = stream
				body, _ := json.Marshal(request)
				w := serveRequest(a.Handler(), key, path, string(body))
				if w.Code != 200 || calls.Load() != 2 || helpers.Load() != 4 {
					t.Fatalf("status=%d calls=%d helpers=%d body=%s", w.Code, calls.Load(), helpers.Load(), w.Body)
				}
			})
		}
	}
}

func TestConfiguredFinalCachePrefixNPlusOne(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	setSavers(t, a, map[string]string{"cavemanEnabled": "true", "ponytailEnabled": "true"})
	for _, tail := range []string{"", `,{"role":"user","content":[{"type":"text","text":"next"}]}`} {
		body := `{"model":"fast","system":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"lazy","defer_loading":true}],"messages":[{"role":"user","content":[{"type":"text","text":"first"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"opaque","signature":"sig"},{"type":"text","text":"answer"}]}` + tail + `]}`
		w := serveRequest(a.Handler(), key, "/v1/messages", body)
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, field := range []string{"system", "tools"} {
		if string(bodies[0][field]) != string(bodies[1][field]) || !strings.Contains(string(bodies[0][field]), "cache_control") {
			t.Errorf("unstable cache prefix %s: %s / %s", field, bodies[0][field], bodies[1][field])
		}
	}
	var first, next []json.RawMessage
	json.Unmarshal(bodies[0]["messages"], &first)
	json.Unmarshal(bodies[1]["messages"], &next)
	if len(first) != 2 || len(next) != 3 || string(first[1]) != string(next[1]) || !strings.Contains(string(first[1]), "cache_control") || strings.Contains(string(next[2]), "cache_control") {
		t.Fatalf("unstable history prefix: %s / %s", bodies[0]["messages"], bodies[1]["messages"])
	}
}

func TestSaverCancelledBeforeStreamingDispatch(t *testing.T) {
	for _, helperName := range []string{"headroom", "pxpipe"} {
		t.Run(helperName, func(t *testing.T) {
			started, cancelled := make(chan struct{}), make(chan struct{})
			helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(cancelled)
			}))
			defer helper.Close()
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls.Add(1) }))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 1)
			setSavers(t, a, map[string]string{helperName + "Enabled": "true", helperName + "Url": helper.URL, "pxpipeMinChars": "1"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+key)
			done := make(chan struct{})
			go func() { defer close(done); a.Handler().ServeHTTP(httptest.NewRecorder(), r) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("helper not started")
			}
			cancel()
			select {
			case <-cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("helper not cancelled")
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("request not cancelled")
			}
			if upstreamCalls.Load() != 0 {
				t.Fatal("upstream started after cancellation")
			}
		})
	}
}
