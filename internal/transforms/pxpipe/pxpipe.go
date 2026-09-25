// Package pxpipe owns the PXPIPE large/image-heavy context transform
// (SPEC §17.4). It runs last in the token-saver pipeline slot after Ponytail
// and before prompt-cache anchoring (BDR-012).
package pxpipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const (
	defaultMinChars        = 25000
	defaultTimeout         = 15 * time.Second
	defaultMaxResponseSize = 8 << 20
)

var defaultClient = transport.NewSSRFProtectedClient()

// Client is the HTTP surface PXPIPE needs; tests inject a deterministic fake.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Transform calls the configured external PXPIPE service for large contexts.
// The pipeline owns fail-open behavior, so Apply returns errors rather than
// silently substituting content.
type Transform struct {
	Enabled          bool
	URL              string
	MinChars         int
	Timeout          time.Duration
	MaxResponseBytes int64
	Client           Client
	Diagnostics      func(Diagnostic)
}

// Diagnostic is the request-detail record for one PXPIPE attempt.
type Diagnostic struct {
	Step        string        `json:"step"`
	Status      string        `json:"status"`
	Skipped     bool          `json:"skipped"`
	Attempted   bool          `json:"attempted"`
	Succeeded   bool          `json:"succeeded"`
	InputChars  int           `json:"input_chars"`
	OutputChars int           `json:"output_chars"`
	Duration    time.Duration `json:"duration"`
	Error       string        `json:"error,omitempty"`
}

// Stats is the cumulative process-local counter set exposed by the control API.
type Stats struct {
	Attempts  uint64 `json:"attempts"`
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
	Skipped   uint64 `json:"skipped"`
}

type counters struct{ attempts, succeeded, failed, skipped atomic.Uint64 }

// Service holds the transform plus the health/log/stats control state.
type Service struct {
	Transform Transform
	counters  counters
	logMu     sync.Mutex
	log       []Diagnostic
	logLimit  int
}

func (Transform) Name() string { return "pxpipe" }

type payload struct {
	System   []string             `json:"system"`
	Messages []transforms.Message `json:"messages"`
}

type response struct {
	System   []string             `json:"system"`
	Messages []transforms.Message `json:"messages"`
}

// Apply transforms one request. Context size is estimated from the neutral
// view; requests below the threshold are skipped without contacting PXPIPE.
func (t Transform) Apply(request transforms.Request) (transforms.Request, error) {
	if !t.Enabled {
		return request, nil
	}
	minChars := t.MinChars
	if minChars <= 0 {
		minChars = defaultMinChars
	}
	inputChars := size(request)
	if inputChars < minChars {
		t.report(Diagnostic{Step: "pxpipe", Status: "skipped", Skipped: true, InputChars: inputChars})
		return request, nil
	}
	if strings.TrimSpace(t.URL) == "" {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Error: "pxpipe URL is required"})
	}
	endpoint, err := validateURL(t.URL)
	if err != nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Error: err.Error()})
	}
	if _, err := transport.ValidatePublicURL(endpoint.String()); err != nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Error: err.Error()})
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	body, err := json.Marshal(payload{System: request.System, Messages: request.Messages})
	if err != nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Error: err.Error()})
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Error: err.Error()})
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	started := time.Now()
	result, err := t.client().Do(httpRequest)
	if err != nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Duration: time.Since(started), Error: "PXPIPE request failed"})
	}
	defer result.Body.Close()
	maxBytes := t.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseSize
	}
	responseBody, err := io.ReadAll(io.LimitReader(result.Body, maxBytes+1))
	if err != nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Duration: time.Since(started), Error: "PXPIPE response read failed"})
	}
	if int64(len(responseBody)) > maxBytes {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Duration: time.Since(started), Error: "pxpipe response exceeds configured byte limit"})
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Duration: time.Since(started), Error: fmt.Sprintf("pxpipe returned HTTP %d", result.StatusCode)})
	}
	var transformed response
	if err := json.Unmarshal(responseBody, &transformed); err != nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Duration: time.Since(started), Error: "pxpipe returned invalid JSON"})
	}
	if transformed.System == nil || transformed.Messages == nil {
		return request, t.fail(Diagnostic{Step: "pxpipe", Attempted: true, InputChars: inputChars, Duration: time.Since(started), Error: "pxpipe response is missing system or messages"})
	}
	updated := request.Clone()
	updated.System = transformed.System
	updated.Messages = transformed.Messages
	diagnostic := Diagnostic{Step: "pxpipe", Status: "succeeded", Attempted: true, Succeeded: true, InputChars: inputChars, OutputChars: size(updated), Duration: time.Since(started)}
	t.report(diagnostic)
	return updated, nil
}

// Health probes the PXPIPE service health endpoint.
func (t Transform) Health(ctx context.Context) error {
	endpoint, err := validateURL(t.URL)
	if err != nil {
		return err
	}
	if _, err := transport.ValidatePublicURL(endpoint.String()); err != nil {
		return err
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/health"
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	response, err := t.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("pxpipe health returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (t Transform) client() Client {
	if t.Client != nil {
		return t.Client
	}
	return defaultClient
}

func (t Transform) fail(diagnostic Diagnostic) error {
	diagnostic.Status = "failed"
	t.report(diagnostic)
	return errors.New(diagnostic.Error)
}

func (t Transform) report(diagnostic Diagnostic) {
	if t.Diagnostics != nil {
		t.Diagnostics(diagnostic)
	}
}

func validateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("pxpipe URL must be an HTTP(S) URL without user info, query or fragment")
	}
	return parsed, nil
}

func size(request transforms.Request) int {
	total := 0
	for _, block := range request.System {
		total += utf8.RuneCountInString(block)
	}
	for _, message := range request.Messages {
		total += utf8.RuneCountInString(message.Content)
	}
	return total
}

// NewService wires a transform to the control-plane state (stats/log/health).
func NewService(transform Transform) *Service {
	service := &Service{Transform: transform, logLimit: 100}
	service.Transform.Diagnostics = service.observe
	return service
}

func (s *Service) observe(diagnostic Diagnostic) {
	switch {
	case diagnostic.Skipped:
		s.counters.skipped.Add(1)
	case diagnostic.Succeeded:
		s.counters.attempts.Add(1)
		s.counters.succeeded.Add(1)
	default:
		s.counters.attempts.Add(1)
		s.counters.failed.Add(1)
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.log = append(s.log, diagnostic)
	if len(s.log) > s.logLimit {
		s.log = s.log[len(s.log)-s.logLimit:]
	}
}

// Stats returns cumulative counters for the control API.
func (s *Service) Stats() Stats {
	return Stats{Attempts: s.counters.attempts.Load(), Succeeded: s.counters.succeeded.Load(), Failed: s.counters.failed.Load(), Skipped: s.counters.skipped.Load()}
}

// Logs returns a copy of the bounded diagnostic log, oldest first.
func (s *Service) Logs() []Diagnostic {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return append([]Diagnostic(nil), s.log...)
}

// Health reports service availability through the transform's probe.
func (s *Service) Health(ctx context.Context) error { return s.Transform.Health(ctx) }
