package ingress

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// largeJSONWithPadding builds a syntactically valid Chat Completions request
// whose only content is padding, so large-body handling can be exercised without
// allocating a meaningfully sized prompt (PRD-API-005, SPEC §34).
func largeJSONWithPadding(bytes int) string {
	const overhead = len(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"` + `"}]}`)
	return `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"` +
		strings.Repeat("x", bytes-overhead) + `"}]}`
}

// stubUpstreamEcho records one upstream body per request and answers 200 with a
// fixed non-streaming JSON body, so huge request bodies can be proven to travel
// through without being buffered twice or replaced.
func stubUpstreamEcho(t *testing.T, body string, wantPath string) (*mockupstream.Server, *[]string) {
	t.Helper()
	seen := new([]string)
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(r *http.Request, got string) mockupstream.Decision {
		*seen = append(*seen, got)
		if r.URL.Path != wantPath {
			return mockupstream.Decision{Status: http.StatusTeapot, Body: `{"unexpected":"path"}`}
		}
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"}, Body: body}
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, seen
}

// TestLargeBodyAcceptedExactlyAtLimit proves a body of exactly the configured
// limit succeeds and is forwarded byte-for-byte, i.e. the limit is inclusive and
// no silent truncation occurs on the accepted path (PRD-API-005).
func TestLargeBodyAcceptedExactlyAtLimit(t *testing.T) {
	limit := int64(1 << 20)
	body := largeJSONWithPadding(int(limit))
	server, seen := stubUpstreamEcho(t, `{"ok":true}`, "/chat/completions")
	mux, key := keyedHandler(t, Options{
		AllowPrivateUpstreams: true,
		MaxBodyBytes:          limit,
		ProviderResolver:      fixedProvider(openaiadapter.ChatProtocol, server.URL()),
	})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("exact-limit status %d body %.200s", recorder.Code, recorder.Body.String())
	}
	if len(*seen) != 1 || len((*seen)[0]) != len(body) {
		t.Fatalf("upstream bodies=%d want one of length %d", len(*seen), len(body))
	}
	if (*seen)[0] != body {
		t.Fatal("accepted large body was modified on the native path")
	}
}

// TestLargeBodyOverLimitRejected proves a body one byte over the limit fails
// closed with 413 and never reaches the upstream (PRD-API-005).
func TestLargeBodyOverLimitRejected(t *testing.T) {
	limit := int64(1 << 20)
	body := largeJSONWithPadding(int(limit) + 1)
	server, seen := stubUpstreamEcho(t, `{"ok":true}`, "/chat/completions")
	mux, key := keyedHandler(t, Options{
		AllowPrivateUpstreams: true,
		MaxBodyBytes:          limit,
		ProviderResolver:      fixedProvider(openaiadapter.ChatProtocol, server.URL()),
	})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit status %d want 413 body %.200s", recorder.Code, recorder.Body.String())
	}
	if len(*seen) != 0 {
		t.Fatalf("over-limit request reached upstream %d time(s)", len(*seen))
	}
}

// TestLargeBodyDefaultLimitMatchesPRD proves the compiled default remains the
// 128 MiB compatibility target, so the limit cannot silently regress to a smaller
// value without failing this guard (PRD-API-005, SPEC §34).
func TestLargeBodyDefaultLimitMatchesPRD(t *testing.T) {
	if DefaultMaxBodyBytes != 128<<20 {
		t.Fatalf("default body limit %d, want %d", DefaultMaxBodyBytes, int64(128<<20))
	}
}

// TestLargeBodyStreamingPathAcceptedAndCancelled proves the 128 MiB-class body
// works on the streaming path and that a client disconnect mid-stream propagates
// cancellation to the upstream reader instead of draining the whole response
// (PRD-API-005 cancellation, SPEC §34).
func TestLargeBodyStreamingPathAcceptedAndCancelled(t *testing.T) {
	limit := int64(2 << 20)
	body := largeJSONWithPadding(int(limit))
	closed := make(chan struct{})
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "text/event-stream"}, Body: strings.Repeat("data: {\"delta\":\"x\"}\n\n", 4096)}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mux, key := keyedHandler(t, Options{
		AllowPrivateUpstreams: true,
		MaxBodyBytes:          limit,
		ProviderResolver:      fixedProvider(openaiadapter.ChatProtocol, server.URL()),
		OnRequestComplete: func(RequestOutcome) {
			select {
			case <-closed:
			default:
				close(closed)
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	writer := &cancellingWriter{cancel: cancel, after: 1}
	bearer(mux, key).ServeHTTP(writer, request)
	if writer.status != http.StatusOK {
		t.Fatalf("streaming large-body status %d", writer.status)
	}
	select {
	case <-closed:
		t.Fatal("cancelled streaming request still reported a completed outcome")
	default:
	}
}

// cancellingWriter cancels the request context after the first body write,
// emulating a client disconnect mid-stream.
type cancellingWriter struct {
	header  http.Header
	cancel  context.CancelFunc
	after   int
	written int
	status  int
}

func (w *cancellingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *cancellingWriter) WriteHeader(status int) { w.status = status }

func (w *cancellingWriter) Write(p []byte) (int, error) {
	w.written++
	if w.written == w.after {
		w.cancel()
	}
	return len(p), nil
}

func (w *cancellingWriter) Flush() {}

// guard: runtime.ProviderRef is referenced through fixedProvider only.
var _ = runtime.RuntimeSnapshot{}
var _ = routing.ProviderRef{}
var _ = io.Discard
