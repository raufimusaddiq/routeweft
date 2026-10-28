package usage

// Differential tests for the Command Code, Kenari, OpenCode Go, Ollama, Vercel
// AI Gateway and Xiaomi MiMo quota readers (#225) against
// testdata/billing_quota_pin.json, which compat/parity/generate-billing-quota.mjs
// records by evaluating the pin's own readers. Every case replays the upstream
// answers the pin saw and compares the requests and the normalized read model
// with the pin's (wantRegionalRequests, wantRegionalState). The differences
// Routeweft keeps on purpose are named where they are asserted (PARITY §6.31).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

type billingCase struct {
	regionalCase
	Now        *time.Time `json:"now"`
	Credential *struct {
		AccessToken *string `json:"accessToken"`
		Account     bool    `json:"account"`
		PassToken   string  `json:"passToken"`
	} `json:"credential"`
	Reads []struct {
		billingCase
		After int64 `json:"after"`
	} `json:"reads"`
}

type billingRecord struct {
	Pin         string                 `json:"pin"`
	Now         time.Time              `json:"now"`
	TZ          string                 `json:"tz"`
	CommandCode map[string]billingCase `json:"commandcode"`
	Kenari      map[string]billingCase `json:"kenari"`
	OpenCodeGo  map[string]billingCase `json:"opencode-go"`
	Ollama      map[string]billingCase `json:"ollama"`
	Vercel      map[string]billingCase `json:"vercel-ai-gateway"`
	Mimo        map[string]billingCase `json:"xiaomi-mimo"`
}

// The synthetic MiMo account the generator used.
var billingAccount = map[string]string{"mimoPassToken": "fixture-pass-token", "mimoUserId": "fixture-user", "mimoCUserId": "fixture-cuser"}

func loadBillingPin(t *testing.T) billingRecord {
	t.Helper()
	raw, err := os.ReadFile("testdata/billing_quota_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var record billingRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Pin != "1c3b9f47837d42ea64992063309064af755a14a3" || record.TZ != "UTC" {
		t.Fatalf("pin=%q tz=%q", record.Pin, record.TZ)
	}
	if bytes.Contains(raw, []byte(scopedToken)) {
		t.Fatal("the pin record carries the fixture key")
	}
	return record
}

func (c billingCase) now(record billingRecord) regionalRecord {
	if c.Now != nil {
		return regionalRecord{Now: *c.Now}
	}
	return regionalRecord{Now: record.Now}
}

func runBilling(t *testing.T, record billingRecord, provider string, cases map[string]billingCase, expect regionalExpect) {
	t.Helper()
	for _, name := range names(cases) {
		t.Run(name, func(t *testing.T) {
			test := cases[name]
			runRegional(t, test.now(record), provider, test.regionalCase, quota.Credential{AccessToken: scopedToken, APIKey: true}, expect, name)
		})
	}
}

func near(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 1e-9
}

func pinAmount(t *testing.T, raw json.RawMessage) float64 {
	t.Helper()
	value, ok := shared.IsNumber(raw)
	if !ok {
		t.Fatalf("pin amount %s", raw)
	}
	return value
}

// commandCodeAmounts: every window keeps the pin's used, total and remaining.
// A positive total is a bounded window; a zero cap with usage has no fraction
// and is not unlimited; the credits without a known plan cap are unlimited.
func commandCodeAmounts(t *testing.T, name string, window quota.Window, pin regionalQuota) {
	used, total, left := pinAmount(t, pin.Used), pinAmount(t, pin.Total), pinAmount(t, pin.Remaining)
	if window.Count == nil || !near(window.Count.Used, used) || !near(window.Count.Total, total) || !near(window.Count.Remaining, left) || window.Count.Currency != "" {
		t.Fatalf("%s count=%+v, pin %v/%v/%v", name, window.Count, used, total, left)
	}
	switch {
	case window.Unlimited || total <= 0:
		if window.Remaining != nil {
			t.Fatalf("%s has a fraction without a bound", name)
		}
	case !near(window.Remaining, math.Max(0, math.Min(1, (total-used)/total))):
		t.Fatalf("%s remaining=%v, pin %v of %v used", name, window.Remaining, used, total)
	}
}

func TestCommandCodeQuotaIsThePins(t *testing.T) {
	runBilling(t, loadBillingPin(t), "commandcode", loadBillingPin(t).CommandCode, regionalExpect{
		statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "multiple-window": quota.StatusAvailable,
			"empty": quota.StatusAvailable, "window-shapes": quota.StatusAvailable},
		window: commandCodeAmounts,
	})
}

// kenariAmounts: the rupiah amounts stay in the count; remaining is what the
// total leaves after the used amount.
func kenariAmounts(t *testing.T, name string, window quota.Window, pin regionalQuota) {
	if window.Count == nil || window.Count.Currency != "IDR" || window.Remaining == nil {
		t.Fatalf("%s=%+v count=%+v", name, window, window.Count)
	}
	used, total := pinAmount(t, pin.Used), pinAmount(t, pin.Total)
	if !near(window.Count.Used, used) || !near(window.Count.Total, total) || !near(window.Count.Remaining, total-used) {
		t.Fatalf("%s count=%+v, pin %v of %v", name, window.Count, used, total)
	}
}

func TestKenariQuotaIsThePins(t *testing.T) {
	record := loadBillingPin(t)
	noPlan := "Kenari connected. No plan data returned."
	runBilling(t, record, "kenari", record.Kenari, regionalExpect{
		notices: map[string]string{"no-plan": noPlan, "plan-empty-string": noPlan, "invalid-json": noPlan,
			"empty": "Kenari connected. No quota windows were returned.", "no-windows": "Kenari connected. No quota windows were returned."},
		// A missing amount is 0, so amount-missing is 0% and exhausted, as in
		// the pin (kenari.js:39-44).
		statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "zero-capacity": quota.StatusExhausted, "expired": quota.StatusAvailable,
			"multiple-window": quota.StatusAvailable, "amount-missing": quota.StatusExhausted},
		window: kenariAmounts,
	})
}

// percentOnly: a percentage reader reports no absolute amounts.
func percentOnly(t *testing.T, name string, window quota.Window, _ regionalQuota) {
	if window.Count != nil || window.Remaining == nil {
		t.Fatalf("%s=%+v", name, window)
	}
}

func TestOpenCodeGoQuotaIsThePins(t *testing.T) {
	record := loadBillingPin(t)
	noData, noValid := "OpenCode Go usage response did not contain quota data.", "OpenCode Go usage response did not contain valid quota data."
	runBilling(t, record, "opencode-go", record.OpenCodeGo, regionalExpect{
		notices: map[string]string{"no-usage": noData, "usage-string": noData, "invalid-json": noData, "empty": noValid, "usage-array": noValid,
			"percent-shapes": noValid, "auth403-entitlement": "OpenCode Go subscription required for this API key."},
		statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "multiple-window": quota.StatusAvailable},
		window:   percentOnly,
	})
}

func TestOllamaQuotaIsThePins(t *testing.T) {
	record := loadBillingPin(t)
	none := "Ollama Cloud connected. No usage limits reported."
	runBilling(t, record, "ollama", record.Ollama, regionalExpect{
		notices:  map[string]string{"empty": none, "no-limits": none, "body-null": none, "limit-shapes": none},
		statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusExhausted, "multiple-window": quota.StatusAvailable},
		window:   percentOnly,
	})
}

// vercelDollars: "Used (USD)" keeps the amount spent, unbounded;
// "Remaining (USD)" keeps the balance against the $5 allocation. The pin also
// sets the balance as that row's `used`, which is not a spent amount and is
// not copied.
func vercelDollars(t *testing.T, name string, window quota.Window, pin regionalQuota) {
	count := window.Count
	if count == nil || count.Currency != "USD" {
		t.Fatalf("%s count=%+v", name, count)
	}
	switch name {
	case "Used (USD)":
		if !near(count.Used, pinAmount(t, pin.Used)) || count.Total != nil || count.Remaining != nil || window.Remaining != nil {
			t.Fatalf("%s=%+v count=%+v", name, window, count)
		}
	case "Remaining (USD)":
		if count.Used != nil || !near(count.Total, pinAmount(t, pin.Total)) || !near(count.Remaining, pinAmount(t, pin.Remaining)) {
			t.Fatalf("%s count=%+v", name, count)
		}
	default:
		t.Fatalf("window %s", name)
	}
}

func TestVercelQuotaIsThePins(t *testing.T) {
	record := loadBillingPin(t)
	byok := "Vercel AI Gateway connected. No credit allocation found (BYOK or unfunded account)."
	runBilling(t, record, "vercel-ai-gateway", record.Vercel, regionalExpect{
		notices:  map[string]string{"empty": byok, "no-fields": byok, "body-null": byok},
		statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusExhausted, "multiple-window": quota.StatusAvailable},
		window:   vercelDollars,
	})
}

var mimoExpect = regionalExpect{
	notices: map[string]string{"key/empty": "Usage data missing percent field.", "key/percent-string": "Usage data missing percent field.",
		"key/auth401": mimoSessionRequired},
	statuses: map[string]quota.Status{"key/zero": quota.StatusExhausted, "key/expired": quota.StatusAvailable, "account/zero": quota.StatusExhausted,
		"account/expired": quota.StatusAvailable, "key/multiple-window": quota.StatusAvailable},
	window: percentOnly,
}

func (c billingCase) mimoCredential() quota.Credential {
	var credential quota.Credential
	if c.Credential.AccessToken != nil {
		credential.AccessToken = strings.Replace(*c.Credential.AccessToken, "<token>", scopedToken, 1)
	}
	if c.Credential.Account {
		credential.ProviderData = map[string]string{}
		for key, value := range billingAccount {
			credential.ProviderData[key] = value
		}
		if c.Credential.PassToken != "" {
			credential.ProviderData["mimoPassToken"] = c.Credential.PassToken
		}
	}
	return credential
}

func TestMimoQuotaIsThePins(t *testing.T) {
	record := loadBillingPin(t)
	for _, name := range names(record.Mimo) {
		test := record.Mimo[name]
		if test.Reads != nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			if name == "account/sso-no-service-token" {
				// The sts hop set no service cookie. The pin goes on with the
				// passport-level serviceToken from the earlier hop and sends it
				// to the usage endpoint; Routeweft refuses that token there and
				// reads with the key instead (PARITY "MiMo account quota").
				stub := &replay{steps: test.Upstream}
				readers, _ := Readers(stub, func() time.Time { return record.Now })
				state, err := readers["xiaomi-mimo"].Usage(context.Background(), test.mimoCredential())
				noSecret(t, state, err)
				wantRegionalRequests(t, stub.requests[:5], test.Requests[:5], nil)
				if last := stub.requests[5]; err != nil || last.URL.Host != "aistudio.xiaomimimo.com" || last.Header.Get("Cookie") != "" {
					t.Fatalf("err=%v last=%s", err, last.URL)
				}
				wantRegionalState(t, record.Now, state, err, test.regionalCase, mimoExpect, name)
				return
			}
			runRegional(t, regionalRecord{Now: record.Now}, "xiaomi-mimo", test.regionalCase, test.mimoCredential(), mimoExpect, name)
		})
	}
}

// TestMimoSessionIsKeptLikeThePins replays reads in sequence on one reader:
// the account session is kept for 30 minutes per passToken, a failed
// handshake is not kept, and a failed usage read does not drop the session
// (mimoAccount.js:29-34,181-218).
func TestMimoSessionIsKeptLikeThePins(t *testing.T) {
	record := loadBillingPin(t)
	for _, name := range names(record.Mimo) {
		test := record.Mimo[name]
		if test.Reads == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			now := record.Now
			stub := &replay{steps: test.Upstream}
			readers, _ := Readers(stub, func() time.Time { return now })
			for i, read := range test.Reads {
				now = record.Now.Add(time.Duration(read.After) * time.Millisecond)
				before := len(stub.requests)
				state, err := readers["xiaomi-mimo"].Usage(context.Background(), read.mimoCredential())
				noSecret(t, state, err)
				wantRegionalRequests(t, stub.requests[before:], read.Requests, nil)
				wantRegionalState(t, now, state, err, read.regionalCase, mimoExpect, name)
				if i > 0 && len(read.Requests) == 1 && strings.Contains(read.Requests[0].Headers["Cookie"], "passToken") {
					t.Fatal("a kept session sent the passToken")
				}
			}
		})
	}
}

// TestBillingFixturesCoverEveryOutcome is the acceptance list for #225: every
// reader has zero, empty, expired, multiple-window, timeout, auth and error
// answers, and MiMo has them on both its key and its account path.
func TestBillingFixturesCoverEveryOutcome(t *testing.T) {
	record := loadBillingPin(t)
	outcomes := []string{"zero", "empty", "expired", "multiple-window", "timeout", "auth401", "error500"}
	readers := map[string]map[string]billingCase{"commandcode": record.CommandCode, "kenari": record.Kenari, "opencode-go": record.OpenCodeGo,
		"ollama": record.Ollama, "vercel-ai-gateway": record.Vercel}
	for reader, cases := range readers {
		for _, outcome := range outcomes {
			if _, ok := cases[outcome]; !ok {
				t.Errorf("%s has no %s answer", reader, outcome)
			}
		}
	}
	for _, path := range []string{"key/", "account/"} {
		for _, outcome := range outcomes {
			if _, ok := record.Mimo[path+outcome]; !ok {
				t.Errorf("xiaomi-mimo has no %s%s answer", path, outcome)
			}
		}
	}
	if len(record.Mimo["session-reused"].Reads) != 2 {
		t.Error("xiaomi-mimo has no kept-session answer")
	}
}

// TestBillingNoticesAreNotQuota: an answer without quota (no plan, no
// windows, the missing subscription, BYOK, the key that cannot read MiMo's
// quota) is a notice with status unknown, never exhausted, available or
// unlimited, and carries no window.
func TestBillingNoticesAreNotQuota(t *testing.T) {
	record := loadBillingPin(t)
	for _, test := range []struct {
		provider, name string
		test           billingCase
		credential     quota.Credential
	}{
		{"kenari", "no-plan", record.Kenari["no-plan"], quota.Credential{AccessToken: scopedToken}},
		{"opencode-go", "auth403-entitlement", record.OpenCodeGo["auth403-entitlement"], quota.Credential{AccessToken: scopedToken}},
		{"ollama", "empty", record.Ollama["empty"], quota.Credential{AccessToken: scopedToken}},
		{"vercel-ai-gateway", "empty", record.Vercel["empty"], quota.Credential{AccessToken: scopedToken}},
		{"xiaomi-mimo", "key/auth401", record.Mimo["key/auth401"], record.Mimo["key/auth401"].mimoCredential()},
	} {
		readers, _ := Readers(&replay{steps: test.test.Upstream}, func() time.Time { return record.Now })
		state, err := readers[test.provider].Usage(context.Background(), test.credential)
		encoded, _ := json.Marshal(state)
		if err != nil || state.Message == "" || len(state.Windows) != 0 || state.DeriveStatus(record.Now) != quota.StatusUnknown || bytes.Contains(encoded, []byte("unlimited\":true")) {
			t.Errorf("%s %s: %s, %v", test.provider, test.name, encoded, err)
		}
	}
}

// TestMimoConcurrentReadsShareOneHandshake: reads of one account that arrive
// while its handshake runs wait for it rather than each running the SSO chain
// (mimoAccount.js:195-212). Run with -race.
func TestMimoConcurrentReadsShareOneHandshake(t *testing.T) {
	record := loadBillingPin(t)
	steps := record.Mimo["account/zero"].Upstream
	var mu sync.Mutex
	handshakes, usages := 0, 0
	release := make(chan struct{})
	client := mimoClientFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		step := steps[0]
		switch {
		case request.URL.Path == "/api/user/usage":
			usages++
			step = steps[5]
		case request.URL.Path == "/api/user/xiaomi/me":
			handshakes++
		default:
			for i, sent := range record.Mimo["account/zero"].Requests {
				if sent.URL == request.URL.String() {
					step = steps[i]
				}
			}
		}
		mu.Unlock()
		if request.URL.Path == "/api/user/xiaomi/me" {
			<-release
		}
		return (&replay{steps: []pinStep{step}}).Do(request)
	})
	readers, _ := Readers(client, func() time.Time { return record.Now })
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{ProviderData: billingAccount})
			if err == nil && len(state.Windows) != 1 {
				err = fmt.Errorf("windows=%v", state.Windows)
			}
			results <- err
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if handshakes != 1 || usages != 8 {
		t.Fatalf("handshakes=%d usages=%d", handshakes, usages)
	}
}

// TestBillingSharedValueSemantics pins the JavaScript readings the #225
// readers rely on.
func TestBillingSharedValueSemantics(t *testing.T) {
	var keys []string
	for _, entry := range shared.Entries(json.RawMessage(`{"b":1,"10":2,"a":3,"2":4,"02":5,"4294967295":6,"b":7}`)) {
		keys = append(keys, entry.Key+"="+string(entry.Value))
	}
	if strings.Join(keys, " ") != "2=4 10=2 b=7 a=3 02=5 4294967295=6" {
		t.Fatalf("entries %v", keys)
	}
	if shared.Entries(json.RawMessage(`[1]`)) != nil || shared.Entries(json.RawMessage(`null`)) != nil {
		t.Fatal("only an object has entries here")
	}
	for raw, want := range map[string]string{`1792540800000`: "2026-10-21T00:00:00Z", `"2026-10-21T00:00:00Z"`: "2026-10-21T00:00:00Z",
		`"1792540800"`: "", `0`: "", `""`: "", `9e15`: "", `true`: ""} {
		got := ""
		if at := shared.Date(json.RawMessage(raw)); at != nil {
			got = at.Format(time.RFC3339)
		}
		if got != want {
			t.Errorf("Date(%s)=%q, want %q", raw, got, want)
		}
	}
	for raw, want := range map[string]float64{`"0x10"`: 16, `" 2 "`: 2, `"1e3"`: 1000, `"\ufeff3"`: 3} {
		if got, ok := shared.FiniteNumber(json.RawMessage(raw)); !ok || got != want {
			t.Errorf("FiniteNumber(%s)=%v,%v", raw, got, ok)
		}
	}
	for _, raw := range []string{`"1_000"`, `"Infinity"`, `"  "`, `true`, `null`} {
		if _, ok := shared.FiniteNumber(json.RawMessage(raw)); ok {
			t.Errorf("FiniteNumber(%s) is a number", raw)
		}
	}
	if got := searchParam("orgId", "a b&c=d/é~*+"); got != "orgId=a+b%26c%3Dd%2F%C3%A9%7E*%2B" {
		t.Fatalf("searchParam %s", got)
	}
	if got := encodeURIComponent("https://x/a?b=c&d=e f!'()*~"); got != "https%3A%2F%2Fx%2Fa%3Fb%3Dc%26d%3De%20f!'()*~" {
		t.Fatalf("encodeURIComponent %s", got)
	}
}
