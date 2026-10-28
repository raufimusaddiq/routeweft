package usage

// Differential tests for the Codex, Claude and GitHub Copilot quota readers
// (#222) against testdata/scoped_quota_pin.json, which
// compat/parity/generate-scoped-quota.mjs records by evaluating the pin's own
// readers. Every case replays the upstream answer the pin saw and compares the
// normalized read model with the pin's result. The differences Routeweft keeps
// on purpose are named where they are asserted (PARITY §6.28).

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
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

const scopedToken = "fixture-token"

type pinStep struct {
	Status  int             `json:"status"`
	Body    json.RawMessage `json:"body"`
	Network string          `json:"network"`
	// Base64 is a binary body (gRPC-web frames, #224).
	Base64 *string `json:"base64"`
}

type pinRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
}

type pinCase struct {
	Upstream json.RawMessage `json:"upstream"`
	Result   json.RawMessage `json:"result"`
	Error    string          `json:"error"`
	Requests []pinRequest    `json:"requests"`
}

type pinSequenceCall struct {
	AdvanceMs int64           `json:"advanceMs"`
	Force     bool            `json:"force"`
	Upstream  []pinStep       `json:"upstream"`
	Result    json.RawMessage `json:"result"`
	Requests  []pinRequest    `json:"requests"`
}

type pinRecord struct {
	Pin             string                                       `json:"pin"`
	Now             time.Time                                    `json:"now"`
	Codex           map[string]pinCase                           `json:"codex"`
	Claude          map[string]pinCase                           `json:"claude"`
	Github          map[string]pinCase                           `json:"github"`
	ClaudeSequences map[string]struct{ Calls []pinSequenceCall } `json:"claudeSequences"`
}

func loadScopedPin(t *testing.T) pinRecord {
	t.Helper()
	raw, err := os.ReadFile("testdata/scoped_quota_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var record pinRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Pin != "1c3b9f47837d42ea64992063309064af755a14a3" {
		t.Fatalf("pin=%q", record.Pin)
	}
	if bytes.Contains(raw, []byte(scopedToken)) {
		t.Fatal("the pin record carries the fixture token")
	}
	return record
}

// steps decodes a case's upstream: one step (Codex, GitHub) or a list (Claude).
func (c pinCase) steps(t *testing.T) []pinStep {
	t.Helper()
	var list []pinStep
	if json.Unmarshal(c.Upstream, &list) == nil {
		return list
	}
	var one pinStep
	if err := json.Unmarshal(c.Upstream, &one); err != nil {
		t.Fatal(err)
	}
	return []pinStep{one}
}

// replay answers each request with the next scripted step and records it. A
// network step fails the request as a deadline would.
type replay struct {
	steps    []pinStep
	requests []*http.Request
}

func (r *replay) Do(request *http.Request) (*http.Response, error) {
	r.requests = append(r.requests, request)
	if len(r.requests) > len(r.steps) {
		return nil, fmt.Errorf("unscripted request %d to %s", len(r.requests), request.URL)
	}
	step := r.steps[len(r.requests)-1]
	if step.Network != "" {
		return nil, fmt.Errorf("%s: %w", step.Network, context.DeadlineExceeded)
	}
	body := []byte(step.Body)
	var text string
	if json.Unmarshal(step.Body, &text) == nil {
		body = []byte(text)
	}
	if step.Base64 != nil {
		var err error
		if body, err = base64.StdEncoding.DecodeString(*step.Base64); err != nil {
			return nil, err
		}
	}
	return &http.Response{StatusCode: step.Status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
}

// wantRequests compares the requests with the pin's: the same URLs and methods
// in order, and every header the pin sent, with the same value. extra lists the
// headers Routeweft adds on purpose.
func wantRequests(t *testing.T, got []*http.Request, want []pinRequest, extra ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("requests=%d, pin sent %d", len(got), len(want))
	}
	allowed := map[string]bool{}
	for _, name := range extra {
		allowed[http.CanonicalHeaderKey(name)] = true
	}
	for i, request := range got {
		if request.URL.String() != want[i].URL || request.Method != want[i].Method {
			t.Fatalf("request %d=%s %s, pin %s %s", i, request.Method, request.URL, want[i].Method, want[i].URL)
		}
		for name, value := range want[i].Headers {
			sent := request.Header.Get(name)
			if strings.EqualFold(name, "authorization") {
				sent = strings.Replace(sent, scopedToken, "<token>", 1)
			}
			if sent != value {
				t.Fatalf("request %d header %s=%q, pin %q", i, name, sent, value)
			}
		}
		for name := range request.Header {
			if _, ok := headerIn(want[i].Headers, name); !ok && !allowed[name] {
				t.Fatalf("request %d sends %s, which the pin does not", i, name)
			}
		}
	}
}

func headerIn(headers map[string]string, name string) (string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

type pinQuota struct {
	Used                json.RawMessage `json:"used"`
	Total               json.RawMessage `json:"total"`
	Remaining           json.RawMessage `json:"remaining"`
	RemainingPercentage json.RawMessage `json:"remainingPercentage"`
	ResetAt             *string         `json:"resetAt"`
	Unlimited           json.RawMessage `json:"unlimited"`
}

type pinResult struct {
	Plan               *string                           `json:"plan"`
	Message            string                            `json:"message"`
	Organization       string                            `json:"organization"`
	ResetDate          json.RawMessage                   `json:"resetDate"`
	ExtraUsage         json.RawMessage                   `json:"extraUsage"`
	LimitReached       json.RawMessage                   `json:"limitReached"`
	ReviewLimitReached json.RawMessage                   `json:"reviewLimitReached"`
	SparkLimitReached  json.RawMessage                   `json:"sparkLimitReached"`
	ResetCredits       *struct{ AvailableCount float64 } `json:"resetCredits"`
	Quotas             json.RawMessage                   `json:"quotas"`
}

func (c pinCase) result(t *testing.T) pinResult {
	t.Helper()
	var result pinResult
	if err := json.Unmarshal(c.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// quotas returns the pin's quota map; it is nil when the pin reported none (a
// message) and also when it reported the legacy organization object raw.
func (r pinResult) quotas() map[string]pinQuota {
	var quotas map[string]pinQuota
	if json.Unmarshal(r.Quotas, &quotas) != nil {
		return nil
	}
	return quotas
}

func pinTime(t *testing.T, text *string) *time.Time {
	t.Helper()
	if text == nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *text)
	if err != nil {
		t.Fatal(err)
	}
	return &parsed
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func windowsByName(state quota.State) map[string]quota.Window {
	windows := make(map[string]quota.Window, len(state.Windows))
	for _, window := range state.Windows {
		windows[window.Name] = window
	}
	return windows
}

func names[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameFraction(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 1e-9
}

// noSecret proves neither the read model nor an error carries the token.
func noSecret(t *testing.T, state quota.State, err error) {
	t.Helper()
	encoded, _ := json.Marshal(state)
	if bytes.Contains(encoded, []byte(scopedToken)) || (err != nil && strings.Contains(err.Error(), scopedToken)) {
		t.Fatalf("token exposed: state=%s err=%v", encoded, err)
	}
}

// wantStatus pins the routing reading of each fixture: a known zero with a
// future reset is exhausted, a past reset is not, nothing reported is unknown.
func wantStatus(t *testing.T, state quota.State, now time.Time, cases map[string]quota.Status, name string) {
	t.Helper()
	if want, ok := cases[name]; ok {
		if got := state.DeriveStatus(now); got != want {
			t.Fatalf("status=%s, want %s", got, want)
		}
	}
}

var codexAllowedHeaders = []string{"OpenAI-Beta", "originator"} // usage.go get(); PARITY §6.28

func TestCodexQuotaIsThePins(t *testing.T) {
	record := loadScopedPin(t)
	statuses := map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "empty": quota.StatusUnknown, "multiple-window": quota.StatusExhausted}
	for _, name := range names(record.Codex) {
		test := record.Codex[name]
		t.Run(name, func(t *testing.T) {
			stub := &replay{steps: test.steps(t)}
			state, err := (quota.CodexClient{Client: stub}).Usage(context.Background(), quota.Credential{AccessToken: scopedToken})
			noSecret(t, state, err)
			wantRequests(t, stub.requests, test.Requests, codexAllowedHeaders...)
			if test.Error != "" {
				if err == nil {
					t.Fatalf("the pin failed (%s); Routeweft read %+v", test.Error, state)
				}
				return
			}
			pin := test.result(t)
			if pin.Message != "" {
				// The pin reports a non-2xx answer as a message
				// (codex.js:117-119); Routeweft reports it as a failed read,
				// with 401/403 as re-authentication, never as quota.
				if err == nil {
					t.Fatalf("the pin answered %q; Routeweft read %+v", pin.Message, state)
				}
				if status := stub.steps[0].Status; (status == 401 || status == 403) != (err == codexprovider.ErrReauthRequired) {
					t.Fatalf("status %d err=%v", status, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pin.Plan == nil || state.Plan != *pin.Plan {
				t.Fatalf("plan=%q, pin %v", state.Plan, pin.Plan)
			}
			quotas, windows := pin.quotas(), windowsByName(state)
			if fmt.Sprint(names(quotas)) != fmt.Sprint(names(windows)) {
				t.Fatalf("windows=%v, pin %v", names(windows), names(quotas))
			}
			reached := map[string]json.RawMessage{"": pin.LimitReached, "review": pin.ReviewLimitReached, "spark": pin.SparkLimitReached}
			for key, want := range quotas {
				window := windows[key]
				used, _ := shared.FiniteNumber(want.Used)
				if !sameFraction(window.Remaining, (100-used)/100) || !sameTime(window.ResetAt, pinTime(t, want.ResetAt)) || window.Unlimited {
					t.Fatalf("%s=%+v, pin %+v", key, window, want)
				}
				group, _, found := strings.Cut(key, "_")
				if !found {
					group = ""
				}
				if window.LimitReached != shared.Truthy(reached[group]) {
					t.Fatalf("%s limitReached=%v, pin %s", key, window.LimitReached, reached[group])
				}
				if window.DisplayName == "" {
					t.Fatalf("%s has no reference label", key)
				}
			}
			// The pin reports an omitted reset-credit count as 0; Routeweft
			// keeps it unknown.
			omitted := shared.Nullish(shared.Field(shared.Field(test.stepBody(t), "rate_limit_reset_credits"), "available_count"))
			switch {
			case omitted && state.AvailableReset != nil:
				t.Fatalf("omitted count read as %v", *state.AvailableReset)
			case !omitted && (state.AvailableReset == nil || *state.AvailableReset != pin.ResetCredits.AvailableCount):
				t.Fatalf("availableReset=%v, pin %v", state.AvailableReset, pin.ResetCredits.AvailableCount)
			}
			wantStatus(t, state, record.Now, statuses, name)
		})
	}
}

func (c pinCase) stepBody(t *testing.T) json.RawMessage {
	t.Helper()
	return c.steps(t)[0].Body
}

func TestGitHubQuotaIsThePins(t *testing.T) {
	record := loadScopedPin(t)
	statuses := map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "empty-snapshots": quota.StatusAvailable,
		"zero-entitlement": quota.StatusAvailable, "multiple-window": quota.StatusAvailable, "unrecognized": quota.StatusUnknown,
		// Two windows without a bound: not exhausted; the read model labels the
		// account unknown because no remaining fraction is observed.
		"free-empty": quota.StatusAvailable}
	for _, name := range names(record.Github) {
		test := record.Github[name]
		t.Run(name, func(t *testing.T) {
			stub := &replay{steps: test.steps(t)}
			readers, err := Readers(stub, func() time.Time { return record.Now })
			if err != nil {
				t.Fatal(err)
			}
			state, err := readers["github"].Usage(context.Background(), quota.Credential{AccessToken: scopedToken})
			noSecret(t, state, err)
			wantRequests(t, stub.requests, test.Requests)
			if test.Error != "" {
				if err == nil {
					t.Fatalf("the pin failed (%s); Routeweft read %+v", test.Error, state)
				}
				if status := stub.steps[0].Status; (status == 401 || status == 403) && !strings.Contains(err.Error(), "rejected") {
					t.Fatalf("status %d err=%v", status, err)
				}
				// The pin's error carries the upstream body (github.js:38-39);
				// Routeweft's never does.
				var body string
				if json.Unmarshal(stub.steps[0].Body, &body) == nil && body != "" && strings.Contains(err.Error(), body) {
					t.Fatalf("upstream body in error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pin := test.result(t)
			if state.Message != pin.Message {
				t.Fatalf("message=%q, pin %q", state.Message, pin.Message)
			}
			if want := ""; pin.Plan != nil {
				want = *pin.Plan
				if state.Plan != want {
					t.Fatalf("plan=%q, pin %q", state.Plan, want)
				}
			} else if state.Plan != want {
				t.Fatalf("plan=%q, pin none", state.Plan)
			}
			if want := shared.String(pin.ResetDate); state.Metadata["resetDate"] != want {
				t.Fatalf("resetDate=%q, pin %q", state.Metadata["resetDate"], want)
			}
			quotas, windows := pin.quotas(), windowsByName(state)
			for key, want := range quotas {
				window, ok := windows[key]
				if !ok {
					t.Fatalf("missing %s; windows=%v", key, names(windows))
				}
				if window.Unlimited != shared.Truthy(want.Unlimited) || !sameTime(window.ResetAt, pinTime(t, want.ResetAt)) {
					t.Fatalf("%s=%+v, pin %+v", key, window, want)
				}
				used, _ := shared.FiniteNumber(want.Used)
				total, _ := shared.FiniteNumber(want.Total)
				if window.Count == nil || window.Count.Used == nil || *window.Count.Used != used || window.Count.Total == nil || *window.Count.Total != total {
					t.Fatalf("%s count=%+v, pin used %s total %s", key, window.Count, want.Used, want.Total)
				}
				if remaining, ok := shared.FiniteNumber(want.Remaining); ok != (window.Count.Remaining != nil) || ok && *window.Count.Remaining != remaining {
					t.Fatalf("%s remaining=%v, pin %s", key, window.Count.Remaining, want.Remaining)
				}
				// An unlimited window keeps its amounts (github.js:94-97) but has
				// no remaining fraction: the flag, not the amounts, bounds it.
				if window.Unlimited {
					if window.Remaining != nil {
						t.Fatalf("%s unlimited with fraction %v", key, *window.Remaining)
					}
					continue
				}
				// A zero allowance has no remaining fraction: it is neither
				// unlimited (as Routeweft read it before #222) nor exhausted.
				if total <= 0 {
					if window.Remaining != nil {
						t.Fatalf("%s zero allowance read as %v", key, *window.Remaining)
					}
					continue
				}
				if !sameFraction(window.Remaining, math.Max(0, math.Min(1, (total-used)/total))) {
					t.Fatalf("%s remaining=%v", key, window.Remaining)
				}
			}
			if len(windows) > len(quotas) {
				t.Fatalf("windows=%v, pin %v", names(windows), names(quotas))
			}
			wantStatus(t, state, record.Now, statuses, name)
		})
	}
}

// claudeErrorCases are the pin's soft failures that are a failed read in
// Routeweft: the pin turns a thrown error into a message (claude.js:144-146).
var claudeErrorCases = map[string]bool{"timeout": true, "invalid-json": true}

func TestClaudeQuotaIsThePins(t *testing.T) {
	record := loadScopedPin(t)
	statuses := map[string]quota.Status{"zero": quota.StatusExhausted, "expired": quota.StatusAvailable, "empty": quota.StatusUnknown,
		"multiple-window": quota.StatusAvailable, "oauth-401-settings-403": quota.StatusUnknown}
	for _, name := range names(record.Claude) {
		test := record.Claude[name]
		t.Run(name, func(t *testing.T) {
			stub := &replay{steps: test.steps(t)}
			client := quota.ClaudeClient{Client: &claudeprovider.UsageClient{Client: stub, Now: func() time.Time { return record.Now }}}
			state, err := client.Usage(context.Background(), quota.Credential{AccessToken: scopedToken})
			noSecret(t, state, err)
			wantRequests(t, stub.requests, test.Requests, "Accept")
			pin := test.result(t)
			if claudeErrorCases[name] {
				if err == nil {
					t.Fatalf("the pin answered %q; Routeweft read %+v", pin.Message, state)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state.Message != pin.Message || state.Metadata["organization"] != pin.Organization {
				t.Fatalf("message=%q metadata=%v, pin %q %q", state.Message, state.Metadata, pin.Message, pin.Organization)
			}
			if want := ""; pin.Plan != nil {
				want = *pin.Plan
				if state.Plan != want {
					t.Fatalf("plan=%q, pin %q", state.Plan, want)
				}
			} else if state.Plan != want {
				t.Fatalf("plan=%q, pin none", state.Plan)
			}
			if shared.Nullish(pin.ExtraUsage) != (state.ExtraUsage == nil) || (state.ExtraUsage != nil && !jsonEqual(state.ExtraUsage, pin.ExtraUsage)) {
				t.Fatalf("extraUsage=%s, pin %s", state.ExtraUsage, pin.ExtraUsage)
			}
			quotas, windows := pin.quotas(), windowsByName(state)
			if quotas == nil && len(pin.Quotas) > 0 {
				// The legacy organization usage object (claude.js:178-184) is
				// provider-owned and not a window; it is not read as one.
				if len(windows) != 0 {
					t.Fatalf("legacy object read as windows %v", names(windows))
				}
				return
			}
			if fmt.Sprint(names(quotas)) != fmt.Sprint(names(windows)) {
				t.Fatalf("windows=%v, pin %v", names(windows), names(quotas))
			}
			for key, want := range quotas {
				window := windows[key]
				remaining, _ := shared.FiniteNumber(want.RemainingPercentage)
				if !sameFraction(window.Remaining, math.Min(remaining, 100)/100) || !sameTime(window.ResetAt, pinTime(t, want.ResetAt)) || window.Unlimited {
					t.Fatalf("%s=%+v, pin %+v", key, window, want)
				}
			}
			wantStatus(t, state, record.Now, statuses, name)
		})
	}
}

// TestClaudeScopedWeeklyKeepsItsScope proves limits[] windows carry the
// provider's kind and model display name, which the window name lowercases.
func TestClaudeScopedWeeklyKeepsItsScope(t *testing.T) {
	record := loadScopedPin(t)
	want := map[string]map[string]string{
		"multiple-window":       {"weekly fable (7d)": "Fable"},
		"limits-entries":        {"weekly opus max (7d)": "Opus Max", "weekly haiku (7d)": "Haiku", "weekly 7 (7d)": "7"},
		"limits-override-keyed": {"weekly fable (7d)": "FABLE"},
	}
	for name, models := range want {
		test := record.Claude[name]
		client := quota.ClaudeClient{Client: &claudeprovider.UsageClient{Client: &replay{steps: test.steps(t)}, Now: func() time.Time { return record.Now }}}
		state, err := client.Usage(context.Background(), quota.Credential{AccessToken: scopedToken})
		if err != nil {
			t.Fatal(err)
		}
		for _, window := range state.Windows {
			model, scoped := models[window.Name]
			if scoped != (window.Scope != nil) || scoped && (window.Scope.Kind != "weekly_scoped" || window.Scope.Model != model) {
				t.Fatalf("%s %s scope=%+v, want %q", name, window.Name, window.Scope, model)
			}
		}
	}
}

// TestClaudeSequencesFollowThePin replays the pin's multi-read cases. The
// OAuth 429 cool-down and its legacy fallback match request for request. After
// a soft failure the pin serves the last good read as a fresh one and then never
// reads upstream again until a forced refresh (claude.js:37-54: the settled
// promise stays cached). Routeweft reports the soft failure, which keeps the
// last good windows on display, and reads again on the next refresh.
func TestClaudeSequencesFollowThePin(t *testing.T) {
	record := loadScopedPin(t)
	cooldown := record.ClaudeSequences["cooldown-skips-oauth"].Calls
	stub := &replay{}
	now := record.Now
	client := &claudeprovider.UsageClient{Client: stub, Now: func() time.Time { return now }}
	for i, call := range cooldown {
		now = now.Add(time.Duration(call.AdvanceMs) * time.Millisecond)
		stub.steps = append(stub.steps, call.Upstream...)
		before := len(stub.requests)
		usage, err := client.Usage(context.Background(), scopedToken, call.Force)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		wantRequests(t, stub.requests[before:], call.Requests, "Accept")
		var pin pinResult
		_ = json.Unmarshal(call.Result, &pin)
		if usage.Message != pin.Message || (pin.Quotas != nil) != (usage.Quotas != nil) {
			t.Fatalf("call %d=%+v, pin %s", i, usage, call.Result)
		}
	}

	stale := record.ClaudeSequences["stale-after-soft-failure"].Calls
	stub = &replay{}
	now = record.Now
	reader := quota.ClaudeClient{Client: &claudeprovider.UsageClient{Client: stub, Now: func() time.Time { return now }}}
	softFailure := stale[1].Upstream
	for i, call := range stale {
		now = now.Add(time.Duration(call.AdvanceMs) * time.Millisecond)
		steps := call.Upstream
		if i == 2 {
			if len(call.Requests) != 0 {
				t.Fatalf("the pin re-read after a soft failure: %v", call.Requests)
			}
			steps = softFailure
		}
		stub.steps = append(stub.steps, steps...)
		before := len(stub.requests)
		state, err := reader.Usage(context.Background(), quota.Credential{AccessToken: scopedToken})
		if i == 0 {
			if err != nil || len(state.Windows) == 0 {
				t.Fatalf("first read=%+v %v", state, err)
			}
			continue
		}
		if err != claudeprovider.ErrUsageUnavailable || len(stub.requests)-before != len(softFailure) {
			t.Fatalf("call %d: err=%v requests=%d", i, err, len(stub.requests)-before)
		}
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && fmt.Sprint(left) == fmt.Sprint(right)
}

// TestScopedQuotaSharedValueSemantics pins the JavaScript value rules the
// readers port (shared.js:15-52) on the inputs the fixtures exercise.
func TestScopedQuotaSharedValueSemantics(t *testing.T) {
	for raw, truthy := range map[string]bool{`0`: false, `-0`: false, `""`: false, `false`: false, `null`: false, ``: false, `{}`: true, `[]`: true, `"0"`: true, `1`: true} {
		if shared.Truthy(json.RawMessage(raw)) != truthy {
			t.Fatalf("Truthy(%s)", raw)
		}
	}
	for raw, want := range map[string]string{`"1792540800"`: "2026-10-21T00:00:00Z", `1792540800000`: "2026-10-21T00:00:00Z", `1792540800000.9`: "2026-10-21T00:00:00Z",
		`"2026-11-01"`: "2026-11-01T00:00:00Z", `"Wed, 21 Oct 2026 00:00:00 GMT"`: "2026-10-21T00:00:00Z", `"2026-10-21T00:00:00.123456Z"`: "2026-10-21T00:00:00.123Z"} {
		got := shared.ResetTime(json.RawMessage(raw))
		if got == nil || got.Format(time.RFC3339Nano) != want {
			t.Fatalf("ResetTime(%s)=%v, want %s", raw, got, want)
		}
	}
	for _, raw := range []string{`0`, `""`, `"not a date"`, `true`, `{}`, `1e20`} {
		if got := shared.ResetTime(json.RawMessage(raw)); got != nil {
			t.Fatalf("ResetTime(%s)=%v", raw, got)
		}
	}
	if got := shared.DateTime(json.RawMessage(`"1792540800"`)); got != nil {
		t.Fatalf("DateTime read a digit string as a timestamp: %v", got)
	}
	for raw, want := range map[string]string{`" 42 "`: "42", `"1e3"`: "1000", `7`: "7"} {
		got, ok := shared.FiniteNumber(json.RawMessage(raw))
		if !ok || strconv.FormatFloat(got, 'f', -1, 64) != want {
			t.Fatalf("FiniteNumber(%s)=%v %v", raw, got, ok)
		}
	}
	for _, raw := range []string{`"abc"`, `""`, `"Infinity"`, `null`, `true`} {
		if _, ok := shared.FiniteNumber(json.RawMessage(raw)); ok {
			t.Fatalf("FiniteNumber(%s) accepted", raw)
		}
	}
}
