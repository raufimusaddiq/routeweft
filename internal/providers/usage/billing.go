package usage

// The Command Code, Kenari, OpenCode Go, Vercel AI Gateway and Ollama readers
// (#225). Each mirrors one pinned handler and reads the body with the pin's
// value rules (providers/shared/jsvalue.go). Absolute amounts stay in the
// window's count, with the currency when they are money; answers without quota
// are fixed-text notices (status unknown), and every failure is an error.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// jsonBody is `response.json()`: the body without a leading byte-order mark,
// or false when it is not JSON.
func jsonBody(body []byte) (json.RawMessage, bool) {
	payload := bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	if !json.Valid(payload) {
		return nil, false
	}
	return payload, true
}

// jsonOrEmpty is `response.json().catch(() => ({}))`.
func jsonOrEmpty(body []byte) json.RawMessage {
	if payload, ok := jsonBody(body); ok {
		return payload
	}
	return json.RawMessage(`{}`)
}

// toFinite is the pin's toFiniteNumber(value, 0).
func toFinite(raw json.RawMessage) float64 {
	value, _ := shared.FiniteNumber(raw)
	return value
}

// amount is a reported amount for a count. A non-finite one (Vercel's
// `Number("Infinity")`) is no amount: the pin's JSON writes it as null, and Go
// cannot encode it.
func amount(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

// share is the remaining fraction of a positive total, clamped to [0,1].
func share(left, total float64) *float64 {
	return fraction(left / total * 100)
}

// stringValue reports a JSON string value (`typeof value === "string"`).
func stringValue(raw json.RawMessage) (string, bool) {
	var text string
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' || json.Unmarshal(trimmed, &text) != nil {
		return "", false
	}
	return text, true
}

// capitalize is `s.charAt(0).toUpperCase() + s.slice(1).toLowerCase()`.
func capitalize(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	return strings.ToUpper(string(first)) + strings.ToLower(text[size:])
}

// searchParam is one `name=value` pair as URLSearchParams serializes it
// (application/x-www-form-urlencoded): a space is "+", and every byte but
// ASCII letters, digits and `*-._` is percent-encoded.
func searchParam(name, value string) string {
	encode := func(text string) string {
		var out strings.Builder
		for i := 0; i < len(text); i++ {
			c := text[i]
			switch {
			case c == ' ':
				out.WriteByte('+')
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '*', c == '-', c == '.', c == '_':
				out.WriteByte(c)
			default:
				fmt.Fprintf(&out, "%%%02X", c)
			}
		}
		return out.String()
	}
	return encode(name) + "=" + encode(value)
}

// commandCodePlans and commandCodeCaps are PLAN_NAMES and PLAN_CAPS
// (commandcode.js:11-31).
var commandCodePlans = map[string]string{
	"individual-go": "Go", "individual-goat": "GOAT", "individual-pro": "Pro",
	"individual-pro-v1": "Pro", "individual-provider": "Provider", "individual-max": "Max",
	"individual-ultra": "Ultra", "teams-pro": "Teams Pro",
}

var commandCodeCaps = map[string]float64{
	"individual-go": 10, "individual-goat": 70, "individual-pro": 30,
	"individual-pro-v1": 80, "individual-provider": 15, "individual-max": 150,
	"individual-ultra": 300, "teams-pro": 40,
}

// commandcode mirrors open-sse/services/usage/commandcode.js:33-134. whoami
// gives the org; credits and subscriptions are then read with `orgId` as
// URLSearchParams spells it (any non-null id, even ""). The subscription is
// `data.planId` only, so an array of subscriptions has no plan, as in the pin.
// Credits are the monthly, purchased and free balances; against a known plan
// cap they are a bounded window, and without one the pin calls them unlimited.
// The five-hour and weekly limits are windows unless both amounts are zero; a
// zero cap with usage has no remaining fraction.
func (r apiKeyReader) commandcode(ctx context.Context, key string) (quota.State, error) {
	key = shared.Trim(key)
	const base = "https://api.commandcode.ai"
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
	query := ""
	if org := shared.Field(shared.Field(jsonOrEmpty(reply.body), "org"), "id"); !shared.Nullish(org) {
		query = "?" + searchParam("orgId", shared.Template(org))
	}
	// The pin starts both reads before awaiting either (Promise.all), so both
	// are sent even when the first fails.
	creditsReply, creditsErr := r.getJSON(ctx, base+"/alpha/billing/credits"+query, key)
	subsReply, subsErr := r.getJSON(ctx, base+"/alpha/billing/subscriptions"+query, key)
	if creditsErr != nil || subsErr != nil {
		return failure("commandcode billing request failed")
	}
	auth := func(status int) bool { return status == http.StatusUnauthorized || status == http.StatusForbidden }
	if auth(creditsReply.status) || auth(subsReply.status) {
		return failure("commandcode authentication failed")
	}
	if creditsReply.status < 200 || creditsReply.status >= 300 {
		return failure("commandcode credits returned HTTP %d", creditsReply.status)
	}
	if subsReply.status < 200 || subsReply.status >= 300 {
		return failure("commandcode subscriptions returned HTTP %d", subsReply.status)
	}
	credits, subscription := jsonOrEmpty(creditsReply.body), shared.Field(jsonOrEmpty(subsReply.body), "data")
	plan, planCap := "Command Code", 0.0
	if planID := shared.Field(subscription, "planId"); shared.Truthy(planID) {
		id := shared.Template(planID)
		plan = id
		if name, ok := commandCodePlans[id]; ok {
			plan = name
		}
		planCap = commandCodeCaps[id]
	}
	balances := shared.Field(credits, "credits")
	remaining := toFinite(shared.Field(balances, "monthlyCredits")) + toFinite(shared.Field(balances, "purchasedCredits")) +
		toFinite(shared.Field(balances, "freeCredits"))
	used, total := 0.0, remaining
	if planCap > 0 {
		used, total = math.Max(0, planCap-remaining), planCap
	}
	creditsWindow := quota.Window{Name: "Credits", ResetAt: shared.ResetTime(shared.Field(subscription, "currentPeriodEnd")),
		Count: &quota.Count{Used: amount(used), Total: amount(total), Remaining: amount(remaining)}}
	if planCap > 0 {
		creditsWindow.Remaining = share(total-used, total)
	} else {
		creditsWindow.Unlimited = true
	}
	windows := []quota.Window{creditsWindow}
	limits := shared.Field(credits, "windowLimits")
	for _, limit := range []struct{ key, name string }{{"fiveHour", "Session (5h)"}, {"weekly", "Weekly"}} {
		item := shared.Field(limits, limit.key)
		used, total := toFinite(shared.Field(item, "used")), toFinite(shared.Field(item, "cap"))
		if total <= 0 && used <= 0 {
			continue
		}
		left := math.Max(0, total-used)
		next := quota.Window{Name: limit.name, ResetAt: shared.ResetTime(shared.Field(item, "resetAt")),
			Count: &quota.Count{Used: amount(used), Total: amount(total), Remaining: amount(left)}}
		if total > 0 {
			next.Remaining = share(left, total)
		}
		windows = append(windows, next)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// kenari mirrors open-sse/services/usage/kenari.js:9-56. Each entry of
// `plan.windows`, in JavaScript's key order, is a window: `month` and `week`
// are labelled, any other key is its own name, and a repeated label replaces
// the earlier window in its place. The remaining percentage is rounded as the
// pin rounds it; the rupiah amounts stay in the count. The reset is passed
// through raw by the pin and read as `new Date(value)` (shared.Date). A missing
// or unreadable amount is 0, as toFiniteNumber reads it, so a window with
// neither amount is 0% (kenari.js:39-44): the owner chose the pin's reading
// for #225.
func (r apiKeyReader) kenari(ctx context.Context, key string) (quota.State, error) {
	reply, err := r.getJSON(ctx, "https://kenari.id/v1/account/quota", shared.Trim(key))
	if err != nil {
		return failure("kenari usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("kenari authentication failed")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("kenari quota returned HTTP %d", reply.status)
	}
	body, _ := jsonBody(reply.body)
	plan := shared.Field(body, "plan")
	if !shared.Truthy(plan) {
		return quota.State{Message: "Kenari connected. No plan data returned.", ObservedAt: r.now()}, nil
	}
	name, ok := stringValue(shared.Field(plan, "name"))
	if !ok {
		name = "Kenari"
	}
	var windows []quota.Window
	for _, entry := range shared.Entries(shared.Field(plan, "windows")) {
		if shared.Nullish(entry.Value) {
			// `win.used_rp` on null throws, and the pin reports the whole read
			// as an error.
			return failure("kenari quota window %q is null", entry.Key)
		}
		label := entry.Key
		switch label {
		case "month":
			label = "Monthly (IDR)"
		case "week":
			label = "Weekly (IDR)"
		}
		used := toFinite(shared.Field(entry.Value, "used_rp"))
		total := used + toFinite(shared.Field(entry.Value, "remaining_rp"))
		percent := 0.0
		if total > 0 {
			percent = jsRound((total - used) / total * 100)
		}
		next := quota.Window{Name: label, Remaining: fraction(percent), ResetAt: shared.Date(shared.Field(entry.Value, "resets_at")),
			Count: &quota.Count{Used: amount(used), Total: amount(total), Remaining: amount(total - used), Currency: "IDR"}}
		windows = putWindow(windows, next)
	}
	if len(windows) == 0 {
		// The pin answers with the plan and an empty quota map.
		return quota.State{Plan: name, Message: "Kenari connected. No quota windows were returned.", ObservedAt: r.now()}, nil
	}
	return quota.State{Plan: name, Windows: windows, ObservedAt: r.now()}, nil
}

// opencodeGo mirrors open-sse/services/usage/opencode-go.js:16-107: the used
// percentage of `usage.rolling`, `.weekly` and `.monthly`, always in that
// order, each a finite number or numeric string clamped to [0, 100]. A 403
// whose error type is EntitlementError is the account without the
// subscription: a notice, like an answer without quota.
func (r apiKeyReader) opencodeGo(ctx context.Context, key string) (quota.State, error) {
	reply, err := r.getJSON(ctx, "https://opencode.ai/zen/go/v1/usage", shared.Trim(key))
	if err != nil {
		return failure("opencode-go usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized {
		return failure("opencode-go authentication failed")
	}
	if reply.status == http.StatusForbidden {
		body, _ := jsonBody(reply.body)
		if kind, _ := stringValue(shared.Field(shared.Field(body, "error"), "type")); kind == "EntitlementError" {
			return quota.State{Plan: "OpenCode Go", Message: "OpenCode Go subscription required for this API key.", ObservedAt: r.now()}, nil
		}
		return failure("opencode-go access forbidden for this key")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("opencode-go usage returned HTTP %d", reply.status)
	}
	body, _ := jsonBody(reply.body)
	usage := bytes.TrimSpace(shared.Field(body, "usage"))
	if len(usage) == 0 || usage[0] != '{' && usage[0] != '[' {
		return quota.State{Plan: "OpenCode Go", Message: "OpenCode Go usage response did not contain quota data.", ObservedAt: r.now()}, nil
	}
	var windows []quota.Window
	for _, period := range []struct{ key, name string }{{"rolling", "Rolling"}, {"weekly", "Weekly"}, {"monthly", "Monthly"}} {
		item := shared.Field(usage, period.key)
		percent, ok := shared.FiniteNumber(shared.Field(item, "percent"))
		if !ok {
			continue
		}
		used := math.Max(0, math.Min(100, percent))
		windows = append(windows, quota.Window{Name: period.name, Remaining: fraction(100 - used), ResetAt: shared.ResetTime(shared.Field(item, "resetsAt"))})
	}
	if len(windows) == 0 {
		return quota.State{Plan: "OpenCode Go", Message: "OpenCode Go usage response did not contain valid quota data.", ObservedAt: r.now()}, nil
	}
	return quota.State{Plan: "OpenCode Go", Windows: windows, ObservedAt: r.now()}, nil
}

// vercelMonthlyCredit is MONTHLY_CREDIT (misc.js:191-193): the API does not
// report the allocation, so the documented $5 is the denominator.
const vercelMonthlyCredit = 5.0

// vercel mirrors getVercelAiGatewayUsage (open-sse/services/usage/misc.js:161-228):
// `Number(x) || 0` of the balance and the amount used. Neither positive is the
// BYOK or unfunded account, a notice. Otherwise "Used (USD)" is the amount spent
// with no cap (unlimited) and "Remaining (USD)" the balance against $5; the
// dollar amounts stay in each window's count.
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
	body, ok := jsonBody(reply.body)
	if !ok {
		return failure("vercel credits response is not JSON")
	}
	balance := shared.OrZero(shared.Number(shared.Field(body, "balance")))
	spent := shared.OrZero(shared.Number(shared.Field(body, "total_used")))
	if balance <= 0 && spent <= 0 {
		return quota.State{Plan: "Pay-as-you-go", Message: "Vercel AI Gateway connected. No credit allocation found (BYOK or unfunded account).", ObservedAt: r.now()}, nil
	}
	windows := []quota.Window{
		{Name: "Used (USD)", Unlimited: true, Count: &quota.Count{Used: amount(spent), Currency: "USD"}},
		{Name: "Remaining (USD)", Remaining: share(balance, vercelMonthlyCredit),
			Count: &quota.Count{Total: amount(vercelMonthlyCredit), Remaining: amount(balance), Currency: "USD"}},
	}
	return quota.State{Plan: "Pay-as-you-go", Windows: windows, ObservedAt: r.now()}, nil
}

// ollama mirrors getOllamaUsage (open-sse/services/usage/misc.js:27-144).
// `limits.session`, `.weekly` and `.monthly` are 0..1 used ratios, in that
// order, rounded to a whole used percentage. The plan comes from a best-effort
// POST /api/me; on the free plan the monthly window resets on the signup
// anniversary (CreatedAt). No other window reports a reset.
func (r apiKeyReader) ollama(ctx context.Context, key string, _ quota.Credential) (quota.State, error) {
	headers := map[string]string{"Authorization": "Bearer " + key, "Accept": "application/json"}
	reply, err := r.call(ctx, http.MethodGet, "https://ollama.com/api/usage", headers, nil)
	if err != nil {
		return failure("ollama usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("ollama API key invalid or expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("ollama usage returned HTTP %d", reply.status)
	}
	body, ok := jsonBody(reply.body)
	if !ok {
		return failure("ollama usage response is not JSON")
	}
	planRaw, created := "", json.RawMessage(nil)
	// The request has no body; Content-Length is written as the pin sends it.
	headers["Content-Length"] = "0"
	if me, err := r.call(ctx, http.MethodPost, "https://ollama.com/api/me", headers, nil); err == nil && me.status >= 200 && me.status < 300 {
		if account, ok := jsonBody(me.body); ok {
			planRaw, _ = stringValue(shared.Field(account, "Plan"))
			created = shared.Field(account, "CreatedAt")
		}
	}
	plan := "Ollama Cloud"
	if planRaw != "" {
		plan = capitalize(planRaw)
	}
	var monthlyReset *time.Time
	if strings.ToLower(planRaw) == "free" && shared.Truthy(created) {
		monthlyReset = signupReset(shared.Date(created), r.now())
	}
	limits := shared.Field(body, "limits")
	var windows []quota.Window
	for _, limit := range []struct{ key, name string }{{"session", "Session (5h)"}, {"weekly", "Weekly (7d)"}, {"monthly", "Monthly"}} {
		raw := shared.Field(shared.Field(limits, limit.key), "usage")
		ratio := shared.Number(raw)
		if shared.Nullish(raw) || math.IsNaN(ratio) {
			continue
		}
		used := jsRound(math.Max(0, math.Min(1, ratio)) * 100)
		next := quota.Window{Name: limit.name, Remaining: fraction(100 - used)}
		if limit.key == "monthly" {
			next.ResetAt = monthlyReset
		}
		windows = append(windows, next)
	}
	if len(windows) == 0 {
		return quota.State{Plan: plan, Message: "Ollama Cloud connected. No usage limits reported.", ObservedAt: r.now()}, nil
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

// signupReset is nextMonthlyResetFromSignup (misc.js:33-55): the first
// monthly anniversary of the signup after now, in UTC, on the signup's day
// clamped to the month's last day, to the second. Months before the signup
// are not counted, so a signup more than a month ahead has no reset.
func signupReset(anchor *time.Time, now time.Time) *time.Time {
	if anchor == nil {
		return nil
	}
	start, now := anchor.UTC(), now.UTC()
	elapsed := (now.Year()-start.Year())*12 + int(now.Month()) - int(start.Month())
	for i := max(0, elapsed); i <= elapsed+1; i++ {
		months := int(start.Month()) - 1 + i
		year, month := start.Year()+months/12, time.Month(months%12+1)
		last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
		candidate := time.Date(year, month, min(start.Day(), last), start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
		if candidate.After(now) {
			return &candidate
		}
	}
	return nil
}
