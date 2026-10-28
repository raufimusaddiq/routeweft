package usage

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

func TestKenariPinnedZeroCapacity(t *testing.T) {
	for _, test := range []struct {
		provider, name string
		remaining      float64
	}{

		{"kenari", "Weekly (IDR)", 0}, {"kenari", "Monthly (IDR)", 0.75},
	} {
		t.Run(test.provider+test.name, func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + test.provider + ".json")
			if err != nil {
				t.Fatal(err)
			}
			state, err := reader(t, test.provider, &stubClient{status: 200, body: string(body)}).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			for _, window := range state.Windows {
				if window.Name != test.name {
					continue
				}
				if window.Unlimited || window.Remaining == nil || *window.Remaining != test.remaining {
					t.Fatalf("window=%+v", window)
				}
				if test.provider == "kenari" && window.ResetAt == nil {
					t.Fatal("lost reset")
				}
				return
			}
			t.Fatal("missing window")
		})
	}
}

func TestGrokPinnedRESTCredits(t *testing.T) {
	body, err := os.ReadFile("testdata/grok-cli.json")
	if err != nil {
		t.Fatal(err)
	}
	requests := []string{}
	client := pingUsageClient(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		payload := string(body)
		if r.URL.Path == "/v1/user" {
			payload = `{"subscriptionTier":null,"hasGrokCodeAccess":true}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	readers, err := Readers(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := readers["grok-cli"].Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	// The prepaid balance is shown full with no reset, as the pin shows it (grok-cli.js:214-225).
	if len(state.Windows) != 2 || state.Windows[0].Name != "On-demand" || *state.Windows[0].Remaining != .65 || state.Windows[0].ResetAt == nil ||
		state.Windows[1].Name != "Prepaid" || *state.Windows[1].Remaining != 1 || state.Windows[1].ResetAt != nil || state.Plan != "Grok Code" {
		t.Fatalf("state=%+v", state)
	}
	if strings.Join(requests, ",") != "https://cli-chat-proxy.grok.com/v1/billing?format=credits,https://cli-chat-proxy.grok.com/v1/user?include=subscription" {
		t.Fatalf("requests=%v", requests)
	}
}

type pingUsageClient func(*http.Request) (*http.Response, error)

func (f pingUsageClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestGrokRESTPaidFreeAndMonthlyShapes(t *testing.T) {
	for _, test := range []struct {
		name, billing, profile string
		wantName               string
		remaining              float64
		// notice is the pin's answer when neither REST nor the gRPC weekly pool
		// (here not a frame) reports a window; it is never a fabricated bound.
		notice string
	}{
		{"monthly", `{"monthlyLimit":{"val":1000},"includedUsed":{"val":275},"totalUsed":{"val":300},"resetAt":"2026-08-01T00:00:00Z"}`, `{"subscription_tier":"premium_plus"}`, "Monthly included", .725, ""},
		{"weekly shared", `{"config":{"creditUsagePercent":99,"onDemandCap":{"val":0},"onDemandUsed":{"val":0},"productUsage":[{"usagePercent":97},{"usagePercent":2}]}}`, `{"subscriptionTier":"XPremiumPlus"}`, "Weekly SuperGrok", .01, ""},
		{"free zero", `{"config":{"onDemandCap":{"val":0},"onDemandUsed":{"val":0}}}`, `{"subscriptionTier":null}`, "On-demand", 0, ""},
		{"paid zero", `{"config":{"onDemandCap":{"val":0},"onDemandUsed":{"val":0}}}`, `{"subscriptionTier":"XPremiumPlus"}`, "", 0, "Subscription access is active; Grok does not expose a numeric included quota."},
		{"unknown", `{}`, `{}`, "", 0, "Grok Build connected, but no credit allotment was returned. Free promo may be exhausted."},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := pingUsageClient(func(r *http.Request) (*http.Response, error) {
				payload := test.billing
				if r.URL.Path == "/v1/user" {
					payload = test.profile
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))}, nil
			})
			readers, _ := Readers(client, nil)
			state, err := readers["grok-cli"].Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if test.notice != "" {
				if len(state.Windows) != 0 || state.Message != test.notice || state.DeriveStatus(time.Now()) != quota.StatusUnknown {
					t.Fatalf("fabricated bound: %+v", state)
				}
				return
			}
			if len(state.Windows) != 1 || state.Windows[0].Name != test.wantName || *state.Windows[0].Remaining != test.remaining || state.Windows[0].Unlimited {
				t.Fatalf("windows=%+v", state.Windows)
			}
		})
	}
}

// A window whose amounts are absent or null is unknown, not exhausted: an
// exhausted status skips the account in routing.
func TestKenariAbsentAmountsAreUnknownNotExhausted(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{"plan":{"name":"P","windows":{"week":{"resets_at":"2026-10-25T00:00:00Z"}}}}`,
		"null":   `{"plan":{"name":"P","windows":{"week":{"used_rp":null,"remaining_rp":null,"resets_at":"2026-10-25T00:00:00Z"}}}}`,
		"half":   `{"plan":{"name":"P","windows":{"week":{"used_rp":5,"resets_at":"2026-10-25T00:00:00Z"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			state, err := reader(t, "kenari", &stubClient{status: 200, body: body}).Usage(context.Background(), quota.Credential{AccessToken: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Windows) != 1 {
				t.Fatalf("windows=%+v", state.Windows)
			}
			if w := state.Windows[0]; w.Remaining != nil {
				t.Fatalf("absent amount read as bound %v", *w.Remaining)
			}
			if got := state.DeriveStatus(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)); got == quota.StatusExhausted {
				t.Fatalf("status=%s would skip the account", got)
			}
		})
	}
}
