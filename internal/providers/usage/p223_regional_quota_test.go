package usage

// Differential tests for the GLM, Kimi, MiniMax and CodeBuddy quota readers
// (#223) against testdata/regional_quota_pin.json, which
// compat/parity/generate-regional-quota.mjs records by evaluating the pin's own
// readers. Every case replays the upstream answers the pin saw, for each
// region, and compares the requests and the normalized read model with the
// pin's. The differences Routeweft keeps on purpose are named where they are
// asserted (PARITY §6.29).

import (
	"bytes"
	"context"
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

type regionalRequest struct {
	pinRequest
	Body *string `json:"body"`
}

type regionalCase struct {
	Upstream []pinStep         `json:"upstream"`
	Result   json.RawMessage   `json:"result"`
	Error    string            `json:"error"`
	Requests []regionalRequest `json:"requests"`
}

type regionalRecord struct {
	Pin       string                             `json:"pin"`
	Now       time.Time                          `json:"now"`
	TZ        string                             `json:"tz"`
	GLM       map[string]map[string]regionalCase `json:"glm"`
	Kimi      map[string]map[string]regionalCase `json:"kimi"`
	MiniMax   map[string]map[string]regionalCase `json:"minimax"`
	CodeBuddy map[string]map[string]regionalCase `json:"codebuddy"`
}

func loadRegionalPin(t *testing.T) regionalRecord {
	t.Helper()
	raw, err := os.ReadFile("testdata/regional_quota_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var record regionalRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Pin != "1c3b9f47837d42ea64992063309064af755a14a3" || record.TZ != "UTC" {
		t.Fatalf("pin=%q tz=%q", record.Pin, record.TZ)
	}
	if bytes.Contains(raw, []byte(scopedToken)) {
		t.Fatal("the pin record carries the fixture token")
	}
	return record
}

type regionalQuota struct {
	pinQuota
	Recurring *bool `json:"recurring"`
}

// orderedQuotas returns the pin's quota keys in the order the pin's object
// holds them, which is the order its page lists them, and the quotas.
func orderedQuotas(t *testing.T, raw json.RawMessage) ([]string, map[string]regionalQuota) {
	t.Helper()
	if shared.Nullish(raw) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("quotas %s", raw)
	}
	var keys []string
	quotas := map[string]regionalQuota{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		var value regionalQuota
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, token.(string))
		quotas[token.(string)] = value
	}
	return keys, quotas
}

// regionalExpect is how one reader's pin answers map onto Routeweft.
type regionalExpect struct {
	// notices are the pin messages Routeweft reports as a reader notice (an
	// answer without quota), keyed by case, with Routeweft's fixed text. Every
	// other pin message is a failed read, as in #222.
	notices map[string]string
	// statuses pins the routing reading of named cases.
	statuses map[string]quota.Status
	// identity lists the client-identity headers whose value Routeweft sets
	// differently or not at all (PARITY §6.29).
	identity []string
	// window checks the provider-specific window metadata.
	window func(t *testing.T, name string, window quota.Window, pin regionalQuota)
}

func runRegional(t *testing.T, record regionalRecord, provider string, test regionalCase, credential quota.Credential, expect regionalExpect, name string) {
	t.Helper()
	stub := &replay{steps: test.Upstream}
	readers, err := Readers(stub, func() time.Time { return record.Now })
	if err != nil {
		t.Fatal(err)
	}
	state, err := readers[provider].Usage(context.Background(), credential)
	noSecret(t, state, err)
	wantRegionalRequests(t, stub.requests, test.Requests, expect.identity)
	if test.Error != "" {
		t.Fatalf("the pin threw %q; its readers catch every failure", test.Error)
	}
	var pin pinResult
	if err := json.Unmarshal(test.Result, &pin); err != nil {
		t.Fatal(err)
	}
	wantPlan := ""
	if pin.Plan != nil {
		wantPlan = *pin.Plan
	}
	if pin.Message != "" {
		notice, ok := expect.notices[name]
		if !ok {
			// The pin turns every failure into a message; Routeweft reports a
			// failed read, which keeps the last good windows on display.
			if err == nil {
				t.Fatalf("the pin answered %q; Routeweft read %+v", pin.Message, state)
			}
			return
		}
		if err != nil || state.Message != notice || len(state.Windows) != 0 || state.Plan != wantPlan {
			t.Fatalf("notice=%q plan=%q windows=%v err=%v; pin %q plan %q", state.Message, state.Plan, state.Windows, err, pin.Message, wantPlan)
		}
		if state.DeriveStatus(record.Now) != quota.StatusUnknown {
			t.Fatalf("a notice derived %s", state.DeriveStatus(record.Now))
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	keys, quotas := orderedQuotas(t, pin.Quotas)
	if notice, ok := expect.notices[name]; ok && len(keys) == 0 {
		// The pin answers with the plan and no quota at all (glm.js:84).
		if state.Message != notice || len(state.Windows) != 0 || state.Plan != wantPlan {
			t.Fatalf("state=%+v, pin plan %q with no quotas", state, wantPlan)
		}
		return
	}
	if state.Plan != wantPlan || state.Message != "" {
		t.Fatalf("plan=%q message=%q, pin plan %q", state.Plan, state.Message, wantPlan)
	}
	var got []string
	for _, window := range state.Windows {
		got = append(got, window.Name)
	}
	if fmt.Sprint(got) != fmt.Sprint(keys) {
		t.Fatalf("windows=%q, pin %q", got, keys)
	}
	for _, window := range state.Windows {
		want := quotas[window.Name]
		if window.Unlimited || shared.Truthy(want.Unlimited) || !sameTime(window.ResetAt, pinTime(t, want.ResetAt)) {
			t.Fatalf("%s=%+v, pin %+v", window.Name, window, want)
		}
		if percent, ok := shared.FiniteNumber(want.RemainingPercentage); ok {
			if !sameFraction(window.Remaining, math.Max(0, math.Min(100, percent))/100) {
				t.Fatalf("%s remaining=%v, pin %v%%", window.Name, window.Remaining, percent)
			}
		}
		if expect.window != nil {
			expect.window(t, window.Name, window, want)
		}
	}
	if want, ok := expect.statuses[name]; ok {
		if status := state.DeriveStatus(record.Now); status != want {
			t.Fatalf("status=%s, want %s", status, want)
		}
	}
}

// wantRegionalRequests compares the requests with the pin's: the same URLs,
// methods and bodies in order, and every header with the pin's value, except
// the client-identity headers named in identity.
func wantRegionalRequests(t *testing.T, got []*http.Request, want []regionalRequest, identity []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("requests=%d, pin sent %d", len(got), len(want))
	}
	differs := map[string]bool{}
	for _, name := range identity {
		differs[http.CanonicalHeaderKey(name)] = true
	}
	for i, request := range got {
		if request.URL.String() != want[i].URL || request.Method != want[i].Method {
			t.Fatalf("request %d=%s %s, pin %s %s", i, request.Method, request.URL, want[i].Method, want[i].URL)
		}
		var body []byte
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		if (want[i].Body == nil) != (request.Body == nil) || want[i].Body != nil && string(body) != *want[i].Body {
			t.Fatalf("request %d body=%q, pin %v", i, body, want[i].Body)
		}
		for name, value := range want[i].Headers {
			if differs[http.CanonicalHeaderKey(name)] {
				continue
			}
			sent := request.Header.Get(name)
			if strings.EqualFold(name, "authorization") || strings.EqualFold(name, "x-api-key") {
				sent = strings.Replace(sent, scopedToken, "<token>", 1)
			}
			if sent != value {
				t.Fatalf("request %d header %s=%q, pin %q", i, name, sent, value)
			}
		}
		for name := range request.Header {
			if _, ok := headerIn(want[i].Headers, name); !ok && !differs[name] {
				t.Fatalf("request %d sends %s, which the pin does not", i, name)
			}
		}
	}
}

// glmExpect: GLM windows are percentages only, so they carry no count; each
// keeps the provider's limit type, unit and number as sent.
var glmExpect = regionalExpect{
	notices: map[string]string{"empty": "GLM connected. No quota limits were returned.", "no-data": "GLM connected. No quota limits were returned.",
		"data-not-object": "GLM connected. No quota limits were returned."},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "multiple-window": quota.StatusAvailable,
		"out-of-range": quota.StatusExhausted, "empty": quota.StatusUnknown},
	window: func(t *testing.T, name string, window quota.Window, _ regionalQuota) {
		if window.Count != nil || window.Recurring != nil || window.Limit == nil || window.Limit.Type != "TOKENS_LIMIT" && window.Limit.Type != "CREDIT_LIMIT" {
			t.Fatalf("%s metadata=%+v", name, window)
		}
	},
}

func TestGLMQuotaIsThePins(t *testing.T) {
	record := loadRegionalPin(t)
	for _, provider := range []string{"glm", "glm-cn"} {
		for _, name := range names(record.GLM[provider]) {
			t.Run(provider+"/"+name, func(t *testing.T) {
				runRegional(t, record, provider, record.GLM[provider][name], quota.Credential{AccessToken: scopedToken, APIKey: true}, glmExpect, name)
			})
		}
	}
}

// kimiAmounts: Kimi windows keep the absolute amounts the pin reports, and the
// provider's remaining count only where it sent one.
func kimiAmounts(t *testing.T, name string, window quota.Window, pin regionalQuota) {
	used, _ := shared.FiniteNumber(pin.Used)
	total, _ := shared.FiniteNumber(pin.Total)
	if window.Count == nil || window.Count.Used == nil || *window.Count.Used != used || window.Count.Total == nil || *window.Count.Total != total {
		t.Fatalf("%s count=%+v, pin used %s total %s", name, window.Count, pin.Used, pin.Total)
	}
}

var kimiExpect = regionalExpect{
	notices: map[string]string{
		"empty":            "Kimi Coding connected. Usage tracked per request.",
		"empty-with-level": "Kimi Coding connected. Usage tracked per request.",
		"usage-not-object": "Kimi Coding connected. Usage tracked per request.",
		// The pin shows the provider's localized text when it has one
		// (kimi.js:63-66); Routeweft's notice is fixed.
		"auth403-permission":     "Kimi connected, but this account has no permission to view usage. Subscribe to Kimi Code to access quota.",
		"auth403-subscribe-text": "Kimi connected, but this account has no permission to view usage. Subscribe to Kimi Code to access quota.",
	},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "multiple-window": quota.StatusAvailable,
		"remaining-precedence": quota.StatusAvailable},
	// Routeweft's device identity is its own (onboarding/device.go
	// kimiDeviceHeaders); the device id is the stored one, as in the pin.
	identity: []string{"X-Msh-Platform", "X-Msh-Version", "X-Msh-Device-Name", "X-Msh-Device-Model"},
	window:   kimiAmounts,
}

// kimiCredentials are the Routeweft connections that take each of the pin's
// auth branches (kimi.js:109-121).
var kimiCredentials = map[string]quota.Credential{
	// An API-key connection stores the key as its access token.
	"apikey": {AccessToken: scopedToken, APIKey: true},
	// A connection holding both: the stored key wins.
	"apikey-and-token": {AccessToken: scopedToken, ProviderData: map[string]string{"apiKey": scopedToken, "deviceId": "device-fixture"}},
	"oauth":            {AccessToken: scopedToken, ProviderData: map[string]string{"deviceId": " device-fixture "}},
}

func TestKimiQuotaIsThePins(t *testing.T) {
	record := loadRegionalPin(t)
	for _, mode := range names(record.Kimi) {
		credential, ok := kimiCredentials[mode]
		if !ok {
			t.Fatalf("no credential for %s", mode)
		}
		for _, name := range names(record.Kimi[mode]) {
			t.Run(mode+"/"+name, func(t *testing.T) {
				runRegional(t, record, "kimi", record.Kimi[mode][name], credential, kimiExpect, name)
			})
		}
	}
}

// miniMaxAmounts: a counted bucket keeps its counts; a percent-only bucket,
// which the pin normalizes to a total of 100, has none.
func miniMaxAmounts(t *testing.T, name string, window quota.Window, pin regionalQuota) {
	if window.Count == nil {
		return
	}
	for field, raw := range map[string]json.RawMessage{"used": pin.Used, "total": pin.Total, "remaining": pin.Remaining} {
		want, _ := shared.FiniteNumber(raw)
		got := map[string]*float64{"used": window.Count.Used, "total": window.Count.Total, "remaining": window.Count.Remaining}[field]
		if got == nil || *got != want {
			t.Fatalf("%s count %s=%v, pin %v", name, field, got, want)
		}
	}
}

// miniMaxNoData is the pin's no-quota notice (minimax.js:185-186). It is also
// the answer to a 200 whose body is empty or not JSON, which the pin reads as
// {} (minimax.js:155-159).
const miniMaxNoData = "MiniMax connected. No quota data was returned."

var miniMaxExpect = regionalExpect{
	notices: map[string]string{"empty": miniMaxNoData, "no-quota-models": miniMaxNoData, "no-fallback-empty": miniMaxNoData,
		"no-fallback-invalid-json": miniMaxNoData, "status-ok-msg-no-data": miniMaxNoData, "status-ok-no-models-key": miniMaxNoData,
		"body-empty": miniMaxNoData, "body-null": miniMaxNoData, "body-array": miniMaxNoData, "body-html": miniMaxNoData},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "camel-envelope": quota.StatusAvailable},
	window:   miniMaxAmounts,
}

func TestMiniMaxQuotaIsThePins(t *testing.T) {
	record := loadRegionalPin(t)
	for _, provider := range []string{"minimax", "minimax-cn"} {
		for _, name := range names(record.MiniMax[provider]) {
			t.Run(provider+"/"+name, func(t *testing.T) {
				runRegional(t, record, provider, record.MiniMax[provider][name], quota.Credential{AccessToken: scopedToken, APIKey: true}, miniMaxExpect, name)
			})
		}
	}
}

// codebuddyPacks: a pack keeps its absolute amounts and the pin's recurring
// flag, and its remaining fraction is spent over size; a zero size has none,
// which is neither unlimited nor exhausted.
func codebuddyPacks(t *testing.T, name string, window quota.Window, pin regionalQuota) {
	used, _ := shared.FiniteNumber(pin.Used)
	total, _ := shared.FiniteNumber(pin.Total)
	if window.Count == nil || *window.Count.Used != used || *window.Count.Total != total || window.Count.Remaining != nil {
		t.Fatalf("%s count=%+v, pin %+v", name, window.Count, pin)
	}
	if pin.Recurring == nil || window.Recurring == nil || *window.Recurring != *pin.Recurring {
		t.Fatalf("%s recurring=%v, pin %v", name, window.Recurring, pin.Recurring)
	}
	if total <= 0 {
		if window.Remaining != nil {
			t.Fatalf("%s zero capacity read as %v", name, *window.Remaining)
		}
		return
	}
	if !sameFraction(window.Remaining, math.Max(0, math.Min(1, (total-used)/total))) {
		t.Fatalf("%s remaining=%v, pin %v/%v", name, window.Remaining, used, total)
	}
}

var codebuddyExpect = regionalExpect{
	notices: map[string]string{"empty": "CodeBuddy connected. No credit package found.", "no-data": "CodeBuddy connected. No credit package found."},
	statuses: map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "zero-capacity": quota.StatusAvailable,
		"multiple-window": quota.StatusAvailable},
	// Routeweft does not present CodeBuddy's client identity (owner-approved,
	// PARITY "CodeBuddy CN and international wire").
	identity: []string{"User-Agent", "X-Product", "X-IDE-Type", "X-IDE-Name", "x-requested-with", "x-codebuddy-request"},
	window:   codebuddyPacks,
}

func TestCodeBuddyQuotaIsThePins(t *testing.T) {
	record := loadRegionalPin(t)
	for _, provider := range []string{"codebuddy-cn", "codebuddy-intl"} {
		for _, name := range names(record.CodeBuddy[provider]) {
			t.Run(provider+"/"+name, func(t *testing.T) {
				runRegional(t, record, provider, record.CodeBuddy[provider][name], quota.Credential{AccessToken: scopedToken}, codebuddyExpect, name)
			})
		}
	}
}

// TestRegionalFixturesCoverEveryOutcome is the acceptance list for #223: every
// reader, in every region, has zero, empty, expired, multiple-window, timeout,
// auth and error fixtures.
func TestRegionalFixturesCoverEveryOutcome(t *testing.T) {
	record := loadRegionalPin(t)
	outcomes := []string{"zero", "empty", "expired", "multiple-window", "timeout", "auth401", "error500|both-500"}
	for reader, groups := range map[string]map[string]map[string]regionalCase{"glm": record.GLM, "kimi": {"apikey": record.Kimi["apikey"]},
		"minimax": record.MiniMax, "codebuddy": record.CodeBuddy} {
		for group, cases := range groups {
			for _, outcome := range outcomes {
				found := false
				for _, name := range strings.Split(outcome, "|") {
					_, ok := cases[name]
					found = found || ok
				}
				if !found {
					t.Errorf("%s %s has no %s fixture", reader, group, outcome)
				}
			}
		}
	}
	if len(record.GLM) != 2 || len(record.MiniMax) != 2 || len(record.CodeBuddy) != 2 || len(record.Kimi) != 3 {
		t.Fatalf("regions: glm %d minimax %d codebuddy %d kimi modes %d", len(record.GLM), len(record.MiniMax), len(record.CodeBuddy), len(record.Kimi))
	}
}

// TestRegionalWindowsKeepProviderMetadata pins the read-model fields on named
// fixtures: GLM's raw unit and number (a string unit is not 3), MiniMax counts
// only on counted buckets, CodeBuddy refill versus one-shot packs.
func TestRegionalWindowsKeepProviderMetadata(t *testing.T) {
	record := loadRegionalPin(t)
	read := func(provider string, test regionalCase, credential quota.Credential) map[string]quota.Window {
		t.Helper()
		readers, err := Readers(&replay{steps: test.Upstream}, func() time.Time { return record.Now })
		if err != nil {
			t.Fatal(err)
		}
		state, err := readers[provider].Usage(context.Background(), credential)
		if err != nil {
			t.Fatal(err)
		}
		return windowsByName(state)
	}
	glm := read("glm", record.GLM["glm"]["raw-shapes"], quota.Credential{AccessToken: scopedToken})
	if limit := glm["Tokens"].Limit; limit == nil || string(limit.Unit) != `"3"` || string(limit.Number) != `"5"` {
		t.Fatalf("Tokens limit=%+v", limit)
	}
	if limit := glm["Session (undefinedh)"].Limit; limit == nil || limit.Type != "CREDIT_LIMIT" || string(limit.Unit) != "3" || limit.Number != nil {
		t.Fatalf("Session (undefinedh) limit=%+v", limit)
	}
	glm = read("glm-cn", record.GLM["glm-cn"]["multiple-window"], quota.Credential{AccessToken: scopedToken})
	if limit := glm["Limit (1000)"].Limit; limit == nil || limit.Type != "CREDIT_LIMIT" || string(limit.Unit) != "5" || string(limit.Number) != "1000" {
		t.Fatalf("Limit (1000)=%+v", limit)
	}

	minimax := read("minimax", record.MiniMax["minimax"]["multiple-window"], quota.Credential{AccessToken: scopedToken})
	if minimax["M-series (5h)"].Count != nil || minimax["M-series (7d)"].Count != nil {
		t.Fatal("a percent-only bucket reported the synthetic 100 as an amount")
	}
	if count := minimax["Speech 02 HD (7d)"].Count; count == nil || *count.Used != 900 || *count.Total != 1000 || *count.Remaining != 100 {
		t.Fatalf("Speech 02 HD (7d) count=%+v", count)
	}
	// The coding-plan endpoints count what remains (minimax.js:190).
	cn := read("minimax-cn", record.MiniMax["minimax-cn"]["multiple-window"], quota.Credential{AccessToken: scopedToken})
	if count := cn["Speech 02 HD (5h)"].Count; count == nil || *count.Used != 70 || *count.Remaining != 30 {
		t.Fatalf("CN Speech 02 HD (5h) count=%+v", count)
	}

	packs := read("codebuddy-intl", record.CodeBuddy["codebuddy-intl"]["multiple-window"], quota.Credential{AccessToken: scopedToken})
	for name, recurring := range map[string]bool{"Weekly": true, "Monthly": true, "Bonus Pack 1": false, "Bonus Pack 2": false} {
		if window := packs[name]; window.Recurring == nil || *window.Recurring != recurring {
			t.Fatalf("%s recurring=%v", name, window.Recurring)
		}
	}
	if count := packs["Monthly"].Count; count == nil || *count.Used != 6.54 || *count.Total != 500 {
		t.Fatalf("Monthly reads the *Precise strings: %+v", count)
	}
	zero := read("codebuddy-cn", record.CodeBuddy["codebuddy-cn"]["zero-capacity"], quota.Credential{AccessToken: scopedToken})
	if window := zero["Bonus Pack 1"]; window.Unlimited || window.Remaining != nil || window.Count == nil || *window.Count.Total != 0 {
		t.Fatalf("zero capacity=%+v", window)
	}
}

// TestKimiAPIKeyConnectionUsesTheKeyHeader is the read path the pin takes for
// an API-key connection (kimi.js:116-117), which Routeweft read with Bearer and
// device headers before #223 because the key was only its access token.
func TestKimiAPIKeyConnectionUsesTheKeyHeader(t *testing.T) {
	stub := &stubClient{status: http.StatusOK, body: `{"usage":{"limit":100,"used":10}}`}
	if _, err := reader(t, "kimi", stub).Usage(context.Background(), quota.Credential{AccessToken: "fixture-key", APIKey: true}); err != nil {
		t.Fatal(err)
	}
	if stub.request.Header.Get("x-api-key") != "fixture-key" || stub.request.Header.Get("Authorization") != "" || stub.request.Header.Get("X-Msh-Device-Id") != "" {
		t.Fatalf("headers=%v", stub.request.Header)
	}
}

// TestRegionalSharedValueSemantics pins the JavaScript Number() and String()
// rules the #223 readers port, on the inputs the fixtures exercise and their
// edges.
func TestRegionalSharedValueSemantics(t *testing.T) {
	for raw, want := range map[string]float64{`null`: 0, `true`: 1, `false`: 0, `""`: 0, `"  "`: 0, `" 12 "`: 12, `"1e3"`: 1000,
		`".5"`: 0.5, `"5."`: 5, `"0x10"`: 16, `"0b11"`: 3, `"-7"`: -7, `[]`: 0, `["4"]`: 4, `[null]`: 0, `3.25`: 3.25} {
		if got := shared.Number(json.RawMessage(raw)); got != want {
			t.Fatalf("Number(%s)=%v, want %v", raw, got, want)
		}
	}
	for _, raw := range []string{``, `"abc"`, `"1_000"`, `"inf"`, `"nan"`, `"0x1p3"`, `"1e"`, `"."`, `{}`, `[1,2]`, `[true]`, `"-0x10"`} {
		if got := shared.Number(json.RawMessage(raw)); !math.IsNaN(got) {
			t.Fatalf("Number(%s)=%v, want NaN", raw, got)
		}
	}
	if !math.IsInf(shared.Number(json.RawMessage(`"Infinity"`)), 1) || !math.IsInf(shared.Number(json.RawMessage(`1e400`)), 1) {
		t.Fatal("Number did not keep Infinity")
	}
	if shared.OrZero(math.NaN()) != 0 || !math.IsInf(shared.OrZero(math.Inf(1)), 1) {
		t.Fatal("OrZero is not `|| 0`")
	}
	for raw, want := range map[string]string{``: "undefined", `null`: "null", `5`: "5", `2.5`: "2.5", `"5"`: "5", `true`: "true",
		`{"a":1}`: "[object Object]", `[1,null,"x"]`: "1,,x"} {
		if got := shared.Template(json.RawMessage(raw)); got != want {
			t.Fatalf("Template(%s)=%q, want %q", raw, got, want)
		}
	}
	if got := shared.ResetTime(json.RawMessage(`"2026-10-31 23:59:59"`)); got == nil || !got.Equal(time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)) {
		t.Fatalf("Tencent date=%v", got)
	}
}

// TestMiniMaxStatusMessageIsThePins: a non-zero base_resp status is a failed
// read whose text is the pin's message, `MiniMax connected. ${status_msg ||
// "Upstream quota API error"}` with status_msg read `snake ?? camel ?? ""`,
// spelled by String() and trimmed by String.prototype.trim (minimax.js:161-163,
// 177-179). It is never a notice and never quota.
func TestMiniMaxStatusMessageIsThePins(t *testing.T) {
	record := loadRegionalPin(t)
	checked := 0
	for _, provider := range []string{"minimax", "minimax-cn"} {
		for _, name := range names(record.MiniMax[provider]) {
			test := record.MiniMax[provider][name]
			var pin pinResult
			if err := json.Unmarshal(test.Result, &pin); err != nil {
				t.Fatal(err)
			}
			// The status branch is the only "MiniMax connected." message that
			// is not an HTTP error, a fetch failure or the no-quota notice.
			if !strings.HasPrefix(pin.Message, "MiniMax connected. ") || pin.Message == miniMaxNoData ||
				strings.Contains(pin.Message, "usage endpoint error") || strings.Contains(pin.Message, "Unable to fetch usage") {
				continue
			}
			readers, err := Readers(&replay{steps: test.Upstream}, func() time.Time { return record.Now })
			if err != nil {
				t.Fatal(err)
			}
			state, err := readers[provider].Usage(context.Background(), quota.Credential{AccessToken: scopedToken})
			if err == nil || err.Error() != pin.Message || len(state.Windows) != 0 || state.Message != "" {
				t.Fatalf("%s/%s: err=%v state=%+v, pin %q", provider, name, err, state, pin.Message)
			}
			checked++
		}
	}
	// Per region: no-fallback-status, the ten status-code-* answers that end in
	// the status branch, and status-snake-null-camel-error.
	if checked != 24 {
		t.Fatalf("checked %d status answers, want 24", checked)
	}
}

// TestJavaScriptTrim pins String.prototype.trim: U+FEFF and the Unicode
// space separators go, U+0085 (which strings.TrimSpace removes) stays.
func TestJavaScriptTrim(t *testing.T) {
	for in, want := range map[string]string{"\u3000\ufeffbad\u00a0\u2028": "bad", " \t\v\f\r\n x \u2000\u200a\u202f\u205f\u1680\u2029": "x",
		"\u0085x\u0085": "\u0085x\u0085", "": "", "\u200b x": "\u200b x"} {
		if got := shared.Trim(in); got != want {
			t.Fatalf("Trim(%q)=%q, want %q", in, got, want)
		}
	}
}
