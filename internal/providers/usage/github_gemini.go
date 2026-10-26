package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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
	var body struct {
		Plan             string `json:"copilot_plan"`
		ResetDate        any    `json:"quota_reset_date"`
		LimitedResetDate any    `json:"limited_user_reset_date"`
		AccessTypeSKU    string `json:"access_type_sku"`
		QuotaSnapshots   map[string]struct {
			Entitlement json.RawMessage `json:"entitlement"`
			Remaining   json.RawMessage `json:"remaining"`
			Unlimited   bool            `json:"unlimited"`
		} `json:"quota_snapshots"`
		MonthlyQuotas map[string]json.RawMessage `json:"monthly_quotas"`
		LimitedQuotas map[string]json.RawMessage `json:"limited_user_quotas"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("github usage response: %v", err)
	}
	if len(body.QuotaSnapshots) > 0 {
		reset := resetAt(body.ResetDate)
		var windows []quota.Window
		for _, name := range []string{"chat", "completions", "premium_interactions"} {
			snapshot, ok := body.QuotaSnapshots[name]
			if !ok {
				continue
			}
			entitlement, _ := number(snapshot.Entitlement)
			remaining, _ := number(snapshot.Remaining)
			windows = append(windows, window(name, entitlement-remaining, entitlement, reset))
			if snapshot.Unlimited {
				windows[len(windows)-1] = quota.Window{Name: name, Unlimited: true, ResetAt: reset}
			}
		}
		state := r.state(body.Plan, windows...)
		return state, nil
	}
	if len(body.MonthlyQuotas) > 0 || len(body.LimitedQuotas) > 0 {
		reset := resetAt(body.LimitedResetDate)
		var windows []quota.Window
		for _, name := range []string{"chat", "completions"} {
			total, _ := number(body.MonthlyQuotas[name])
			used, _ := number(body.LimitedQuotas[name])
			windows = append(windows, window(name, used, total, reset))
		}
		plan := body.Plan
		if plan == "" {
			plan = body.AccessTypeSKU
		}
		state := r.state(plan, windows...)
		return state, nil
	}
	return failure("github usage response had no recognizable quota shape")
}
