package app

import (
	"context"
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

// TestClientDisconnectKeepsExactlyOneUsageRecordWithoutLeaks cancels the client
// mid-request through the real HTTP stack (stream and non-stream) and asserts
// the abandoned request is accounted exactly once and no goroutine outlives it.
func TestClientDisconnectKeepsExactlyOneUsageRecordWithoutLeaks(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			upstreamDone := make(chan struct{}, 1)
			started := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Read the body so net/http's background read can see the disconnect.
				_, _ = io.Copy(io.Discard, r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
					w.(http.Flusher).Flush()
				}
				started <- struct{}{}
				<-r.Context().Done()
				upstreamDone <- struct{}{}
			}))
			a, key := servingApp(t, upstream.URL, 1)
			c09Observability(t, a)
			ctx, cancelService := context.WithCancel(context.Background())
			var running sync.WaitGroup
			running.Add(1)
			go func() { defer running.Done(); a.usage.Run(ctx) }()
			server := httptest.NewServer(a.Handler())
			client := &http.Client{Transport: &http.Transport{}}

			baseline := runtime.NumGoroutine()
			requestCtx, cancel := context.WithCancel(context.Background())
			body := `{"model":"fast","messages":[{"role":"user","content":"hi"}]`
			if stream {
				body += `,"stream":true`
			}
			request, _ := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body+"}"))
			request.Header.Set("Authorization", "Bearer "+key)
			request.Header.Set("X-Request-Id", "c09-cancel")
			clientDone := make(chan error, 1)
			go func() {
				response, err := client.Do(request)
				if err == nil {
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
			if len(rows) != 1 || rows[0].requestID != "c09-cancel" || rows[0].termination != "client_abandoned" {
				t.Fatalf("enabled=%v written=%d lost=%d usage=%+v want exactly one client_abandoned record for c09-cancel", a.usage.Enabled(), a.usage.Written(), a.usage.Lost(), rows)
			}
			if a.usage.Lost() != 0 {
				t.Fatalf("lost=%d", a.usage.Lost())
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
	a.drainGrace = 300 * time.Millisecond

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
	defer cancel()
	go a.usage.Run(ctx)

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
				if strings.Contains(fmt.Sprint(value), "198.51.100.77") || strings.Contains(fmt.Sprint(value), "203.0.113.9") {
					t.Errorf("table %s column %s persisted a client address: %v", name, columns[i], value)
				}
			}
		}
		rows.Close()
	}
}
