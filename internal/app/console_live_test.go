package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// #277 (U08): one live channel. A signed-in session opens /admin/v1/events once and receives the
// console lines, the clear, configuration changes and completed requests on it, in the SSE framing
// the dashboard's single EventSource reads. The upstream is injected; nothing leaves the process.

type sseFrame struct {
	ID    string
	Event string
	Data  map[string]any
}

// readFrames reads SSE frames from body onto the returned channel until it closes. Comment lines
// (": connected", ": heartbeat") are skipped, as an EventSource skips them.
func readFrames(t *testing.T, body io.Reader) <-chan sseFrame {
	t.Helper()
	frames := make(chan sseFrame, 64)
	go func() {
		defer close(frames)
		reader := bufio.NewReader(body)
		var frame sseFrame
		var data []string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if len(data) > 0 {
					var decoded map[string]any
					if json.Unmarshal([]byte(strings.Join(data, "\n")), &decoded) == nil {
						frame.Data = decoded
						frames <- frame
					}
				}
				frame, data = sseFrame{}, nil
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "id: "):
				frame.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				frame.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()
	return frames
}

// waitFrame returns the next frame of the given event type, skipping others, or fails after d.
func waitFrame(t *testing.T, frames <-chan sseFrame, event string, d time.Duration) sseFrame {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case frame, open := <-frames:
			if !open {
				t.Fatalf("the stream closed while waiting for %s", event)
			}
			if frame.Event == event {
				return frame
			}
		case <-deadline:
			t.Fatalf("no %s event within %s", event, d)
		}
	}
}

func TestOneSessionChannelCarriesConsoleConfigAndRequestEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5}}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	// A session needs an admin; the fixture hash keeps each login cheap, as in the C10 tests.
	if _, err := a.store.DB().Exec("INSERT INTO admin_users (id,username,password_hash) VALUES (?,?,?)", "live-admin", "operator", storetest.AdminHash(c03Password)); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	cookie := c03Login(t, a)
	server := httptest.NewServer(a.Handler())
	defer server.Close()

	// Without a session there is no channel.
	denied, err := http.Get(server.URL + "/admin/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("events without a session: %d", denied.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/admin/v1/events", nil)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events %d %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	frames := readFrames(t, response.Body)
	admin := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// 1. A console line: pushed in a batch of its own, with the shipped window.
	a.log.Info("live console line", "marker", "m-1")
	frame := waitFrame(t, frames, "console.log", 3*time.Second)
	lines, _ := frame.Data["data"].(map[string]any)["lines"].([]any)
	var found bool
	for _, raw := range lines {
		if line := raw.(map[string]any); line["message"] == "live console line" && line["level"] == "INFO" {
			found = true
		}
	}
	if !found || frame.ID == "" || fmt.Sprint(frame.Data["id"]) != frame.ID && fmt.Sprintf("%.0f", frame.Data["id"]) != frame.ID {
		t.Fatalf("console frame id=%q %v", frame.ID, frame.Data)
	}

	// 2. The clear reaches every page, and the buffer really is empty.
	res := admin(http.MethodDelete, "/admin/v1/logs", "")
	res.Body.Close()
	cleared := waitFrame(t, frames, "console.cleared", 3*time.Second)
	if n, _ := cleared.Data["data"].(map[string]any)["cleared"].(float64); n < 1 {
		t.Fatalf("cleared frame %v", cleared.Data)
	}
	res = admin(http.MethodGet, "/admin/v1/logs?page=1&pageSize=10", "")
	var logs struct {
		Total int `json:"total"`
	}
	_ = json.NewDecoder(res.Body).Decode(&logs)
	res.Body.Close()
	if logs.Total != 0 {
		t.Fatalf("logs after the clear: %d", logs.Total)
	}

	// 3. The same channel carries a configuration change ...
	res = admin(http.MethodPatch, "/admin/v1/settings", `{"set":{"stickyRoundRobinLimit":"5"}}`)
	patched, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("settings patch %d %s", res.StatusCode, patched)
	}
	waitFrame(t, frames, "config.updated", 3*time.Second)

	// 4. ... and a request served through the live path to the injected upstream.
	served := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	if served.Code != http.StatusOK {
		t.Fatalf("served %d %s", served.Code, served.Body)
	}
	done := waitFrame(t, frames, "request.completed", 3*time.Second)
	if payload, _ := done.Data["data"].(map[string]any); payload["status"] != float64(200) || payload["requestId"] == "" {
		t.Fatalf("request.completed %v", done.Data)
	}
}

// The line a client reads next after a clear is a new one: ids keep rising (no reuse), so a page can tell a
// fresh line from one it already holds.
func TestConsoleIDsKeepRisingAcrossAClear(t *testing.T) {
	a := New(Config{DataDir: storetest.DataDir(t)}, nil)
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.Close() })
	events, cancel := a.events.Subscribe()
	defer cancel()
	idOf := func() uint64 {
		deadline := time.After(3 * time.Second)
		for {
			select {
			case event := <-events:
				if event.Type == "console.log" {
					lines := event.Data.(map[string]any)["lines"].([]any)
					return lines[len(lines)-1].(map[string]any)["id"].(uint64)
				}
			case <-deadline:
				t.Fatal("no console.log event")
			}
		}
	}
	a.log.Info("first")
	before := idOf()
	a.logs.PublishTo(a.events) // re-attaching changes nothing about ids
	a.log.Info("second")
	after := idOf()
	if after <= before {
		t.Fatalf("ids went %v then %v", before, after)
	}
}
