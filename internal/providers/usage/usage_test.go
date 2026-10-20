package usage

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// stubClient answers every usage request with one scripted response and records
// what the reader sent, so a test asserts the wire contract without a network.
type stubClient struct {
	status  int
	body    string
	headers http.Header
	request *http.Request
	bodyRaw string
}

func (s *stubClient) Do(request *http.Request) (*http.Response, error) {
	s.request = request
	if request.Body != nil {
		raw, _ := io.ReadAll(request.Body)
		s.bodyRaw = string(raw)
	}
	header := s.headers
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: s.status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func reader(t *testing.T, id string, stub *stubClient) quota.UsageClient {
	t.Helper()
	readers, err := Readers(stub, func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	client, ok := readers[id]
	if !ok {
		t.Fatalf("no usage reader registered for %q", id)
	}
	return client
}

// TestGLMReportsBothLimitTypes proves the GLM reader keeps TOKENS_LIMIT and
// CREDIT_LIMIT as separate windows and labels them by unit.
func TestGLMReportsBothLimitTypes(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"data":{"level":"pro","limits":[
		{"type":"TOKENS_LIMIT","percentage":40,"nextResetTime":1761000000000,"unit":3,"number":5},
		{"type":"CREDIT_LIMIT","percentage":10,"unit":6,"number":1}
	]}}`}
	state, err := reader(t, "glm", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan != "Pro" {
		t.Fatalf("plan=%q", state.Plan)
	}
	got := map[string]float64{}
	for _, window := range state.Windows {
		if window.Remaining == nil {
			t.Fatalf("window %q has no remaining bound", window.Name)
		}
		got[window.Name] = *window.Remaining
	}
	if len(got) != 2 {
		t.Fatalf("windows=%v", got)
	}
	if value, ok := got["Session (5h)"]; !ok || value != 0.6 {
		t.Fatalf("session window=%v (want 0.6 remaining)", got)
	}
	if value, ok := got["Weekly (7d)"]; !ok || value != 0.9 {
		t.Fatalf("weekly window=%v (want 0.9 remaining)", got)
	}
	if stub.request.Header.Get("Authorization") != "Bearer fixture-key" {
		t.Fatalf("authorization header=%q", stub.request.Header.Get("Authorization"))
	}
}

// TestGLMRejectsExpiredKey keeps a 401 from being reported as a healthy account.
func TestGLMRejectsExpiredKey(t *testing.T) {
	stub := &stubClient{status: http.StatusUnauthorized, body: `{}`}
	if _, err := reader(t, "glm", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"}); err == nil {
		t.Fatal("accepted an expired GLM key")
	}
}

// TestKimiUsesAPIKeyHeader proves the dual-auth branch: an API key goes in
// x-api-key, an OAuth token goes in Authorization with the device headers.
func TestKimiUsesAPIKeyHeader(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"usage":{"limit":100,"used":25,"resetTime":"2026-11-01T00:00:00Z"},
		"user":{"membership":{"level":"LEVEL_BASIC"}}}`}
	state, err := reader(t, "kimi", stub).Usage(context.Background(), quota.Credential{
		ProviderData: map[string]string{"apiKey": "fixture-api-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.request.Header.Get("x-api-key") != "fixture-api-key" || stub.request.Header.Get("Authorization") != "" {
		t.Fatalf("api-key branch sent %v", stub.request.Header)
	}
	if state.Plan != "Moderato" {
		t.Fatalf("plan=%q", state.Plan)
	}
	if len(state.Windows) != 1 || state.Windows[0].Remaining == nil || *state.Windows[0].Remaining != 0.75 {
		t.Fatalf("windows=%+v", state.Windows)
	}
}

func TestKimiOAuthSendsDeviceHeaders(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"usage":{"limit":100,"used":10}}`}
	_, err := reader(t, "kimi", stub).Usage(context.Background(), quota.Credential{
		AccessToken:  "fixture-token",
		ProviderData: map[string]string{"deviceId": "fixture-device"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.request.Header.Get("Authorization") != "Bearer fixture-token" {
		t.Fatalf("authorization=%q", stub.request.Header.Get("Authorization"))
	}
	if stub.request.Header.Get("X-Msh-Device-Id") != "fixture-device" {
		t.Fatalf("device id header=%q", stub.request.Header.Get("X-Msh-Device-Id"))
	}
}

// TestKimiEntitlementIsNotExpiry keeps a 403 from being mistaken for reauth.
func TestKimiEntitlementIsNotExpiry(t *testing.T) {
	stub := &stubClient{status: http.StatusForbidden, body: `{"code":"permission_denied"}`}
	_, err := reader(t, "kimi", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "no usage permission") {
		t.Fatalf("403 mapped to %v", err)
	}
}

// TestGitHubPaidAndFreeShapes covers both quota_snapshots and monthly_quotas.
func TestGitHubPaidAndFreeShapes(t *testing.T) {
	paid := &stubClient{status: http.StatusOK, body: `{"copilot_plan":"individual",
		"quota_reset_date":"2026-11-01T00:00:00Z",
		"quota_snapshots":{
			"chat":{"entitlement":100,"remaining":40},
			"premium_interactions":{"entitlement":50,"remaining":50}}}`}
	state, err := reader(t, "github", paid).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 2 {
		t.Fatalf("paid windows=%+v", state.Windows)
	}
	if *state.Windows[0].Remaining != 0.4 {
		t.Fatalf("chat remaining=%v", *state.Windows[0].Remaining)
	}
	if paid.request.Header.Get("Authorization") != "token fixture" {
		t.Fatalf("github auth header=%q", paid.request.Header.Get("Authorization"))
	}

	free := &stubClient{status: http.StatusOK, body: `{"access_type_sku":"free",
		"limited_user_reset_date":"2026-11-01T00:00:00Z",
		"monthly_quotas":{"chat":100,"completions":100},
		"limited_user_quotas":{"chat":10,"completions":0}}`}
	state, err = reader(t, "github", free).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 2 || *state.Windows[0].Remaining != 0.9 {
		t.Fatalf("free windows=%+v", state.Windows)
	}
}

// TestKiroAPIKeySendsTokenType proves the API-key header the quota endpoint
// requires, and that a placeholder profile ARN is never injected.
func TestKiroAPIKeySendsTokenType(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"subscriptionInfo":{"subscriptionTitle":"Pro"},
		"usageBreakdownList":[{"resourceType":"AGENTIC_REQUEST",
			"currentUsageWithPrecision":20,"usageLimitWithPrecision":100}]}`}
	state, err := reader(t, "kiro", stub).Usage(context.Background(), quota.Credential{
		AccessToken:  "fixture-token",
		ProviderData: map[string]string{"authMethod": "api_key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.request.Header.Get("tokentype") != "API_KEY" {
		t.Fatalf("tokentype header=%q", stub.request.Header.Get("tokentype"))
	}
	if state.Plan != "Pro" || len(state.Windows) != 1 || *state.Windows[0].Remaining != 0.8 {
		t.Fatalf("state=%+v", state)
	}
}

// TestKiroFreeTrialIsSeparateWindow keeps expiring bonus credits out of the
// recurring window.
func TestKiroFreeTrialIsSeparateWindow(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"nextDateReset":1761000000,
		"usageBreakdownList":[{"resourceType":"AGENTIC_REQUEST",
			"currentUsageWithPrecision":20,"usageLimitWithPrecision":100,
			"freeTrialInfo":{"currentUsageWithPrecision":5,"usageLimitWithPrecision":10,
				"freeTrialExpiry":1761000000000}}]}`}
	state, err := reader(t, "kiro", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, window := range state.Windows {
		names[window.Name] = true
	}
	if !names["agentic_request"] || !names["agentic_request_freetrial"] {
		t.Fatalf("windows=%v", names)
	}
}

// TestGeminiUsesStoredProjectID proves the project id comes from the connection
// so a quota read needs no second discovery round trip.
func TestGeminiUsesStoredProjectID(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"buckets":[
		{"modelId":"gemini-2.5-pro","remainingFraction":0.25,"resetTime":"2026-11-01T00:00:00Z"}]}`}
	state, err := reader(t, "gemini-cli", stub).Usage(context.Background(), quota.Credential{
		AccessToken:  "fixture-token",
		ProviderData: map[string]string{"projectId": "fixture-project"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stub.bodyRaw, "fixture-project") {
		t.Fatalf("request body=%q", stub.bodyRaw)
	}
	if len(state.Windows) != 1 || *state.Windows[0].Remaining != 0.25 {
		t.Fatalf("windows=%+v", state.Windows)
	}
}

// TestAntigravityFreeTierSkipsPerModelQuota mirrors the reference: a free-tier
// account has only the weekly pool, so misleading per-model numbers are dropped.
func TestAntigravityFreeTierSkipsPerModelQuota(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"currentTier":{"name":"Free"},"paidTier":{"id":"free-tier"},
		"cloudaicompanionProject":"fixture-project"}`}
	state, err := reader(t, "antigravity", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	// The first call is loadCodeAssist; with no weekly data the read fails rather
	// than inventing a window.
	if err == nil {
		for _, window := range state.Windows {
			if window.Name == "gemini-3.8-flash-high" {
				t.Fatalf("free tier reported per-model quota: %+v", state.Windows)
			}
		}
	}
}

// TestVercelBalanceIsNotAUsagePercentage keeps the USD balance out of the
// remaining-fraction field, which the dashboard treats as a percentage.
func TestVercelBalanceIsNotAUsagePercentage(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"balance":"3.75","total_used":"1.25"}`}
	state, err := reader(t, "vercel-ai-gateway", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 2 {
		t.Fatalf("windows=%+v", state.Windows)
	}
	if !state.Windows[0].Unlimited {
		t.Fatalf("used window should be unbounded: %+v", state.Windows[0])
	}
	if state.Windows[1].Remaining == nil || *state.Windows[1].Remaining != 0.75 {
		t.Fatalf("remaining window=%+v", state.Windows[1])
	}
}

// TestDeepSeekReportsBalance keeps a credit balance out of the used/total shape.
func TestDeepSeekReportsBalance(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"is_available":true,
		"balance_infos":[{"currency":"usd","total_balance":"12.50","granted_balance":"0","topped_up_balance":"12.50"}]}`}
	state, err := reader(t, "deepseek", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 1 || state.Windows[0].Name != "Balance (USD)" {
		t.Fatalf("windows=%+v", state.Windows)
	}
	if state.Windows[0].Remaining == nil || *state.Windows[0].Remaining != 1 {
		t.Fatalf("balance window=%+v", state.Windows[0])
	}
}

// TestMissingCredentialFails proves a reader never calls upstream with an empty
// credential and reports a fabricated healthy state.
func TestMissingCredentialFails(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{}`}
	for _, id := range []string{"glm", "kimi", "github", "deepseek", "kenari", "opencode-go", "vercel-ai-gateway", "commandcode", "qoder", "zed"} {
		if _, err := reader(t, id, stub).Usage(context.Background(), quota.Credential{}); err == nil {
			t.Errorf("%s accepted an empty credential", id)
		}
	}
	if stub.request != nil {
		t.Fatal("a reader called upstream with an empty credential")
	}
}

// TestRegistryCoversEveryOracleHandler is the drift guard for the reader set:
// every provider the reference dispatcher can read must have a reader here.
func TestRegistryCoversEveryOracleHandler(t *testing.T) {
	readers, err := Readers(&stubClient{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Providers that keep a dedicated engine outside this package, and iflow, whose
	// reference handler makes no request and so is deliberately unreadable.
	dedicated := map[string]bool{"codex": true, "claude": true, "iflow": true}
	for _, id := range oracleHandledProviders {
		if _, ok := readers[id]; !ok && !dedicated[id] {
			t.Errorf("reference reads usage for %q but no reader is registered", id)
		}
	}
}

// oracleHandledProviders is the reference USAGE_HANDLERS key set, kept here so this
// unit test runs without ORACLE_DIR. The authoritative comparison against the oracle
// is TestUsageReadersMatchOracleHandlers in internal/providers/parity, which also
// fails on a reader the reference does not handle.
var oracleHandledProviders = []string{
	"antigravity", "claude", "codebuddy-cn", "codebuddy-intl", "codex", "commandcode",
	"deepseek", "gemini-cli", "github", "glm", "glm-cn", "grok-cli", "groq",
	"kenari", "kimi", "kiro", "minimax", "minimax-cn", "ollama", "opencode-go",
	"vercel-ai-gateway", "xiaomi-mimo", "zed",
}

// TestEveryReaderMakesARequestOrFails proves no reader fabricates a healthy state:
// each registered reader must either call upstream or return an error. A stub that
// answers with a plan and no windows would make routing trust a number nobody
// measured, which is why iflow is absent rather than stubbed.
func TestEveryReaderMakesARequestOrFails(t *testing.T) {
	all, err := Readers(&stubClient{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for id, client := range all {
		// The stub answers everything with an empty JSON object, so a reader that
		// made a request will have seen one; one that did not must have errored.
		stub := &stubClient{status: 200, body: `{}`}
		one, err := Readers(stub, nil)
		if err != nil {
			t.Fatal(err)
		}
		client = one[id]
		state, err := client.Usage(context.Background(), quota.Credential{
			AccessToken:  "fixture-credential",
			ProviderData: map[string]string{"apiKey": "fixture-credential"},
		})
		if err == nil && stub.request == nil {
			t.Errorf("%s returned state %+v without making a request", id, state)
		}
		if err == nil && len(state.Windows) == 0 {
			t.Errorf("%s reported plan %q with no windows and no error", id, state.Plan)
		}
	}
}
