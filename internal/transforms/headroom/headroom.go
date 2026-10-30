// Package headroom owns the external Headroom compression integration. It speaks
// the helper's /v1/compress interface as the reference does
// (open-sse/rtk/headroom.js, PARITY 6.37): POST {messages, model[, config]} to
// <base>/v1/compress and read {messages, tokens_before, tokens_after, tokens_saved}.
package headroom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const (
	defaultMaxResponseBytes = 4 << 20
	defaultTimeout          = 3 * time.Second
	// phantomShrink is how much the messages must shrink for a reported saving to be
	// believed (isHeadroomPhantomSavings, minShrinkRatio 0.05).
	phantomShrink = 0.05
)

var defaultClient = transport.NewSSRFProtectedClient()

// Client is the small HTTP surface required by Headroom; tests can supply a
// deterministic fake without replacing global transports.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Transform calls the configured external compression service. Errors are
// returned to Pipeline, which implements the normative fail-open behavior.
type Transform struct {
	AllowPrivate bool
	// URL is the helper's base URL; /v1/compress is appended.
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
	// Error is why the request was left alone, in the reference's words.
	Error string
	// Endpoint is the compress URL without user info, query or fragment.
	Endpoint string
	// TokensBefore, TokensAfter and TokensSaved are what the helper reported.
	TokensBefore, TokensAfter, TokensSaved float64
	// MessagesBefore and MessagesAfter are the JSON sizes of the messages sent and
	// received; Phantom is set when tokens were reported saved but the messages
	// shrank by less than five percent.
	MessagesBefore, MessagesAfter int
	Phantom                       bool
}

func (Transform) Name() string { return "headroom" }

// message is one conversation turn as the helper sees it (OpenAI Chat shape).
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type compressConfig struct {
	CompressUserMessages bool `json:"compress_user_messages"`
}

type payload struct {
	Messages []message       `json:"messages"`
	Model    string          `json:"model,omitempty"`
	Config   *compressConfig `json:"config,omitempty"`
}

type reply struct {
	Messages     []replyMessage `json:"messages"`
	TokensBefore float64        `json:"tokens_before"`
	TokensAfter  float64        `json:"tokens_after"`
	TokensSaved  float64        `json:"tokens_saved"`
}

type replyMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Apply sends one bounded compression request. It rejects malformed/non-HTTP(S)
// URLs, caps time and response size, and never sends content to diagnostics.
func (t Transform) Apply(request transforms.Request) (transforms.Request, error) {
	return t.ApplyContext(context.Background(), request)
}

func (t Transform) ApplyContext(parent context.Context, request transforms.Request) (transforms.Request, error) {
	if strings.TrimSpace(t.URL) == "" {
		return request, t.skip(Diagnostic{Enabled: true, Error: "missing proxy URL"})
	}
	if request.Unsafe != "" {
		return request, t.skip(Diagnostic{Enabled: true, Error: request.Unsafe})
	}
	if len(request.Messages) == 0 {
		return request, t.skip(Diagnostic{Enabled: true, Error: "unsupported " + orUnknown(request.Protocol) + " request shape"})
	}
	base, err := validateURL(t.URL)
	if err != nil {
		return request, t.skip(Diagnostic{Enabled: true, Error: err.Error()})
	}
	validate := transport.ValidatePublicURL
	if t.AllowPrivate {
		validate = transport.ValidateTrustedLocalURL
	}
	if _, err := validate(base.String()); err != nil {
		return request, t.skip(Diagnostic{Enabled: true, Error: err.Error()})
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/compress"
	diagnostic := Diagnostic{Enabled: true, Endpoint: endpoint.String()}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := t.Client
	if client == nil {
		client = defaultClient
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	sent := toWire(request)
	out := payload{Messages: sent, Model: request.Model}
	if t.CompressUserMessages {
		out.Config = &compressConfig{CompressUserMessages: true}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return request, t.skip(withError(diagnostic, "unexpected error: "+err.Error()))
	}
	diagnostic.MessagesBefore = jsonSize(sent)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return request, t.skip(withError(diagnostic, "unexpected error: "+scrub(err.Error())))
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	started := time.Now()
	result, err := client.Do(httpRequest)
	diagnostic.Attempted = true
	diagnostic.Duration = time.Since(started)
	if err != nil {
		return request, t.skip(withError(diagnostic, "request failed: "+scrub(err.Error())))
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return request, t.skip(withError(diagnostic, fmt.Sprintf("proxy returned HTTP %d", result.StatusCode)))
	}
	maxBytes := t.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	responseBody, err := io.ReadAll(io.LimitReader(result.Body, maxBytes+1))
	if err != nil {
		return request, t.skip(withError(diagnostic, "request failed: "+scrub(err.Error())))
	}
	if int64(len(responseBody)) > maxBytes {
		return request, t.skip(withError(diagnostic, "proxy response exceeds configured byte limit"))
	}
	var compressed reply
	if err := json.Unmarshal(responseBody, &compressed); err != nil {
		return request, t.skip(withError(diagnostic, "unexpected error: proxy returned invalid JSON"))
	}
	if compressed.Messages == nil {
		return request, t.skip(withError(diagnostic, "proxy response missing messages[]"))
	}
	if len(compressed.Messages) != len(sent) {
		return request, t.skip(withError(diagnostic, "proxy response did not match the message count"))
	}
	texts := make([]string, len(sent))
	for i, got := range compressed.Messages {
		if got.Role != sent[i].Role {
			return request, t.skip(withError(diagnostic, "proxy response did not preserve message order"))
		}
		text, ok := textOf(got.Content)
		if !ok {
			return request, t.skip(withError(diagnostic, "proxy response missing text content"))
		}
		texts[i] = text
	}
	updated := request.Clone()
	offset := len(request.System)
	for i := range request.System {
		updated.System[i] = texts[i]
	}
	for i := range request.Messages {
		text := texts[offset+i]
		// The helper compresses user turns only when asked; whatever it did to one
		// otherwise is discarded (a stricter rule than the reference's).
		if !t.CompressUserMessages && request.Messages[i].Role == "user" && !request.Messages[i].ToolResult {
			text = request.Messages[i].Content
		}
		updated.Messages[i].Content = text
	}
	received := make([]message, len(sent))
	for i := range sent {
		received[i] = message{Role: sent[i].Role, Content: texts[i]}
	}
	diagnostic.MessagesAfter = jsonSize(received)
	diagnostic.TokensBefore, diagnostic.TokensAfter, diagnostic.TokensSaved = compressed.TokensBefore, compressed.TokensAfter, compressed.TokensSaved
	diagnostic.Phantom = diagnostic.TokensSaved > 0 && diagnostic.MessagesBefore > 0 && diagnostic.MessagesAfter > 0 &&
		float64(diagnostic.MessagesAfter) >= float64(diagnostic.MessagesBefore)*(1-phantomShrink)
	diagnostic.Succeeded = true
	t.report(diagnostic)
	logResult(diagnostic)
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
		timeout = defaultTimeout
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

// toWire is the conversation as the helper reads it: the system slots first, then
// each turn, a tool result as a "tool" turn.
func toWire(request transforms.Request) []message {
	wire := make([]message, 0, len(request.System)+len(request.Messages))
	for _, text := range request.System {
		wire = append(wire, message{Role: "system", Content: text})
	}
	for _, turn := range request.Messages {
		role := turn.Role
		if turn.ToolResult {
			role = "tool"
		}
		wire = append(wire, message{Role: role, Content: turn.Content})
	}
	return wire
}

// textOf reads a reply turn's text: a string, or an array of strings and
// {text} parts joined by newlines (textFromHeadroomMessage).
func textOf(raw json.RawMessage) (string, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, true
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", false
	}
	var found []string
	for _, part := range parts {
		var s string
		if err := json.Unmarshal(part, &s); err == nil {
			found = append(found, s)
			continue
		}
		var object struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal(part, &object); err == nil && object.Text != nil {
			found = append(found, *object.Text)
		}
	}
	if len(found) == 0 {
		return "", false
	}
	return strings.Join(found, "\n"), true
}

var (
	userInfoPattern = regexp.MustCompile(`//[^/@\s]+@`)
	queryPattern    = regexp.MustCompile(`(https?://[^\s?#]+)[?#][^\s)]*`)
)

// scrub removes user info and queries from URLs in an error message
// (scrubSensitiveUrlText).
func scrub(text string) string {
	return queryPattern.ReplaceAllString(userInfoPattern.ReplaceAllString(text, "//"), "$1")
}

func validateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("headroom URL must be an HTTP(S) URL without user info, query or fragment")
	}
	return parsed, nil
}

func jsonSize(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func withError(diagnostic Diagnostic, text string) Diagnostic {
	diagnostic.Error = text
	return diagnostic
}

func orUnknown(text string) string {
	if text == "" {
		return "unknown"
	}
	return text
}

// skip reports a request left alone and returns the reason as an error, which
// the pipeline turns into fail-open.
func (t Transform) skip(diagnostic Diagnostic) error {
	t.report(diagnostic)
	slog.Warn("headroom skipped", "reason", diagnostic.Error, "endpoint", diagnostic.Endpoint)
	return errors.New(diagnostic.Error)
}

func (t Transform) report(diagnostic Diagnostic) {
	if t.Diagnostics != nil {
		t.Diagnostics(diagnostic)
	}
}

// logResult writes the reference's HEADROOM lines (formatHeadroomLog and the
// phantom-savings warning) for a compressed request.
func logResult(d Diagnostic) {
	percent := 0.0
	if d.TokensBefore > 0 {
		percent = d.TokensSaved / d.TokensBefore * 100
	}
	slog.Info("headroom compressed", "tokensSaved", d.TokensSaved, "tokensBefore", d.TokensBefore, "tokensAfter", d.TokensAfter,
		"savedPercent", fmt.Sprintf("%.1f", percent), "messageBytesBefore", d.MessagesBefore, "messageBytesAfter", d.MessagesAfter)
	if d.Phantom {
		slog.Warn("headroom reported a token saving but the messages shrank by less than 5%; the provider may bill near the original payload",
			"messageBytesBefore", d.MessagesBefore, "messageBytesAfter", d.MessagesAfter)
	}
}
