package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/providers/mimoaccount"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
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

	// mimo keeps Xiaomi MiMo account sessions between reads.
	mimo *mimoaccount.Sessions
	// desktop is the consented MiMo Desktop session renewal (#299); nil never
	// reads host credentials.
	desktop MimoDesktopRenewal
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

// glm mirrors open-sse/services/usage/glm.js:18-88 for both regions. Values are
// read as the pin reads them: `Number(x) || 0` for the percentage and the reset
// (milliseconds only), strict `=== 3` / `=== 6` for the unit, the raw number in
// the label, and a later limit with the same label replaces the earlier one in
// its place (glm.js:69). Each window keeps the provider's type, unit and number.
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
	var body json.RawMessage
	if err := decode(reply.body, &body); err != nil {
		return failure("glm quota response: %v", err)
	}
	data, _ := shared.Object(shared.Field(body, "data"))
	limits, _ := shared.Array(data["limits"])
	var windows []quota.Window
	for _, limit := range limits {
		var kind string
		if json.Unmarshal(shared.Field(limit, "type"), &kind) != nil || kind != "TOKENS_LIMIT" && kind != "CREDIT_LIMIT" {
			continue
		}
		used := shared.OrZero(shared.Number(shared.Field(limit, "percentage")))
		var reset *time.Time
		if resetMs := shared.OrZero(shared.Number(shared.Field(limit, "nextResetTime"))); resetMs > 0 {
			// new Date(ms).toISOString() throws past the Date range, and the pin
			// reports the whole read as an error (glm.js:85-86).
			if math.Trunc(resetMs) > jsMaxTime {
				return failure("glm quota reset time is out of range")
			}
			parsed := time.UnixMilli(int64(resetMs)).UTC()
			reset = &parsed
		}
		unit, number := shared.Field(limit, "unit"), shared.Field(limit, "number")
		unitValue, unitIsNumber := shared.IsNumber(unit)
		var label string
		switch {
		case unitIsNumber && unitValue == 3:
			label = "Session (" + shared.Template(number) + "h)"
		case unitIsNumber && unitValue == 6:
			label = "Weekly (7d)"
		case kind == "TOKENS_LIMIT":
			label = "Tokens"
		default:
			label = "Limit (" + shared.Template(number) + ")"
		}
		next := percentWindow(label, math.Max(0, 100-used), reset)
		next.Limit = &quota.Limit{Type: kind, Unit: append(json.RawMessage(nil), unit...), Number: append(json.RawMessage(nil), number...)}
		windows = putWindow(windows, next)
	}
	plan := "Unknown"
	var level string
	if json.Unmarshal(data["level"], &level) == nil && level != "" {
		first, size := utf8.DecodeRuneInString(level)
		plan = strings.ToUpper(string(first)) + strings.ToLower(level[size:])
	}
	if len(windows) == 0 {
		// The pin answers with the plan and an empty quota map; the notice keeps
		// a windowless read from passing as a measured one.
		return quota.State{Plan: plan, Message: "GLM connected. No quota limits were returned.", ObservedAt: r.now()}, nil
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// jsMaxTime is the largest |time value| a JavaScript Date holds, in ms.
const jsMaxTime = 8.64e15

// putWindow adds a window keyed by name the way the pin's `quotas[key] = ...`
// does: a repeated name replaces the earlier window where it stood.
func putWindow(windows []quota.Window, next quota.Window) []quota.Window {
	for i := range windows {
		if windows[i].Name == next.Name {
			windows[i] = next
			return windows
		}
	}
	return append(windows, next)
}

// miniMaxEndpoints are the pin's registry usage URLs, tried in order
// (providers/registry/minimax.js:35-40, minimax-cn.js:35-40).
var miniMaxEndpoints = map[string][]string{
	"minimax": {
		"https://www.minimax.io/v1/token_plan/remains",
		"https://api.minimax.io/v1/api/openplatform/coding_plan/remains",
	},
	"minimax-cn": {
		"https://www.minimaxi.com/v1/api/openplatform/coding_plan/remains",
		"https://api.minimaxi.com/v1/api/openplatform/coding_plan/remains",
	},
}

// miniMaxAuthLike is the pin's auth test over status_msg and the raw body
// (minimax.js:165).
var miniMaxAuthLike = regexp.MustCompile(`(?i)token plan|coding plan|invalid api key|invalid key|unauthorized|inactive`)

// minimax mirrors open-sse/services/usage/minimax.js:133-234. The next endpoint
// is tried only after a 404, a 405, a 5xx or a failed request (minimax.js:173,
// 227-229); an auth answer (401, 403, status 1004 or auth wording anywhere in
// the answer), any other HTTP error, an upstream status and an answer without
// quota all end the read. A body that is empty or not JSON is read as {}
// (minimax.js:155-159), so a 200 carrying one is the pin's no-quota notice.
// Both the snake_case and the camelCase envelopes are read, snake first (`??`).
func (r apiKeyReader) minimax(ctx context.Context, key string) (quota.State, error) {
	endpoints := miniMaxEndpoints[r.Name]
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
		"Content-Type":  "application/json",
	}
	var lastErr error
	for index, endpoint := range endpoints {
		canFallback := index < len(endpoints)-1
		reply, err := r.call(ctx, http.MethodGet, endpoint, headers, nil)
		if err != nil {
			lastErr = err
			if !canFallback {
				break
			}
			continue
		}
		// response.text() drops a leading byte-order mark before JSON.parse.
		payload := json.RawMessage(bytes.TrimPrefix(reply.body, []byte("\ufeff")))
		if !json.Valid(payload) {
			payload = nil
		}
		baseResp := shared.FirstNonNullish(shared.Field(payload, "base_resp"), shared.Field(payload, "baseResp"))
		statusCode := shared.OrZero(shared.Number(shared.FirstNonNullish(shared.Field(baseResp, "status_code"), shared.Field(baseResp, "statusCode"))))
		statusMsg := ""
		if raw := shared.FirstNonNullish(shared.Field(baseResp, "status_msg"), shared.Field(baseResp, "statusMsg")); raw != nil {
			statusMsg = shared.Trim(shared.Template(raw))
		}
		if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden || statusCode == 1004 ||
			miniMaxAuthLike.MatchString(statusMsg+" "+string(reply.body)) {
			return failure("minimax API key invalid or inactive")
		}
		if reply.status < 200 || reply.status >= 300 {
			lastErr = fmt.Errorf("minimax usage returned HTTP %d", reply.status)
			if (reply.status == http.StatusNotFound || reply.status == http.StatusMethodNotAllowed || reply.status >= 500) && canFallback {
				continue
			}
			return failure("%v", lastErr)
		}
		if statusCode != 0 {
			// A failed read, worded as the pin's message (minimax.js:177-179).
			if statusMsg == "" {
				statusMsg = "Upstream quota API error"
			}
			return failure("MiniMax connected. %s", statusMsg)
		}
		state, err := parseMiniMax(payload, strings.Contains(endpoint, "/coding_plan/remains"), r.now())
		if err != nil {
			// A reset past the Date range throws inside the pin's loop, which
			// moves on like a failed request (minimax.js:227-229).
			lastErr = err
			if !canFallback {
				break
			}
			continue
		}
		return state, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("minimax usage unavailable")
	}
	return failure("minimax usage: %v", lastErr)
}

// parseMiniMax reads model_remains (minimax.js:181-226). A bucket with counts
// keeps them as the window's count; a percent-only bucket (M-series) is
// normalized to 100 as the pin does and has no count, since 100 is not the
// provider's unit. The provider's remaining percent wins over the counts.
func parseMiniMax(payload json.RawMessage, countMeansRemaining bool, now time.Time) (quota.State, error) {
	models, _ := shared.Array(shared.FirstNonNullish(shared.Field(payload, "model_remains"), shared.Field(payload, "modelRemains")))
	var windows []quota.Window
	for _, model := range models {
		if !miniMaxHasQuota(model) {
			continue
		}
		name := miniMaxName(model)
		for _, bucket := range []struct {
			suffix                string
			total, count, percent [2]string
			remains, end          [2]string
		}{
			{" (5h)", [2]string{"current_interval_total_count", "currentIntervalTotalCount"},
				[2]string{"current_interval_usage_count", "currentIntervalUsageCount"},
				[2]string{"current_interval_remaining_percent", "currentIntervalRemainingPercent"},
				[2]string{"remains_time", "remainsTime"}, [2]string{"end_time", "endTime"}},
			{" (7d)", [2]string{"current_weekly_total_count", "currentWeeklyTotalCount"},
				[2]string{"current_weekly_usage_count", "currentWeeklyUsageCount"},
				[2]string{"current_weekly_remaining_percent", "currentWeeklyRemainingPercent"},
				[2]string{"weekly_remains_time", "weeklyRemainsTime"}, [2]string{"weekly_end_time", "weeklyEndTime"}},
		} {
			total := math.Max(0, shared.OrZero(shared.Number(miniMaxField(model, bucket.total))))
			percent, hasPercent := miniMaxPercent(model, bucket.percent)
			if total <= 0 && !hasPercent {
				continue
			}
			count := math.Max(0, shared.OrZero(shared.Number(miniMaxField(model, bucket.count))))
			percentOnly := total <= 0
			if percentOnly {
				total = 100
				if countMeansRemaining {
					count = jsRound(total * (percent / 100))
				} else {
					count = jsRound(total * (1 - percent/100))
				}
			}
			used := math.Min(math.Max(0, count), total)
			if countMeansRemaining {
				used = math.Max(total-count, 0)
			}
			remaining := math.Max(total-used, 0)
			if !hasPercent {
				percent = math.Max(0, math.Min(100, remaining/total*100))
			}
			reset, err := miniMaxReset(model, now, bucket.remains, bucket.end)
			if err != nil {
				return quota.State{}, err
			}
			next := percentWindow(name+bucket.suffix, percent, reset)
			if !percentOnly {
				next.Count = &quota.Count{Used: &used, Total: &total, Remaining: &remaining}
			}
			windows = putWindow(windows, next)
		}
	}
	if len(windows) == 0 {
		return quota.State{Message: "MiniMax connected. No quota data was returned.", ObservedAt: now}, nil
	}
	return quota.State{Windows: windows, ObservedAt: now}, nil
}

// miniMaxField is `model[snake] ?? model[camel]`.
func miniMaxField(model json.RawMessage, keys [2]string) json.RawMessage {
	return shared.FirstNonNullish(shared.Field(model, keys[0]), shared.Field(model, keys[1]))
}

// miniMaxHasQuota is hasMiniMaxQuota (minimax.js:61-68).
func miniMaxHasQuota(model json.RawMessage) bool {
	for _, keys := range [][2]string{{"current_interval_total_count", "currentIntervalTotalCount"}, {"current_weekly_total_count", "currentWeeklyTotalCount"}} {
		if math.Max(0, shared.OrZero(shared.Number(miniMaxField(model, keys)))) > 0 {
			return true
		}
	}
	for _, keys := range [][2]string{{"current_interval_remaining_percent", "currentIntervalRemainingPercent"}, {"current_weekly_remaining_percent", "currentWeeklyRemainingPercent"}} {
		if _, ok := miniMaxPercent(model, keys); ok {
			return true
		}
	}
	return false
}

// miniMaxPercent is getMiniMaxProvidedPercent (minimax.js:44-51): a finite
// Number() of the field, clamped to 0..100.
func miniMaxPercent(model json.RawMessage, keys [2]string) (float64, bool) {
	raw := miniMaxField(model, keys)
	if shared.Nullish(raw) {
		return 0, false
	}
	value := shared.Number(raw)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return math.Max(0, math.Min(100, value)), true
}

// miniMaxReset is getMiniMaxResetAt (minimax.js:70-74): a positive remaining
// duration counts from the read, otherwise the end time is parsed.
func miniMaxReset(model json.RawMessage, now time.Time, remains, end [2]string) (*time.Time, error) {
	if remainsMs := shared.OrZero(shared.Number(miniMaxField(model, remains))); remainsMs > 0 {
		at := math.Trunc(float64(now.UnixMilli()) + remainsMs)
		if at > jsMaxTime {
			return nil, fmt.Errorf("minimax reset time is out of range")
		}
		parsed := time.UnixMilli(int64(at)).UTC()
		return &parsed, nil
	}
	return shared.ResetTime(miniMaxField(model, end)), nil
}

var (
	miniMaxSeparators = regexp.MustCompile(`[_-]+`)
	miniMaxSpaces     = regexp.MustCompile(`\s+`)
	miniMaxWordStart  = regexp.MustCompile(`\b\w`)
	miniMaxWords      = []struct {
		word *regexp.Regexp
		with string
	}{{regexp.MustCompile(`\bTo\b`), "to"}, {regexp.MustCompile(`\bTts\b`), "TTS"}, {regexp.MustCompile(`\bHd\b`), "HD"}}
)

// miniMaxName is formatMiniMaxQuotaName (minimax.js:24-42).
func miniMaxName(model json.RawMessage) string {
	raw := shared.FirstNonNullish(shared.Field(model, "model_name"), shared.Field(model, "modelName"))
	name := ""
	if shared.Truthy(raw) {
		name = strings.TrimSpace(shared.Template(raw))
	}
	switch name {
	case "":
		return "MiniMax"
	case "MiniMax-M*", "general":
		return "M-series"
	}
	name = strings.TrimSpace(miniMaxSpaces.ReplaceAllString(miniMaxSeparators.ReplaceAllString(name, " "), " "))
	name = miniMaxWordStart.ReplaceAllStringFunc(name, strings.ToUpper)
	for _, word := range miniMaxWords {
		name = word.word.ReplaceAllString(name, word.with)
	}
	return name
}

// jsRound is Math.round: halves round up.
func jsRound(value float64) float64 { return math.Floor(value + 0.5) }

// codebuddyRefillGap separates a refill pack, whose cycle ends well before the
// pack expires, from a one-shot bonus pack (codebuddy-cn.js:86-93).
const codebuddyRefillGap = 2 * 24 * time.Hour

// codebuddy mirrors open-sse/services/usage/codebuddy-cn.js:46-146 for both
// regions. Refill packs come first, soonest cycle end first, each labelled by
// its cadence with a per-cadence counter and read from the Cycle* balance; bonus
// packs follow as "Bonus Pack N", soonest expiry first, from the lifetime
// Capacity balance. The *Precise strings win over the plain numbers (`??`).
// Each window keeps its absolute amounts and the pin's recurring flag; a zero
// capacity is neither unlimited nor exhausted. The plan is the first refill
// pack's, else the first package's.
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
	var body json.RawMessage
	if err := decode(reply.body, &body); err != nil {
		return failure("codebuddy quota response: %v", err)
	}
	// `json?.code !== 0`: an absent or string code is an error too.
	if code, ok := shared.IsNumber(shared.Field(body, "code")); !ok || code != 0 {
		return failure("codebuddy quota error: %s", shared.String(shared.Field(body, "msg")))
	}
	accounts, _ := shared.Array(shared.Field(shared.Field(shared.Field(shared.Field(body, "data"), "Response"), "Data"), "Accounts"))
	if len(accounts) == 0 {
		return quota.State{Message: "CodeBuddy connected. No credit package found.", ObservedAt: r.now()}, nil
	}
	cycleEnd := func(account json.RawMessage) float64 {
		if end := shared.ResetTime(shared.Field(account, "CycleEndTime")); end != nil {
			return float64(end.UnixMilli())
		}
		return math.Inf(1)
	}
	var refills, bonuses []json.RawMessage
	for _, account := range accounts {
		if _, ok := shared.Object(account); !ok {
			// The pin's field reads throw on a null package and the whole read
			// fails (codebuddy-cn.js:135-136).
			return failure("codebuddy returned a malformed credit package")
		}
		end, deduction := cycleEnd(account), shared.Number(shared.Field(account, "DeductionEndTime"))
		if !math.IsInf(end, 0) && !math.IsNaN(deduction) && !math.IsInf(deduction, 0) && deduction-end > float64(codebuddyRefillGap.Milliseconds()) {
			refills = append(refills, account)
		} else {
			bonuses = append(bonuses, account)
		}
	}
	byCycleEnd := func(packs []json.RawMessage) {
		sort.SliceStable(packs, func(i, j int) bool { return cycleEnd(packs[i]) < cycleEnd(packs[j]) })
	}
	byCycleEnd(refills)
	byCycleEnd(bonuses)
	recurring, oneShot := true, false
	var windows []quota.Window
	seen := map[string]int{}
	for _, account := range refills {
		base := codebuddyCadence(account)
		seen[base]++
		name := base
		if seen[base] > 1 {
			name = fmt.Sprintf("%s %d", base, seen[base])
		}
		next := codebuddyWindow(name, account, "CycleCapacityUsed", "CycleCapacitySize")
		next.Recurring = &recurring
		windows = putWindow(windows, next)
	}
	for i, account := range bonuses {
		next := codebuddyWindow(fmt.Sprintf("Bonus Pack %d", i+1), account, "CapacityUsed", "CapacitySize")
		next.Recurring = &oneShot
		windows = putWindow(windows, next)
	}
	base := accounts[0]
	if len(refills) > 0 {
		base = refills[0]
	}
	plan := "CodeBuddy"
	if raw := shared.FirstTruthy(shared.Field(base, "PackageName"), shared.Field(base, "SubProductName")); raw != nil {
		plan = shared.Template(raw)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// codebuddyWindow reads one package balance: `Number(precise ?? plain)`, 0
// when not finite (codebuddy-cn.js:29-32). The reset is the cycle end.
func codebuddyWindow(name string, account json.RawMessage, used, size string) quota.Window {
	amount := func(field string) float64 {
		value := shared.Number(shared.FirstNonNullish(shared.Field(account, field+"Precise"), shared.Field(account, field)))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0
		}
		return value
	}
	spent, total := amount(used), amount(size)
	next := quota.Window{Name: name, ResetAt: shared.ResetTime(shared.Field(account, "CycleEndTime")), Count: &quota.Count{Used: &spent, Total: &total}}
	if total > 0 {
		remaining := math.Max(0, math.Min(1, (total-spent)/total))
		next.Remaining = &remaining
	}
	return next
}

// codebuddyCadence labels a refill pack by its cycle length
// (codebuddy-cn.js:35-44).
func codebuddyCadence(account json.RawMessage) string {
	start := shared.ResetTime(shared.Field(account, "CycleStartTime"))
	end := shared.ResetTime(shared.Field(account, "CycleEndTime"))
	if start != nil && end != nil {
		days := float64(end.UnixMilli()-start.UnixMilli()) / 86400000
		switch {
		case days <= 1.5:
			return "Daily"
		case days <= 10:
			return "Weekly"
		}
	}
	return "Monthly"
}

// deepseek mirrors open-sse/services/usage/deepseek.js: a credit balance per
// currency, not a usage window (deepseek.js:11-37,90-108). Each currency is its
// own window carrying a quota.Credit, so CNY and USD are never summed, a zero
// balance stays zero rather than unlimited, and a missing or malformed amount
// is reported as such instead of being read as zero the way the pin's
// toFiniteNumber fallback does. Remaining stays nil on every balance window:
// a zero balance in one currency must not exhaust an account holding credit in
// another, so routing is unchanged by these windows.
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
		Available      json.RawMessage   `json:"is_available"`
		AvailableCamel json.RawMessage   `json:"isAvailable"`
		Balances       []json.RawMessage `json:"balance_infos"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("deepseek balance response: %v", err)
	}
	var windows []quota.Window
	for _, raw := range body.Balances {
		credit, ok := deepseekCredit(raw)
		if !ok {
			continue
		}
		name := "Balance (" + credit.Currency + ")"
		next := quota.Window{Name: name, Credit: &credit}
		// The pin keys quotas by name, so a repeated currency replaces the
		// earlier entry rather than adding a second window of the same name.
		replaced := false
		for i := range windows {
			if windows[i].Name == name {
				windows[i], replaced = next, true
			}
		}
		if !replaced {
			windows = append(windows, next)
		}
	}
	if len(windows) == 0 {
		return failure("deepseek returned no balance")
	}
	available := deepseekFlag(body.Available)
	if available == nil {
		available = deepseekFlag(body.AvailableCamel)
	}
	plan := "DeepSeek"
	switch {
	case available == nil:
		plan = "DeepSeek (Availability Unknown)"
	case !*available:
		plan = "DeepSeek (Insufficient Balance)"
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now(), CreditAvailable: available}, nil
}

// deepseekFlag reads is_available; anything but a JSON boolean is unreported.
func deepseekFlag(raw json.RawMessage) *bool {
	var value bool
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

// deepseekCredit parses one balance_infos entry. Like the pin it skips an entry
// that is not an object or has no string currency (deepseek.js:15-18) and
// accepts the camelCase spellings (deepseek.js:20,27,31).
func deepseekCredit(raw json.RawMessage) (quota.Credit, bool) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || item == nil {
		return quota.Credit{}, false
	}
	var currency string
	if json.Unmarshal(item["currency"], &currency) != nil || strings.TrimSpace(currency) == "" {
		return quota.Credit{}, false
	}
	field := func(snake, camel string) json.RawMessage {
		if value, ok := item[snake]; ok && strings.TrimSpace(string(value)) != "null" {
			return value
		}
		return item[camel]
	}
	credit := quota.Credit{Currency: strings.ToUpper(strings.TrimSpace(currency))}
	total, state := creditAmount(field("total_balance", "totalBalance"))
	credit.State, credit.Total = state, total
	credit.Granted, _ = creditAmount(field("granted_balance", "grantedBalance"))
	credit.ToppedUp, _ = creditAmount(field("topped_up_balance", "toppedUpBalance"))
	return credit, true
}

// creditAmount reads a decimal amount sent as a JSON number or numeric string.
// It is strict where number() is lenient: "12abc", NaN and infinities are
// malformed, not a partial parse.
func creditAmount(raw json.RawMessage) (*float64, quota.CreditState) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil, quota.CreditMissing
	}
	var value float64
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		var quoted string
		if json.Unmarshal([]byte(text), &quoted) != nil {
			return nil, quota.CreditMalformed
		}
		quoted = strings.TrimSpace(quoted)
		if quoted == "" {
			return nil, quota.CreditMissing
		}
		parsed, err := strconv.ParseFloat(quoted, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, quota.CreditMalformed
		}
		value = parsed
	}
	switch {
	case value > 0:
		return &value, quota.CreditPositive
	case value < 0:
		return &value, quota.CreditNegative
	default:
		value = 0 // normalize -0
		return &value, quota.CreditZero
	}
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
