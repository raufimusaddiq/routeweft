// Package usage implements the per-provider quota readers that LiteRouter
// exposes, adapted to the normalized quota.State contract.
//
// Each reader mirrors one handler in the reference dispatcher
// (open-sse/services/usage.js USAGE_HANDLERS) and reads the same upstream
// endpoint with the same credential and headers, so a provider that reports
// usage there reports it here. Normalization (used -> remaining fraction) is the
// only shared part.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// maxBodyBytes bounds every usage response so a hostile or broken upstream
// cannot exhaust memory in the control plane.
const maxBodyBytes = 1 << 20

// requestTimeout bounds one usage read; quota refresh must never block routing.
const requestTimeout = 15 * time.Second

// HTTPClient is the minimal client contract. The app passes a pooled,
// SSRF-guarded client so usage reads obey the same outbound policy as inference.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Client is the shared transport for every reader.
type Client struct {
	HTTP HTTPClient
	Now  func() time.Time
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// response is one usage read. A non-2xx status is not an error: each reader
// decides what a status means for that provider (401 is reauth, 403 may be an
// entitlement, 429 a cooldown). Headers are kept because some providers report
// rate limits only there (Groq).
type response struct {
	status  int
	body    []byte
	headers http.Header
}

// call performs one usage request and returns the response.
func (c *Client) call(ctx context.Context, method, endpoint string, headers map[string]string, body []byte) (response, error) {
	return c.callWithin(ctx, requestTimeout, method, endpoint, headers, body)
}

// callWithin is call with a per-call bound for the providers whose reference
// handler sets its own (Cloud Code, Xiaomi MiMo usage reads).
func (c *Client) callWithin(ctx context.Context, timeout time.Duration, method, endpoint string, headers map[string]string, body []byte) (response, error) {
	if c.HTTP == nil {
		return response{}, errors.New("usage client requires an HTTP client")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return response{}, fmt.Errorf("build usage request: %w", err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	reply, err := c.HTTP.Do(request)
	if err != nil {
		return response{}, fmt.Errorf("usage request failed: %w", err)
	}
	defer reply.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(reply.Body, maxBodyBytes))
	if err != nil {
		return response{status: reply.StatusCode, headers: reply.Header}, fmt.Errorf("read usage response: %w", err)
	}
	return response{status: reply.StatusCode, body: payload, headers: reply.Header}, nil
}

// decode unmarshals a usage payload into a typed target.
func decode(payload []byte, target any) error {
	if len(payload) == 0 {
		return errors.New("usage response was empty")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode usage response: %w", err)
	}
	return nil
}

// number reads a JSON value that providers send as a number or a numeric string.
func number(raw json.RawMessage) (float64, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		return value, true
	}
	var quoted string
	if err := json.Unmarshal([]byte(text), &quoted); err == nil {
		var parsed float64
		if _, err := fmt.Sscan(strings.TrimSpace(quoted), &parsed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// fraction converts a remaining percentage into the normalized fraction.
func fraction(remainingPercent float64) *float64 { return quota.RemainingPercent(remainingPercent) }

// resetAt parses the reset formats providers use: ISO strings, Unix seconds and
// Unix milliseconds. An unparseable value becomes nil, never a bogus time.
func resetAt(raw any) *time.Time {
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return &parsed
		}
		if parsed, err := time.Parse("2006-01-02", trimmed); err == nil {
			return &parsed
		}
		return numericReset(trimmed)
	case float64:
		return numericReset(fmt.Sprintf("%.0f", value))
	case json.Number:
		return numericReset(value.String())
	default:
		return nil
	}
}

func numericReset(text string) *time.Time {
	var value float64
	if _, err := fmt.Sscan(text, &value); err != nil || value <= 0 {
		return nil
	}
	// Providers mix seconds and milliseconds; 1e12 separates them.
	if value < 1e12 {
		value *= 1000
	}
	parsed := time.UnixMilli(int64(value)).UTC()
	return &parsed
}

// window builds one normalized quota window from a used/total pair.
func window(name string, used, total float64, reset *time.Time) quota.Window {
	if total <= 0 {
		return quota.Window{Name: name, Unlimited: true, ResetAt: reset}
	}
	remaining := (total - used) / total
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 1 {
		remaining = 1
	}
	return quota.Window{Name: name, Remaining: &remaining, ResetAt: reset}
}

// countWindow builds a window that keeps the provider's absolute amounts. Its
// remaining fraction is (total − used) / total; a zero total has none, so it is
// neither unlimited nor exhausted.
func countWindow(name string, used, total float64, remaining *float64, reset *time.Time) quota.Window {
	window := quota.Window{Name: name, ResetAt: reset, Count: &quota.Count{Used: &used, Total: &total, Remaining: remaining}}
	if total > 0 {
		left := math.Max(0, math.Min(1, (total-used)/total))
		window.Remaining = &left
	}
	return window
}

// finite is JavaScript's `Number(x) || 0` with ±Infinity also read as 0: the
// read model is JSON, which cannot carry either.
func finite(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

// percentWindow builds one window from a remaining percentage.
func percentWindow(name string, remainingPercent float64, reset *time.Time) quota.Window {
	return quota.Window{Name: name, Remaining: fraction(remainingPercent), ResetAt: reset}
}

// state assembles a normalized snapshot.
func (c *Client) state(plan string, windows ...quota.Window) quota.State {
	return quota.State{Plan: plan, Windows: windows, ObservedAt: c.now()}
}

// failure turns a transport/status failure into an error observation. The
// caller records it as StatusError, which routing treats as "not exhausted".
func failure(format string, args ...any) (quota.State, error) {
	return quota.State{}, fmt.Errorf(format, args...)
}
