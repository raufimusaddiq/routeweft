package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// githubReader mirrors open-sse/services/usage/github.js.
//
// Uses the GitHub OAuth access token (not the Copilot token) against
// copilot_internal/user, and supports both the paid quota_snapshots shape and
// the free/limited monthly_quotas shape.
type githubReader struct{ Client }

func (r githubReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return failure("github access token is required")
	}
	reply, err := r.call(ctx, http.MethodGet, "https://api.github.com/copilot_internal/user", map[string]string{
		"Authorization":         "token " + credential.AccessToken,
		"Accept":                "application/json",
		"X-GitHub-Api-Version":  "2022-11-28",
		"User-Agent":            "GitHubCopilotChat/0.26.7",
		"Editor-Version":        "vscode/1.100.0",
		"Editor-Plugin-Version": "copilot-chat/0.26.7",
	}, nil)
	if err != nil {
		return failure("github usage request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("github token rejected")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("github usage returned HTTP %d", reply.status)
	}
	data, ok := shared.Object(reply.body)
	if !ok {
		return failure("github usage response is not a JSON object")
	}
	// github.js:45-58: a truthy quota_snapshots is the paid shape, even when
	// empty, and wins over the free shape.
	if snapshots := data["quota_snapshots"]; shared.Truthy(snapshots) {
		reset := shared.ResetTime(data["quota_reset_date"])
		var windows []quota.Window
		for _, name := range []string{"chat", "completions", "premium_interactions"} {
			windows = append(windows, githubSnapshotWindow(name, shared.Field(snapshots, name), reset))
		}
		state := r.state(shared.String(data["copilot_plan"]), windows...)
		state.Metadata = githubMetadata(data["quota_reset_date"])
		return state, nil
	}
	// github.js:59-82: the free/limited shape always reports chat and
	// completions; a missing amount is 0.
	if shared.Truthy(data["monthly_quotas"]) || shared.Truthy(data["limited_user_quotas"]) {
		reset := shared.ResetTime(data["limited_user_reset_date"])
		var windows []quota.Window
		for _, name := range []string{"chat", "completions"} {
			total, _ := shared.FiniteNumber(shared.Field(data["monthly_quotas"], name))
			used, _ := shared.FiniteNumber(shared.Field(data["limited_user_quotas"], name))
			windows = append(windows, githubCountWindow(name, &used, &total, nil, reset))
		}
		state := r.state(shared.String(shared.FirstTruthy(data["copilot_plan"], data["access_type_sku"])), windows...)
		state.Metadata = githubMetadata(data["limited_user_reset_date"])
		return state, nil
	}
	// github.js:85: answered, but with no quota the reader recognizes. This is
	// an unsupported quota shape, not a failed read.
	return quota.State{Message: "GitHub Copilot connected. Unable to parse quota data.", ObservedAt: r.now()}, nil
}

// githubSnapshotWindow is formatGitHubQuotaSnapshot (github.js:91-99) with the
// absolute amounts kept: used is entitlement - remaining. An absent snapshot is
// unlimited with zero amounts (github.js:92); an unlimited snapshot keeps the
// amounts the provider sent beside the flag (github.js:94-97).
func githubSnapshotWindow(name string, snapshot json.RawMessage, reset *time.Time) quota.Window {
	if !shared.Truthy(snapshot) {
		used, total := 0.0, 0.0
		return quota.Window{Name: name, Unlimited: true, ResetAt: reset, Count: &quota.Count{Used: &used, Total: &total}}
	}
	var total, remaining, used *float64
	if value, ok := shared.FiniteNumber(shared.Field(snapshot, "entitlement")); ok {
		total = &value
	}
	if value, ok := shared.FiniteNumber(shared.Field(snapshot, "remaining")); ok {
		remaining = &value
	}
	if total != nil && remaining != nil {
		value := *total - *remaining
		used = &value
	}
	if shared.Truthy(shared.Field(snapshot, "unlimited")) {
		return quota.Window{Name: name, Unlimited: true, ResetAt: reset, Count: &quota.Count{Used: used, Total: total, Remaining: remaining}}
	}
	return githubCountWindow(name, used, total, remaining, reset)
}

// githubCountWindow derives the remaining fraction from the absolute amounts.
// A zero or unreported total is not unlimited and not exhausted: the bound is
// unknown, and the reported amounts stay in Count.
func githubCountWindow(name string, used, total, remaining *float64, reset *time.Time) quota.Window {
	window := quota.Window{Name: name, ResetAt: reset, Count: &quota.Count{Used: used, Total: total, Remaining: remaining}}
	if total == nil || *total <= 0 {
		return window
	}
	left := *total - valueOr(used, 0)
	if remaining != nil {
		left = *remaining
	}
	fraction := left / *total
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	window.Remaining = &fraction
	return window
}

// githubMetadata keeps the reset date as the provider sent it (github.js:52,67).
func githubMetadata(resetDate json.RawMessage) map[string]string {
	if shared.Nullish(resetDate) {
		return nil
	}
	return map[string]string{"resetDate": shared.String(resetDate)}
}

func valueOr(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}
