package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// kimi mirrors open-sse/services/usage/kimi.js. Kimi is dual-auth: an API key
// uses x-api-key, an OAuth device connection uses Bearer plus the X-Msh device
// headers. A 403 is an entitlement gap, not an expired session.
func (r apiKeyReader) kimi(ctx context.Context, credential quota.Credential) (quota.State, error) {
	headers := map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
	apiKey := ""
	if credential.ProviderData != nil {
		apiKey = credential.ProviderData["apiKey"]
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
	if reply.status == http.StatusForbidden {
		return failure("kimi account has no usage permission")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("kimi usage returned HTTP %d", reply.status)
	}
	var body struct {
		Usage struct {
			Limit     json.RawMessage `json:"limit"`
			Used      json.RawMessage `json:"used"`
			Remaining json.RawMessage `json:"remaining"`
			ResetTime any             `json:"resetTime"`
		} `json:"usage"`
		Limits []struct {
			Detail struct {
				Limit     json.RawMessage `json:"limit"`
				Remaining json.RawMessage `json:"remaining"`
				ResetTime any             `json:"resetTime"`
			} `json:"detail"`
		} `json:"limits"`
		User struct {
			Membership struct {
				Level string `json:"level"`
			} `json:"membership"`
		} `json:"user"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("kimi usage response: %v", err)
	}
	var windows []quota.Window
	if limit, ok := number(body.Usage.Limit); ok && limit > 0 {
		used, _ := number(body.Usage.Used)
		windows = append(windows, window("Weekly", used, limit, resetAt(body.Usage.ResetTime)))
	}
	for _, item := range body.Limits {
		limit, ok := number(item.Detail.Limit)
		if !ok || limit <= 0 {
			continue
		}
		remaining, hasRemaining := number(item.Detail.Remaining)
		used := 0.0
		if hasRemaining {
			used = limit - remaining
		}
		windows = append(windows, window("Ratelimit", used, limit, resetAt(item.Detail.ResetTime)))
	}
	plan := kimiPlanName(body.User.Membership.Level)
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// kimiDeviceHeaders mirrors buildKimiHeaders: the device id must stay stable for
// the whole OAuth session or the quota call is rejected.
func kimiDeviceHeaders(deviceID string) map[string]string {
	if strings.TrimSpace(deviceID) == "" {
		deviceID = "routeweft"
	}
	return map[string]string{
		"X-Msh-Platform":    "routeweft",
		"X-Msh-Device-Id":   deviceID,
		"X-Msh-Device-Name": "routeweft",
	}
}

func kimiPlanName(level string) string {
	levels := map[string]string{
		"LEVEL_BASIC":        "Moderato",
		"LEVEL_INTERMEDIATE": "Allegretto",
		"LEVEL_ADVANCED":     "Allegro",
		"LEVEL_STANDARD":     "Vivace",
	}
	if name, ok := levels[level]; ok {
		return name
	}
	if level != "" {
		return strings.ToLower(strings.TrimPrefix(level, "LEVEL_"))
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

// xiaomi mirrors open-sse/services/usage/xiaomi-mimo.js. The weekly quota lives
// behind the account session, which an sk- key cannot reach, so a key-only
// connection reports that rather than a fabricated number.
func (r apiKeyReader) xiaomi(ctx context.Context, credential quota.Credential) (quota.State, error) {
	key := credential.AccessToken
	if key == "" {
		key = credential.ProviderData["apiKey"]
	}
	if key == "" {
		return failure("xiaomi-mimo credential is required")
	}
	reply, err := r.call(ctx, http.MethodGet, "https://aistudio.xiaomimimo.com/open-apis/v1/user/usage", map[string]string{
		"Authorization": "Bearer " + key,
		"X-Mimo-Source": "mimocode-cli",
		"Accept":        "application/json",
	}, nil)
	if err != nil {
		return failure("xiaomi usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized {
		return failure("xiaomi weekly quota requires an account session; an API key alone is insufficient")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("xiaomi usage returned HTTP %d", reply.status)
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Percent   json.RawMessage `json:"percent"`
			ResetDate string          `json:"resetDate"`
		} `json:"data"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("xiaomi usage response: %v", err)
	}
	if body.Code != 0 {
		return failure("xiaomi usage returned code %d", body.Code)
	}
	remaining, ok := number(body.Data.Percent)
	if !ok {
		return failure("xiaomi usage response had no percent")
	}
	windows := []quota.Window{percentWindow("Weekly", remaining, resetAt(body.Data.ResetDate))}
	return quota.State{Plan: "Xiaomi MiMo", Windows: windows, ObservedAt: r.now()}, nil
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
	if prepaid, ok := grokNumber(value("prepaidBalance")); ok && prepaid > 0 {
		windows = append(windows, percentWindow("Prepaid", 100, nil))
	}
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
