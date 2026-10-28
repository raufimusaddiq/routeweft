package usage

// Differential tests for the Grok CLI, Kiro, Qoder and Zed quota readers (#224)
// against testdata/account_quota_pin.json, which
// compat/parity/generate-account-quota.mjs records by evaluating the pin's own
// readers and its usage page normalizer. Every case replays the upstream
// answers the pin saw and compares the requests and the read model with the
// pin's. The differences kept on purpose are named where they are asserted
// (PARITY §6.30).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

const accountPAT = "pt-fixture-pat"

type accountRequest struct {
	pinRequest
	Body json.RawMessage `json:"body"`
}

type pageRow struct {
	Name             string  `json:"name"`
	RemainingPercent float64 `json:"remainingPercent"`
}

type accountCase struct {
	Upstream     []pinStep         `json:"upstream"`
	Result       json.RawMessage   `json:"result"`
	Error        string            `json:"error"`
	Page         []pageRow         `json:"page"`
	Requests     []accountRequest  `json:"requests"`
	Token        string            `json:"token"`
	ProviderData map[string]string `json:"providerData"`
	Credential   string            `json:"credential"`
}

type accountRecord struct {
	Pin              string                            `json:"pin"`
	Now              time.Time                         `json:"now"`
	Grok             map[string]accountCase            `json:"grok"`
	Kiro             map[string]map[string]accountCase `json:"kiro"`
	KiroProviderData map[string]map[string]string      `json:"kiroProviderData"`
	Qoder            map[string]map[string]accountCase `json:"qoder"`
	Zed              map[string]map[string]accountCase `json:"zed"`
}

func loadAccountPin(t *testing.T) accountRecord {
	t.Helper()
	raw, err := os.ReadFile("testdata/account_quota_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var record accountRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Pin != "1c3b9f47837d42ea64992063309064af755a14a3" {
		t.Fatalf("pin=%q", record.Pin)
	}
	if bytes.Contains(raw, []byte(scopedToken)) || bytes.Contains(raw, []byte(accountPAT)) {
		t.Fatal("the pin record carries a fixture credential")
	}
	return record
}

type accountResult struct {
	Plan                 *string         `json:"plan"`
	Message              *string         `json:"message"`
	Quotas               json.RawMessage `json:"quotas"`
	IsQuotaExceeded      json.RawMessage `json:"isQuotaExceeded"`
	TotalUsagePercentage json.RawMessage `json:"totalUsagePercentage"`
	HasOverdueInvoices   json.RawMessage `json:"hasOverdueInvoices"`
	TrialStarted         json.RawMessage `json:"trialStarted"`
	PlanID               json.RawMessage `json:"planId"`
}

type accountQuota struct {
	pinQuota
	Unit json.RawMessage `json:"unit"`
}

// accountExpect is how one reader's pin answers map onto Routeweft.
type accountExpect struct {
	// notices maps a pin answer without quota to Routeweft's fixed notice.
	notices func(name string, pin accountResult) (string, bool)
	// failures names answers the pin reads but Routeweft reports as a failed
	// read, each kept on purpose.
	failures map[string]bool
	// window checks the window's amounts against the pin's.
	window func(t *testing.T, window quota.Window, pin accountQuota)
	// state checks provider metadata.
	state    func(t *testing.T, state quota.State, pin accountResult)
	statuses map[string]quota.Status
}

func runAccount(t *testing.T, record accountRecord, provider string, test accountCase, credential quota.Credential, expect accountExpect, name string) {
	t.Helper()
	stub := &replay{steps: test.Upstream}
	readers, err := Readers(stub, func() time.Time { return record.Now })
	if err != nil {
		t.Fatal(err)
	}
	state, err := readers[provider].Usage(context.Background(), credential)
	noSecret(t, state, err)
	if encoded, _ := json.Marshal(state); bytes.Contains(encoded, []byte(accountPAT)) || err != nil && strings.Contains(err.Error(), accountPAT) {
		t.Fatalf("PAT exposed: %s %v", encoded, err)
	}
	wantAccountRequests(t, stub.requests, test.Requests, credential.AccessToken)
	if test.Error != "" {
		t.Fatalf("the pin threw %q; its readers catch every failure", test.Error)
	}
	var pin accountResult
	if err := json.Unmarshal(test.Result, &pin); err != nil {
		t.Fatal(err)
	}
	wantPlan := ""
	if pin.Plan != nil {
		wantPlan = *pin.Plan
	}
	keys, quotas := orderedAccountQuotas(t, pin.Quotas)
	if expect.failures[name] {
		if err == nil {
			t.Fatalf("Routeweft read %+v; this answer is a failed read on purpose", state)
		}
		return
	}
	if len(keys) == 0 {
		if notice, ok := expect.notices(name, pin); ok {
			if err != nil || state.Message != notice || len(state.Windows) != 0 || state.Plan != wantPlan {
				t.Fatalf("notice=%q plan=%q windows=%v err=%v; pin %+v", state.Message, state.Plan, state.Windows, err, pin)
			}
			if state.DeriveStatus(record.Now) != quota.StatusUnknown {
				t.Fatalf("a notice derived %s", state.DeriveStatus(record.Now))
			}
			if expect.state != nil {
				expect.state(t, state, pin)
			}
			return
		}
		// The pin turns every failure into a message; Routeweft reports a
		// failed read, which keeps the last good windows on display.
		if err == nil {
			t.Fatalf("the pin answered %+v; Routeweft read %+v", pin, state)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	wantMessage := ""
	if pin.Message != nil {
		wantMessage = *pin.Message
	}
	if state.Plan != wantPlan || state.Message != wantMessage {
		t.Fatalf("plan=%q message=%q, pin plan %q message %q", state.Plan, state.Message, wantPlan, wantMessage)
	}
	pageByName := map[string]pageRow{}
	var pageNames []string
	for _, row := range test.Page {
		pageByName[row.Name] = row
		pageNames = append(pageNames, row.Name)
	}
	var got, shown []string
	for _, window := range state.Windows {
		got = append(got, window.Name)
		label := window.Name
		if window.DisplayName != "" {
			label = window.DisplayName
		}
		shown = append(shown, label)
	}
	// The windows are the rows the pin's page shows, in its order.
	if fmt.Sprint(shown) != fmt.Sprint(pageNames) {
		t.Fatalf("windows=%q, pin page %q (quotas %q)", shown, pageNames, keys)
	}
	for i, window := range state.Windows {
		want, ok := quotas[got[i]]
		if !ok {
			t.Fatalf("%s is not a pin quota (%q)", got[i], keys)
		}
		if window.Unlimited != shared.Truthy(want.Unlimited) || !sameTime(window.ResetAt, pinTime(t, want.ResetAt)) {
			t.Fatalf("%s=%+v reset %v, pin %+v", window.Name, window, window.ResetAt, want)
		}
		total, _ := shared.FiniteNumber(want.Total)
		percent, hasPercent := shared.FiniteNumber(want.RemainingPercentage)
		switch {
		case window.Unlimited:
			if window.Remaining != nil {
				t.Fatalf("%s unlimited with remaining %v", window.Name, *window.Remaining)
			}
		case total == 0:
			// A zero allowance has no fraction: it is neither unlimited nor
			// exhausted (PARITY §6.28). The pin's page shows it as 0%.
			if window.Remaining != nil {
				t.Fatalf("%s zero allowance remaining=%v", window.Name, *window.Remaining)
			}
		case hasPercent:
			if !sameFraction(window.Remaining, math.Max(0, math.Min(100, percent))/100) {
				t.Fatalf("%s remaining=%v, pin %v%%", window.Name, window.Remaining, percent)
			}
		default:
			// The page rounds used against total (utils.js:263-269).
			row := pageByName[shown[i]]
			if window.Remaining == nil || math.Abs(*window.Remaining*100-row.RemainingPercent) > 0.5 {
				t.Fatalf("%s remaining=%v, pin page %v%%", window.Name, window.Remaining, row.RemainingPercent)
			}
		}
		if expect.window != nil {
			expect.window(t, window, want)
		}
	}
	if expect.state != nil {
		expect.state(t, state, pin)
	}
	if want, ok := expect.statuses[name]; ok {
		if status := state.DeriveStatus(record.Now); status != want {
			t.Fatalf("status=%s, want %s", status, want)
		}
	}
}

func orderedAccountQuotas(t *testing.T, raw json.RawMessage) ([]string, map[string]accountQuota) {
	t.Helper()
	if shared.Nullish(raw) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("quotas %s", raw)
	}
	var keys []string
	quotas := map[string]accountQuota{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		var value accountQuota
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, token.(string))
		quotas[token.(string)] = value
	}
	return keys, quotas
}

// wantAccountRequests compares the requests with the pin's: URL, method, body
// and every header, with the connection's credential read as "<token>".
func wantAccountRequests(t *testing.T, got []*http.Request, want []accountRequest, token string) {
	t.Helper()
	redact := func(text string) string {
		for _, secret := range []string{strings.TrimSpace(token), accountPAT} {
			if secret != "" {
				text = strings.ReplaceAll(text, secret, "<token>")
			}
		}
		return text
	}
	if len(got) != len(want) {
		var urls []string
		for _, request := range got {
			urls = append(urls, request.URL.String())
		}
		t.Fatalf("requests=%d %q, pin sent %d", len(got), urls, len(want))
	}
	for i, request := range got {
		if request.URL.String() != want[i].URL || request.Method != want[i].Method {
			t.Fatalf("request %d=%s %s, pin %s %s", i, request.Method, request.URL, want[i].Method, want[i].URL)
		}
		var body []byte
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		var wantBody []byte
		var text string
		var binary struct{ Base64 string }
		switch {
		case shared.Nullish(want[i].Body):
		case json.Unmarshal(want[i].Body, &text) == nil:
			wantBody = []byte(text)
		case json.Unmarshal(want[i].Body, &binary) == nil:
			wantBody, _ = base64.StdEncoding.DecodeString(binary.Base64)
		}
		if shared.Nullish(want[i].Body) != (request.Body == nil) || redact(string(body)) != string(wantBody) {
			t.Fatalf("request %d body=%q, pin %s", i, body, want[i].Body)
		}
		for name, value := range want[i].Headers {
			if sent := redact(request.Header.Get(name)); sent != value {
				t.Fatalf("request %d header %s=%q, pin %q", i, name, sent, value)
			}
		}
		for name := range request.Header {
			if _, ok := headerIn(want[i].Headers, name); !ok {
				t.Fatalf("request %d sends %s, which the pin does not", i, name)
			}
		}
	}
}

func pinNumber(raw json.RawMessage) float64 {
	value, _ := shared.FiniteNumber(raw)
	return value
}

func countField(window quota.Window, field string) *float64 {
	if window.Count == nil {
		return nil
	}
	return map[string]*float64{"used": window.Count.Used, "total": window.Count.Total, "remaining": window.Count.Remaining}[field]
}

func wantCount(t *testing.T, window quota.Window, field string, got *float64, want float64) {
	t.Helper()
	if window.Count == nil {
		t.Fatalf("%s has no count, pin %s %v", window.Name, field, want)
	}
	if got == nil || *got != want {
		t.Fatalf("%s count %s=%v, pin %v", window.Name, field, got, want)
	}
}

// grokExpect: counted windows keep their amounts. The pin's synthetic 1/1 rows
// are not amounts: the exhausted free row keeps the provider's 0 cap, and a
// balance keeps only the balance.
var grokExpect = accountExpect{
	notices: func(_ string, pin accountResult) (string, bool) {
		if pin.Plan == nil || pin.Message == nil {
			return "", false
		}
		return *pin.Message, true
	},
	// An unknown tier (the profile read failed) is not read as free: the pin's
	// synthetic exhausted row would keep a paid account out of routing.
	failures: map[string]bool{"free-exhausted-user-failed": true, "free-exhausted-user-network": true},
	window: func(t *testing.T, window quota.Window, pin accountQuota) {
		used, total := pinNumber(pin.Used), pinNumber(pin.Total)
		switch {
		case window.Name == "Weekly SuperGrok":
			if window.Count != nil {
				t.Fatalf("weekly pool count=%+v", window.Count)
			}
		case window.Count == nil:
			t.Fatalf("%s has no count, pin %+v", window.Name, pin)
		case window.Name == "Prepaid":
			wantCount(t, window, "remaining", countField(window, "remaining"), total)
		case window.Count.Total != nil && *window.Count.Total == 0:
			if used != 1 || total != 1 || *window.Remaining != 0 {
				t.Fatalf("%s=%+v, pin %+v", window.Name, window, pin)
			}
		case window.Count.Total == nil:
			if window.Count.Remaining == nil {
				t.Fatalf("%s count=%+v", window.Name, window.Count)
			}
		default:
			wantCount(t, window, "used", countField(window, "used"), used)
			wantCount(t, window, "total", countField(window, "total"), total)
		}
	},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "over-used": quota.StatusExhausted, "expired": quota.StatusAvailable,
		"free-exhausted": quota.StatusExhausted, "on-demand": quota.StatusAvailable, "paid-grpc-full": quota.StatusExhausted,
		"paid-grpc-weekly": quota.StatusAvailable, "multiple-window": quota.StatusAvailable, "credit-bag-remaining-zero": quota.StatusExhausted},
}

func TestGrokCLIQuotaIsThePins(t *testing.T) {
	record := loadAccountPin(t)
	for _, name := range names(record.Grok) {
		t.Run(name, func(t *testing.T) {
			test := record.Grok[name]
			token := scopedToken
			if test.Token != "" {
				token = test.Token
			}
			runAccount(t, record, "grok-cli", test, quota.Credential{AccessToken: token, ProviderData: test.ProviderData}, grokExpect, name)
		})
	}
}

// kiroExpect: Kiro windows keep used, total and the pin's total − used.
var kiroExpect = accountExpect{
	notices: func(_ string, pin accountResult) (string, bool) {
		if pin.Message != nil {
			return "", false
		}
		return "Kiro connected. No usage limits were returned.", true
	},
	window: func(t *testing.T, window quota.Window, pin accountQuota) {
		wantCount(t, window, "used", countField(window, "used"), pinNumber(pin.Used))
		wantCount(t, window, "total", countField(window, "total"), pinNumber(pin.Total))
		wantCount(t, window, "remaining", countField(window, "remaining"), pinNumber(pin.Remaining))
	},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "over-used": quota.StatusExhausted, "expired": quota.StatusAvailable,
		"get-ok": quota.StatusAvailable, "zero-limit": quota.StatusAvailable},
}

func TestKiroQuotaIsThePins(t *testing.T) {
	record := loadAccountPin(t)
	for _, mode := range names(record.Kiro) {
		data, ok := record.KiroProviderData[mode]
		if !ok {
			t.Fatalf("no provider data for %s", mode)
		}
		for _, name := range names(record.Kiro[mode]) {
			t.Run(mode+"/"+name, func(t *testing.T) {
				credential := quota.Credential{AccessToken: scopedToken, ProviderData: data, APIKey: strings.HasPrefix(mode, "api_key")}
				runAccount(t, record, "kiro", record.Kiro[mode][name], credential, kiroExpect, name)
			})
		}
	}
}

// qoderExpect: each pool keeps used, total, the provider's remaining and its
// unit; the exceeded flag and total percentage are metadata.
var qoderExpect = accountExpect{
	notices: func(string, accountResult) (string, bool) { return "", false },
	window: func(t *testing.T, window quota.Window, pin accountQuota) {
		wantCount(t, window, "used", countField(window, "used"), pinNumber(pin.Used))
		wantCount(t, window, "total", countField(window, "total"), pinNumber(pin.Total))
		wantCount(t, window, "remaining", countField(window, "remaining"), pinNumber(pin.Remaining))
		if window.Count == nil || window.Count.Unit != shared.String(pin.Unit) {
			t.Fatalf("%s unit=%q, pin %s", window.Name, window.Count.Unit, pin.Unit)
		}
	},
	state: func(t *testing.T, state quota.State, pin accountResult) {
		want := map[string]string{"isQuotaExceeded": string(pin.IsQuotaExceeded), "totalUsagePercentage": shared.String(pin.TotalUsagePercentage)}
		if fmt.Sprint(state.Metadata) != fmt.Sprint(want) {
			t.Fatalf("metadata=%v, pin %v", state.Metadata, want)
		}
	},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "over-used": quota.StatusExhausted, "expired": quota.StatusAvailable,
		"multiple-window": quota.StatusAvailable, "exceeded-flag-only": quota.StatusAvailable, "remaining-only": quota.StatusAvailable},
}

func TestQoderQuotaIsThePins(t *testing.T) {
	record := loadAccountPin(t)
	for _, mode := range names(record.Qoder) {
		for _, name := range names(record.Qoder[mode]) {
			t.Run(mode+"/"+name, func(t *testing.T) {
				test := record.Qoder[mode][name]
				credential := quota.Credential{AccessToken: scopedToken}
				switch {
				case mode == "pat" || test.Credential == "apiKey":
					credential = quota.Credential{AccessToken: accountPAT, APIKey: true}
				case test.Credential == "accessToken":
					credential = quota.Credential{AccessToken: accountPAT}
				}
				runAccount(t, record, "qoder", test, credential, qoderExpect, name)
			})
		}
	}
}

// zedExpect: windows keep the clamped used count and the limit; the plan id,
// trial and overdue flags are metadata, and the pin's notes are the message.
var zedExpect = accountExpect{
	notices: func(_ string, pin accountResult) (string, bool) {
		if pin.Plan == nil {
			return "", false
		}
		if pin.Message != nil {
			return *pin.Message, true
		}
		return "Zed connected. No usage limits were returned.", true
	},
	window: func(t *testing.T, window quota.Window, pin accountQuota) {
		wantCount(t, window, "used", countField(window, "used"), pinNumber(pin.Used))
		if !window.Unlimited {
			wantCount(t, window, "total", countField(window, "total"), pinNumber(pin.Total))
		}
	},
	state: func(t *testing.T, state quota.State, pin accountResult) {
		want := map[string]string{"hasOverdueInvoices": string(pin.HasOverdueInvoices), "trialStarted": string(pin.TrialStarted)}
		if !shared.Nullish(pin.PlanID) {
			want["planId"] = shared.String(pin.PlanID)
		}
		if fmt.Sprint(state.Metadata) != fmt.Sprint(want) {
			t.Fatalf("metadata=%v, pin %v", state.Metadata, want)
		}
	},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "over-used": quota.StatusExhausted, "expired": quota.StatusAvailable,
		"unlimited": quota.StatusAvailable, "zero-limit": quota.StatusAvailable, "token-billing": quota.StatusAvailable},
}

func TestZedQuotaIsThePins(t *testing.T) {
	record := loadAccountPin(t)
	credentials := map[string]quota.Credential{
		"user":         {AccessToken: scopedToken, ProviderData: map[string]string{"userId": "user-fixture"}},
		"system-id":    {AccessToken: scopedToken, ProviderData: map[string]string{"userId": "user-fixture", "systemId": "system-fixture"}},
		"no-user":      {AccessToken: scopedToken},
		"padded-token": {AccessToken: " " + scopedToken + " ", ProviderData: map[string]string{"userId": "user-fixture"}},
	}
	for _, mode := range names(record.Zed) {
		credential, ok := credentials[mode]
		if !ok {
			t.Fatalf("no credential for %s", mode)
		}
		for _, name := range names(record.Zed[mode]) {
			t.Run(mode+"/"+name, func(t *testing.T) {
				runAccount(t, record, "zed", record.Zed[mode][name], credential, zedExpect, name)
			})
		}
	}
}
