package codex

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestUsageNormalizesWindowsAndResetCredits(t *testing.T) {
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("ChatGPT-Account-ID") != "acct_1" || r.Header.Get("OpenAI-Beta") != "codex-1" {
			t.Errorf("headers=%v", r.Header)
		}
		return response(200, `{"plan_type":"plus","rate_limit":{"limit_reached":true,"primary_window":{"used_percent":42,"reset_at":"2026-06-18T00:25:18Z"},"secondary_window":{"used_percent":150}},"code_review_rate_limit":{"primary_window":{"percent_used":10}},"additional_rate_limits":[{"limit_name":"gpt-5.3-codex-spark","primary_window":{"used_percent":5}}],"rate_limit_reset_credits":{"available_count":2}}`), nil
	}}
	usage, err := (UsageClient{Client: client, AccountID: "acct_1"}).Usage(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Plan != "plus" || !usage.LimitReached || usage.ReviewLimitReached || usage.SparkLimitReached || usage.AvailableReset != 2 {
		t.Fatalf("usage=%+v", usage)
	}
	if usage.Quotas["session"].Used != 42 || usage.Quotas["session"].Remaining != 58 || usage.Quotas["session"].ResetAt == nil {
		t.Fatalf("session quota=%+v", usage.Quotas["session"])
	}
	if usage.Quotas["weekly"].Used != 100 || usage.Quotas["weekly"].Remaining != 0 {
		t.Fatalf("weekly clamp=%+v", usage.Quotas["weekly"])
	}
	if usage.Quotas["review_session"].Used != 10 || usage.Quotas["spark_session"].Used != 5 {
		t.Fatalf("quotas=%v", usage.Quotas)
	}
}

func TestUsageFailureIsUnavailableNotExhausted(t *testing.T) {
	client := fakeClient{do: func(*http.Request) (*http.Response, error) { return response(503, "nope"), nil }}
	if _, err := (UsageClient{Client: client}).Usage(context.Background(), "token"); err == nil || strings.Contains(err.Error(), "limit") {
		t.Fatalf("err=%v", err)
	}
	if _, err := (UsageClient{Client: client}).Usage(context.Background(), ""); err != ErrReauthRequired {
		t.Fatalf("missing token err=%v", err)
	}
}

func TestResetCreditsListingAndConsume(t *testing.T) {
	requests := 0
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Method == http.MethodPost {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			if !strings.Contains(string(body), "redeem_request_id") || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("consume request body=%s headers=%v", body, r.Header)
			}
			return response(200, `{"code":"reset","windows_reset":2,"message":"ok"}`), nil
		}
		return response(200, `{"available_count":2,"credits":[{"status":"available","granted_at":"2026-06-18T00:25:18Z","expires_at":null},{"status":"redeemed","granted_at":"bad-date"}]}`), nil
	}}
	listing, err := (UsageClient{Client: client}).ResetCredits(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if listing.AvailableCount != 2 || len(listing.Credits) != 2 || listing.Credits[0].ExpiresAt != nil || listing.Credits[0].GrantedAt == nil || listing.Credits[1].GrantedAt != nil {
		t.Fatalf("listing=%+v", listing)
	}
	result, err := (UsageClient{Client: client}).ConsumeResetCredit(context.Background(), "token", "redeem-1")
	if err != nil || !result.OK || result.NoCredit || result.WindowsReset != 2 || result.Code != "reset" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := (UsageClient{Client: client}).ConsumeResetCredit(context.Background(), "token", ""); err == nil {
		t.Fatal("accepted empty redeem request id")
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestResetCreditsSurfacesStructuredError(t *testing.T) {
	client := fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(403, `{"error":{"message":"Reset credits are unavailable for this account"}}`), nil
	}}
	_, err := (UsageClient{Client: client}).ResetCredits(context.Background(), "token")
	if err != ErrReauthRequired {
		t.Fatalf("err=%v", err)
	}
	unauthorized := fakeClient{do: func(*http.Request) (*http.Response, error) { return response(401, `{}`), nil }}
	if _, err := (UsageClient{Client: unauthorized}).Usage(context.Background(), "token"); err != ErrReauthRequired {
		t.Fatalf("usage 401 err=%v", err)
	}
	oversized := fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(200, strings.Repeat("x", maxResponseBytes+1)), nil
	}}
	if _, err := (UsageClient{Client: oversized}).ResetCredits(context.Background(), "token"); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized err=%v", err)
	}
}
