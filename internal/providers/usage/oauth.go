package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// kimi mirrors open-sse/services/usage/kimi.js:103-211. Kimi is dual-auth, and
// an API key wins (kimi.js:109-121): a key goes in x-api-key alone, an OAuth
// device connection uses Bearer plus the X-Msh device headers. The key is the
// stored provider-data copy or, on an API-key connection, the access token.
//
// Values are read as the pin reads them: lower- and capitalized field names
// (`??`), the provider's remaining count ahead of its used count, the reset
// aliases (`||`), and one "Ratelimit" window that a later limit replaces.
// Windows keep the absolute amounts. A 403 for a missing usage entitlement and
// an answer without quota are notices with the plan, not failures.
func (r apiKeyReader) kimi(ctx context.Context, credential quota.Credential) (quota.State, error) {
	headers := map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
	apiKey := credential.ProviderData["apiKey"]
	if apiKey == "" && credential.APIKey {
		apiKey = credential.AccessToken
	}
	switch {
	case apiKey != "":
		headers["x-api-key"] = apiKey
	case credential.AccessToken != "":
		headers["Authorization"] = "Bearer " + credential.AccessToken
		for key, value := range kimiDeviceHeaders(credential.ProviderData["deviceId"]) {
			headers[key] = value
		}
	default:
		return failure("kimi credential is required")
	}
	reply, err := r.call(ctx, http.MethodGet, "https://api.kimi.com/coding/v1/usages", headers, nil)
	if err != nil {
		return failure("kimi usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized {
		return failure("kimi authentication expired")
	}
	if reply.status == http.StatusForbidden && kimiNoUsagePermission(reply.body) {
		// The pin shows the provider's localized text when there is one; the
		// notice here is fixed, so no upstream text reaches the read model.
		return quota.State{Plan: "Kimi Coding", Message: "Kimi connected, but this account has no permission to view usage. Subscribe to Kimi Code to access quota.", ObservedAt: r.now()}, nil
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("kimi usage returned HTTP %d", reply.status)
	}
	data := json.RawMessage(reply.body)
	if len(bytes.TrimSpace(data)) == 0 {
		data = json.RawMessage("{}")
	}
	if !json.Valid(data) {
		return failure("kimi usage response was not JSON")
	}
	var windows []quota.Window
	usage, _ := shared.Object(shared.Field(data, "usage"))
	if limit := kimiNumber(shared.FirstNonNullish(usage["limit"], usage["Limit"]), 0); limit > 0 {
		used := kimiNumber(shared.FirstNonNullish(usage["used"], usage["Used"]), 0)
		remainingRaw := shared.FirstNonNullish(usage["remaining"], usage["Remaining"])
		remaining := math.NaN()
		if !shared.Nullish(remainingRaw) && string(bytes.TrimSpace(remainingRaw)) != `""` {
			remaining = kimiNumber(remainingRaw, math.NaN())
		}
		reset := shared.ResetTime(shared.FirstTruthy(usage["resetTime"], usage["ResetTime"], usage["reset_at"], usage["resetAt"]))
		windows = append(windows, kimiWindow("Weekly", used, limit, remaining, reset))
	}
	items, _ := shared.Array(shared.Field(data, "limits"))
	for _, item := range items {
		detail, _ := shared.Object(shared.Field(item, "detail"))
		limit := kimiNumber(shared.FirstNonNullish(detail["limit"], detail["Limit"]), 0)
		if limit <= 0 {
			continue
		}
		remaining := kimiNumber(shared.FirstNonNullish(detail["remaining"], detail["Remaining"]), math.NaN())
		reported := remaining
		if math.IsNaN(remaining) {
			remaining = limit
		}
		reset := shared.ResetTime(shared.FirstTruthy(detail["resetTime"], detail["reset_at"], detail["resetAt"]))
		next := kimiWindow("Ratelimit", math.Max(0, limit-remaining), limit, remaining, reset)
		if math.IsNaN(reported) {
			next.Count.Remaining = nil
		}
		windows = putWindow(windows, next)
	}
	plan := kimiPlanName(shared.Field(shared.Field(shared.Field(data, "user"), "membership"), "level"))
	if len(windows) == 0 {
		return quota.State{Plan: plan, Message: "Kimi Coding connected. Usage tracked per request.", ObservedAt: r.now()}, nil
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// kimiNumber is toFiniteNumber (shared.js:45-52) with its fallback.
func kimiNumber(raw json.RawMessage, fallback float64) float64 {
	if value, ok := shared.FiniteNumber(raw); ok {
		return value
	}
	return fallback
}

// kimiWindow is makeQuota (kimi.js:75-95): the remaining percentage comes
// from the provider's remaining count when there is one, else from used. The
// count keeps the amounts; remaining is set only when the provider sent it.
func kimiWindow(name string, used, total, remaining float64, reset *time.Time) quota.Window {
	total, used = math.Max(0, total), math.Max(0, used)
	percent := 0.0
	switch {
	case total > 0 && !math.IsNaN(remaining):
		percent = math.Max(0, remaining) / total * 100
	case total > 0:
		percent = math.Max(0, total-used) / total * 100
	}
	count := &quota.Count{Used: &used, Total: &total}
	if !math.IsNaN(remaining) {
		count.Remaining = &remaining
	}
	return quota.Window{Name: name, Remaining: fraction(percent), ResetAt: reset, Count: count}
}

// kimiNoUsagePermission is the pin's entitlement test for a 403
// (kimi.js:32-67): the REASON_FEATURE_NO_PERMISSION reason, or permission or
// subscription wording in the code, the localized message or the body.
func kimiNoUsagePermission(body []byte) bool {
	parsed := json.RawMessage(body)
	if !json.Valid(parsed) {
		parsed = nil
	}
	details, _ := shared.Array(shared.Field(parsed, "details"))
	var detail json.RawMessage
	if len(details) > 0 {
		detail = details[0]
	}
	debug := shared.FirstTruthy(shared.Field(detail, "debug"), shared.Field(parsed, "debug"))
	if shared.String(shared.FirstTruthy(shared.Field(debug, "reason"), shared.Field(parsed, "reason"))) == "REASON_FEATURE_NO_PERMISSION" {
		return true
	}
	localized := shared.FirstTruthy(shared.Field(shared.Field(debug, "localizedMessage"), "message"),
		shared.Field(shared.Field(detail, "localizedMessage"), "message"), shared.Field(parsed, "message"))
	text := string(body)
	if code := shared.FirstTruthy(shared.Field(parsed, "code")); code != nil {
		text = shared.Template(code) + " " + text
	}
	if localized != nil {
		text = shared.Template(localized) + " " + text
	}
	return kimiPermissionWords.MatchString(text)
}

var kimiPermissionWords = regexp.MustCompile(`(?i)permission_denied|do not have permission|subscribe`)

// kimiDeviceHeaders mirrors buildKimiHeaders: the device id must stay stable for
// the whole OAuth session or the quota call is rejected. Like the pin it sends
// the stored id trimmed.
func kimiDeviceHeaders(deviceID string) map[string]string {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		deviceID = "routeweft"
	}
	return map[string]string{
		"X-Msh-Platform":    "routeweft",
		"X-Msh-Device-Id":   deviceID,
		"X-Msh-Device-Name": "routeweft",
	}
}

// kimiPlanName is getKimiPlanName with the "Kimi Coding" default
// (kimi.js:18-30,195-196): a falsy level, or one that is only "LEVEL_", has no
// name; any other value is spelled as String() spells it.
func kimiPlanName(raw json.RawMessage) string {
	levels := map[string]string{
		"LEVEL_BASIC":        "Moderato",
		"LEVEL_INTERMEDIATE": "Allegretto",
		"LEVEL_ADVANCED":     "Allegro",
		"LEVEL_STANDARD":     "Vivace",
	}
	if !shared.Truthy(raw) {
		return "Kimi Coding"
	}
	level := shared.Template(raw)
	if name, ok := levels[level]; ok {
		return name
	}
	if name := strings.ToLower(strings.TrimPrefix(level, "LEVEL_")); name != "" {
		return name
	}
	return "Kimi Coding"
}

// vercel mirrors getVercelAiGatewayUsage in open-sse/services/usage/misc.js.
func (r apiKeyReader) vercel(ctx context.Context, key string) (quota.State, error) {
	reply, err := r.getJSON(ctx, "https://ai-gateway.vercel.sh/v1/credits", key)
	if err != nil {
		return failure("vercel usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("vercel API key invalid or expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("vercel credits returned HTTP %d", reply.status)
	}
	var body struct {
		Balance   json.RawMessage `json:"balance"`
		TotalUsed json.RawMessage `json:"total_used"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("vercel credits response: %v", err)
	}
	balance, _ := number(body.Balance)
	totalUsed, _ := number(body.TotalUsed)
	if balance <= 0 && totalUsed <= 0 {
		return failure("vercel returned no credit allocation")
	}
	// The API does not report the monthly allocation, so the documented $5 free
	// credit is the denominator, exactly as the reference does.
	const monthlyCredit = 5.0
	windows := []quota.Window{
		{Name: "Used (USD)", Unlimited: true},
		window("Remaining (USD)", monthlyCredit-balance, monthlyCredit, nil),
	}
	return quota.State{Plan: "Pay-as-you-go", Windows: windows, ObservedAt: r.now()}, nil
}

// ollama mirrors getOllamaUsage in open-sse/services/usage/misc.js. Limits are
// 0..1 ratios; the free plan has one monthly window and no reset timestamp, so
// the reset is derived from the signup date.
func (r apiKeyReader) ollama(ctx context.Context, key string, credential quota.Credential) (quota.State, error) {
	reply, err := r.call(ctx, http.MethodGet, "https://ollama.com/api/usage", map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}, nil)
	if err != nil {
		return failure("ollama usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("ollama API key invalid or expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("ollama usage returned HTTP %d", reply.status)
	}
	var body struct {
		Limits map[string]struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"limits"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("ollama usage response: %v", err)
	}
	labels := map[string]string{
		"session": "Session (5h)",
		"weekly":  "Weekly (7d)",
		"monthly": "Monthly",
	}
	var windows []quota.Window
	for key, label := range labels {
		limit, ok := body.Limits[key]
		if !ok {
			continue
		}
		usedRatio, ok := number(limit.Usage)
		if !ok {
			continue
		}
		remaining := 1 - usedRatio
		if remaining < 0 {
			remaining = 0
		}
		if remaining > 1 {
			remaining = 1
		}
		windows = append(windows, quota.Window{Name: label, Remaining: &remaining})
	}
	if len(windows) == 0 {
		return failure("ollama returned no usage windows")
	}
	plan := "Ollama Cloud"
	if label, ok := credential.ProviderData["plan"]; ok && label != "" {
		plan = label
	}
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// qoderReader mirrors the pin's qoder handler (open-sse/services/usage.js:45-50)
// and getQoderUsage (services/usage/misc.js:230-290). A personal access token
// (pt-) is exchanged for a job token first, through the same client and cache
// rules as the serving path (qoderModels.js:66-163).
type qoderReader struct {
	Client
	tokens *wire.QoderClient
}

func (r qoderReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	token := credential.AccessToken
	if wire.IsQoderPAT(token) {
		stored := wire.QoderCredentials{AccessToken: token}
		if credential.APIKey {
			stored = wire.QoderCredentials{APIKey: token}
		}
		exchange, cancel := context.WithTimeout(ctx, requestTimeout)
		resolved, err := r.tokens.ResolveCredentials(exchange, stored)
		cancel()
		switch {
		case err == nil:
			token = resolved.AccessToken
		case credential.APIKey:
			// The pin then has no access token to read with.
			return failure("qoder personal access token exchange failed: %v", err)
		}
	}
	if token == "" {
		return failure("qoder access token is required")
	}
	reply, err := r.call(ctx, http.MethodGet, "https://openapi.qoder.sh/api/v2/quota/usage", map[string]string{
		"Authorization": "Bearer " + token, "Accept": "application/json",
	}, nil)
	if err != nil {
		return failure("qoder usage request failed: %v", err)
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("qoder usage returned HTTP %d", reply.status)
	}
	body := json.RawMessage(reply.body)
	if !json.Valid(body) || !shared.Truthy(body) {
		return failure("qoder usage response was not JSON")
	}
	expires := shared.Number(shared.Field(body, "expiresAt"))
	var reset *time.Time
	if !math.IsNaN(expires) && !math.IsInf(expires, 0) && expires > 0 {
		if expires > 8.64e15 {
			return failure("qoder usage expiresAt is out of range")
		}
		at := time.UnixMilli(int64(expires)).UTC()
		reset = &at
	}
	// The page shows used against total and never the provider's remaining
	// count, and drops an organization pool with no total (ProviderLimits/utils.js:459-482).
	windows := []quota.Window{qoderWindow("user", "Personal", shared.Field(body, "userQuota"), reset)}
	if organization := qoderWindow("organization", "Organization", shared.Field(body, "orgResourcePackage"), reset); *organization.Count.Total != 0 {
		windows = append(windows, organization)
	}
	metadata := map[string]string{
		"isQuotaExceeded":      strconv.FormatBool(shared.Truthy(shared.Field(body, "isQuotaExceeded"))),
		"totalUsagePercentage": strconv.FormatFloat(finite(shared.Number(shared.Field(body, "totalUsagePercentage"))), 'f', -1, 64),
	}
	return quota.State{Windows: windows, Metadata: metadata, ObservedAt: r.now()}, nil
}

// qoderWindow reads one pool as `Number(x) || 0` with its unit, "credits" when unnamed.
func qoderWindow(name, label string, pool json.RawMessage, reset *time.Time) quota.Window {
	if !shared.Truthy(pool) {
		pool = nil
	}
	amount := func(field string) float64 { return finite(shared.Number(shared.Field(pool, field))) }
	remaining := amount("remaining")
	window := countWindow(name, amount("used"), amount("total"), &remaining, reset)
	window.DisplayName = label
	window.Count.Unit = "credits"
	if unit := shared.Field(pool, "unit"); shared.Truthy(unit) {
		window.Count.Unit = shared.Template(unit)
	}
	return window
}

// zed mirrors getZedUsage (open-sse/services/usage/zed.js:116-222) and
// fetchZedAuthenticatedUser (shared/zedAuth.js:155-219): plan usage from the
// authenticated user record, with the pin's labels, camelCase aliases, trial
// and overdue flags, and token-billed model requests kept out of the windows.
func (r apiKeyReader) zed(ctx context.Context, credential quota.Credential) (quota.State, error) {
	token := shared.Trim(credential.AccessToken)
	if token == "" {
		return failure("zed access token is required")
	}
	userID := credential.ProviderData["userId"]
	if userID == "" {
		return failure("zed credential is missing its user id")
	}
	headers := map[string]string{"Accept": "application/json", "Authorization": userID + " " + token}
	if systemID := credential.ProviderData["systemId"]; systemID != "" {
		headers["x-zed-system-id"] = systemID
	}
	reply, err := r.call(ctx, http.MethodGet, "https://cloud.zed.dev/client/users/me", headers, nil)
	if err != nil {
		return failure("zed usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("zed authentication failed")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("zed usage returned HTTP %d", reply.status)
	}
	// A body that is not JSON is the pin's {raw: text}, which has no plan.
	user := json.RawMessage(reply.body)
	if !json.Valid(user) {
		user = nil
	}
	plan := shared.FirstTruthy(shared.Field(user, "plan"))
	planID := shared.FirstTruthy(shared.Field(plan, "plan_v3"), shared.Field(plan, "plan_v2"), shared.Field(plan, "plan"), shared.Field(user, "plan_v3"))
	reset := shared.ResetTime(shared.Field(shared.Field(plan, "subscription_period"), "ended_at"))
	if reset == nil {
		reset = shared.ResetTime(shared.Field(shared.Field(plan, "subscriptionPeriod"), "endedAt"))
	}
	usage := shared.FirstTruthy(shared.Field(plan, "usage"))
	var windows []quota.Window
	if predictions := shared.FirstTruthy(shared.Field(usage, "edit_predictions"), shared.Field(usage, "editPredictions")); predictions != nil {
		windows = append(windows, zedWindow("Edit Predictions", shared.Field(predictions, "used"), shared.Field(predictions, "limit"), reset))
	}
	message := ""
	if requests := shared.FirstTruthy(shared.Field(usage, "model_requests"), shared.Field(usage, "modelRequests")); requests != nil {
		limit := shared.Field(requests, "limit")
		if unlimited, total := zedLimit(limit); unlimited || total > 0 {
			windows = append(windows, zedWindow("Hosted Model Requests", shared.Field(requests, "used"), limit, reset))
		} else {
			// A zero limit on Pro/Student is token billing, not a 0-request cap.
			message = "Hosted AI models are billed per token (not request count). Edit Predictions are tracked below. Token spend is on dashboard.zed.dev."
		}
	}
	label := zedPlanLabel(planID)
	trial := shared.Truthy(shared.FirstTruthy(shared.Field(plan, "trial_started_at"), shared.Field(plan, "trialStartedAt")))
	if trial && !strings.Contains(strings.ToLower(label), "trial") {
		label += " (Trial active)"
	}
	overdue := shared.Truthy(shared.FirstTruthy(shared.Field(plan, "has_overdue_invoices"), shared.Field(plan, "hasOverdueInvoices")))
	if overdue {
		message = "This Zed account has overdue invoices. Usage may be blocked until billing is resolved."
	}
	if message == "" && len(windows) == 0 {
		// The pin answers with the plan and an empty quota map.
		message = "Zed connected. No usage limits were returned."
	}
	metadata := map[string]string{"hasOverdueInvoices": strconv.FormatBool(overdue), "trialStarted": strconv.FormatBool(trial)}
	if planID != nil {
		metadata["planId"] = shared.Template(planID)
	}
	return quota.State{Plan: label, Windows: windows, Message: message, Metadata: metadata, ObservedAt: r.now()}, nil
}

// zedLimit is parseZedUsageLimit (zed.js:39-63): "unlimited", a number, a
// numeric string or {limited: N}; anything else is a zero limit.
func zedLimit(limit json.RawMessage) (unlimited bool, total float64) {
	if shared.Nullish(limit) {
		return false, 0
	}
	if string(shared.Field(limit, "unlimited")) == "true" {
		return true, 0
	}
	if value, ok := shared.IsNumber(limit); ok {
		return false, math.Max(0, value)
	}
	var text string
	if json.Unmarshal(limit, &text) == nil {
		if shared.Trim(text) == "unlimited" {
			return true, 0
		}
		if value := shared.Number(limit); !math.IsNaN(value) && !math.IsInf(value, 0) {
			return false, math.Max(0, value)
		}
		return false, 0
	}
	if value, ok := shared.IsNumber(shared.FirstNonNullish(shared.Field(limit, "limited"), shared.Field(limit, "Limited"))); ok {
		return false, math.Max(0, value)
	}
	return false, 0
}

// zedWindow is makeZedQuotaRow (zed.js:71-105): used is clamped to the limit.
func zedWindow(name string, rawUsed, limit json.RawMessage, reset *time.Time) quota.Window {
	used, _ := shared.FiniteNumber(rawUsed)
	used = math.Max(0, used)
	unlimited, total := zedLimit(limit)
	if unlimited {
		return quota.Window{Name: name, Unlimited: true, ResetAt: reset, Count: &quota.Count{Used: &used}}
	}
	if total > 0 {
		used = math.Min(used, total)
	}
	return countWindow(name, used, total, nil, reset)
}

// zedPlanLabel is formatZedPlanLabel (zed.js:13-34).
func zedPlanLabel(raw json.RawMessage) string {
	id := ""
	if shared.Truthy(raw) {
		id = shared.Trim(shared.Template(raw))
	}
	if id == "" {
		return "Zed"
	}
	switch strings.ToLower(id) {
	case "zed_free":
		return "Zed Free"
	case "zed_pro":
		return "Zed Pro"
	case "zed_pro_trial":
		return "Zed Pro Trial"
	case "zed_student":
		return "Zed Student"
	case "zed_business":
		return "Zed Business"
	}
	words := strings.Fields(strings.ReplaceAll(id, "_", " "))
	for i, word := range words {
		first, size := utf8.DecodeRuneInString(word)
		words[i] = strings.ToUpper(string(first)) + strings.ToLower(word[size:])
	}
	return strings.Join(words, " ")
}

// iflow has no usage reader. The reference's handler returns a fixed "usage
// tracked per request" message without making a request, which is not a quota
// read; a stub that reports a healthy plan would make routing trust a number
// nobody measured. The provider is therefore absent from the reader set and the
// observer reports ErrNotConfigured, which routing treats as unknown.
