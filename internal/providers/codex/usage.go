package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// QuotaWindow is one Codex rate-limit window. Values are percentages.
type QuotaWindow struct {
	Used      float64    `json:"used"`
	Total     float64    `json:"total"`
	Remaining float64    `json:"remaining"`
	ResetAt   *time.Time `json:"resetAt,omitempty"`
}

// Usage is the normalized Codex usage snapshot.
type Usage struct {
	Plan               string                 `json:"plan"`
	LimitReached       bool                   `json:"limitReached"`
	ReviewLimitReached bool                   `json:"reviewLimitReached"`
	SparkLimitReached  bool                   `json:"sparkLimitReached"`
	AvailableReset     float64                `json:"availableResetCredits"`
	Quotas             map[string]QuotaWindow `json:"quotas"`
}

// ResetCredit is one reset-credit entry.
type ResetCredit struct {
	Status    string     `json:"status"`
	GrantedAt *time.Time `json:"grantedAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// ResetCredits is the normalized reset-credit listing.
type ResetCredits struct {
	AvailableCount float64       `json:"availableCount"`
	Credits        []ResetCredit `json:"credits"`
}

// ResetResult reports the outcome of consuming one reset credit.
type ResetResult struct {
	OK           bool    `json:"ok"`
	NoCredit     bool    `json:"noCredit"`
	Status       int     `json:"status"`
	Code         string  `json:"code,omitempty"`
	WindowsReset float64 `json:"windowsReset"`
	Message      string  `json:"message,omitempty"`
}

// UsageClient fetches Codex usage and manages reset credits. AccountID maps to
// the ChatGPT-Account-ID header and is not a secret.
type UsageClient struct {
	Client    HTTPClient
	AccountID string
}

// Usage fetches and normalizes the Codex usage snapshot. A non-OK upstream is
// reported as unavailability rather than fabricated exhaustion.
func (c UsageClient) Usage(ctx context.Context, accessToken string) (Usage, error) {
	if strings.TrimSpace(accessToken) == "" {
		return Usage{}, ErrReauthRequired
	}
	body, status, err := c.get(ctx, UsageEndpoint, accessToken)
	if err != nil {
		return Usage{}, err
	}
	if status < 200 || status >= 300 {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return Usage{}, ErrReauthRequired
		}
		return Usage{}, fmt.Errorf("codex usage endpoint returned HTTP %d", status)
	}
	var payload usagePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Usage{}, errors.New("codex usage endpoint returned invalid JSON")
	}
	quotas := map[string]QuotaWindow{}
	primary := firstWindow(payload.RateLimit, payload.RateLimits)
	appendQuotaWindows(quotas, "", primary)
	appendQuotaWindows(quotas, "review", reviewWindow(payload))
	appendQuotaWindows(quotas, "spark", sparkWindow(payload))
	return Usage{
		Plan:               choose(payload.PlanType, payload.Summary.Plan, "unknown"),
		LimitReached:       limitReached(primary),
		ReviewLimitReached: limitReached(reviewWindow(payload)),
		SparkLimitReached:  limitReached(sparkWindow(payload)),
		AvailableReset:     nonNegative(payload.RateLimitResetCredits.AvailableCount),
		Quotas:             quotas,
	}, nil
}

// ResetCredits lists available reset credits.
func (c UsageClient) ResetCredits(ctx context.Context, accessToken string) (ResetCredits, error) {
	if strings.TrimSpace(accessToken) == "" {
		return ResetCredits{}, ErrReauthRequired
	}
	body, status, err := c.get(ctx, ResetCreditsURL, accessToken)
	if err != nil {
		return ResetCredits{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ResetCredits{}, ErrReauthRequired
	}
	var payload struct {
		AvailableCount float64 `json:"available_count"`
		Credits        []struct {
			Status    string `json:"status"`
			GrantedAt any    `json:"granted_at"`
			ExpiresAt any    `json:"expires_at"`
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil && status >= 200 && status < 300 {
		return ResetCredits{}, errors.New("codex reset credits response is invalid JSON")
	}
	if status < 200 || status >= 300 {
		return ResetCredits{}, errors.New(errorMessage(body, fmt.Sprintf("Codex reset credits API unavailable (%d).", status)))
	}
	credits := make([]ResetCredit, 0, len(payload.Credits))
	for _, credit := range payload.Credits {
		credits = append(credits, ResetCredit{Status: choose(credit.Status, "", "unknown"), GrantedAt: isoTime(credit.GrantedAt), ExpiresAt: isoTime(credit.ExpiresAt)})
	}
	return ResetCredits{AvailableCount: nonNegative(payload.AvailableCount), Credits: credits}, nil
}

// ConsumeResetCredit spends one reset credit. This is irreversible, so the
// caller must treat it as an explicit operator action.
func (c UsageClient) ConsumeResetCredit(ctx context.Context, accessToken, redeemRequestID string) (ResetResult, error) {
	if strings.TrimSpace(accessToken) == "" {
		return ResetResult{}, ErrReauthRequired
	}
	if strings.TrimSpace(redeemRequestID) == "" {
		return ResetResult{}, errors.New("codex reset credit requires a redeem request id")
	}
	payload, _ := json.Marshal(map[string]string{"redeem_request_id": redeemRequestID})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, ResetCreditsConsumeURL, strings.NewReader(string(payload)))
	if err != nil {
		return ResetResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	c.authorize(request, accessToken)
	response, err := c.do(request)
	if err != nil {
		return ResetResult{}, errors.New("codex reset credit request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return ResetResult{}, errors.New("codex reset credit response could not be read")
	}
	if len(body) > maxResponseBytes {
		return ResetResult{}, errors.New("codex reset credit response exceeds size limit")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ResetResult{}, ErrReauthRequired
	}
	var decoded struct {
		Code         string  `json:"code"`
		WindowsReset float64 `json:"windows_reset"`
		Message      string  `json:"message"`
	}
	_ = json.Unmarshal(body, &decoded)
	return ResetResult{
		OK:           response.StatusCode >= 200 && response.StatusCode < 300 && (decoded.Code == "reset" || decoded.WindowsReset > 0),
		NoCredit:     response.StatusCode >= 200 && response.StatusCode < 300 && decoded.Code == "no_credit",
		Status:       response.StatusCode,
		Code:         decoded.Code,
		WindowsReset: decoded.WindowsReset,
		Message:      decoded.Message,
	}, nil
}

const (
	ResetCreditsConsumeURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	resetCreditsBetaHeader = "codex-1"
)

func (c UsageClient) get(ctx context.Context, endpoint, accessToken string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("OpenAI-Beta", resetCreditsBetaHeader)
	request.Header.Set("originator", Originator)
	c.authorize(request, accessToken)
	response, err := c.do(request)
	if err != nil {
		return nil, 0, errors.New("codex usage request failed")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, response.StatusCode, errors.New("codex usage response could not be read")
	}
	if len(body) > maxResponseBytes {
		return nil, response.StatusCode, errors.New("codex usage response exceeds size limit")
	}
	return body, response.StatusCode, nil
}

func (c UsageClient) authorize(request *http.Request, accessToken string) {
	request.Header.Set("Authorization", "Bearer "+accessToken)
	if c.AccountID != "" {
		request.Header.Set("ChatGPT-Account-ID", c.AccountID)
	}
}

func (c UsageClient) do(request *http.Request) (*http.Response, error) {
	client := c.Client
	if client == nil {
		client = transport.NewSSRFProtectedClient()
	}
	return client.Do(request)
}

type usagePayload struct {
	PlanType              string            `json:"plan_type"`
	RateLimit             json.RawMessage   `json:"rate_limit"`
	RateLimits            json.RawMessage   `json:"rate_limits"`
	CodeReviewRateLimit   json.RawMessage   `json:"code_review_rate_limit"`
	ReviewRateLimit       json.RawMessage   `json:"review_rate_limit"`
	SparkRateLimit        json.RawMessage   `json:"spark_rate_limit"`
	AdditionalRateLimits  []json.RawMessage `json:"additional_rate_limits"`
	RateLimitResetCredits struct {
		AvailableCount float64 `json:"available_count"`
	} `json:"rate_limit_reset_credits"`
	Summary struct {
		Plan string `json:"plan"`
	} `json:"summary"`
}

type quotaWindow struct {
	UsedPercent float64 `json:"used_percent"`
	PercentUsed float64 `json:"percent_used"`
	ResetAt     any     `json:"reset_at"`
	ResetsAt    any     `json:"resets_at"`
	LimitReach  bool    `json:"limit_reached"`
}

type windowPair struct {
	Primary   json.RawMessage `json:"primary_window"`
	Secondary json.RawMessage `json:"secondary_window"`
	Primary2  json.RawMessage `json:"primary"`
	Second2   json.RawMessage `json:"secondary"`
}

func firstWindow(raw ...json.RawMessage) json.RawMessage {
	for _, candidate := range raw {
		if len(candidate) > 0 && string(candidate) != "null" {
			return candidate
		}
	}
	return nil
}

func reviewWindow(payload usagePayload) json.RawMessage {
	if window := firstWindow(payload.CodeReviewRateLimit, payload.ReviewRateLimit); window != nil {
		return window
	}
	return additionalWindow(payload, func(id string) bool { return strings.Contains(id, "review") })
}

func sparkWindow(payload usagePayload) json.RawMessage {
	if window := firstWindow(payload.SparkRateLimit); window != nil {
		return window
	}
	return additionalWindow(payload, func(id string) bool { return strings.Contains(id, "spark") })
}

func additionalWindow(payload usagePayload, match func(string) bool) json.RawMessage {
	for _, entry := range payload.AdditionalRateLimits {
		var metadata struct {
			LimitName       string          `json:"limit_name"`
			MeteredFeature  string          `json:"metered_feature"`
			ID              string          `json:"id"`
			RateLimit       json.RawMessage `json:"rate_limit"`
			PrimaryWindow   json.RawMessage `json:"primary_window"`
			SecondaryWindow json.RawMessage `json:"secondary_window"`
			Primary         json.RawMessage `json:"primary"`
			Secondary       json.RawMessage `json:"secondary"`
			Limit           json.RawMessage `json:"limit"`
		}
		if json.Unmarshal(entry, &metadata) != nil {
			continue
		}
		id := strings.ToLower(metadata.LimitName + metadata.MeteredFeature + metadata.ID)
		if match(id) {
			if window := firstWindow(metadata.RateLimit, metadata.Limit); window != nil {
				return window
			}
			window, _ := json.Marshal(map[string]json.RawMessage{"primary_window": firstWindow(metadata.PrimaryWindow, metadata.Primary), "secondary_window": firstWindow(metadata.SecondaryWindow, metadata.Secondary)})
			return window
		}
	}
	return nil
}

func appendQuotaWindows(quotas map[string]QuotaWindow, prefix string, raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var pair windowPair
	if err := json.Unmarshal(raw, &pair); err != nil {
		return false
	}
	added := false
	if window, ok := decodeWindow(pair.Primary, pair.Primary2); ok {
		quotas[quotaKey(prefix, "session")] = window
		added = true
	}
	if window, ok := decodeWindow(pair.Secondary, pair.Second2); ok {
		quotas[quotaKey(prefix, "weekly")] = window
		added = true
	}
	return added
}

func quotaKey(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "_" + name
}

func decodeWindow(raw ...json.RawMessage) (QuotaWindow, bool) {
	for _, candidate := range raw {
		if len(candidate) == 0 || string(candidate) == "null" {
			continue
		}
		var window quotaWindow
		if err := json.Unmarshal(candidate, &window); err != nil {
			continue
		}
		used := window.UsedPercent
		if used == 0 {
			used = window.PercentUsed
		}
		used = clamp(used)
		return QuotaWindow{Used: used, Total: 100, Remaining: 100 - used, ResetAt: isoTime(firstNonNil(window.ResetAt, window.ResetsAt))}, true
	}
	return QuotaWindow{}, false
}

func limitReached(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var window quotaWindow
	if err := json.Unmarshal(raw, &window); err != nil {
		return false
	}
	return window.LimitReach
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func isoTime(value any) *time.Time {
	switch typed := value.(type) {
	case nil:
		return nil
	case float64:
		seconds := typed
		if seconds > 1e12 {
			seconds /= 1000
		}
		stamp := time.Unix(int64(seconds), 0).UTC()
		return &stamp
	case string:
		if typed == "" {
			return nil
		}
		if parsed, err := time.Parse(time.RFC3339, typed); err == nil {
			stamp := parsed.UTC()
			return &stamp
		}
	}
	return nil
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func nonNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func choose(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func errorMessage(body []byte, fallback string) string {
	var payload struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fallback
	}
	if payload.Message != "" {
		return payload.Message
	}
	if payload.Detail != "" {
		return payload.Detail
	}
	switch typed := payload.Error.(type) {
	case string:
		if typed != "" {
			return typed
		}
	case map[string]any:
		if message, ok := typed["message"].(string); ok && message != "" {
			return message
		}
	}
	return fallback
}
