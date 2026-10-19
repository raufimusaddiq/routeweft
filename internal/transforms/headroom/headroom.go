// Package headroom owns the external Headroom compression integration.
package headroom

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
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const defaultMaxResponseBytes = 4 << 20

var defaultClient = transport.NewSSRFProtectedClient()

// Client is the small HTTP surface required by Headroom; tests can supply a
// deterministic fake without replacing global transports.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Transform calls the configured external compression service. Errors are
// returned to Pipeline, which implements the normative fail-open behavior.
type Transform struct {
	AllowPrivate         bool
	URL                  string
	Timeout              time.Duration
	CompressUserMessages bool
	MaxResponseBytes     int64
	Client               Client
	Diagnostics          func(Diagnostic)
}

// Diagnostic records the result without request content.
type Diagnostic struct {
	Enabled   bool
	Attempted bool
	Succeeded bool
	Duration  time.Duration
	Error     string
}

func (Transform) Name() string { return "headroom" }

type payload struct {
	System               []string             `json:"system"`
	Messages             []transforms.Message `json:"messages"`
	CompressUserMessages bool                 `json:"compress_user_messages"`
}

type response struct {
	System   []string             `json:"system"`
	Messages []transforms.Message `json:"messages"`
}

// Apply sends one bounded compression request. It rejects malformed/non-HTTP(S)
// URLs, caps time and response size, and never sends content to diagnostics.
func (t Transform) Apply(request transforms.Request) (transforms.Request, error) {
	return t.ApplyContext(context.Background(), request)
}

func (t Transform) ApplyContext(parent context.Context, request transforms.Request) (transforms.Request, error) {
	if strings.TrimSpace(t.URL) == "" {
		t.report(Diagnostic{Enabled: true, Error: "headroom URL is required"})
		return request, errors.New("headroom URL is required")
	}
	endpoint, err := validateURL(t.URL)
	if err != nil {
		t.report(Diagnostic{Enabled: true, Error: err.Error()})
		return request, err
	}
	validate := transport.ValidatePublicURL
	if t.AllowPrivate {
		validate = transport.ValidateTrustedLocalURL
	}
	if _, err := validate(endpoint.String()); err != nil {
		t.report(Diagnostic{Enabled: true, Error: err.Error()})
		return request, err
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := t.Client
	if client == nil {
		client = defaultClient
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	body, err := json.Marshal(payload{System: request.System, Messages: request.Messages, CompressUserMessages: t.CompressUserMessages})
	if err != nil {
		t.report(Diagnostic{Enabled: true, Error: err.Error()})
		return request, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		t.report(Diagnostic{Enabled: true, Error: err.Error()})
		return request, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	started := time.Now()
	result, err := client.Do(httpRequest)
	diagnostic := Diagnostic{Enabled: true, Attempted: true, Duration: time.Since(started)}
	if err != nil {
		diagnostic.Error = err.Error()
		t.report(diagnostic)
		return request, err
	}
	defer result.Body.Close()
	maxBytes := t.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	responseBody, err := io.ReadAll(io.LimitReader(result.Body, maxBytes+1))
	if err != nil {
		diagnostic.Error = err.Error()
		t.report(diagnostic)
		return request, err
	}
	if int64(len(responseBody)) > maxBytes {
		diagnostic.Error = "headroom response exceeds configured byte limit"
		t.report(diagnostic)
		return request, errors.New(diagnostic.Error)
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		diagnostic.Error = fmt.Sprintf("headroom returned HTTP %d", result.StatusCode)
		t.report(diagnostic)
		return request, errors.New(diagnostic.Error)
	}
	var compressed response
	if err := json.Unmarshal(responseBody, &compressed); err != nil {
		diagnostic.Error = "headroom returned invalid JSON"
		t.report(diagnostic)
		return request, errors.New(diagnostic.Error)
	}
	if compressed.System == nil || compressed.Messages == nil {
		diagnostic.Error = "headroom response is missing system or messages"
		t.report(diagnostic)
		return request, errors.New(diagnostic.Error)
	}
	if err := transforms.ValidateReplacement(request, compressed.System, compressed.Messages); err != nil {
		diagnostic.Error = err.Error()
		t.report(diagnostic)
		return request, err
	}
	if !t.CompressUserMessages {
		for i := range compressed.Messages {
			if request.Messages[i].Role == "user" && !request.Messages[i].ToolResult {
				compressed.Messages[i].Content = request.Messages[i].Content
			}
		}
	}
	updated := request.Clone()
	updated.System = compressed.System
	updated.Messages = compressed.Messages
	diagnostic.Succeeded = true
	t.report(diagnostic)
	return updated, nil
}

// Health checks service availability with the configured timeout and URL.
func (t Transform) Health(ctx context.Context) error {
	endpoint, err := validateURL(t.URL)
	if err != nil {
		return err
	}
	// Same policy selection as Apply: the default Headroom endpoint is local, so a
	// probe must honor the explicit trusted-local deployment option.
	validate := transport.ValidatePublicURL
	if t.AllowPrivate {
		validate = transport.ValidateTrustedLocalURL
	}
	if _, err := validate(endpoint.String()); err != nil {
		return err
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/health"
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := t.Client
	if client == nil {
		client = defaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &transforms.HealthStatusError{Service: "headroom", StatusCode: response.StatusCode}
	}
	return nil
}

func validateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("headroom URL must be an HTTP(S) URL without user info, query or fragment")
	}
	return parsed, nil
}

func (t Transform) report(diagnostic Diagnostic) {
	if t.Diagnostics != nil {
		t.Diagnostics(diagnostic)
	}
}
