package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
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

// qoder mirrors getQoderUsage in open-sse/services/usage/misc.js. The response
// carries user and organization credit pools plus one absolute reset.
func (r apiKeyReader) qoder(ctx context.Context, credential quota.Credential) (quota.State, error) {
	token := credential.AccessToken
	if token == "" {
		return failure("qoder access token is required")
	}
	reply, err := r.getJSON(ctx, "https://openapi.qoder.sh/api/v2/quota/usage", token)
	if err != nil {
		return failure("qoder usage request failed: %v", err)
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("qoder usage returned HTTP %d", reply.status)
	}
	var body struct {
		UserQuota struct {
			Total     json.RawMessage `json:"total"`
			Used      json.RawMessage `json:"used"`
			Remaining json.RawMessage `json:"remaining"`
			Unit      string          `json:"unit"`
		} `json:"userQuota"`
		OrgQuota struct {
			Total     json.RawMessage `json:"total"`
			Used      json.RawMessage `json:"used"`
			Remaining json.RawMessage `json:"remaining"`
			Unit      string          `json:"unit"`
		} `json:"orgResourcePackage"`
		ExpiresAt json.RawMessage `json:"expiresAt"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("qoder usage response: %v", err)
	}
	expires, _ := number(body.ExpiresAt)
	var reset *time.Time
	if expires > 0 {
		parsed := time.UnixMilli(int64(expires)).UTC()
		reset = &parsed
	}
	userTotal, _ := number(body.UserQuota.Total)
	userUsed, _ := number(body.UserQuota.Used)
	orgTotal, _ := number(body.OrgQuota.Total)
	orgUsed, _ := number(body.OrgQuota.Used)
	windows := []quota.Window{
		window("user", userUsed, userTotal, reset),
		window("organization", orgUsed, orgTotal, reset),
	}
	return quota.State{Plan: "Qoder", Windows: windows, ObservedAt: r.now()}, nil
}

// zed mirrors getZedUsage in open-sse/services/usage/zed.js: plan limits from the
// authenticated user record.
func (r apiKeyReader) zed(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if credential.AccessToken == "" {
		return failure("zed access token is required")
	}
	userID := credential.ProviderData["userId"]
	reply, err := r.call(ctx, http.MethodGet, "https://cloud.zed.dev/client/users/me", map[string]string{
		"Authorization": userID + " " + credential.AccessToken,
		"Accept":        "application/json",
	}, nil)
	if err != nil {
		return failure("zed usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized {
		return failure("zed authentication expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("zed usage returned HTTP %d", reply.status)
	}
	var body struct {
		Plan struct {
			V3    string `json:"plan_v3"`
			Usage struct {
				EditPredictions struct {
					Limit json.RawMessage `json:"limit"`
					Used  json.RawMessage `json:"used"`
				} `json:"edit_predictions"`
				ModelRequests *struct {
					Limit json.RawMessage `json:"limit"`
					Used  json.RawMessage `json:"used"`
				} `json:"model_requests"`
			} `json:"usage"`
		} `json:"plan"`
		SubscriptionPeriod struct {
			EndedAt any `json:"ended_at"`
		} `json:"subscription_period"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("zed usage response: %v", err)
	}
	reset := resetAt(body.SubscriptionPeriod.EndedAt)
	var windows []quota.Window
	appendZedWindow := func(name string, limit, used json.RawMessage) {
		if len(limit) == 0 {
			return
		}
		if strings.Contains(string(limit), "unlimited") {
			windows = append(windows, quota.Window{Name: name, Unlimited: true, ResetAt: reset})
			return
		}
		total, ok := number(limit)
		if !ok {
			return
		}
		count, _ := number(used)
		windows = append(windows, window(name, count, total, reset))
	}
	appendZedWindow("Edit predictions", body.Plan.Usage.EditPredictions.Limit, body.Plan.Usage.EditPredictions.Used)
	if body.Plan.Usage.ModelRequests != nil {
		appendZedWindow("Model requests", body.Plan.Usage.ModelRequests.Limit, body.Plan.Usage.ModelRequests.Used)
	}
	plan := body.Plan.V3
	if plan == "" {
		plan = "Zed"
	}
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// iflow has no usage reader. The reference's handler returns a fixed "usage
// tracked per request" message without making a request, which is not a quota
// read; a stub that reports a healthy plan would make routing trust a number
// nobody measured. The provider is therefore absent from the reader set and the
// observer reports ErrNotConfigured, which routing treats as unknown.
// REST credit envelopes are readable; the optional gRPC weekly fallback remains separate.
type grokCLIReader struct{ Client }

func (r grokCLIReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if credential.AccessToken == "" {
		return failure("grok-cli access token is required")
	}
	headers := map[string]string{
		"Authorization": "Bearer " + credential.AccessToken, "Accept": "application/json",
		"x-xai-token-auth": "xai-grok-cli", "User-Agent": "grok-shell/1.0.44 (linux; x86_64)",
		"x-grok-client-identifier": "grok-shell", "x-grok-client-version": "1.0.44", "x-grok-client-mode": "headless",
	}
	if email := credential.ProviderData["email"]; email != "" {
		headers["x-email"] = email
	}
	userID := credential.ProviderData["userId"]
	if userID == "" {
		userID = credential.ProviderData["principalId"]
	}
	if userID != "" {
		headers["x-userid"] = userID
	}
	reply, err := r.call(ctx, http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", headers, nil)
	if err != nil {
		return failure("grok-cli billing request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("grok-cli authentication expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("grok-cli billing returned HTTP %d", reply.status)
	}
	var billing map[string]json.RawMessage
	if err := decode(reply.body, &billing); err != nil {
		return failure("grok-cli billing response: %v", err)
	}
	var config map[string]json.RawMessage
	_ = json.Unmarshal(billing["config"], &config)
	for key, value := range config {
		billing[key] = value
	}
	value := func(names ...string) json.RawMessage {
		for _, name := range names {
			if raw := billing[name]; len(raw) > 0 && string(raw) != "null" {
				return raw
			}
		}
		return nil
	}
	var reset *time.Time
	for _, name := range []string{"billingPeriodEnd", "billing_period_end", "resetAt", "resetsAt", "periodEnd"} {
		var raw any
		_ = json.Unmarshal(billing[name], &raw)
		if reset = resetAt(raw); reset != nil {
			break
		}
	}
	if reset == nil {
		var period struct {
			End any `json:"end"`
		}
		_ = json.Unmarshal(billing["currentPeriod"], &period)
		reset = resetAt(period.End)
	}
	profile, profileErr := r.call(ctx, http.MethodGet, "https://cli-chat-proxy.grok.com/v1/user?include=subscription", headers, nil)
	var user struct {
		Tier         string `json:"subscriptionTier"`
		TierSnake    string `json:"subscription_tier"`
		Subscription struct {
			Tier string `json:"tier"`
		} `json:"subscription"`
	}
	profileKnown := profileErr == nil && profile.status >= 200 && profile.status < 300 && decode(profile.body, &user) == nil
	tier := user.Tier
	if tier == "" {
		tier = user.TierSnake
	}
	if tier == "" {
		tier = user.Subscription.Tier
	}
	if tier == "" {
		_ = json.Unmarshal(value("subscriptionTier", "subscription_tier"), &tier)
	}
	paid := tier != "" && !strings.EqualFold(tier, "free") && !strings.EqualFold(tier, "none") && !strings.EqualFold(tier, "null")
	var windows []quota.Window
	if limit, ok := grokNumber(value("monthlyLimit", "monthly_limit")); ok && limit > 0 {
		used, ok := grokNumber(value("includedUsed", "included_used"))
		if !ok {
			used, _ = grokNumber(value("totalUsed", "total_used"))
		}
		windows = append(windows, window("Monthly included", used, limit, reset))
	}
	cap, hasCap := grokNumber(value("onDemandCap"))
	used, hasUsed := grokNumber(value("onDemandUsed"))
	if hasCap && cap > 0 {
		windows = append(windows, window("On-demand", used, cap, reset))
	} else if profileKnown && !paid && hasCap && cap == 0 && hasUsed {
		windows = append(windows, percentWindow("On-demand", 0, reset))
	}
	// prepaidBalance is a remaining amount with no known allotment or reset, so
	// no window is synthesized from it.
	if percent, ok := grokNumber(value("creditUsagePercent", "credit_usage_percent")); ok && percent >= 0 {
		windows = append(windows, window("Weekly SuperGrok", percent, 100, reset))
	}
	if len(windows) == 0 {
		return failure("grok-cli exposes no supported REST credit allotment")
	}
	plan := "Grok Build"
	if tier != "" {
		plan = tier
	}
	return r.state(plan, windows...), nil
}

func grokNumber(raw json.RawMessage) (float64, bool) {
	var wrapped struct {
		Value json.RawMessage `json:"val"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Value) > 0 {
		return number(wrapped.Value)
	}
	return number(raw)
}
