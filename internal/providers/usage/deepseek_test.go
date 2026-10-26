package usage

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// deepseekFixture loads testdata/deepseek.json, the oracle's ACTIVE_BALANCE.
func deepseekFixture(t *testing.T) *stubClient {
	t.Helper()
	raw, err := os.ReadFile("testdata/deepseek.json")
	if err != nil {
		t.Fatal(err)
	}
	return &stubClient{status: http.StatusOK, body: string(raw)}
}

func deepseekUsage(t *testing.T, stub *stubClient) (quota.State, error) {
	t.Helper()
	return reader(t, "deepseek", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
}

// credit returns the balance window for one currency, failing when it is
// absent, unlimited or carries a remaining fraction that routing would read.
func credit(t *testing.T, state quota.State, currency string) quota.Credit {
	t.Helper()
	for _, w := range state.Windows {
		if w.Name != "Balance ("+currency+")" {
			continue
		}
		if w.Unlimited || w.Remaining != nil || w.Credit == nil {
			t.Fatalf("%s window=%+v: a balance is never unlimited or a fraction", currency, w)
		}
		if w.Credit.Currency != currency {
			t.Fatalf("%s window currency=%q", currency, w.Credit.Currency)
		}
		return *w.Credit
	}
	t.Fatalf("no %s balance in %+v", currency, state.Windows)
	return quota.Credit{}
}

func wantAmount(t *testing.T, label string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s=%v want %v", label, got, want)
	}
}

// TestDeepSeekPinnedBalanceKeepsCurrenciesApart is the #212 regression on the
// pin's fixture: zero CNY beside positive USD stays two currencies, the zero is
// a zero (not unlimited), and it does not exhaust the funded account.
func TestDeepSeekPinnedBalanceKeepsCurrenciesApart(t *testing.T) {
	stub := deepseekFixture(t)
	state, err := deepseekUsage(t, stub)
	if err != nil {
		t.Fatal(err)
	}
	if stub.request.Method != http.MethodGet || stub.request.URL.String() != "https://api.deepseek.com/user/balance" {
		t.Fatalf("request=%s %s", stub.request.Method, stub.request.URL)
	}
	if got := stub.request.Header.Get("Authorization"); got != "Bearer fixture-key" {
		t.Fatalf("authorization=%q", got)
	}
	if state.Plan != "DeepSeek" || state.CreditAvailable == nil || !*state.CreditAvailable || len(state.Windows) != 2 {
		t.Fatalf("state=%+v", state)
	}
	usd := credit(t, state, "USD")
	if usd.State != quota.CreditPositive {
		t.Fatalf("usd=%+v", usd)
	}
	wantAmount(t, "usd total", usd.Total, 12.5)
	wantAmount(t, "usd granted", usd.Granted, 2.5)
	wantAmount(t, "usd topped up", usd.ToppedUp, 10)
	cny := credit(t, state, "CNY")
	if cny.State != quota.CreditZero {
		t.Fatalf("cny=%+v", cny)
	}
	wantAmount(t, "cny total", cny.Total, 0)
	if got := state.DeriveStatus(groqNow); got != quota.StatusAvailable {
		t.Fatalf("status=%s: zero CNY must not exhaust an account with USD credit", got)
	}
}

// TestDeepSeekBothZeroIsZeroNotUnlimited covers the pin's insufficient-balance
// case (deepseek-usage.test.js:95-117): every currency at zero is reported as
// zero per currency, with the provider's unavailability flag kept explicit.
func TestDeepSeekBothZeroIsZeroNotUnlimited(t *testing.T) {
	state, err := deepseekUsage(t, &stubClient{status: http.StatusOK, body: `{"is_available":false,"balance_infos":[
		{"currency":"USD","total_balance":"0","granted_balance":"0","topped_up_balance":"0"},
		{"currency":"cny","total_balance":0,"granted_balance":0,"topped_up_balance":0}]}`})
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan != "DeepSeek (Insufficient Balance)" || state.CreditAvailable == nil || *state.CreditAvailable {
		t.Fatalf("state=%+v", state)
	}
	for _, currency := range []string{"USD", "CNY"} {
		c := credit(t, state, currency)
		if c.State != quota.CreditZero {
			t.Fatalf("%s=%+v", currency, c)
		}
		wantAmount(t, currency+" total", c.Total, 0)
	}
}

// TestDeepSeekUnobservedAmountsStayDistinct keeps missing, malformed and
// negative balances apart from zero, and an unreported availability flag apart
// from both available and unavailable. The pin folds all three amounts to 0
// through toFiniteNumber and Math.max (deepseek.js:19-22,93).
func TestDeepSeekUnobservedAmountsStayDistinct(t *testing.T) {
	state, err := deepseekUsage(t, &stubClient{status: http.StatusOK, body: `{"balance_infos":[
		{"currency":"USD"},
		{"currency":"CNY","total_balance":"12abc"},
		{"currency":"EUR","total_balance":"-1.50"},
		{"currency":"GBP","total_balance":"NaN"},
		{"currency":"JPY","totalBalance":"300","grantedBalance":"oops"},
		{"total_balance":"5"},
		"not an object",
		null]}`})
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan != "DeepSeek (Availability Unknown)" || state.CreditAvailable != nil {
		t.Fatalf("unreported is_available must stay unknown: %+v", state)
	}
	if len(state.Windows) != 5 {
		t.Fatalf("entries without a currency must be skipped: %+v", state.Windows)
	}
	for currency, want := range map[string]quota.CreditState{
		"USD": quota.CreditMissing, "CNY": quota.CreditMalformed, "GBP": quota.CreditMalformed,
	} {
		if c := credit(t, state, currency); c.State != want || c.Total != nil {
			t.Fatalf("%s=%+v want %s with no amount", currency, c, want)
		}
	}
	eur := credit(t, state, "EUR")
	if eur.State != quota.CreditNegative {
		t.Fatalf("eur=%+v", eur)
	}
	wantAmount(t, "eur total", eur.Total, -1.5)
	jpy := credit(t, state, "JPY")
	if jpy.State != quota.CreditPositive || jpy.Granted != nil {
		t.Fatalf("jpy=%+v", jpy)
	}
	wantAmount(t, "jpy total", jpy.Total, 300)
	if got := state.DeriveStatus(groqNow); got == quota.StatusExhausted {
		t.Fatal("unobserved balances must not exhaust the account")
	}
}

// TestDeepSeekRepeatedCurrencyReplaces follows the pin's keyed quotas object.
func TestDeepSeekRepeatedCurrencyReplaces(t *testing.T) {
	state, err := deepseekUsage(t, &stubClient{status: http.StatusOK, body: `{"is_available":true,"balance_infos":[
		{"currency":"USD","total_balance":"1"},{"currency":"usd","total_balance":"0"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 1 || credit(t, state, "USD").State != quota.CreditZero {
		t.Fatalf("windows=%+v", state.Windows)
	}
}

// TestDeepSeekUpstreamFailuresAreErrors keeps a failed call distinct from every
// balance outcome: it is an error observation, never an empty healthy plan.
func TestDeepSeekUpstreamFailuresAreErrors(t *testing.T) {
	for name, stub := range map[string]*stubClient{
		"unauthorized": {status: http.StatusUnauthorized, body: `{"error":"no"}`},
		"server":       {status: http.StatusInternalServerError, body: `oops`},
		"not json":     {status: http.StatusOK, body: `<html>`},
		"no balances":  {status: http.StatusOK, body: `{"is_available":true,"balance_infos":[]}`},
		"no currency":  {status: http.StatusOK, body: `{"is_available":true,"balance_infos":[{"total_balance":"5"}]}`},
	} {
		if state, err := deepseekUsage(t, stub); err == nil {
			t.Errorf("%s: state=%+v, want an error", name, state)
		}
	}
	observer := quota.Observer{Clients: map[string]quota.UsageClient{"deepseek": reader(t, "deepseek", &stubClient{status: http.StatusBadGateway})}}
	state, err := observer.Observe(context.Background(), "deepseek", "a", quota.Credential{AccessToken: "fixture-key"})
	if err == nil || state.Status != quota.StatusError || len(state.Windows) != 0 || state.CreditAvailable != nil {
		t.Fatalf("failed read=%+v err=%v", state, err)
	}
}
