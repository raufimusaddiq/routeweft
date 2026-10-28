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

// P08 per-provider quota evidence for the readers that had no pin-shaped
// fixture. Bodies are synthetic, shaped like the pin at 1c3b9f47 (the file and
// lines are named on each test). Nothing here touches a network.

// routedClient answers by URL path so a reader that makes several requests
// (Command Code, MiniMax) can be scripted per endpoint.
type routedClient struct {
	routes   map[string]routedReply
	requests []*http.Request
}

type routedReply struct {
	status int
	body   string
}

func (c *routedClient) Do(request *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, request)
	reply, ok := c.routes[request.URL.Host+request.URL.Path]
	if !ok {
		reply = routedReply{status: http.StatusNotFound, body: `{}`}
	}
	return &http.Response{StatusCode: reply.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply.body))}, nil
}

func routedReader(t *testing.T, id string, client *routedClient) quota.UsageClient {
	t.Helper()
	readers, err := Readers(client, func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return readers[id]
}

func byName(state quota.State) map[string]quota.Window {
	out := map[string]quota.Window{}
	for _, window := range state.Windows {
		out[window.Name] = window
	}
	return out
}

func wantRemaining(t *testing.T, windows map[string]quota.Window, name string, want float64) {
	t.Helper()
	window, ok := windows[name]
	if !ok {
		t.Fatalf("window %q missing from %v", name, windows)
	}
	if window.Unlimited || window.Remaining == nil || *window.Remaining < want-1e-9 || *window.Remaining > want+1e-9 {
		t.Fatalf("window %q = %+v, want remaining %v", name, window, want)
	}
}

// open-sse/services/usage/opencode-go.js:11-17,54-100: Go reports the used
// percentage per period, so 100 means no capacity left. A period the response
// does not carry is not offered at all (unknown, never zero), and a 403 whose
// body is not an EntitlementError is not a subscription hint.
func TestOpenCodeGoUsageUsedPercentAndAbsentPeriods(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"usage":{
		"rolling":{"percent":25,"resetsAt":"2026-10-20T15:00:00Z"},
		"weekly":{"percent":"100","resetsAt":"2026-10-25T00:00:00Z"}}}`}
	state, err := reader(t, "opencode-go", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	windows := byName(state)
	if len(windows) != 2 {
		t.Fatalf("an absent monthly period was invented: %v", windows)
	}
	wantRemaining(t, windows, "Rolling", 0.75)
	wantRemaining(t, windows, "Weekly", 0)
	if windows["Weekly"].ResetAt == nil || !windows["Weekly"].ResetAt.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly reset = %v", windows["Weekly"].ResetAt)
	}
	if state.Plan != "OpenCode Go" || stub.request.URL.String() != "https://opencode.ai/zen/go/v1/usage" || stub.request.Header.Get("Authorization") != "Bearer fixture-key" {
		t.Fatalf("plan %q request %s %v", state.Plan, stub.request.URL, stub.request.Header)
	}
}

func TestOpenCodeGoUsageFailuresAreErrorsNotHealthyAccounts(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusUnauthorized, `{}`, "authentication failed"},
		{http.StatusForbidden, `{"error":{"type":"EntitlementError"}}`, "subscription required"},
		{http.StatusOK, `{"usage":{}}`, "no quota data"},
		{http.StatusOK, `{"usage":{"rolling":{"percent":null}}}`, "no quota data"},
		{http.StatusBadGateway, `{}`, "HTTP 502"},
	} {
		state, err := reader(t, "opencode-go", &stubClient{status: tc.status, body: tc.body}).Usage(context.Background(), quota.Credential{AccessToken: "k"})
		if err == nil || !strings.Contains(err.Error(), tc.want) || len(state.Windows) != 0 {
			t.Errorf("status %d body %s: state %+v err %v, want error containing %q", tc.status, tc.body, state, err, tc.want)
		}
	}
}

// open-sse/services/usage/misc.js:55-140: `limits.<window>.usage` is a 0..1
// used ratio (1.0 = limit reached); a window whose usage is absent or null is
// skipped, not read as an unused window.
func TestOllamaUsageRatioIsUsedFractionAndNullWindowsAreSkipped(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"limits":{
		"session":{"usage":0.25},"weekly":{"usage":1.0},"monthly":{"usage":null}}}`}
	state, err := reader(t, "ollama", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	windows := byName(state)
	if len(windows) != 2 {
		t.Fatalf("null monthly usage became a window: %v", windows)
	}
	wantRemaining(t, windows, "Session (5h)", 0.75)
	wantRemaining(t, windows, "Weekly (7d)", 0)
	if stub.request.URL.String() != "https://ollama.com/api/usage" || stub.request.Header.Get("Authorization") != "Bearer fixture-key" {
		t.Fatalf("request %s %v", stub.request.URL, stub.request.Header)
	}
	if _, err := reader(t, "ollama", &stubClient{status: http.StatusOK, body: `{"limits":{}}`}).Usage(context.Background(), quota.Credential{AccessToken: "k"}); err == nil {
		t.Fatal("a response with no limits was read as a healthy account")
	}
}

// open-sse/services/usage/xiaomi-mimo.js:62-120: `data.percent` is the
// REMAINING percentage and `resetDate` a calendar date. A response without a
// percent must not become a full or an empty window.
func TestXiaomiMimoUsagePercentIsRemaining(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"code":0,"data":{"percent":94,"resetDate":"2026-10-27"}}`}
	state, err := reader(t, "xiaomi-mimo", stub).Usage(context.Background(), quota.Credential{AccessToken: "sk-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	windows := byName(state)
	wantRemaining(t, windows, "Weekly", 0.94)
	if windows["Weekly"].ResetAt == nil || !windows["Weekly"].ResetAt.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("reset = %v", windows["Weekly"].ResetAt)
	}
	if stub.request.Header.Get("X-Mimo-Source") != "mimocode-cli" || stub.request.Header.Get("Authorization") != "Bearer sk-fixture" {
		t.Fatalf("headers %v", stub.request.Header)
	}
	for name, body := range map[string]string{"no percent": `{"code":0,"data":{"resetDate":"2026-10-27"}}`, "non-zero code": `{"code":7,"data":{"percent":50}}`} {
		if _, err := reader(t, "xiaomi-mimo", &stubClient{status: http.StatusOK, body: body}).Usage(context.Background(), quota.Credential{AccessToken: "sk-fixture"}); err == nil {
			t.Errorf("%s was accepted as a quota", name)
		}
	}
	if _, err := reader(t, "xiaomi-mimo", &stubClient{status: http.StatusUnauthorized, body: `{}`}).Usage(context.Background(), quota.Credential{AccessToken: "sk-fixture"}); err == nil {
		t.Error("a 401 (key cannot read the account quota) was accepted")
	}
}

// open-sse/services/usage/zed.js:39-183: a limit is "unlimited", a number or
// {limited:N}; the reset is the plan's subscription period. The authorization
// header is "<user id> <token>". The full pin record is p224_account_quota_test.go.
func TestZedUsageLimitedUnlimitedAndExhausted(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"plan":{"plan_v3":"zed_pro","usage":{
		"edit_predictions":{"limit":"unlimited","used":12},
		"model_requests":{"limit":{"limited":500},"used":500}},
		"subscription_period":{"ended_at":"2026-11-01T00:00:00Z"}}}`}
	state, err := reader(t, "zed", stub).Usage(context.Background(), quota.Credential{AccessToken: "tok", ProviderData: map[string]string{"userId": "42"}})
	if err != nil {
		t.Fatal(err)
	}
	windows := byName(state)
	if !windows["Edit Predictions"].Unlimited {
		t.Fatalf("unlimited edit predictions = %+v", windows["Edit Predictions"])
	}
	wantRemaining(t, windows, "Hosted Model Requests", 0)
	if reset := windows["Hosted Model Requests"].ResetAt; reset == nil || !reset.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("reset %v", reset)
	}
	if stub.request.Header.Get("Authorization") != "42 tok" || stub.request.URL.String() != "https://cloud.zed.dev/client/users/me" {
		t.Fatalf("request %s %v", stub.request.URL, stub.request.Header)
	}
	if state.Plan != "Zed Pro" || state.Metadata["planId"] != "zed_pro" {
		t.Fatalf("plan %q metadata %v", state.Plan, state.Metadata)
	}
}

// Command Code (usage/commandcode.js:60-135): whoami discovers the org, which
// is carried to both billing reads; credits are capped by the plan id and the
// 5h/weekly windows carry their own used/cap.
func TestCommandCodeUsageCarriesOrgAndPlanCap(t *testing.T) {
	client := &routedClient{routes: map[string]routedReply{
		"api.commandcode.ai/alpha/whoami": {200, `{"org":{"id":"org_1"}}`},
		"api.commandcode.ai/alpha/billing/credits": {200, `{"credits":{"monthlyCredits":6,"purchasedCredits":1,"freeCredits":1},
			"windowLimits":{"fiveHour":{"used":3,"cap":10,"resetAt":"2026-10-20T15:00:00Z"},"weekly":{"used":10,"cap":10,"resetAt":"2026-10-25T00:00:00Z"}}}`},
		"api.commandcode.ai/alpha/billing/subscriptions": {200, `{"data":{"planId":"individual-go","currentPeriodEnd":"2026-11-01T00:00:00Z"}}`},
	}}
	state, err := routedReader(t, "commandcode", client).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan != "Go" {
		t.Fatalf("plan %q", state.Plan)
	}
	windows := byName(state)
	wantRemaining(t, windows, "Credits", 0.8)
	wantRemaining(t, windows, "Session (5h)", 0.7)
	wantRemaining(t, windows, "Weekly", 0)
	var billing int
	for _, request := range client.requests {
		if strings.HasPrefix(request.URL.Path, "/alpha/billing/") {
			billing++
			if request.URL.Query().Get("orgId") != "org_1" {
				t.Errorf("%s did not carry the discovered org: %s", request.URL.Path, request.URL.RawQuery)
			}
		}
		if request.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("%s authorization %q", request.URL.Path, request.Header.Get("Authorization"))
		}
	}
	if billing != 2 || client.requests[0].URL.RawQuery != "limits=1" {
		t.Fatalf("requests %d billing, first query %q", billing, client.requests[0].URL.RawQuery)
	}
}

func TestCommandCodeUsageAuthAndBillingFailuresAreErrors(t *testing.T) {
	for name, routes := range map[string]map[string]routedReply{
		"whoami 401":      {"api.commandcode.ai/alpha/whoami": {401, `{}`}},
		"credits failing": {"api.commandcode.ai/alpha/whoami": {200, `{}`}, "api.commandcode.ai/alpha/billing/credits": {500, `{}`}, "api.commandcode.ai/alpha/billing/subscriptions": {200, `{}`}},
	} {
		if _, err := routedReader(t, "commandcode", &routedClient{routes: routes}).Usage(context.Background(), quota.Credential{AccessToken: "k"}); err == nil {
			t.Errorf("%s was read as a healthy account", name)
		}
	}
}

// MiniMax (usage/minimax.js:133-234; registry/minimax.js:35-40,
// minimax-cn.js:35-40): each region reads its own pair of endpoints in order.
// International starts with the token plan, whose count is the USED count; the
// coding-plan endpoints (both China ones) count what REMAINS. A 404 moves on; a
// base_resp auth status is an error rather than an empty quota.
func TestMiniMaxUsageEndpointsRegionsAndCountSemantics(t *testing.T) {
	token := `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-M*",
		"current_interval_total_count":100,"current_interval_usage_count":40,"remains_time":3600000,
		"current_weekly_total_count":1000,"current_weekly_usage_count":250}]}`
	for _, tc := range []struct {
		id, route       string
		session, weekly float64
	}{
		{"minimax", "www.minimax.io/v1/token_plan/remains", 0.6, 0.75},
		{"minimax-cn", "www.minimaxi.com/v1/api/openplatform/coding_plan/remains", 0.4, 0.25},
	} {
		t.Run(tc.id+" first endpoint", func(t *testing.T) {
			client := &routedClient{routes: map[string]routedReply{tc.route: {200, token}}}
			state, err := routedReader(t, tc.id, client).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
			if err != nil {
				t.Fatal(err)
			}
			windows := byName(state)
			wantRemaining(t, windows, "M-series (5h)", tc.session)
			wantRemaining(t, windows, "M-series (7d)", tc.weekly)
			if got := windows["M-series (5h)"].ResetAt; got == nil || !got.Equal(time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)) {
				t.Fatalf("reset %v, want now + remains_time", got)
			}
			if len(client.requests) != 1 || client.requests[0].Header.Get("Authorization") != "Bearer fixture-key" {
				t.Fatalf("requests %+v", client.requests)
			}
		})
	}
	t.Run("coding plan fallback counts remaining", func(t *testing.T) {
		client := &routedClient{routes: map[string]routedReply{
			"www.minimax.io/v1/token_plan/remains":                   {404, `{}`},
			"api.minimax.io/v1/api/openplatform/coding_plan/remains": {200, `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-M2","current_interval_total_count":100,"current_interval_usage_count":40}]}`},
		}}
		state, err := routedReader(t, "minimax", client).Usage(context.Background(), quota.Credential{AccessToken: "k"})
		if err != nil {
			t.Fatal(err)
		}
		wantRemaining(t, byName(state), "MiniMax M2 (5h)", 0.4)
	})
	t.Run("upstream error status is an error", func(t *testing.T) {
		client := &routedClient{routes: map[string]routedReply{"www.minimax.io/v1/token_plan/remains": {200, `{"base_resp":{"status_code":1004,"status_msg":"inactive"},"model_remains":[]}`}}}
		if _, err := routedReader(t, "minimax", client).Usage(context.Background(), quota.Credential{AccessToken: "k"}); err == nil {
			t.Fatal("a base_resp error produced a quota")
		}
	})
}

// The two regional billing readers share one envelope but not one host
// (usage/codebuddy-cn.js:46-145): the International credential must never be
// sent to the China host, nor the reverse.
func TestRegionalReadersUseTheirOwnHost(t *testing.T) {
	for _, tc := range []struct {
		id, host, body string
	}{
		{"glm", "api.z.ai", `{"data":{"level":"max","limits":[{"type":"TOKENS_LIMIT","percentage":20,"unit":3,"number":5}]}}`},
		{"glm-cn", "open.bigmodel.cn", `{"data":{"level":"max","limits":[{"type":"TOKENS_LIMIT","percentage":20,"unit":3,"number":5}]}}`},
		{"codebuddy-cn", "copilot.tencent.com", codebuddyEnvelope},
		{"codebuddy-intl", "www.codebuddy.ai", codebuddyEnvelope},
	} {
		t.Run(tc.id, func(t *testing.T) {
			stub := &stubClient{status: http.StatusOK, body: tc.body}
			state, err := reader(t, tc.id, stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
			if err != nil || len(state.Windows) == 0 {
				t.Fatalf("state %+v err %v", state, err)
			}
			if stub.request.URL.Host != tc.host {
				t.Fatalf("%s read %s, want host %s", tc.id, stub.request.URL, tc.host)
			}
			if strings.HasPrefix(tc.id, "codebuddy") {
				wantRemaining(t, byName(state), "Monthly", 0.9)
			} else {
				wantRemaining(t, byName(state), "Session (5h)", 0.8)
			}
		})
	}
}

const codebuddyEnvelope = `{"code":0,"data":{"Response":{"Data":{"Accounts":[{
	"PackageName":"Pro","CycleStartTime":"2026-10-01 00:00:00","CycleEndTime":"2026-10-31T00:00:00Z","DeductionEndTime":1796000000000,
	"CycleCapacitySizePrecise":1000,"CycleCapacityUsedPrecise":100}]}}}}`

// open-sse/services/usage/misc.js:226-290: a Qoder account has a user pool and
// an organization pool with one absolute expiry (ms). A pool spent in full has
// nothing left; both carry the one reset.
func TestQoderUsageUserAndOrganizationPools(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"userQuota":{"total":100,"used":40,"remaining":60,"unit":"credits"},
		"orgResourcePackage":{"total":1000,"used":1000,"remaining":0,"unit":"credits"},
		"totalUsagePercentage":40,"isQuotaExceeded":false,"expiresAt":1793000000000}`}
	state, err := reader(t, "qoder", stub).Usage(context.Background(), quota.Credential{AccessToken: "dt-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	windows := byName(state)
	wantRemaining(t, windows, "user", 0.6)
	wantRemaining(t, windows, "organization", 0)
	if windows["user"].DisplayName != "Personal" || windows["user"].Count.Unit != "credits" || *windows["user"].Count.Remaining != 60 {
		t.Fatalf("user pool %+v %+v", windows["user"], windows["user"].Count)
	}
	reset := time.UnixMilli(1793000000000).UTC()
	for name, window := range windows {
		if window.ResetAt == nil || !window.ResetAt.Equal(reset) {
			t.Errorf("%s reset = %v, want the one expiry %v", name, window.ResetAt, reset)
		}
	}
	if stub.request.URL.String() != "https://openapi.qoder.sh/api/v2/quota/usage" || stub.request.Header.Get("Authorization") != "Bearer dt-fixture" {
		t.Fatalf("request %s %v", stub.request.URL, stub.request.Header)
	}
	if _, err := reader(t, "qoder", &stubClient{status: http.StatusInternalServerError, body: `{}`}).Usage(context.Background(), quota.Credential{AccessToken: "dt-fixture"}); err == nil {
		t.Error("a 500 was read as a quota")
	}
}
