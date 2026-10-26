package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// apiKeyReader covers the providers whose quota endpoint authenticates with the
// connection's API key and reports simple windowed percentages or balances.
//
// One reader per shape rather than one per provider: GLM/GLM-CN, MiniMax/MiniMax-CN
// and CodeBuddy CN/INTL differ only by endpoint, so the endpoint is data.
type apiKeyReader struct {
	Client

	// Name is the provider id, used for plan labels and endpoint selection.
	Name string
}

func (r apiKeyReader) key(credential quota.Credential) string {
	if strings.TrimSpace(credential.AccessToken) != "" {
		return credential.AccessToken
	}
	return ""
}

func (r apiKeyReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	// kimi and xiaomi-mimo resolve their credential inside the reader because
	// they accept either an API key or an OAuth token.
	if r.Name == "kimi" || r.Name == "xiaomi-mimo" {
		switch r.Name {
		case "kimi":
			return r.kimi(ctx, credential)
		case "xiaomi-mimo":
			return r.xiaomi(ctx, credential)
		}
	}
	key := r.key(credential)
	if key == "" {
		return failure("%s credential is required", r.Name)
	}
	switch r.Name {
	case "glm", "glm-cn":
		return r.glm(ctx, key)
	case "minimax", "minimax-cn":
		return r.minimax(ctx, key)
	case "codebuddy-cn", "codebuddy-intl":
		return r.codebuddy(ctx, key)
	case "deepseek":
		return r.deepseek(ctx, key)
	case "groq":
		return r.groq(ctx, key)
	case "kenari":
		return r.kenari(ctx, key)
	case "opencode-go":
		return r.opencodeGo(ctx, key)
	case "commandcode":
		return r.commandcode(ctx, key)
	case "vercel-ai-gateway":
		return r.vercel(ctx, key)
	case "ollama":
		return r.ollama(ctx, key, credential)
	case "qoder":
		return r.qoder(ctx, credential)
	case "zed":
		return r.zed(ctx, credential)
	}
	return failure("no usage reader for %s", r.Name)
}

// getJSON is the shared read path for the simple JSON quota endpoints.
func (r apiKeyReader) getJSON(ctx context.Context, endpoint, key string) (response, error) {
	return r.call(ctx, http.MethodGet, endpoint, map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}, nil)
}

// glm mirrors open-sse/services/usage/glm.js. Both TOKENS_LIMIT and CREDIT_LIMIT
// are accepted and the window label is derived from the unit.
func (r apiKeyReader) glm(ctx context.Context, key string) (quota.State, error) {
	endpoint := "https://api.z.ai/api/monitor/usage/quota/limit"
	if r.Name == "glm-cn" {
		endpoint = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
	}
	reply, err := r.getJSON(ctx, endpoint, key)
	if err != nil {
		return failure("glm usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized {
		return failure("glm API key invalid or expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("glm quota returned HTTP %d", reply.status)
	}
	var body struct {
		Data struct {
			Level  string `json:"level"`
			Limits []struct {
				Type          string          `json:"type"`
				Percentage    json.RawMessage `json:"percentage"`
				NextResetTime json.RawMessage `json:"nextResetTime"`
				Unit          int             `json:"unit"`
				Number        int             `json:"number"`
			} `json:"limits"`
		} `json:"data"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("glm quota response: %v", err)
	}
	var windows []quota.Window
	for _, limit := range body.Data.Limits {
		if limit.Type != "TOKENS_LIMIT" && limit.Type != "CREDIT_LIMIT" {
			continue
		}
		used, _ := number(limit.Percentage)
		resetMs, _ := number(limit.NextResetTime)
		var reset *time.Time
		if resetMs > 0 {
			parsed := time.UnixMilli(int64(resetMs)).UTC()
			reset = &parsed
		}
		label := "Tokens"
		switch {
		case limit.Unit == 3:
			label = fmt.Sprintf("Session (%dh)", limit.Number)
		case limit.Unit == 6:
			label = "Weekly (7d)"
		case limit.Type == "CREDIT_LIMIT":
			label = fmt.Sprintf("Limit (%d)", limit.Number)
		}
		windows = append(windows, percentWindow(label, 100-used, reset))
	}
	plan := "Unknown"
	if body.Data.Level != "" {
		plan = strings.ToUpper(body.Data.Level[:1]) + strings.ToLower(body.Data.Level[1:])
	}
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// minimax mirrors open-sse/services/usage/minimax.js. The M-series shared pool
// reports percent-only buckets, so a zero count falls back to the percentage.
func (r apiKeyReader) minimax(ctx context.Context, key string) (quota.State, error) {
	endpoints := []string{
		"https://www.minimax.io/v1/token_plan/remains",
		"https://www.minimax.io/v1/coding_plan/remains",
	}
	if r.Name == "minimax-cn" {
		endpoints = []string{
			"https://api.minimaxi.com/v1/token_plan/remains",
			"https://api.minimaxi.com/v1/coding_plan/remains",
		}
	}
	var lastErr error
	for _, endpoint := range endpoints {
		reply, err := r.getJSON(ctx, endpoint, key)
		if err != nil {
			lastErr = err
			continue
		}
		if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
			return failure("minimax API key invalid or inactive")
		}
		if reply.status < 200 || reply.status >= 300 {
			lastErr = fmt.Errorf("minimax usage returned HTTP %d", reply.status)
			continue
		}
		state, err := parseMiniMax(reply.body, strings.Contains(endpoint, "coding_plan"), r.now())
		if err != nil {
			lastErr = err
			continue
		}
		return state, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("minimax usage unavailable")
	}
	return failure("minimax usage: %v", lastErr)
}

func parseMiniMax(payload []byte, countMeansRemaining bool, now time.Time) (quota.State, error) {
	var body struct {
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
		Models []map[string]any `json:"model_remains"`
	}
	if err := decode(payload, &body); err != nil {
		return quota.State{}, err
	}
	if body.BaseResp.StatusCode != 0 {
		return quota.State{}, fmt.Errorf("minimax quota error: %s", body.BaseResp.StatusMsg)
	}
	var windows []quota.Window
	for _, model := range body.Models {
		name := miniMaxName(model)
		sessionTotal := miniMaxNumber(model, "current_interval_total_count", "currentIntervalTotalCount")
		sessionUsed := miniMaxNumber(model, "current_interval_usage_count", "currentIntervalUsageCount")
		sessionPercent, hasSessionPercent := miniMaxPercent(model, "current_interval_remaining_percent", "currentIntervalRemainingPercent")
		if sessionTotal > 0 || hasSessionPercent {
			total, used := miniMaxTotals(sessionTotal, sessionUsed, sessionPercent, hasSessionPercent, countMeansRemaining)
			windows = append(windows, window(name+" (5h)", used, total, miniMaxReset(model, now, "remains_time", "remainsTime", "end_time", "endTime")))
		}
		weeklyTotal := miniMaxNumber(model, "current_weekly_total_count", "currentWeeklyTotalCount")
		weeklyUsed := miniMaxNumber(model, "current_weekly_usage_count", "currentWeeklyUsageCount")
		weeklyPercent, hasWeeklyPercent := miniMaxPercent(model, "current_weekly_remaining_percent", "currentWeeklyRemainingPercent")
		if weeklyTotal > 0 || hasWeeklyPercent {
			total, used := miniMaxTotals(weeklyTotal, weeklyUsed, weeklyPercent, hasWeeklyPercent, countMeansRemaining)
			windows = append(windows, window(name+" (7d)", used, total, miniMaxReset(model, now, "weekly_remains_time", "weeklyRemainsTime", "weekly_end_time", "weeklyEndTime")))
		}
	}
	if len(windows) == 0 {
		return quota.State{}, fmt.Errorf("minimax returned no quota data")
	}
	return quota.State{Windows: windows, ObservedAt: now}, nil
}

// miniMaxTotals normalizes the percent-only M-series buckets to a 100 base so a
// bucket with no counts still renders a meaningful bar.
func miniMaxTotals(total, count float64, percent float64, hasPercent, countMeansRemaining bool) (float64, float64) {
	if total > 0 {
		if countMeansRemaining {
			return total, total - count
		}
		return total, count
	}
	if !hasPercent {
		return 0, 0
	}
	if countMeansRemaining {
		return 100, 100 - percent
	}
	return 100, 100 - percent
}

func miniMaxName(model map[string]any) string {
	raw := miniMaxString(model, "model_name", "modelName")
	if raw == "" {
		return "MiniMax"
	}
	if raw == "MiniMax-M*" || raw == "general" {
		return "M-series"
	}
	return raw
}

func miniMaxString(model map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := model[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func miniMaxNumber(model map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := model[key].(float64); ok {
			return value
		}
	}
	return 0
}

func miniMaxPercent(model map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := model[key].(float64); ok {
			return value, true
		}
	}
	return 0, false
}

func miniMaxReset(model map[string]any, now time.Time, keys ...string) *time.Time {
	for _, key := range keys {
		if value, ok := model[key].(float64); ok && value > 0 {
			parsed := time.UnixMilli(now.UnixMilli() + int64(value)).UTC()
			return &parsed
		}
	}
	return nil
}

// codebuddy mirrors open-sse/services/usage/codebuddy-cn.js. Refill packs and
// bonus packs are separate windows: a refill pack resets on its cycle, a bonus
// pack simply expires.
func (r apiKeyReader) codebuddy(ctx context.Context, key string) (quota.State, error) {
	endpoint := "https://copilot.tencent.com/v2/billing/meter/get-user-resource"
	if r.Name == "codebuddy-intl" {
		endpoint = "https://www.codebuddy.ai/v2/billing/meter/get-user-resource"
	}
	reply, err := r.call(ctx, http.MethodPost, endpoint, map[string]string{
		"User-Agent":    "Routeweft",
		"Authorization": "Bearer " + key,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}, []byte("{}"))
	if err != nil {
		return failure("codebuddy usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("codebuddy credential invalid or expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("codebuddy quota returned HTTP %d", reply.status)
	}
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Response struct {
				Data struct {
					Accounts []map[string]any `json:"Accounts"`
				} `json:"Data"`
			} `json:"Response"`
		} `json:"data"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("codebuddy quota response: %v", err)
	}
	if body.Code != 0 {
		return failure("codebuddy quota error: %s", body.Msg)
	}
	accounts := body.Data.Response.Data.Accounts
	if len(accounts) == 0 {
		return failure("codebuddy returned no credit package")
	}
	var windows []quota.Window
	refill, bonus := 0, 0
	for _, account := range accounts {
		cycleEnd := resetAt(account["CycleEndTime"])
		deductionEnd, _ := number(rawJSON(account["DeductionEndTime"]))
		refillPack := cycleEnd != nil && deductionEnd > 0 &&
			time.UnixMilli(int64(deductionEnd)).Sub(*cycleEnd) > 48*time.Hour
		if refillPack {
			refill++
			used := miniMaxNumber(account, "CycleCapacityUsedPrecise", "CycleCapacityUsed")
			total := miniMaxNumber(account, "CycleCapacitySizePrecise", "CycleCapacitySize")
			windows = append(windows, window(codebuddyCadence(account, refill), used, total, cycleEnd))
			continue
		}
		bonus++
		used := miniMaxNumber(account, "CapacityUsedPrecise", "CapacityUsed")
		total := miniMaxNumber(account, "CapacitySizePrecise", "CapacitySize")
		windows = append(windows, window(fmt.Sprintf("Bonus Pack %d", bonus), used, total, cycleEnd))
	}
	plan := ""
	if name, ok := accounts[0]["PackageName"].(string); ok && name != "" {
		plan = name
	} else if name, ok := accounts[0]["SubProductName"].(string); ok && name != "" {
		plan = name
	}
	if plan == "" {
		plan = "CodeBuddy"
	}
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// codebuddyCadence labels a refill pack by its cycle length, and disambiguates
// repeats so two monthly packs do not collide on one key.
func codebuddyCadence(account map[string]any, index int) string {
	label := "Monthly"
	start := resetAt(account["CycleStartTime"])
	end := resetAt(account["CycleEndTime"])
	if start != nil && end != nil {
		days := end.Sub(*start).Hours() / 24
		switch {
		case days <= 1.5:
			label = "Daily"
		case days <= 10:
			label = "Weekly"
		}
	}
	if index > 1 {
		return fmt.Sprintf("%s %d", label, index)
	}
	return label
}

func rawJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

// deepseek mirrors open-sse/services/usage/deepseek.js: a credit balance, not a
// usage window.
func (r apiKeyReader) deepseek(ctx context.Context, key string) (quota.State, error) {
	reply, err := r.getJSON(ctx, "https://api.deepseek.com/user/balance", key)
	if err != nil {
		return failure("deepseek balance request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("deepseek authentication failed")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("deepseek balance returned HTTP %d", reply.status)
	}
	var body struct {
		Available bool `json:"is_available"`
		Balances  []struct {
			Currency string          `json:"currency"`
			Total    json.RawMessage `json:"total_balance"`
			Granted  json.RawMessage `json:"granted_balance"`
			ToppedUp json.RawMessage `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("deepseek balance response: %v", err)
	}
	if len(body.Balances) == 0 {
		return failure("deepseek returned no balance")
	}
	var windows []quota.Window
	for _, balance := range body.Balances {
		currency := strings.ToUpper(balance.Currency)
		if currency == "" {
			continue
		}
		total, _ := number(balance.Total)
		windows = append(windows, window("Balance ("+currency+")", 0, total, nil))
	}
	plan := "DeepSeek"
	if !body.Available {
		plan = "DeepSeek (Insufficient Balance)"
	}
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// groq mirrors open-sse/services/usage/groq.js: rate limits arrive as headers on
// the models list, so reading quota costs no tokens.
func (r apiKeyReader) groq(ctx context.Context, key string) (quota.State, error) {
	reply, err := r.call(ctx, http.MethodGet, "https://api.groq.com/openai/v1/models", map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}, nil)
	if err != nil {
		return failure("groq usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("groq authentication failed")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("groq usage returned HTTP %d", reply.status)
	}
	now := r.now()
	var windows []quota.Window
	var malformed []string
	for _, bucket := range []struct{ label, suffix string }{{"Requests", "requests"}, {"Tokens", "tokens"}} {
		window, reported, err := groqWindow(reply.headers, bucket.label, bucket.suffix, now)
		switch {
		case err != nil:
			malformed = append(malformed, err.Error())
			// A malformed bucket is shown without a bound: unknown, never zero or
			// unlimited, so it cannot steer routing.
			windows = append(windows, quota.Window{Name: bucket.label})
		case reported:
			windows = append(windows, window)
		}
	}
	if len(malformed) == len(windows) {
		if len(malformed) == 0 {
			// The key works but Groq sent no bucket: not reported, not healthy.
			return failure("groq connected but reported no rate-limit headers")
		}
		return failure("groq rate-limit headers malformed: %s", strings.Join(malformed, "; "))
	}
	return quota.State{Plan: "Groq", Windows: windows, ObservedAt: now}, nil
}

// groqWindow reads one x-ratelimit-{limit,remaining,reset}-<suffix> bucket. The
// bucket is unreported when both counts are absent; a half-present, empty,
// non-numeric, negative or non-positive-limit value is malformed. Neither ever
// becomes a zero or unlimited window.
func groqWindow(headers http.Header, label, suffix string, now time.Time) (quota.Window, bool, error) {
	limitKey := "x-ratelimit-limit-" + suffix
	remainingKey := "x-ratelimit-remaining-" + suffix
	limitRaw, limitOK := headerValue(headers, limitKey)
	remainingRaw, remainingOK := headerValue(headers, remainingKey)
	if !limitOK && !remainingOK {
		return quota.Window{}, false, nil
	}
	limit, err := groqCount(limitKey, limitRaw, limitOK)
	if err != nil {
		return quota.Window{}, true, err
	}
	if limit <= 0 {
		return quota.Window{}, true, fmt.Errorf("%s is not positive", limitKey)
	}
	remaining, err := groqCount(remainingKey, remainingRaw, remainingOK)
	if err != nil {
		return quota.Window{}, true, err
	}
	var reset *time.Time
	if raw, ok := headerValue(headers, "x-ratelimit-reset-"+suffix); ok {
		if duration, ok := groqDuration(raw); ok {
			at := now.Add(duration).UTC()
			reset = &at
		}
	}
	return window(label, math.Max(0, limit-remaining), limit, reset), true, nil
}

func headerValue(headers http.Header, key string) (string, bool) {
	values := headers.Values(key)
	if len(values) == 0 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func groqCount(key, raw string, present bool) (float64, error) {
	if !present {
		return 0, fmt.Errorf("%s is missing", key)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("%s is not a count", key)
	}
	return value, nil
}

// groqDuration parses Groq's Go-style reset durations ("2m59.56s", "7.66s",
// "120ms"). An unparseable or negative value yields no reset, never a bogus time.
func groqDuration(raw string) (time.Duration, bool) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < 0 {
		return 0, false
	}
	return duration, true
}

// kenari mirrors open-sse/services/usage/kenari.js.
func (r apiKeyReader) kenari(ctx context.Context, key string) (quota.State, error) {
	reply, err := r.getJSON(ctx, "https://kenari.id/v1/account/quota", key)
	if err != nil {
		return failure("kenari usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("kenari authentication failed")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("kenari quota returned HTTP %d", reply.status)
	}
	var body struct {
		Plan struct {
			Name    string `json:"name"`
			Windows map[string]struct {
				Used      json.RawMessage `json:"used_rp"`
				Remaining json.RawMessage `json:"remaining_rp"`
				ResetsAt  any             `json:"resets_at"`
			} `json:"windows"`
		} `json:"plan"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("kenari quota response: %v", err)
	}
	var windows []quota.Window
	for name, item := range body.Plan.Windows {
		used, usedOK := number(item.Used)
		remaining, remainingOK := number(item.Remaining)
		label := name
		switch name {
		case "month":
			label = "Monthly (IDR)"
		case "week":
			label = "Weekly (IDR)"
		}
		if !usedOK || !remainingOK {
			// Absent or null amounts are unknown, never exhausted: an exhausted
			// window skips the account in routing.
			windows = append(windows, quota.Window{Name: label, ResetAt: resetAt(item.ResetsAt)})
			continue
		}
		percent := 0.0
		if total := used + remaining; total > 0 {
			percent = remaining / total * 100
		}
		windows = append(windows, percentWindow(label, percent, resetAt(item.ResetsAt)))
	}
	plan := body.Plan.Name
	if plan == "" {
		plan = "Kenari"
	}
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// opencodeGo mirrors open-sse/services/usage/opencode-go.js.
func (r apiKeyReader) opencodeGo(ctx context.Context, key string) (quota.State, error) {
	reply, err := r.getJSON(ctx, "https://opencode.ai/zen/go/v1/usage", key)
	if err != nil {
		return failure("opencode-go usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized {
		return failure("opencode-go authentication failed")
	}
	if reply.status == http.StatusForbidden {
		return failure("opencode-go subscription required for this key")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("opencode-go usage returned HTTP %d", reply.status)
	}
	var body struct {
		Usage map[string]struct {
			Percent  json.RawMessage `json:"percent"`
			ResetsAt any             `json:"resetsAt"`
		} `json:"usage"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("opencode-go usage response: %v", err)
	}
	names := map[string]string{"rolling": "Rolling", "weekly": "Weekly", "monthly": "Monthly"}
	var windows []quota.Window
	for period, label := range names {
		item, ok := body.Usage[period]
		if !ok {
			continue
		}
		used, ok := number(item.Percent)
		if !ok {
			continue
		}
		windows = append(windows, percentWindow(label, 100-used, resetAt(item.ResetsAt)))
	}
	if len(windows) == 0 {
		return failure("opencode-go returned no quota data")
	}
	return quota.State{Plan: "OpenCode Go", Windows: windows, ObservedAt: r.now()}, nil
}

// commandcode mirrors open-sse/services/usage/commandcode.js: whoami gives the
// org, then credits and subscriptions are read in parallel.
func (r apiKeyReader) commandcode(ctx context.Context, key string) (quota.State, error) {
	base := "https://api.commandcode.ai"
	reply, err := r.getJSON(ctx, base+"/alpha/whoami?limits=1", key)
	if err != nil {
		return failure("commandcode whoami failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("commandcode authentication failed")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("commandcode whoami returned HTTP %d", reply.status)
	}
	var whoami struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	_ = decode(reply.body, &whoami)
	orgQuery := ""
	if whoami.Org.ID != "" {
		orgQuery = "?orgId=" + whoami.Org.ID
	}
	creditsReply, creditsErr := r.getJSON(ctx, base+"/alpha/billing/credits"+orgQuery, key)
	subsReply, subsErr := r.getJSON(ctx, base+"/alpha/billing/subscriptions"+orgQuery, key)
	if creditsErr != nil || subsErr != nil {
		return failure("commandcode billing request failed")
	}
	if creditsReply.status < 200 || creditsReply.status >= 300 || subsReply.status < 200 || subsReply.status >= 300 {
		return failure("commandcode billing returned HTTP %d/%d", creditsReply.status, subsReply.status)
	}
	var credits struct {
		Credits struct {
			Monthly   json.RawMessage `json:"monthlyCredits"`
			Purchased json.RawMessage `json:"purchasedCredits"`
			Free      json.RawMessage `json:"freeCredits"`
		} `json:"credits"`
		WindowLimits map[string]struct {
			Used    json.RawMessage `json:"used"`
			Cap     json.RawMessage `json:"cap"`
			ResetAt any             `json:"resetAt"`
		} `json:"windowLimits"`
	}
	var subs struct {
		Data struct {
			PlanID           string `json:"planId"`
			CurrentPeriodEnd any    `json:"currentPeriodEnd"`
		} `json:"data"`
	}
	if err := decode(creditsReply.body, &credits); err != nil {
		return failure("commandcode credits response: %v", err)
	}
	_ = decode(subsReply.body, &subs)
	monthly, _ := number(credits.Credits.Monthly)
	purchased, _ := number(credits.Credits.Purchased)
	free, _ := number(credits.Credits.Free)
	remaining := monthly + purchased + free
	cap := commandCodeCap(subs.Data.PlanID)
	windows := []quota.Window{window("Credits", cap-remaining, cap, resetAt(subs.Data.CurrentPeriodEnd))}
	if window5h, ok := credits.WindowLimits["fiveHour"]; ok {
		used, _ := number(window5h.Used)
		total, _ := number(window5h.Cap)
		windows = append(windows, window("Session (5h)", used, total, resetAt(window5h.ResetAt)))
	}
	if weekly, ok := credits.WindowLimits["weekly"]; ok {
		used, _ := number(weekly.Used)
		total, _ := number(weekly.Cap)
		windows = append(windows, window("Weekly", used, total, resetAt(weekly.ResetAt)))
	}
	plan := commandCodePlan(subs.Data.PlanID)
	if len(windows) == 0 {
		return failure("%s returned no quota windows", r.Name)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

func commandCodePlan(id string) string {
	names := map[string]string{
		"individual-go": "Go", "individual-goat": "GOAT", "individual-pro": "Pro",
		"individual-pro-v1": "Pro", "individual-provider": "Provider", "individual-max": "Max",
		"individual-ultra": "Ultra", "teams-pro": "Teams Pro",
	}
	if name, ok := names[id]; ok {
		return name
	}
	if id != "" {
		return id
	}
	return "Command Code"
}

func commandCodeCap(id string) float64 {
	caps := map[string]float64{
		"individual-go": 10, "individual-goat": 70, "individual-pro": 30,
		"individual-pro-v1": 80, "individual-provider": 15, "individual-max": 150,
		"individual-ultra": 300, "teams-pro": 40,
	}
	return caps[id]
}
