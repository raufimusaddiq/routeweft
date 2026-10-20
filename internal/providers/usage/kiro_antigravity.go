package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// kiroReader mirrors open-sse/services/usage/kiro.js.
//
// Kiro exposes GetUsageLimits on three surfaces; the reference tries all three in
// order because CodeWhisperer, the Q host and the AWS SSO session accept
// different token kinds. API-key and external-idp connections need an extra
// header or the quota call is rejected.
type kiroReader struct{ Client }

func (r kiroReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return failure("kiro access token is required")
	}
	authMethod := credential.ProviderData["authMethod"]
	if authMethod == "" {
		authMethod = "builder-id"
	}
	headers := map[string]string{
		"Authorization": "Bearer " + credential.AccessToken,
		"Accept":        "application/json",
	}
	switch authMethod {
	case "api_key":
		headers["tokentype"] = "API_KEY"
	case "external_idp":
		headers["TokenType"] = "EXTERNAL_IDP"
	}
	// An API-key connection must not receive the shared placeholder profileArn:
	// CodeWhisperer rejects a profile the key does not own.
	profileARN := credential.ProviderData["profileArn"]
	if profileARN != "" && !strings.Contains(profileARN, "?") {
		headers["x-amz-user-agent"] = "aws-sdk-js/1.0.0 KiroIDE"
	}
	region := credential.ProviderData["region"]
	if region == "" {
		region = "us-east-1"
	}

	query := "?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST"
	attempts := []struct {
		method   string
		endpoint string
		headers  map[string]string
		body     []byte
	}{
		{http.MethodGet, "https://codewhisperer.us-east-1.amazonaws.com/getUsageLimits" + query, headers, nil},
		{http.MethodPost, "https://codewhisperer.us-east-1.amazonaws.com/", mergeHeaders(headers, map[string]string{
			"Content-Type": "application/x-amz-json-1.0",
			"x-amz-target": "AmazonCodeWhispererService.GetUsageLimits",
		}), kiroBody(profileARN)},
		{http.MethodGet, fmt.Sprintf("https://q.%s.amazonaws.com/getUsageLimits", region) + query, headers, nil},
	}
	sawAuthError := false
	var lastErr error
	for _, attempt := range attempts {
		reply, err := r.call(ctx, attempt.method, attempt.endpoint, attempt.headers, attempt.body)
		if err != nil {
			lastErr = err
			continue
		}
		if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
			sawAuthError = true
			lastErr = fmt.Errorf("kiro quota returned HTTP %d", reply.status)
			continue
		}
		if reply.status < 200 || reply.status >= 300 {
			lastErr = fmt.Errorf("kiro quota returned HTTP %d", reply.status)
			continue
		}
		state, err := parseKiroQuota(reply.body, r.now())
		if err != nil {
			lastErr = err
			continue
		}
		return state, nil
	}
	if sawAuthError {
		return failure("kiro quota API rejected the current %s credential", authMethod)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kiro quota API unavailable")
	}
	return failure("kiro usage unavailable: %v", lastErr)
}

func kiroBody(profileARN string) []byte {
	body := `{"origin":"AI_EDITOR","resourceType":"AGENTIC_REQUEST"}`
	if profileARN != "" {
		encoded, err := json.Marshal(profileARN)
		if err == nil {
			body = fmt.Sprintf(`{"origin":"AI_EDITOR","resourceType":"AGENTIC_REQUEST","profileArn":%s}`, encoded)
		}
	}
	return []byte(body)
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

// parseKiroQuota normalizes usageBreakdownList. Free-trial credits are a
// separate window because they expire on their own schedule.
func parseKiroQuota(payload []byte, now time.Time) (quota.State, error) {
	var body struct {
		NextDateReset any `json:"nextDateReset"`
		ResetDate     any `json:"resetDate"`
		Subscription  struct {
			Title string `json:"subscriptionTitle"`
		} `json:"subscriptionInfo"`
		UsageBreakdown []struct {
			ResourceType string          `json:"resourceType"`
			Current      json.RawMessage `json:"currentUsageWithPrecision"`
			Limit        json.RawMessage `json:"usageLimitWithPrecision"`
			FreeTrial    *struct {
				Current   json.RawMessage `json:"currentUsageWithPrecision"`
				Limit     json.RawMessage `json:"usageLimitWithPrecision"`
				ExpiresAt any             `json:"freeTrialExpiry"`
			} `json:"freeTrialInfo"`
		} `json:"usageBreakdownList"`
	}
	if err := decode(payload, &body); err != nil {
		return quota.State{}, err
	}
	if len(body.UsageBreakdown) == 0 {
		return quota.State{}, fmt.Errorf("kiro usage response had no breakdown")
	}
	reset := resetAt(body.NextDateReset)
	if reset == nil {
		reset = resetAt(body.ResetDate)
	}
	var windows []quota.Window
	for _, item := range body.UsageBreakdown {
		resource := strings.ToLower(item.ResourceType)
		if resource == "" {
			resource = "unknown"
		}
		used, _ := number(item.Current)
		total, _ := number(item.Limit)
		windows = append(windows, window(resource, used, total, reset))
		if item.FreeTrial != nil {
			freeUsed, _ := number(item.FreeTrial.Current)
			freeTotal, _ := number(item.FreeTrial.Limit)
			freeReset := resetAt(item.FreeTrial.ExpiresAt)
			if freeReset == nil {
				freeReset = reset
			}
			windows = append(windows, window(resource+"_freetrial", freeUsed, freeTotal, freeReset))
		}
	}
	plan := body.Subscription.Title
	if plan == "" {
		plan = "Kiro"
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: now}, nil
}

// antigravityReader mirrors getAntigravityUsage in google.js plus the weekly
// overlay in antigravity-weekly.js. The weekly summary is best-effort: a failure
// there never removes the per-model rows.
type antigravityReader struct{ Client }

func (r antigravityReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return failure("antigravity access token is required")
	}
	projectID, plan, paid := r.subscription(ctx, credential.AccessToken)
	var windows []quota.Window
	// Free-tier accounts have only the weekly window: fetchAvailableModels
	// reports misleading per-model numbers for them, so it is skipped entirely.
	if paid {
		windows = append(windows, r.models(ctx, credential.AccessToken, projectID)...)
	}
	windows = append(windows, r.weekly(ctx, credential.AccessToken, projectID)...)
	if len(windows) == 0 {
		return failure("antigravity returned no quota windows")
	}
	if len(windows) == 0 {
		return failure("antigravity returned no quota windows")
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

func (r antigravityReader) subscription(ctx context.Context, accessToken string) (string, string, bool) {
	reply, err := r.call(ctx, http.MethodPost,
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		map[string]string{
			"Authorization": "Bearer " + accessToken,
			"Content-Type":  "application/json",
		},
		[]byte(`{"metadata":{"ideType":9,"pluginType":2},"mode":1}`))
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return "", "Unknown", false
	}
	var body struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
		Tier    struct {
			Name string `json:"name"`
		} `json:"currentTier"`
		PaidTier struct {
			ID string `json:"id"`
		} `json:"paidTier"`
	}
	if err := decode(reply.body, &body); err != nil {
		return "", "Unknown", false
	}
	plan := body.Tier.Name
	if plan == "" {
		plan = "Unknown"
	}
	paid := body.PaidTier.ID != "" && body.PaidTier.ID != "free-tier"
	return projectIDFrom(body.Project), plan, paid
}

// antigravityModels reads per-model quota for paid accounts. Only the models the
// reference considers important are surfaced, because the endpoint returns a long
// tail of internal models.
func (r antigravityReader) models(ctx context.Context, accessToken, projectID string) []quota.Window {
	body := `{}`
	if projectID != "" {
		encoded, err := json.Marshal(projectID)
		if err == nil {
			body = fmt.Sprintf(`{"project":%s}`, encoded)
		}
	}
	reply, err := r.call(ctx, http.MethodPost,
		"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		map[string]string{
			"Authorization":    "Bearer " + accessToken,
			"Content-Type":     "application/json",
			"X-Client-Name":    "antigravity",
			"x-request-source": "local",
		},
		[]byte(body))
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return nil
	}
	var parsed struct {
		Models map[string]struct {
			Internal  bool `json:"isInternal"`
			QuotaInfo *struct {
				RemainingFraction json.RawMessage `json:"remainingFraction"`
				ResetTime         any             `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := decode(reply.body, &parsed); err != nil {
		return nil
	}
	var windows []quota.Window
	for id, model := range parsed.Models {
		if model.QuotaInfo == nil || model.Internal || !antigravityImportant(id) {
			continue
		}
		remaining, ok := number(model.QuotaInfo.RemainingFraction)
		if !ok {
			continue
		}
		windows = append(windows, percentWindow(id, remaining*100, resetAt(model.QuotaInfo.ResetTime)))
	}
	return windows
}

// antigravityImportant mirrors the reference's allowlist of models worth showing.
func antigravityImportant(id string) bool {
	switch id {
	case "gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low",
		"gemini-3.7-flash-high", "gemini-3.7-flash-medium", "gemini-3.7-flash-low",
		"gemini-3.6-flash-high", "gemini-3.6-flash-medium", "gemini-3.6-flash-low",
		"gemini-3.5-flash-low", "gemini-3.5-flash-extra-low",
		"gemini-pro-agent", "gemini-3.1-pro-low",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium",
		"gemini-3.1-flash-image":
		return true
	}
	return false
}

// antigravityWeekly reads the shared 5h/weekly pools from
// retrieveUserQuotaSummary, grouped into Gemini and Claude/GPT families.
func (r antigravityReader) weekly(ctx context.Context, accessToken, projectID string) []quota.Window {
	body := `{}`
	if projectID != "" {
		encoded, err := json.Marshal(projectID)
		if err == nil {
			body = fmt.Sprintf(`{"project":%s}`, encoded)
		}
	}
	reply, err := r.call(ctx, http.MethodPost,
		"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
		map[string]string{
			"Authorization": "Bearer " + accessToken,
			"Content-Type":  "application/json",
			"X-Client-Name": "antigravity",
		},
		[]byte(body))
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return nil
	}
	var parsed struct {
		Groups []struct {
			Name    string `json:"name"`
			Buckets []struct {
				Window            string          `json:"window"`
				RemainingFraction json.RawMessage `json:"remainingFraction"`
				ResetTime         any             `json:"resetTime"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := decode(reply.body, &parsed); err != nil {
		return nil
	}
	var windows []quota.Window
	for _, group := range parsed.Groups {
		family := ""
		switch {
		case strings.Contains(strings.ToLower(group.Name), "gemini"):
			family = "Gemini"
		case strings.Contains(strings.ToLower(group.Name), "claude"),
			strings.Contains(strings.ToLower(group.Name), "gpt"):
			family = "Claude & GPT"
		}
		if family == "" {
			continue
		}
		for _, bucket := range group.Buckets {
			remaining, ok := number(bucket.RemainingFraction)
			if !ok {
				continue
			}
			label := family + " (5h)"
			if strings.EqualFold(bucket.Window, "weekly") {
				label = family + " (Weekly)"
			}
			windows = append(windows, percentWindow(label, remaining*100, resetAt(bucket.ResetTime)))
		}
	}
	return windows
}
