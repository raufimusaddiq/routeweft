package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A bulky Claude request is imaged by the helper, the provider is sent the imaged
// body with the cache anchored on it, and the attempt's summary is kept (#271).
func TestPXPIPEImagesTheFinalClaudeBodyOnTheServingPath(t *testing.T) {
	var helperCalls atomic.Int32
	helper := httptest.NewServer(imageHandler(t, &helperCalls, func(body map[string]any) {
		messages := body["messages"].([]any)
		messages[0].(map[string]any)["content"] = []any{
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"}},
			map[string]any{"type": "text", "text": "summarise the image"},
		}
	}))
	defer helper.Close()
	var received string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received = string(raw)
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	setSavers(t, a, map[string]string{"pxpipeEnabled": "true", "pxpipeUrl": helper.URL, "pxpipeMinChars": "1"})
	body := fmt.Sprintf(`{"model":"fast","max_tokens":50,"system":"you are terse","messages":[{"role":"user","content":%q}]}`, strings.Repeat("a long log line\n", 400))
	w := serveRequest(a.Handler(), key, "/v1/messages", body)
	if w.Code != 200 || helperCalls.Load() != 1 {
		t.Fatalf("status=%d helper=%d body=%s", w.Code, helperCalls.Load(), w.Body)
	}
	for _, want := range []string{`"type":"image"`, `"media_type":"image/png"`, "summarise the image", `"cache_control"`} {
		if !strings.Contains(received, want) {
			t.Errorf("the provider's body lacks %s: %.300s", want, received)
		}
	}
	if strings.Contains(received, "a long log line") {
		t.Errorf("the imaged text was sent as text too")
	}
	logs := a.pxpipe.Logs()
	if len(logs) != 1 || logs[0].Summary == nil || !logs[0].Summary.Applied || logs[0].Summary.ImageCount != 1 || logs[0].Summary.TokensBeforeEst <= logs[0].Summary.TokensAfterEst || logs[0].Summary.TokensSavedEst <= 0 {
		t.Fatalf("summary not kept: %+v", logs)
	}
	if stats := a.pxpipe.Stats(); stats.Succeeded != 1 || stats.Failed != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

// A helper that is slow, declines or breaks never costs the request: the provider
// still gets the original body, and the attempt is counted.
func TestPXPIPEFailsOpenOnTheServingPath(t *testing.T) {
	for name, behave := range map[string]func(http.ResponseWriter, *http.Request){
		"declines": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"applied":false,"reason":"not_profitable"}`)
		},
		"breaks": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		"too slow": func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body) // a server only notices the client leaving once it has read the body
			<-r.Context().Done()
		},
	} {
		t.Run(name, func(t *testing.T) {
			helper := httptest.NewServer(http.HandlerFunc(behave))
			defer helper.Close()
			var received string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				received = string(raw)
				fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 1)
			setSavers(t, a, map[string]string{"pxpipeEnabled": "true", "pxpipeUrl": helper.URL, "pxpipeMinChars": "1", "pxpipeTimeoutMs": "200"})
			started := time.Now()
			w := serveRequest(a.Handler(), key, "/v1/messages", `{"model":"fast","max_tokens":50,"messages":[{"role":"user","content":"keep me as I am"}]}`)
			if w.Code != 200 || !strings.Contains(received, "keep me as I am") || strings.Contains(received, `"type":"image"`) {
				t.Fatalf("status=%d received=%.300s", w.Code, received)
			}
			if time.Since(started) > 5*time.Second {
				t.Fatalf("the request waited %v for a helper with a 200 ms budget", time.Since(started))
			}
			logs := a.pxpipe.Logs()
			if len(logs) != 1 || logs[0].Summary == nil || logs[0].Summary.Applied {
				t.Fatalf("logs=%+v", logs)
			}
			var reason struct{ Reason string }
			encoded, _ := json.Marshal(logs[0].Summary)
			_ = json.Unmarshal(encoded, &reason)
			want := map[string]string{"declines": "not_profitable", "breaks": "transform_error", "too slow": "timeout"}[name]
			if reason.Reason != want {
				t.Fatalf("reason %q, want %q", reason.Reason, want)
			}
		})
	}
}

// Headroom is called at <url>/v1/compress with the conversation in OpenAI shape and
// the model the request is addressed to; its answer is read back into the body.
func TestHeadroomCompressWireOnTheServingPath(t *testing.T) {
	var seen compressRequest
	var calls atomic.Int32
	helper := httptest.NewServer(compressHandler(t, &calls, func(request compressRequest) { seen = request }, func(messages []map[string]any) {
		messages[len(messages)-1]["content"] = "squeezed"
	}))
	defer helper.Close()
	var received string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		received = string(raw)
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	setSavers(t, a, map[string]string{"headroomEnabled": "true", "headroomUrl": helper.URL, "headroomCompressUserMessages": "true"})
	w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"a very long question"}]}`)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
	}
	if seen.Model != "vendor/model" || len(seen.Messages) != 2 || seen.Messages[0]["role"] != "system" || seen.Messages[1]["content"] != "a very long question" || seen.Config["compress_user_messages"] != true {
		t.Errorf("Headroom was sent %+v", seen)
	}
	if !strings.Contains(received, `"squeezed"`) || strings.Contains(received, "a very long question") {
		t.Errorf("the compressed turn did not reach the provider: %s", received)
	}
}
