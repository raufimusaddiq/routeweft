// Package mockupstream serves deterministic upstream responses for fixtures.
//
// It is the local mock upstream named by PRD §19 and SPEC §28.3: inference
// tests point a provider base URL at it and assert byte-for-byte behavior
// without contacting a real provider.
package mockupstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
)

// Decision describes how the mock answers one request.
type Decision struct {
	// ExchangeID is recorded for assertions; empty means no fixture matched.
	ExchangeID string
	// Status is the HTTP status to answer with.
	Status int
	// Headers are applied before the body is written.
	Headers map[string]string
	// Body is written verbatim, so tests see exact wire framing.
	Body string
	// Delay is observed before the body is written, for cancellation tests.
	Delay time.Duration
	// Disconnect closes the connection after writing Body without a terminator.
	Disconnect bool
}

// Server is an in-process mock upstream with request accounting.
type Server struct {
	http  *http.Server
	addr  string
	mu    sync.Mutex
	seen  []SeenRequest
	match func(*http.Request, string) Decision
}

// SeenRequest is one observed upstream request.
type SeenRequest struct {
	Method     string
	Path       string
	Headers    http.Header
	Body       string
	ExchangeID string
}

// Option customizes the mock server.
type Option func(*Server)

// WithMatcher replaces the default fixture matcher.
func WithMatcher(match func(*http.Request, string) Decision) Option {
	return func(s *Server) { s.match = match }
}

// Start launches the mock upstream on a loopback port and returns it.
func Start(fixtureSet []fixtures.Exchange, options ...Option) (*Server, error) {
	server := &Server{}
	server.match = byPathAndBody(fixtureSet)
	for _, option := range options {
		option(server)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server.addr = listener.Addr().String()
	server.http = &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.http.Serve(listener) }()
	return server, nil
}

// URL returns the mock's base URL for provider configuration.
func (s *Server) URL() string { return "http://" + s.addr }

// Requests returns a copy of the observed upstream requests.
func (s *Server) Requests() []SeenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SeenRequest(nil), s.seen...)
}

// Close shuts the mock upstream down.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

// maxFixtureBodyBytes bounds how much mock-upstream request body is buffered for
// fixture matching. It is deliberately above the 128 MiB public ingress limit so
// large-body and long-context compatibility tests can drive real payloads through
// the mock without a second, artificial cap masking product behavior.
const maxFixtureBodyBytes = 160 << 20

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFixtureBodyBytes))
	if err != nil {
		http.Error(w, `{"error":{"message":"mock request exceeds 1 MiB fixture limit"}}`, http.StatusRequestEntityTooLarge)
		return
	}
	bodyText := string(body)
	decision := s.match(r, bodyText)
	s.mu.Lock()
	s.seen = append(s.seen, SeenRequest{
		Method:     r.Method,
		Path:       r.URL.Path,
		Headers:    r.Header.Clone(),
		Body:       bodyText,
		ExchangeID: decision.ExchangeID,
	})
	s.mu.Unlock()

	status := decision.Status
	if status == 0 {
		status = http.StatusNotFound
	}
	for key, value := range decision.Headers {
		w.Header().Set(key, value)
	}
	w.WriteHeader(status)
	if _, err := w.Write([]byte(decision.Body)); err != nil && errors.Is(err, net.ErrClosed) {
		return
	}
	if decision.Delay > 0 {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-time.After(decision.Delay):
		case <-r.Context().Done():
			return
		}
	}
	if decision.Disconnect {
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}
}

// byPathAndBody matches a fixture by request path, then by exact request body,
// then by path alone. That lets one path serve several recorded variants.
func byPathAndBody(set []fixtures.Exchange) func(*http.Request, string) Decision {
	ordered := append([]fixtures.Exchange(nil), set...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	return func(r *http.Request, body string) Decision {
		pathMatches := make([]fixtures.Exchange, 0, len(ordered))
		for _, exchange := range ordered {
			if exchange.Request.Path == r.URL.Path {
				pathMatches = append(pathMatches, exchange)
			}
		}
		for _, exchange := range pathMatches {
			if exactJSON(exchange.Request.Body, body) {
				return decisionFor(exchange)
			}
		}
		if len(pathMatches) > 0 {
			return decisionFor(pathMatches[0])
		}
		return Decision{Status: http.StatusNotFound, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"error":{"message":"no fixture for path","type":"mock_upstream"}}`}
	}
}

func decisionFor(exchange fixtures.Exchange) Decision {
	return Decision{
		ExchangeID: exchange.ID,
		Status:     exchange.Response.Status,
		Headers:    exchange.Response.Headers,
		Body:       exchange.Response.Body,
		Disconnect: exchange.Kind == fixtures.KindCancellation,
	}
}

func exactJSON(want, got string) bool {
	return strings.TrimSpace(want) == strings.TrimSpace(got)
}
