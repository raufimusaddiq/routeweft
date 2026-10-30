package app

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// c09Serve starts the real listener (the http.Server built by serveOnce, not an
// httptest wrapper) and returns its address; the server stops with the test.
func c09Serve(t *testing.T, a *App) string {
	t.Helper()
	address := c09FreeAddress(t)
	a.cfg.Listen = address
	ctx, stop := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Serve(ctx) }()
	waitHTTPReady(t, address)
	t.Cleanup(func() {
		stop()
		select {
		case <-serveErr:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	return address
}

// newH2CUpstream is a counting upstream that fails the test if a client hop
// header reaches it; it streams when the forwarded body asks for it.
func newH2CUpstream(t *testing.T, hits *atomic.Int32) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Upgrade") != "" || r.Header.Get("HTTP2-Settings") != "" {
			t.Errorf("client hop headers reached upstream: %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// PRD-API-005: an h2c-capable client must not fail merely because it attempts
// an HTTP/2 cleartext upgrade. The listener serves HTTP/1.1 only; the upgrade
// attempt is ignored (200, never 101) and the request is served natively, for
// buffered, streamed and health requests alike, with the client's hop headers
// stripped before the upstream sees them.
func TestC09H2CUpgradeAttemptIsServedAsHTTP11OnTheRealListener(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := newH2CUpstream(t, &upstreamHits)
	a, key := servingApp(t, upstream, 1)
	address := c09Serve(t, a)

	for _, tc := range []struct {
		name, path, body, method, wantType, wantBody string
		auth                                         bool
	}{
		{name: "buffered", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`, auth: true, wantType: "application/json", wantBody: `{"choices":[]}`},
		{name: "streamed", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"fast","stream":true,"messages":[{"role":"user","content":"hi"}]}`, auth: true, wantType: "text/event-stream", wantBody: "[DONE]"},
		{name: "health", method: http.MethodGet, path: "/health/live", wantBody: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(tc.method, "http://"+address+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth {
				request.Header.Set("Authorization", "Bearer "+key)
			}
			request.Header.Set("Connection", "Upgrade, HTTP2-Settings")
			request.Header.Set("Upgrade", "h2c")
			request.Header.Set("HTTP2-Settings", "AAMAAABkAARAAAAAAAIAAAAA")
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || response.ProtoMajor != 1 {
				t.Fatalf("status=%d proto=%s want 200 over HTTP/1.x (never 101): %s", response.StatusCode, response.Proto, body)
			}
			if response.Header.Get("Upgrade") != "" {
				t.Fatalf("server offered an upgrade: %v", response.Header)
			}
			if tc.wantType != "" && !strings.HasPrefix(response.Header.Get("Content-Type"), tc.wantType) {
				t.Fatalf("content-type %q want %q", response.Header.Get("Content-Type"), tc.wantType)
			}
			if !strings.Contains(string(body), tc.wantBody) {
				t.Fatalf("body %q missing %q", body, tc.wantBody)
			}
		})
	}
	if upstreamHits.Load() != 2 {
		t.Fatalf("upstream served %d requests, want the 2 inference requests", upstreamHits.Load())
	}
}

// Prior-knowledge HTTP/2 (the client opens with the connection preface and no
// upgrade) is deliberately not served: PARITY records it as unclaimed. The
// evidence is that the listener answers or closes deterministically — it never
// serves the request, never hangs the connection — and keeps serving ordinary
// HTTP/1.1 afterwards.
func TestC09PriorKnowledgeHTTP2IsRefusedWithoutHangingTheListener(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := newH2CUpstream(t, &upstreamHits)
	a, key := servingApp(t, upstream, 1)
	address := c09Serve(t, a)

	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	// RFC 9113 §3.4 client connection preface followed by an empty SETTINGS frame.
	if _, err := io.WriteString(conn, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00"); err != nil {
		t.Fatal(err)
	}
	reply, readErr := io.ReadAll(bufio.NewReader(conn))
	if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection hung after the HTTP/2 preface: %q", reply)
	}
	// Go's HTTP/1.1 server reads the preface as a malformed request and answers
	// 400 with Connection: close; anything else (a 2xx, an HTTP/2 SETTINGS frame)
	// would mean prior-knowledge HTTP/2 is being served.
	if !strings.HasPrefix(string(reply), "HTTP/1.1 400 ") || !strings.Contains(string(reply), "Connection: close") {
		t.Fatalf("unexpected reply to the HTTP/2 preface: %q", reply)
	}
	if upstreamHits.Load() != 0 {
		t.Fatal("the preface reached the upstream")
	}

	// The listener is unharmed: a normal HTTP/1.1 inference request still works.
	request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/completions", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("listener degraded after the preface: %d", response.StatusCode)
	}
}
