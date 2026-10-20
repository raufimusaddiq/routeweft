package ingress

import (
	"io"
	"net/http"
	"strings"
	"time"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

// maxCapturedUsageBytes bounds the one usage object a non-stream capture keeps.
// A real usage object is a few hundred bytes; anything larger is not parsed.
const maxCapturedUsageBytes = 64 << 10

// relayNative forwards a successful native OpenAI Chat or Responses body and
// accounts for it. Bytes reach the client exactly as the upstream sent them:
// usage is read by a tee on the side, never by buffering the response. When no
// telemetry hook is wired the tee is skipped entirely.
func (h *Handler) relayNative(w http.ResponseWriter, r *http.Request, response *http.Response, provider routing.ProviderRef, source string, stream bool, started time.Time) {
	if stream && source == openaiadapter.ChatProtocol {
		// The provider's response-side stream rewrite, when it has one (an empty
		// tool_calls array some gateways put on every delta).
		response.Body = wire.ResponseStream(provider.ProviderID, response.Body)
	}
	if h.opts.OnRequestComplete == nil {
		if stream && source == openaiadapter.ChatProtocol {
			h.copyStream(w, r, response)
		} else if stream {
			h.copyResponsesStream(w, r, response)
		} else {
			_, _ = io.Copy(w, response.Body)
		}
		return
	}
	tw := &trackedWriter{ResponseWriter: w}
	// The response, not the request, says what is coming back: an upstream may
	// ignore stream:true and answer a JSON document, or stream when not asked.
	// Which relay forwards the bytes is unchanged (the request's flag); which
	// reader measures them follows the response's Content-Type.
	sse := responseIsSSE(response, stream)
	var usage promptcache.Usage
	var found, complete bool
	var relayErr error
	var scanner *usageScanner
	var capture *topLevelUsageCapture
	if sse {
		scanner = &usageScanner{family: source}
		response.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.TeeReader(response.Body, scanner), Closer: response.Body}
	} else {
		capture = &topLevelUsageCapture{}
		response.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.TeeReader(response.Body, capture), Closer: response.Body}
	}
	switch {
	case stream && source == openaiadapter.ChatProtocol:
		h.copyStream(tw, r, response)
	case stream:
		h.copyResponsesStream(tw, r, response)
	default:
		_, relayErr = io.Copy(tw, response.Body)
	}
	if sse {
		usage, found = scanner.usage()
		complete = scanner.complete
	} else {
		// A JSON body has no terminal marker; the stream relays report no read
		// error, so only the plain copy can prove truncation.
		complete = relayErr == nil
		if raw, ok := capture.value(); ok {
			usage, found = shared.ParseUsage(source, append(append([]byte(`{"usage":`), raw...), '}'))
		}
	}
	termination := ""
	switch {
	case tw.failed || r.Context().Err() != nil:
		// The client left, however far the relay got: the context is the
		// authority, not a read error that a fast upstream may never produce.
		// The upstream kept generating and was billed, so the request is
		// recorded with whatever usage had arrived.
		termination = TerminationClientAbandoned
	case !complete:
		termination = TerminationUpstreamTruncated
	}
	h.publishOutcome(r, provider, response.StatusCode, sse, usage, started, termination, !found)
}

// responseIsSSE reports whether the upstream answered an event stream. A
// response with no Content-Type is taken to match what was asked for.
func responseIsSSE(response *http.Response, requestedStream bool) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]))
	if mediaType == "" {
		return requestedStream
	}
	return mediaType == "text/event-stream"
}

// trackedWriter remembers whether a write to the client failed, which is how a
// disconnect that has not yet cancelled the request context shows itself.
type trackedWriter struct {
	http.ResponseWriter
	failed bool
}

func (t *trackedWriter) Write(p []byte) (int, error) {
	n, err := t.ResponseWriter.Write(p)
	if err != nil {
		t.failed = true
	}
	return n, err
}

func (t *trackedWriter) Flush() {
	if flusher, ok := t.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (t *trackedWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// topLevelUsageCapture is an io.Writer that watches a JSON document go by and
// keeps only the value of its top-level "usage" key. It tracks string, escape
// and nesting state byte by byte, so memory is bounded by the usage object
// rather than by the response.
type topLevelUsageCapture struct {
	depth     int
	inString  bool
	escaped   bool
	expectKey bool // at depth 1, the next string is a key
	key       []byte
	keyOK     bool // key collected so far fits in a candidate
	afterKey  bool // a key was read; waiting for its value
	isUsage   bool // the pending value belongs to "usage"
	capturing bool
	captureAt int // depth at which the captured value closes
	buf       []byte
	done      bool
	overflow  bool
}

func (c *topLevelUsageCapture) value() ([]byte, bool) {
	if !c.done || c.overflow {
		return nil, false
	}
	return c.buf, true
}

func (c *topLevelUsageCapture) Write(p []byte) (int, error) {
	if c.done {
		return len(p), nil
	}
	for _, b := range p {
		c.step(b)
		if c.done {
			break
		}
	}
	return len(p), nil
}

func (c *topLevelUsageCapture) step(b byte) {
	if c.capturing {
		if len(c.buf) >= maxCapturedUsageBytes {
			c.overflow = true
			c.capturing = false
			c.done = true
			return
		}
		c.buf = append(c.buf, b)
	}
	if c.inString {
		switch {
		case c.escaped:
			c.escaped = false
		case b == '\\':
			c.escaped = true
		case b == '"':
			c.inString = false
			if c.depth == 1 && c.expectKey {
				c.expectKey = false
				c.afterKey = true
				c.isUsage = c.keyOK && string(c.key) == "usage"
			}
		case c.depth == 1 && c.expectKey:
			if len(c.key) < 16 {
				c.key = append(c.key, b)
			} else {
				c.keyOK = false
			}
		}
		return
	}
	switch b {
	case '"':
		c.inString = true
		if c.depth == 1 && c.expectKey {
			c.key, c.keyOK = c.key[:0], true
		}
	case '{', '[':
		if c.depth == 1 && c.afterKey && c.isUsage && b == '{' && !c.capturing {
			c.capturing = true
			c.captureAt = c.depth
			c.buf = append(c.buf[:0], b)
		}
		c.depth++
		c.afterKey = false
	case '}', ']':
		c.depth--
		if c.capturing && c.depth == c.captureAt {
			c.capturing = false
			c.done = true
		}
	case ',':
		if c.depth == 1 {
			c.expectKey = true
			c.afterKey = false
		}
	}
	if b == '{' && c.depth == 1 {
		c.expectKey = true
	}
	if c.depth == 1 && c.afterKey && b != ':' && b != ' ' && b != '\n' && b != '\r' && b != '\t' && b != '{' && b != '[' && b != '"' {
		// A scalar value (usage:null, usage:12) is not an object to capture.
		c.afterKey = false
	}
}
