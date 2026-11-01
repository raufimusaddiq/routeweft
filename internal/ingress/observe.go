package ingress

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/live"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

// Capture is what the inference path saw of one request, kept for its request
// detail (PRD-OBS-002; requestDetailsRepo.js, chatCore/requestDetail.js). It is
// bounded: each body is held up to the configured detail limit, with its true size
// beside it, and it exists only while observability is enabled. Nothing in it has
// been redacted yet; the detail builder does that off the request path.
type Capture struct {
	ClientBody       []byte
	ClientBodySize   int
	ProviderRequest  []byte
	ProviderReqSize  int
	ProviderResponse []byte
	ProviderRespSize int
	// Secrets are the credentials this request used, to be scrubbed from every string.
	Secrets []string
	// RouteMode says how the request reached its provider: "native" or "translated".
	RouteMode   string
	ContentType string
}

// requestCapture is the mutable form of a Capture while the request runs.
type requestCapture struct {
	limit int
	mu    sync.Mutex
	data  Capture
}

func newRequestCapture(limit int, clientBody []byte) *requestCapture {
	c := &requestCapture{limit: limit}
	c.data.ClientBodySize = len(clientBody)
	c.data.ClientBody = bounded(clientBody, limit)
	return c
}

// bounded copies at most limit bytes, so a body larger than the limit is not
// retained whole.
func bounded(body []byte, limit int) []byte {
	if len(body) > limit {
		body = body[:limit]
	}
	return append([]byte(nil), body...)
}

// attempt records the request about to be sent upstream; a later attempt (a
// fallback to another account or model) replaces an earlier one, so the detail
// describes the attempt that produced the answer.
func (c *requestCapture) attempt(secret, mode string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.ProviderReqSize = len(body)
	c.data.ProviderRequest = bounded(body, c.limit)
	c.data.RouteMode = mode
	c.data.Secrets = c.data.Secrets[:0]
	if secret != "" {
		c.data.Secrets = append(c.data.Secrets, secret)
	}
	c.data.ProviderResponse, c.data.ProviderRespSize = nil, 0
}

func (c *requestCapture) response(contentType string, body []byte, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.ContentType = contentType
	c.data.ProviderResponse, c.data.ProviderRespSize = body, size
}

// snapshot returns a copy safe to hand to another goroutine.
func (c *requestCapture) snapshot() *Capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := c.data
	copied.Secrets = append([]string(nil), c.data.Secrets...)
	return &copied
}

// bodyProbe wraps an upstream response body. It notes when the first byte was
// read (time to first token for a stream) and, when a capture is attached, keeps
// the first limit bytes and the true size. It changes nothing the client reads.
type bodyProbe struct {
	io.ReadCloser
	first   atomic.Int64 // unix nanoseconds of the first non-empty read
	capture *requestCapture
	mu      sync.Mutex
	kept    []byte
	size    int
	closed  bool
}

func (p *bodyProbe) Read(buffer []byte) (int, error) {
	count, err := p.ReadCloser.Read(buffer)
	if count > 0 {
		p.first.CompareAndSwap(0, time.Now().UnixNano())
		if p.capture != nil {
			p.mu.Lock()
			p.size += count
			if room := p.capture.limit - len(p.kept); room > 0 {
				p.kept = append(p.kept, buffer[:min(count, room)]...)
			}
			p.mu.Unlock()
		}
	}
	return count, err
}

// Close ends the body and hands the capture what was read.
func (p *bodyProbe) Close() error {
	err := p.ReadCloser.Close()
	p.publish()
	return err
}

// publish gives the capture what was read so far. It is also called when the
// outcome is built, because a handler may finish with a body it never closed.
func (p *bodyProbe) publish() {
	if p.capture == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.capture.response("", append([]byte(nil), p.kept...), p.size)
}

// firstByte is when the first byte of the body arrived, or the zero time.
func (p *bodyProbe) firstByte() time.Time {
	if nanos := p.first.Load(); nanos != 0 {
		return time.Unix(0, nanos)
	}
	return time.Time{}
}

// publishFailure hands a request that failed before any upstream answer to the
// detail sink. The reference keeps a detail for such a request (a fetch that threw,
// chatCore.js:441-453) while counting no usage for it, so this is a detail only.
func (h *Handler) publishFailure(r *http.Request, err error) {
	holder := attributionFrom(r)
	if holder != nil && holder.pending != nil {
		holder.pending(true)
		holder.pending = nil
	}
	if h.opts.OnRequestComplete == nil || holder == nil || holder.capture == nil {
		return
	}
	status, message := http.StatusBadGateway, "upstream request failed"
	var failure *dispatchError
	if errors.As(err, &failure) {
		status, message = failure.status, failure.message
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	holder.capture.response("application/json", body, len(body))
	requestID := ""
	if r != nil {
		requestID = r.Header.Get("X-Request-Id")
	}
	duration := time.Duration(0)
	if !holder.begun.IsZero() {
		duration = time.Since(holder.begun)
	}
	h.opts.OnRequestComplete(RequestOutcome{
		RequestID: requestID, ModelID: holder.upstream, RequestedModel: holder.requested, SelectedModel: holder.selected,
		Status: status, Duration: duration, Capture: holder.capture.snapshot(), DetailOnly: true,
	})
}

// finishLive ends the request's in-flight entry and remembers it among the recent
// ones. An answer of 400 or more, or a client that left, counts as a failure of the
// provider that was serving it.
func (h *Handler) finishLive(r *http.Request, provider routing.ProviderRef, status int, usage promptcache.Usage, unobserved bool) {
	holder := attributionFrom(r)
	if holder != nil && holder.pending != nil {
		holder.pending(status >= 400)
		holder.pending = nil
	}
	if h.opts.Live == nil || unobserved {
		return
	}
	state := "ok"
	if status >= 400 {
		state = "error"
	}
	h.opts.Live.Finish(live.Recent{Model: provider.RecordedModel(), Provider: provider.ProviderID, PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, Status: state})
}
