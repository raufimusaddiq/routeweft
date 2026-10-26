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
