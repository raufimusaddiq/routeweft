package claude

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOAuthUsageParsesWindowsAndCaches(t *testing.T) {
	calls := 0
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("anthropic-beta") != OAuthBeta || r.Header.Get("anthropic-version") != APIVersion || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("headers=%v", r.Header)
		}
		return response(200, `{
			"five_hour":{"utilization":87,"resets_at":"2026-06-18T00:25:18Z"},
			"seven_day":{"utilization":40},
			"seven_day_sonnet":{"utilization":10},
			"extra_usage":{"used":1},
			"limits":[{"kind":"weekly_scoped","percent":150,"scope":{"model":{"display_name":"Fable"}}}]
		}`), nil
	}}
	c := &UsageClient{Client: client, Now: func() time.Time { return time.Unix(0, 0) }}
	usage, err := c.Usage(context.Background(), "token", false)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Plan != "Claude Code" || usage.Quotas["session (5h)"].Used != 87 || usage.Quotas["session (5h)"].Remaining != 13 || usage.Quotas["session (5h)"].ResetAt == nil {
		t.Fatalf("usage=%+v", usage)
	}
	if usage.Quotas["weekly (7d)"].Used != 40 || usage.Quotas["weekly sonnet (7d)"].Used != 10 {
		t.Fatalf("quotas=%v", usage.Quotas)
	}
	if usage.Quotas["weekly fable (7d)"].Used != 100 {
		t.Fatalf("clamped=%v", usage.Quotas["weekly fable (7d)"])
	}
	if len(usage.ExtraUsage) == 0 {
		t.Fatal("missing extra usage")
	}
	if _, err := c.Usage(context.Background(), "token", false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("cache not used: calls=%d", calls)
	}
}

func TestOAuthUsage429CoolsDownThenFallsBackToLegacy(t *testing.T) {
	now := time.Unix(0, 0)
	var paths []string
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/oauth/usage":
			return response(429, `{}`), nil
		case "/v1/settings":
			return response(200, `{"organization_id":"org_1","organization_name":"Acme","plan":"team"}`), nil
		case "/v1/organizations/org_1/usage":
			return response(200, `{"usage":42}`), nil
		}
		t.Errorf("unexpected path %s", r.URL.Path)
		return response(404, `{}`), nil
	}}
	c := &UsageClient{Client: client, Now: func() time.Time { return now }}
	legacy, err := c.Usage(context.Background(), "token", true)
	if err != nil || legacy.Plan != "team" || legacy.Organization != "Acme" || len(legacy.Legacy) == 0 {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
	paths = nil
	if _, err := c.Usage(context.Background(), "token", true); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/settings" {
		t.Fatalf("cooldown ignored: paths=%v", paths)
	}
	now = now.Add(oauthCooldown + time.Second)
	if _, err := c.Usage(context.Background(), "token", true); err != nil {
		t.Fatal(err)
	}
	if len(paths) < 3 || paths[2] != "/api/oauth/usage" {
		t.Fatalf("cooldown did not expire: %v", paths)
	}
}

func TestLegacyWithoutOrganizationReportsAdminMessage(t *testing.T) {
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/oauth/usage" {
			return response(404, `{}`), nil
		}
		return response(200, `{"plan":"pro","organization_name":"Solo"}`), nil
	}}
	usage, err := (&UsageClient{Client: client}).Usage(context.Background(), "token", true)
	if err != nil || usage.Plan != "pro" || !strings.Contains(usage.Message, "admin access") {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

func TestUsageRejectsMissingTokenAndOversizedBody(t *testing.T) {
	if _, err := (&UsageClient{}).Usage(context.Background(), "", true); err == nil {
		t.Fatal("accepted empty token")
	}
	client := fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(200, strings.Repeat("x", maxUsageBytes+1)), nil
	}}
	if _, err := (&UsageClient{Client: client}).Usage(context.Background(), "token", true); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("err=%v", err)
	}
}
