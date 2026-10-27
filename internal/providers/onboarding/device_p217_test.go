package onboarding

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pollAnswer answers every request with one status and body.
type pollAnswer struct {
	status int
	body   string
}

func (p pollAnswer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: p.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(p.body))}, nil
}

// TestDeviceErrorOutcomeFollowsRFC8628 pins the mapping every RFC 8628 device
// method shares (#217): pending and slow_down keep the attempt, expired_token and
// access_denied are their own terminal verdicts (OAuthModal.js:180-186), and any
// other named error is a provider failure.
func TestDeviceErrorOutcomeFollowsRFC8628(t *testing.T) {
	cases := map[string]PollResult{
		"authorization_pending": {Pending: true},
		"slow_down":             {Pending: true, SlowDown: true},
		"expired_token":         {Expired: true},
		"access_denied":         {Denied: true},
		"invalid_grant":         {},
	}
	for code, want := range cases {
		got, ok := deviceErrorOutcome("fixture", code)
		if !ok || got.Pending != want.Pending || got.SlowDown != want.SlowDown || got.Expired != want.Expired || got.Denied != want.Denied {
			t.Fatalf("%s: %+v", code, got)
		}
		if terminal := !got.Pending; terminal != (got.Err != nil) {
			t.Fatalf("%s: terminal=%v err=%v", code, terminal, got.Err)
		}
	}
	if _, ok := deviceErrorOutcome("fixture", ""); ok {
		t.Fatal("no error named is not an outcome")
	}
}

// TestDevicePollsTreatUnavailableAsRetryable: a 408, 429 or 5xx poll answer is a
// retryable error for every device method, even with a JSON body naming an
// error, so a provider outage never burns the operator's attempt.
func TestDevicePollsTreatUnavailableAsRetryable(t *testing.T) {
	meta := map[string]map[string]string{
		"kiro":  {"clientId": "fixture-client", "clientSecret": "fixture-secret", "region": "us-east-1"},
		"qoder": {"nonce": "fixture-nonce", "machineId": "fixture-machine"},
		"kimi":  {"deviceId": "fixture-device-id"},
	}
	for _, provider := range []string{"github", "grok-cli", "kimi", "kiro", "kilocode", "codebuddy-cn", "codebuddy-intl"} {
		flow := flowFor(t, provider)
		for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			result, err := flow.Poll(context.Background(), Params{
				Input:    "fixture-device",
				Verifier: "fixture-verifier",
				Meta:     meta[provider],
				Client:   pollAnswer{status: status, body: `{"error":"server_error","code":500}`},
				Now:      func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) },
			})
			if !errors.Is(err, errPollTransient) || result.Done || result.Err != nil {
				t.Fatalf("%s %d: result=%+v err=%v", provider, status, result, err)
			}
		}
	}
}

// TestKiloApprovalNamesTheAccount: Kilo's approval carries the account's email,
// which the reference stores as the connection's (kilocode.js:47,55).
func TestKiloApprovalNamesTheAccount(t *testing.T) {
	flow := flowFor(t, "kilocode")
	if !flow.InfersIdentity || flow.AsksIdentityUpFront() {
		t.Fatal("kilocode should learn the account, not ask for it")
	}
	result, err := flow.Poll(context.Background(), Params{
		Input:  "KILO-1234",
		Client: pollAnswer{status: 200, body: `{"status":"approved","token":"fixture-token","userEmail":" kilo@example.test "}`},
	})
	if err != nil || !result.Done || result.Result.Identity != "kilo@example.test" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
