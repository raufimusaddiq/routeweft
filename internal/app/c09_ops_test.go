package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/live"
)

// c09Observability turns usage accounting on with a flush interval far longer
// than any test, so a row can only reach SQLite through an explicit flush or the
// shutdown drain — never through the ticker.
func c09Observability(t *testing.T, a *App) {
	t.Helper()
	setSavers(t, a, map[string]string{"enableObservability": "true", "observabilityBatchSize": "100", "observabilityMaxRecords": "1000", "observabilityFlushIntervalMs": "3600000"})
}

func c09FreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

type c09Usage struct {
	requestID, termination string
	status                 int
}

func c09UsageRows(t *testing.T, a *App) []c09Usage {
	t.Helper()
	rows, err := a.store.DB().Query("SELECT request_id, termination, status FROM usage_events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []c09Usage
	for rows.Next() {
		var row c09Usage
		if err := rows.Scan(&row.requestID, &row.termination, &row.status); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// c09Mentions reports whether an event payload names requestID anywhere in its
// JSON. Each subtest starts a fresh app and a fresh request id, so an event for
// another id belongs to another subtest's request.
func c09Mentions(data any, requestID string) bool {
	encoded, err := json.Marshal(data)
	if err != nil {
		return false
	}
	return strings.Contains(string(encoded), requestID)
}

// TestSIGTERMDrainsInFlightRequestAndFlushesAccounting runs the real listener
// (App.Serve), delivers a real SIGTERM through the same signal.NotifyContext
// wiring cmd/routeweft uses while a request is mid-flight at the upstream, and
// asserts SPEC §24: new connections are refused, the in-flight request still
// completes with its full body, its usage record is flushed by the shutdown
// drain (the ticker is disabled), and Serve exits cleanly (PRD-OBS, SPEC §21/24).
func TestSIGTERMDrainsInFlightRequestAndFlushesAccounting(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8},"choices":[]}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	c09Observability(t, a)
	address := c09FreeAddress(t)
	a.cfg.Listen = address

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Serve(ctx) }()
	waitHTTPReady(t, address)

	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/completions", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("X-Request-Id", "c09-drain")
		response, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(request)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		done <- result{status: response.StatusCode, body: string(body), err: err}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the upstream")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	// Readiness drops and the listener closes while the request is still held.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepting after SIGTERM")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.ready.Load() {
		t.Fatal("readiness stayed true after SIGTERM")
	}
	select {
	case err := <-serveErr:
		t.Fatalf("Serve returned (%v) while a request was still in flight", err)
	case res := <-done:
		t.Fatalf("in-flight request ended before the upstream was released: %+v", res)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	res := <-done
	if res.err != nil || res.status != http.StatusOK || !strings.Contains(res.body, `"completion_tokens":5`) {
		t.Fatalf("in-flight request was not completed: %+v", res)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not exit after drain")
	}

	// Serve closed the store, so read the durable result the way an operator
	// would after restart.
	reopened := New(Config{DataDir: a.cfg.DataDir, CredentialKey: a.cfg.CredentialKey}, nil)
	if err := reopened.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer reopened.store.Close()
	rows := c09UsageRows(t, reopened)
	if len(rows) != 1 || rows[0].requestID != "c09-drain" || rows[0].status != http.StatusOK {
		t.Fatalf("enabled=%v lost=%d written=%d usage after drain=%+v want exactly one 200 record for c09-drain", a.usage.Enabled(), a.usage.Lost(), a.usage.Written(), rows)
	}
	if a.usage.Lost() != 0 {
		t.Fatalf("lost=%d", a.usage.Lost())
	}
}

// c09DisconnectCase is one inference route whose abandoned requests must leave a
// record: the relay it exercises is named by transport (the node's only wire).
type c09DisconnectCase struct {
	name, path, streamPath, body, transport string
	systemOne                               bool
	modes                                   []string
}

var c09DisconnectCases = []c09DisconnectCase{
	{name: "chat-native", path: "/v1/chat/completions", transport: "openai-chat", body: `{"model":"fast","messages":[{"role":"user","content":"hi"}]%s}`, modes: []string{"silent", "partial", "stream"}},
	{name: "responses-native", path: "/v1/responses", transport: "openai-responses", body: `{"model":"fast","input":"hi"%s}`, modes: []string{"silent", "partial", "stream"}},
	{name: "responses-translated", path: "/v1/responses", transport: "openai-chat", body: `{"model":"fast","input":"hi"%s}`, modes: []string{"partial", "stream"}},
	{name: "messages-native", path: "/v1/messages", transport: "anthropic-messages", body: `{"model":"fast","max_tokens":1,"messages":[{"role":"user","content":"hi"}]%s}`, modes: []string{"partial", "stream"}},
	{name: "messages-translated", path: "/v1/messages", transport: "openai-chat", body: `{"model":"fast","max_tokens":1,"messages":[{"role":"user","content":"hi"}]%s}`, modes: []string{"partial", "stream"}},
	{name: "gemini-native", path: "/v1beta/models/fast:generateContent", streamPath: "/v1beta/models/fast:streamGenerateContent", transport: "gemini", body: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]%s}`, modes: []string{"partial", "stream"}},
	{name: "ollama-native", path: "/v1/api/chat", transport: "ollama", body: `{"model":"fast","messages":[{"role":"user","content":"hi"}]%s}`, modes: []string{"partial", "stream"}},
	{name: "systemone", path: "/v1/systemone", systemOne: true, modes: []string{"partial", "stream"}},
}

// TestClientDisconnectKeepsExactlyOneUsageRecordWithoutLeaks cancels the client
// mid-request through the real HTTP stack on every relay (native and translated
// Chat/Responses/Messages, Gemini, System One) and asserts the abandoned request
// is accounted exactly once, with the upstream call cancelled and no goroutine
// outliving it. "silent" cancels before any upstream response, "partial" after
// headers and part of a non-stream body, "stream" after the first stream event.
func TestClientDisconnectKeepsExactlyOneUsageRecordWithoutLeaks(t *testing.T) {
	for _, tc := range c09DisconnectCases {
		for _, mode := range tc.modes {
			tc, mode := tc, mode
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				upstreamDone := make(chan struct{}, 1)
				started := make(chan struct{}, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Read the body so net/http's background read can see the disconnect.
					_, _ = io.Copy(io.Discard, r.Body)
					switch mode {
					case "stream":
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
						w.(http.Flusher).Flush()
					case "partial":
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Content-Length", "100000")
						fmt.Fprint(w, `{"id":"x"`)
						w.(http.Flusher).Flush()
					}
					started <- struct{}{}
					<-r.Context().Done()
					upstreamDone <- struct{}{}
				}))
				var a *App
				var key, model string
				if tc.systemOne {
					a, key, model = systemOneApp(t, "typesafe", upstream.URL, 1)
				} else {
					a, key = servingApp(t, upstream.URL, 1)
					setServingProtocol(t, a, tc.transport)
					if _, err := a.runtime.RefreshPoolBindings(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				c09Observability(t, a)
				liveEvents, unsubscribe := a.events.Subscribe()
				defer unsubscribe()
				feed, err := a.feed.Subscribe()
				if err != nil {
					t.Fatal(err)
				}
				defer feed.Close()
				ctx, cancelService := context.WithCancel(context.Background())
				var running sync.WaitGroup
				running.Add(1)
				go func() { defer running.Done(); a.usage.Run(ctx) }()
				server := httptest.NewServer(a.Handler())
				client := &http.Client{Transport: &http.Transport{}}

				baseline := runtime.NumGoroutine()
				requestCtx, cancel := context.WithCancel(context.Background())
				extra := ""
				if mode == "stream" {
					extra = `,"stream":true`
				}
				path := tc.path
				if mode == "stream" && tc.streamPath != "" {
					path, extra = tc.streamPath, ""
				}
				body := fmt.Sprintf(tc.body, extra)
				if tc.systemOne {
					body = fmt.Sprintf(`{"model":%q,"state":null,"questions":{"ok":null}}`, model)
				}
				requestID := fmt.Sprintf("c09-cancel-%s-%s", tc.name, mode)
				request, _ := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+path, strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("X-Request-Id", requestID)
				clientDone := make(chan error, 1)
				gotHeaders := make(chan struct{}, 1)
				go func() {
					response, err := client.Do(request)
					if err == nil {
						gotHeaders <- struct{}{}
						_, err = io.Copy(io.Discard, response.Body)
						response.Body.Close()
					}
					clientDone <- err
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("request never reached upstream")
				}
				if mode == "stream" {
					// Cancel only once the response is flowing, so the record is
					// the abandoned stream and not a pre-response cancel.
					select {
					case <-gotHeaders:
					case <-time.After(5 * time.Second):
						t.Fatal("stream response never reached the client")
					}
				}
				cancel()
				<-clientDone
				select {
				case <-upstreamDone:
				case <-time.After(5 * time.Second):
					t.Fatal("client disconnect did not cancel the upstream request")
				}

				// The record is enqueued as the handler unwinds; wait for it, then
				// flush and require exactly one row.
				deadline := time.Now().Add(5 * time.Second)
				var rows []c09Usage
				for {
					if err := a.usage.FlushNow(context.Background()); err != nil {
						t.Fatal(err)
					}
					rows = c09UsageRows(t, a)
					if len(rows) > 0 || time.Now().After(deadline) {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				// Give any duplicate emission a chance to show up.
				time.Sleep(100 * time.Millisecond)
				if err := a.usage.FlushNow(context.Background()); err != nil {
					t.Fatal(err)
				}
				rows = c09UsageRows(t, a)
				if len(rows) != 1 || rows[0].requestID != requestID || rows[0].termination != "client_abandoned" {
					t.Fatalf("enabled=%v written=%d lost=%d usage=%+v want exactly one client_abandoned record for %s", a.usage.Enabled(), a.usage.Written(), a.usage.Lost(), rows, requestID)
				}
				if a.usage.Lost() != 0 {
					t.Fatalf("lost=%d", a.usage.Lost())
				}
				// A client abandon is never an upstream failure: no relay may rewrite
				// it to a gateway error, and a stream the upstream answered 200 keeps 200.
				if rows[0].status == http.StatusBadGateway || (mode == "stream" && rows[0].status != http.StatusOK) {
					t.Fatalf("abandoned %s recorded status %d", mode, rows[0].status)
				}
				// It was recorded, but never announced: nothing completed. Scope the drain
				// to this subtest's request id: a prior subtest's abandoned request can
				// still land on the shared bus, and that is not this request being
				// announced.
			drain:
				for {
					select {
					case event := <-liveEvents:
						// The live view counts requests in flight (usage.pending) and
						// may say so for this one; it must not list it as finished.
						if snapshot, ok := event.Data.(live.Snapshot); ok && event.Type == "usage.pending" && len(snapshot.Recent) == 0 {
							continue
						}
						if c09Mentions(event.Data, requestID) {
							t.Fatalf("abandoned request announced on the live event bus: %+v", event)
						}
					case event := <-feed.C():
						if event.RequestID == requestID {
							t.Fatalf("abandoned request announced on the usage feed: %+v", event)
						}
					default:
						break drain
					}
				}

				server.Close()
				upstream.Close()
				client.CloseIdleConnections()
				cancelService()
				running.Wait()
				deadline = time.Now().Add(5 * time.Second)
				for runtime.NumGoroutine() > baseline {
					if time.Now().After(deadline) {
						buf := make([]byte, 1<<16)
						t.Fatalf("goroutines did not return to baseline: baseline=%d now=%d\n%s", baseline, runtime.NumGoroutine(), buf[:runtime.Stack(buf, true)])
					}
					time.Sleep(20 * time.Millisecond)
				}
			})
		}
	}
}

// TestSIGTERMDrainDeadlineBoundsShutdownAndStillAccounts holds a request past the
// drain deadline. Shutdown must stay bounded, the connection is cut (cancelling
// the upstream call), Serve returns the deadline error, and the cut request is
// still accounted once as client_abandoned before the batcher's final drain.
func TestSIGTERMDrainDeadlineBoundsShutdownAndStillAccounts(t *testing.T) {
	arrived := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived <- struct{}{}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	c09Observability(t, a)
	address := c09FreeAddress(t)
	a.cfg.Listen = address
	shortenDrainDeadline(t, 300*time.Millisecond)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Serve(ctx) }()
	waitHTTPReady(t, address)

	clientDone := make(chan error, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/completions", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("X-Request-Id", "c09-deadline")
		response, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(request)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		clientDone <- err
	}()
	<-arrived
	started := time.Now()
	stop()
	var serveResult error
	select {
	case serveResult = <-serveErr:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the drain deadline")
	}
	if serveResult == nil {
		t.Fatal("Serve reported a clean drain although a request outlived the deadline")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s with a 300ms drain deadline", elapsed)
	}
	if err := <-clientDone; err == nil {
		t.Fatal("client saw a successful response from a cut connection")
	}
	reopened := New(Config{DataDir: a.cfg.DataDir, CredentialKey: a.cfg.CredentialKey}, nil)
	if err := reopened.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer reopened.store.Close()
	rows := c09UsageRows(t, reopened)
	if len(rows) != 1 || rows[0].requestID != "c09-deadline" || rows[0].termination != "client_abandoned" {
		t.Fatalf("usage=%+v want exactly one client_abandoned record", rows)
	}
}

// TestForwardedHeadersAreNotRecordedAnywhere drives inference and a failed admin
// login that carry spoofed forwarded-IP headers through the real handler, then
// scans every persisted table. Routeweft records no client address at all
// (usage_events has no address column, request details are redacted payloads),
// so the pin is that an attacker-chosen forwarded value never reaches storage
// as an identity, and the throttle bucket stays the socket peer (see
// internal/control/api TestForwardedHeadersNeverChangeTheThrottleBucket).
func TestForwardedHeadersAreNotRecordedAnywhere(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range r.Header {
			for _, value := range values {
				if strings.Contains(value, "198.51.100.77") {
					t.Errorf("forwarded identity leaked upstream in %s", name)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	c09Observability(t, a)
	setSavers(t, a, map[string]string{"observabilityFlushIntervalMs": "10"})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { defer close(runDone); a.usage.Run(ctx) }()
	// Join the batcher so it cannot outlive the test and skew the sibling
	// test's goroutine-baseline assertion.
	defer func() { cancel(); <-runDone }()

	spoof := func(r *http.Request) {
		r.RemoteAddr = "203.0.113.9:6000"
		r.Header.Set("X-Forwarded-For", "198.51.100.77")
		r.Header.Set("X-Real-Ip", "198.51.100.77")
		r.Header.Set("Forwarded", "for=198.51.100.77")
		r.Header.Set("X-Request-Id", "c09-spoof")
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	spoof(request)
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("inference status=%d body=%s", recorder.Code, recorder.Body)
	}
	login := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"nobody","password":"wrong"}`))
	spoof(login)
	a.Handler().ServeHTTP(httptest.NewRecorder(), login)
	if err := a.usage.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := c09UsageRows(t, a); len(rows) != 1 {
		t.Fatalf("usage=%+v want the one inference record", rows)
	}

	tables, err := a.store.DB().Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	tables.Close()
	for _, name := range names {
		rows, err := a.store.DB().Query("SELECT * FROM " + name)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, value := range values {
				// A BLOB must be compared as text; fmt.Sprint prints []byte as decimal
				// numbers, which would let a stored address evade the scan.
				text := fmt.Sprint(value)
				if raw, ok := value.([]byte); ok {
					text = string(raw)
				}
				if strings.Contains(text, "198.51.100.77") || strings.Contains(text, "203.0.113.9") {
					t.Errorf("table %s column %s persisted a client address: %v", name, columns[i], value)
				}
			}
		}
		rows.Close()
	}
}

// TestUpstream499IsAnOrdinaryCompletion guards the cause-not-number gate: an
// upstream that itself answers 499 (some gateways do) still emits the live
// request.completed event and feed frame, because the client did not abandon it.
func TestUpstream499IsAnOrdinaryCompletion(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(499)
		fmt.Fprint(w, `{"error":{"message":"gateway closed"}}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	events, unsubscribe := a.events.Subscribe()
	defer unsubscribe()
	w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 499 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	select {
	case event := <-events:
		if event.Type != "request.completed" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream 499 lost its request.completed event")
	}
}

// TestTranslatedUnreadableBodyIsRecordedOnce covers a billed upstream whose
// non-stream body cannot be read while the client stays connected: the client
// gets a 502 and exactly one 502 usage row is kept (not a lost record).
func TestTranslatedUnreadableBodyIsRecordedOnce(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100000")
		fmt.Fprint(w, `{"id":"x"`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	setServingProtocol(t, a, "openai-chat")
	c09Observability(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { defer close(runDone); a.usage.Run(ctx) }()
	defer func() { cancel(); <-runDone }()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"fast","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-Request-Id", "c09-unreadable")
	liveEvents, unsubscribe := a.events.Subscribe()
	defer unsubscribe()
	feed, err := a.feed.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Close()
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	if err := a.usage.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	rows := c09UsageRows(t, a)
	if len(rows) != 1 || rows[0].requestID != "c09-unreadable" || rows[0].status != http.StatusBadGateway {
		t.Fatalf("usage=%+v want exactly one 502 record", rows)
	}
	// A gateway-side 502 is an answer and a failure the operator must see: it is
	// announced as an ordinary completion on both live surfaces (RUNBOOK 28).
	select {
	case event := <-liveEvents:
		data, _ := event.Data.(map[string]any)
		if event.Type != "request.completed" || data["status"] != http.StatusBadGateway || data["requestId"] != "c09-unreadable" {
			t.Fatalf("live event=%+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("502 was not announced on the live event bus")
	}
	select {
	case frame := <-feed.C():
		if frame.RequestID != "c09-unreadable" || frame.Status != http.StatusBadGateway {
			t.Fatalf("feed frame=%+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("502 was not announced on the usage feed")
	}
}

// c09CancelOnWrite cancels the request the moment the relay writes the body, so
// the cancellation is observed after usage was fully measured.
type c09CancelOnWrite struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *c09CancelOnWrite) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.cancel()
	return n, err
}

// TestAbandonedCompatNonStreamKeepsMeasuredUsage pins that a cancelled request
// whose non-stream body was fully read and parsed keeps its measured usage and
// is not flagged unobserved: the abandon path derives the same fields from the
// same inputs as a completed request (only termination and live signals differ).
func TestAbandonedCompatNonStreamKeepsMeasuredUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	setServingProtocol(t, a, "gemini")
	c09Observability(t, a)
	ctx, stop := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { defer close(runDone); a.usage.Run(ctx) }()
	defer func() { stop(); <-runDone }()

	requestCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/fast:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)).WithContext(requestCtx)
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-Request-Id", "c09-measured")
	a.Handler().ServeHTTP(&c09CancelOnWrite{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}, request)
	if err := a.usage.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	var input, output, observed int
	var termination string
	if err := a.store.DB().QueryRow("SELECT input_tokens, output_tokens, usage_observed, termination FROM usage_events WHERE request_id='c09-measured'").Scan(&input, &output, &observed, &termination); err != nil {
		t.Fatal(err)
	}
	if input != 7 || output != 3 || observed != 1 || termination != "client_abandoned" {
		t.Fatalf("input=%d output=%d observed=%d termination=%q want 7/3/1/client_abandoned", input, output, observed, termination)
	}
}

// TestAbandonedOllamaNativeStreamKeepsMeasuredUsage cancels a native Ollama
// stream after the usage-bearing final object was relayed. The tokens already
// parsed must survive as an observed row; a stream cancelled before any usage
// arrived must be unobserved, never "measured zero".
func TestAbandonedOllamaNativeStreamKeepsMeasuredUsage(t *testing.T) {
	for _, withUsage := range []bool{true, false} {
		t.Run(fmt.Sprintf("usage=%v", withUsage), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/x-ndjson")
				if withUsage {
					fmt.Fprint(w, `{"model":"m","done":true,"prompt_eval_count":5,"eval_count":2}`+"\n")
				} else {
					fmt.Fprint(w, `{"model":"m","message":{"role":"assistant","content":"x"},"done":false}`+"\n")
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 1)
			setServingProtocol(t, a, "ollama")
			c09Observability(t, a)
			ctx, stop := context.WithCancel(context.Background())
			runDone := make(chan struct{})
			go func() { defer close(runDone); a.usage.Run(ctx) }()
			defer func() { stop(); <-runDone }()
			server := httptest.NewServer(a.Handler())
			defer server.Close()

			requestCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, _ := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/v1/api/chat", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}],"stream":true}`))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("X-Request-Id", "c09-ollama")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			buffer := make([]byte, 1)
			if _, err := response.Body.Read(buffer); err != nil {
				t.Fatal(err)
			}
			cancel()
			var input, output, observed int
			var termination string
			deadline := time.Now().Add(5 * time.Second)
			for {
				if err := a.usage.FlushNow(ctx); err != nil {
					t.Fatal(err)
				}
				err := a.store.DB().QueryRow("SELECT input_tokens, output_tokens, usage_observed, termination FROM usage_events WHERE request_id='c09-ollama'").Scan(&input, &output, &observed, &termination)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no usage row: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			wantIn, wantOut, wantObserved := 0, 0, 0
			if withUsage {
				wantIn, wantOut, wantObserved = 5, 2, 1
			}
			if input != wantIn || output != wantOut || observed != wantObserved || termination != "client_abandoned" {
				t.Fatalf("input=%d output=%d observed=%d termination=%q want %d/%d/%d/client_abandoned", input, output, observed, termination, wantIn, wantOut, wantObserved)
			}
		})
	}
}
