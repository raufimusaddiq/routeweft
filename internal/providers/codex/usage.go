package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
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
	AvailableReset     *float64               `json:"availableResetCredits,omitempty"`
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
	return parseUsage(body)
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
	data, decoded := shared.Object(body)
	if !decoded && status >= 200 && status < 300 {
		return ResetCredits{}, errors.New("codex reset credits response is invalid JSON")
	}
	if status < 200 || status >= 300 {
		return ResetCredits{}, errors.New(errorMessage(body, fmt.Sprintf("Codex reset credits API unavailable (%d).", status)))
	}
	// codex.js:176-184: snake_case first, then the camelCase alias.
	entries, _ := shared.Array(data["credits"])
	credits := make([]ResetCredit, 0, len(entries))
	for _, credit := range entries {
		credits = append(credits, ResetCredit{
			Status:    choose(shared.String(shared.FirstTruthy(shared.Field(credit, "status"))), "unknown"),
			GrantedAt: shared.DateTime(shared.FirstNonNullish(shared.Field(credit, "granted_at"), shared.Field(credit, "grantedAt"))),
			ExpiresAt: shared.DateTime(shared.FirstNonNullish(shared.Field(credit, "expires_at"), shared.Field(credit, "expiresAt"))),
		})
	}
	count, _ := shared.FiniteNumber(shared.FirstNonNullish(data["available_count"], data["availableCount"]))
	return ResetCredits{AvailableCount: nonNegative(count), Credits: credits}, nil
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
	request.Header.Set("OpenAI-Beta", resetCreditsBetaHeader)
	request.Header.Set("originator", Originator)
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
	// codex.js:215-226: code is any truthy value, windows_reset toFiniteNumber.
	decoded, _ := shared.Object(body)
	code := shared.String(shared.FirstTruthy(decoded["code"]))
	windowsReset, _ := shared.FiniteNumber(decoded["windows_reset"])
	ok := response.StatusCode >= 200 && response.StatusCode < 300
	return ResetResult{
		OK:           ok && (code == "reset" || windowsReset > 0),
		NoCredit:     ok && code == "no_credit",
		Status:       response.StatusCode,
		Code:         code,
		WindowsReset: windowsReset,
		Message:      shared.String(shared.FirstTruthy(decoded["message"])),
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

// parseUsage reads the wham/usage body as the reference does (codex.js:121-139).
// Every lookup keeps the reference's precedence: `||` skips any falsy value and
// `??` only a missing or null one.
func parseUsage(body []byte) (Usage, error) {
	data, ok := shared.Object(body)
	if !ok {
		return Usage{}, errors.New("codex usage endpoint returned invalid JSON")
	}
	normal := shared.FirstTruthy(data["rate_limit"], data["rate_limits"], shared.Field(data["rate_limits_by_limit_id"], "codex"))
	if normal == nil {
		normal = json.RawMessage(`{}`)
	}
	review, spark := reviewRateLimit(data), sparkRateLimit(data)
	quotas := map[string]QuotaWindow{}
	appendQuotaWindows(quotas, "", normal)
	appendQuotaWindows(quotas, "review", review)
	appendQuotaWindows(quotas, "spark", spark)
	return Usage{
		Plan:               choose(shared.String(shared.FirstTruthy(data["plan_type"], shared.Field(data["summary"], "plan"))), "unknown"),
		LimitReached:       limitReached(normal),
		ReviewLimitReached: limitReached(review),
		SparkLimitReached:  limitReached(spark),
		AvailableReset:     availableReset(shared.Field(data["rate_limit_reset_credits"], "available_count")),
		Quotas:             quotas,
	}, nil
}

// reviewRateLimit is getCodexReviewRateLimit (codex.js:73-88). A
// rate_limits_by_limit_id object is authoritative: when it has no review entry,
// additional_rate_limits is not consulted.
func reviewRateLimit(data map[string]json.RawMessage) json.RawMessage {
	if window := shared.FirstTruthy(data["code_review_rate_limit"], data["review_rate_limit"]); window != nil {
		return window
	}
	if byID, ok := shared.Object(data["rate_limits_by_limit_id"]); ok {
		return shared.FirstTruthy(byID["code_review"], byID["codex_review"], byID["review"])
	}
	return additionalRateLimit(data, "review")
}

// sparkRateLimit is getCodexSparkRateLimit (codex.js:90-105).
func sparkRateLimit(data map[string]json.RawMessage) json.RawMessage {
	if window := shared.FirstTruthy(data["spark_rate_limit"], data["gpt_5_3_codex_spark_rate_limit"]); window != nil {
		return window
	}
	if byID, ok := shared.Object(data["rate_limits_by_limit_id"]); ok {
		return shared.FirstTruthy(byID["gpt-5.3-codex-spark"], byID["gpt_5_3_codex_spark"], byID["spark"])
	}
	return additionalRateLimit(data, "spark")
}

// additionalRateLimit finds the first additional_rate_limits entry whose id
// (limit_name, else metered_feature, else id, lowercased) contains match. Each
// exact id the reference also lists contains match, so containment is the
// whole test.
func additionalRateLimit(data map[string]json.RawMessage, match string) json.RawMessage {
	entries, _ := shared.Array(data["additional_rate_limits"])
	for _, entry := range entries {
		id := strings.ToLower(shared.String(shared.FirstTruthy(shared.Field(entry, "limit_name"), shared.Field(entry, "metered_feature"), shared.Field(entry, "id"))))
		if strings.Contains(id, match) {
			return entry
		}
	}
	return nil
}

// rateLimitBody is getCodexRateLimitBody (codex.js:35-40): an object's nested
// rate_limit when that is an object, else the object itself.
func rateLimitBody(snapshot json.RawMessage) (json.RawMessage, bool) {
	if _, ok := shared.Object(snapshot); !ok {
		return nil, false
	}
	if nested := shared.Field(snapshot, "rate_limit"); shared.Truthy(nested) && isObjectLike(nested) {
		return nested, true
	}
	return snapshot, true
}

// isObjectLike is `typeof value === "object"` for a non-null value.
func isObjectLike(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")
}

// appendQuotaWindows is appendCodexQuotaWindows (codex.js:53-71).
func appendQuotaWindows(quotas map[string]QuotaWindow, prefix string, snapshot json.RawMessage) bool {
	body, ok := rateLimitBody(snapshot)
	if !ok {
		return false
	}
	primary := shared.FirstTruthy(shared.Field(body, "primary_window"), shared.Field(body, "primary"), shared.Field(snapshot, "primary_window"), shared.Field(snapshot, "primary"))
	secondary := shared.FirstTruthy(shared.Field(body, "secondary_window"), shared.Field(body, "secondary"), shared.Field(snapshot, "secondary_window"), shared.Field(snapshot, "secondary"))
	added := false
	if primary != nil {
		quotas[quotaKey(prefix, "session")] = formatWindow(primary)
		added = true
	}
	if secondary != nil {
		quotas[quotaKey(prefix, "weekly")] = formatWindow(secondary)
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

// formatWindow is formatCodexWindow (codex.js:42-51). A used value that is not
// a finite number is 0, as in the reference.
func formatWindow(window json.RawMessage) QuotaWindow {
	used, _ := shared.FiniteNumber(shared.FirstNonNullish(shared.Field(window, "used_percent"), shared.Field(window, "percent_used")))
	used = clamp(used)
	reset := shared.ResetTime(shared.FirstNonNullish(shared.Field(window, "reset_at"), shared.Field(window, "resets_at"), shared.Field(window, "resetAt")))
	return QuotaWindow{Used: used, Total: 100, Remaining: 100 - used, ResetAt: reset}
}

func limitReached(snapshot json.RawMessage) bool {
	body, ok := rateLimitBody(snapshot)
	return ok && shared.Truthy(shared.Field(body, "limit_reached"))
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

// availableReset keeps an omitted count unknown (nil) instead of the
// reference's 0; a reported count is toFiniteNumber floored at 0 (codex.js:125).
func availableReset(raw json.RawMessage) *float64 {
	if shared.Nullish(raw) {
		return nil
	}
	value, _ := shared.FiniteNumber(raw)
	n := nonNegative(value)
	return &n
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

// errorMessage is the reference's errorMessage over
// `message || error || detail` (codex.js:24-29,172): a string as is, an
// object's string message, else the value's JSON.
func errorMessage(body []byte, fallback string) string {
	data, ok := shared.Object(body)
	if !ok {
		return fallback
	}
	value := shared.FirstTruthy(data["message"], data["error"], data["detail"])
	if value == nil {
		return fallback
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	var message string
	if json.Unmarshal(shared.Field(value, "message"), &message) == nil {
		return message
	}
	var compact bytes.Buffer
	if json.Compact(&compact, value) != nil {
		return fallback
	}
	return compact.String()
}
