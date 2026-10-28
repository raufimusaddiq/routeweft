package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// kiroReader mirrors getKiroUsage (open-sse/services/usage/kiro.js:51-188).
//
// GetUsageLimits is tried on CodeWhisperer by GET, then by the AWS JSON POST,
// then on the Q host; the first 2xx JSON answer wins. All three are us-east-1,
// whatever the connection's region, as in the pin's registry entry
// (providers/registry/kiro.js:37-41).
type kiroReader struct{ Client }

const (
	kiroCodeWhisperer = "https://codewhisperer.us-east-1.amazonaws.com"
	kiroQ             = "https://q.us-east-1.amazonaws.com"
)

// kiroDefaultProfileARNs are the public shared profiles the pin sends when an
// account has none of its own, keyed by social sign-in (config/kiroConstants.js:30-42).
var kiroDefaultProfileARNs = map[bool]string{
	false: "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX",
	true:  "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK",
}

func (r kiroReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return failure("kiro access token is required")
	}
	authMethod := credential.ProviderData["authMethod"]
	if authMethod == "" {
		authMethod = "builder-id"
	}
	social := authMethod == "google" || authMethod == "github"
	auth := map[string]string{"Authorization": "Bearer " + credential.AccessToken}
	switch authMethod {
	case "api_key":
		auth["tokentype"] = "API_KEY"
	case "external_idp":
		auth["TokenType"] = "EXTERNAL_IDP"
	}
	// An API key gets no shared placeholder: CodeWhisperer rejects a profile
	// the key's account does not own.
	profileARN := credential.ProviderData["profileArn"]
	if profileARN == "" && authMethod != "api_key" {
		profileARN = kiroDefaultProfileARNs[social]
	}
	body := `{"origin":"AI_EDITOR","resourceType":"AGENTIC_REQUEST"}`
	qQuery := url.Values{"origin": {"AI_EDITOR"}, "resourceType": {"AGENTIC_REQUEST"}}
	if profileARN != "" {
		encoded, _ := json.Marshal(profileARN)
		body = fmt.Sprintf(`{"origin":"AI_EDITOR","profileArn":%s,"resourceType":"AGENTIC_REQUEST"}`, encoded)
		qQuery.Set("profileArn", profileARN)
	}
	attempts := []struct {
		name, method, endpoint string
		headers                map[string]string
		body                   []byte
	}{
		{"codewhisperer-get", http.MethodGet, kiroCodeWhisperer + "/getUsageLimits?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST",
			mergeHeaders(auth, map[string]string{"Accept": "application/json", "x-amz-user-agent": "aws-sdk-js/1.0.0 KiroIDE",
				"User-Agent": "aws-sdk-js/1.0.0 KiroIDE"}), nil},
		{"codewhisperer-post", http.MethodPost, kiroCodeWhisperer, mergeHeaders(auth, map[string]string{"Accept": "application/json",
			"Content-Type": "application/x-amz-json-1.0", "x-amz-target": "AmazonCodeWhispererService.GetUsageLimits"}), []byte(body)},
		{"q-get", http.MethodGet, kiroQ + "/getUsageLimits?" + qQuery.Encode(), mergeHeaders(auth, map[string]string{"Accept": "application/json"}), nil},
	}
	sawAuthError := false
	var lastErr error
	for _, attempt := range attempts {
		reply, err := r.call(ctx, attempt.method, attempt.endpoint, attempt.headers, attempt.body)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", attempt.name, err)
			continue
		}
		if reply.status < 200 || reply.status >= 300 {
			sawAuthError = sawAuthError || reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden
			lastErr = fmt.Errorf("%s: HTTP %d", attempt.name, reply.status)
			continue
		}
		state, err := parseKiroQuota(reply.body, r.now())
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", attempt.name, err)
			continue
		}
		return state, nil
	}
	switch {
	case sawAuthError && authMethod == "idc":
		return failure("kiro quota API is unavailable for the current AWS IAM Identity Center session")
	case sawAuthError && social:
		return failure("kiro quota API authentication expired")
	case sawAuthError:
		return failure("kiro quota API rejected the current token")
	}
	return failure("kiro usage unavailable: %v", lastErr)
}

func mergeHeaders(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

// parseKiroQuota is parseKiroQuotaData (kiro.js:12-49). A body the pin cannot
// read (null, a non-array list, a null entry) fails this attempt, and the next
// one is tried. A repeated resource type replaces the earlier window in place.
func parseKiroQuota(payload []byte, now time.Time) (quota.State, error) {
	if !json.Valid(payload) || shared.Nullish(payload) {
		return quota.State{}, errors.New("kiro usage response was not JSON")
	}
	list := shared.FirstTruthy(shared.Field(payload, "usageBreakdownList"))
	entries, isArray := shared.Array(list)
	if list != nil && !isArray {
		return quota.State{}, errors.New("kiro usageBreakdownList is not a list")
	}
	reset := shared.ResetTime(shared.FirstTruthy(shared.Field(payload, "nextDateReset"), shared.Field(payload, "resetDate")))
	var windows []quota.Window
	put := func(window quota.Window) {
		for i := range windows {
			if windows[i].Name == window.Name {
				windows[i] = window
				return
			}
		}
		windows = append(windows, window)
	}
	for _, entry := range entries {
		if shared.Nullish(entry) {
			return quota.State{}, errors.New("kiro usage entry is null")
		}
		resource := "unknown"
		if raw := shared.Field(entry, "resourceType"); !shared.Nullish(raw) {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return quota.State{}, errors.New("kiro resourceType is not a string")
			}
			if text != "" {
				resource = strings.ToLower(text)
			}
		}
		put(kiroWindow(resource, entry, reset))
		// Free-trial credits expire on their own schedule.
		if trial := shared.Field(entry, "freeTrialInfo"); shared.Truthy(trial) {
			trialReset := reset
			if expiry := shared.Field(trial, "freeTrialExpiry"); shared.Truthy(expiry) {
				trialReset = shared.ResetTime(expiry)
			}
			put(kiroWindow(resource+"_freetrial", trial, trialReset))
		}
	}
	plan := "Kiro"
	if title := shared.Field(shared.Field(payload, "subscriptionInfo"), "subscriptionTitle"); shared.Truthy(title) {
		plan = shared.Template(title)
	}
	if len(windows) == 0 {
		// The pin answers with the plan and an empty quota map.
		return quota.State{Plan: plan, Message: "Kiro connected. No usage limits were returned.", ObservedAt: now}, nil
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: now}, nil
}

// kiroWindow reads `currentUsageWithPrecision || 0` and
// `usageLimitWithPrecision || 0` as numbers; the remaining amount is the pin's
// total − used.
func kiroWindow(name string, entry json.RawMessage, reset *time.Time) quota.Window {
	amount := func(field string) float64 {
		return finite(shared.Number(shared.Field(entry, field)))
	}
	used, total := amount("currentUsageWithPrecision"), amount("usageLimitWithPrecision")
	remaining := total - used
	return countWindow(name, used, total, &remaining, reset)
}
