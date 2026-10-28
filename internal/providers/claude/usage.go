package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const (
	OAuthUsageURL = "https://api.anthropic.com/api/oauth/usage"
	SettingsURL   = "https://api.anthropic.com/v1/settings"
	OrgUsageURL   = "https://api.anthropic.com/v1/organizations/%s/usage"
	APIVersion    = "2023-06-01"
	OAuthBeta     = "oauth-2025-04-20"
	usageCacheTTL = 5 * time.Minute
	oauthCooldown = 3 * time.Minute
	maxUsageBytes = 1 << 20
)

// Quota is one normalized Claude usage window.
type Quota struct {
	Used                float64    `json:"used"`
	Total               float64    `json:"total"`
	Remaining           float64    `json:"remaining"`
	RemainingPercentage float64    `json:"remainingPercentage"`
	ResetAt             *time.Time `json:"resetAt,omitempty"`
	Unlimited           bool       `json:"unlimited"`
	// ScopeKind and ScopeModel are a limits[] entry's kind and the model's
	// display name as reported; the window name lowercases it (claude.js:120).
	ScopeKind  string `json:"scopeKind,omitempty"`
	ScopeModel string `json:"scopeModel,omitempty"`
}

// Usage is normalized Claude Code quota data. Legacy organization usage may
// retain its upstream object shape because that API's schema is provider-owned.
type Usage struct {
	Plan         string           `json:"plan"`
	Organization string           `json:"organization,omitempty"`
	ExtraUsage   json.RawMessage  `json:"extraUsage,omitempty"`
	Quotas       map[string]Quota `json:"quotas,omitempty"`
	Legacy       json.RawMessage  `json:"legacy,omitempty"`
	Message      string           `json:"message,omitempty"`
}

// UsageClient implements Claude OAuth usage with legacy settings/org fallback.
// Results are cached by access token for five minutes; OAuth 429s cool down only
// the quota endpoint for three minutes.
type UsageClient struct {
	Client HTTPClient
	Now    func() time.Time

	mu       sync.Mutex
	cache    map[string]usageCacheEntry
	cooldown map[string]time.Time
}

type usageCacheEntry struct {
	result    Usage
	expiresAt time.Time
}

// Usage fetches normalized Claude usage. force bypasses cache but preserves
// account-scoped cooldown and legacy fallback.
func (c *UsageClient) Usage(ctx context.Context, accessToken string, force bool) (Usage, error) {
	if strings.TrimSpace(accessToken) == "" {
		return Usage{}, errors.New("claude access token is required")
	}
	if !force {
		if cached, ok := c.cached(accessToken); ok {
			return cached, nil
		}
	}
	result, cacheable, err := c.fetch(ctx, accessToken)
	if err != nil {
		return Usage{}, err
	}
	if cacheable {
		c.store(accessToken, result)
		return result, nil
	}
	// The reference prefers the last good read over a soft failure
	// (claude.js:49-50). Routeweft reports the failure instead, so the quota
	// service keeps that read's windows on display without presenting them as
	// a fresh observation to routing (PROVIDER_BASELINE §8).
	if c.hasResult(accessToken) {
		return Usage{}, ErrUsageUnavailable
	}
	return result, nil
}

// ErrUsageUnavailable is a soft failure after an earlier good read: the OAuth
// usage read and its legacy fallback returned no quota.
var ErrUsageUnavailable = errors.New("claude usage unavailable; the last good read is kept")

// hasResult reports a cached read that had windows. A legacy org-usage read is
// cacheable but carries no windows, so there is nothing to keep on display and
// a later soft failure reports its own notice instead.
func (c *UsageClient) hasResult(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[token]
	return ok && len(entry.result.Quotas) > 0
}

func (c *UsageClient) cached(token string) (Usage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.cache[token]; ok && c.now().Before(entry.expiresAt) {
		return entry.result, true
	}
	return Usage{}, false
}

func (c *UsageClient) store(token string, result Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = make(map[string]usageCacheEntry)
	}
	c.cache[token] = usageCacheEntry{result: result, expiresAt: c.now().Add(usageCacheTTL)}
}

func (c *UsageClient) fetch(ctx context.Context, token string) (Usage, bool, error) {
	c.mu.Lock()
	cooling := c.now().Before(c.cooldown[token])
	c.mu.Unlock()
	if !cooling {
		body, status, err := c.request(ctx, OAuthUsageURL, token, map[string]string{"anthropic-beta": OAuthBeta, "anthropic-version": APIVersion})
		if err != nil {
			return Usage{}, false, err
		}
		if status >= 200 && status < 300 {
			result, err := parseOAuthUsage(body)
			return result, err == nil, err
		}
		if status == http.StatusTooManyRequests {
			c.mu.Lock()
			if c.cooldown == nil {
				c.cooldown = make(map[string]time.Time)
			}
			c.cooldown[token] = c.now().Add(oauthCooldown)
			c.mu.Unlock()
		}
	}
	legacy, cacheable, err := c.legacy(ctx, token)
	return legacy, cacheable, err
}

func (c *UsageClient) legacy(ctx context.Context, token string) (Usage, bool, error) {
	settingsBody, status, err := c.request(ctx, SettingsURL, token, map[string]string{"anthropic-version": APIVersion})
	if err != nil {
		return Usage{}, false, err
	}
	if status < 200 || status >= 300 {
		return Usage{Message: "Claude connected. Usage API requires admin permissions."}, false, nil
	}
	var settings struct {
		OrganizationID   string `json:"organization_id"`
		OrganizationName string `json:"organization_name"`
		Plan             string `json:"plan"`
	}
	if err := json.Unmarshal(settingsBody, &settings); err != nil {
		return Usage{}, false, errors.New("claude settings response is invalid JSON")
	}
	result := Usage{Plan: choose(settings.Plan, "Unknown"), Organization: settings.OrganizationName}
	if settings.OrganizationID == "" {
		result.Message = "Claude connected. Usage details require admin access."
		return result, false, nil
	}
	endpoint := fmt.Sprintf(OrgUsageURL, url.PathEscape(settings.OrganizationID))
	usageBody, status, err := c.request(ctx, endpoint, token, map[string]string{"anthropic-version": APIVersion})
	if err != nil {
		return Usage{}, false, err
	}
	if status < 200 || status >= 300 {
		result.Message = "Claude connected. Usage details require admin access."
		return result, false, nil
	}
	result.Legacy = append(json.RawMessage(nil), usageBody...)
	return result, true, nil
}

func (c *UsageClient) request(ctx context.Context, endpoint, token string, headers map[string]string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	client := c.Client
	if client == nil {
		client = transport.NewSSRFProtectedClient()
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, errors.New("claude usage request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxUsageBytes+1))
	if err != nil {
		return nil, response.StatusCode, errors.New("claude usage response could not be read")
	}
	if len(body) > maxUsageBytes {
		return nil, response.StatusCode, errors.New("claude usage response exceeds size limit")
	}
	return body, response.StatusCode, nil
}

// parseOAuthUsage reads the OAuth usage body as the reference does
// (claude.js:77-133). A window needs a numeric utilization; a limits[] entry
// needs kind weekly_scoped, a model display name and a numeric percent, and
// otherwise is skipped on its own. limits[] entries are read last, so one
// replaces a seven_day_<model> window of the same name.
func parseOAuthUsage(body []byte) (Usage, error) {
	data, ok := shared.Object(body)
	if !ok {
		return Usage{}, errors.New("claude OAuth usage response is invalid JSON")
	}
	quotas := make(map[string]Quota)
	for key, raw := range data {
		name := ""
		switch {
		case key == "five_hour":
			name = "session (5h)"
		case key == "seven_day":
			name = "weekly (7d)"
		case strings.HasPrefix(key, "seven_day_"):
			name = "weekly " + strings.TrimPrefix(key, "seven_day_") + " (7d)"
		}
		if name == "" {
			continue
		}
		if used, ok := shared.IsNumber(shared.Field(raw, "utilization")); ok {
			quotas[name] = quota(used, shared.Field(raw, "resets_at"))
		}
	}
	limits, _ := shared.Array(data["limits"])
	for _, limit := range limits {
		if shared.String(shared.Field(limit, "kind")) != "weekly_scoped" {
			continue
		}
		model := strings.TrimSpace(shared.String(shared.Field(shared.Field(shared.Field(limit, "scope"), "model"), "display_name")))
		percent, ok := shared.IsNumber(shared.Field(limit, "percent"))
		if model == "" || !ok {
			continue
		}
		window := quota(percent, shared.Field(limit, "resets_at"))
		window.ScopeKind, window.ScopeModel = "weekly_scoped", model
		quotas["weekly "+strings.ToLower(model)+" (7d)"] = window
	}
	var extra json.RawMessage
	if raw := data["extra_usage"]; !shared.Nullish(raw) {
		extra = append(json.RawMessage(nil), raw...)
	}
	return Usage{Plan: "Claude Code", ExtraUsage: extra, Quotas: quotas}, nil
}

func quota(used float64, reset json.RawMessage) Quota {
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	remaining := 100 - used
	return Quota{Used: used, Total: 100, Remaining: remaining, RemainingPercentage: remaining, ResetAt: shared.ResetTime(reset)}
}

func (c *UsageClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func choose(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
