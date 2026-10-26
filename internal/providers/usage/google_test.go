package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// #221 evidence for the Cloud Code readers, pinned to oracle 264a26d0:
// open-sse/services/usage/google.js and antigravity-weekly.js. Envelopes come
// from testdata/antigravity.json and testdata/gemini-cli.json (provenance in
// testdata/README.md); each test varies one method's reply. Nothing here touches
// a network.

var googleNow = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

// cloudCodeReply is one scripted answer. A non-nil err is a transport failure.
type cloudCodeReply struct {
	status int
	body   string
	err    error
}

// cloudCodeClient answers by Cloud Code method (the path after the colon) and
// records every request with its body.
type cloudCodeClient struct {
	replies  map[string]cloudCodeReply
	requests []*http.Request
	bodies   []string
}

func (c *cloudCodeClient) Do(request *http.Request) (*http.Response, error) {
	raw := ""
	if request.Body != nil {
		payload, _ := io.ReadAll(request.Body)
		raw = string(payload)
	}
	c.requests = append(c.requests, request)
	c.bodies = append(c.bodies, raw)
	method := request.URL.Path[strings.LastIndex(request.URL.Path, ":")+1:]
	reply, ok := c.replies[method]
	if !ok {
		reply = cloudCodeReply{status: http.StatusNotFound, body: `{}`}
	}
	if reply.err != nil {
		return nil, reply.err
	}
	return &http.Response{StatusCode: reply.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply.body))}, nil
}

func (c *cloudCodeClient) methods() []string {
	var out []string
	for _, request := range c.requests {
		out = append(out, request.URL.Host+request.URL.Path)
	}
	return out
}

// cloudCodeFixture loads a testdata envelope set as 200 replies.
func cloudCodeFixture(t *testing.T, name string) *cloudCodeClient {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var envelopes map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelopes); err != nil {
		t.Fatal(err)
	}
	client := &cloudCodeClient{replies: map[string]cloudCodeReply{}}
	for method, body := range envelopes {
		client.replies[method] = cloudCodeReply{status: http.StatusOK, body: string(body)}
	}
	return client
}

func googleUsage(t *testing.T, id string, client *cloudCodeClient, credential quota.Credential) (quota.State, error) {
	t.Helper()
	readers, err := Readers(client, func() time.Time { return googleNow })
	if err != nil {
		t.Fatal(err)
	}
	return readers[id].Usage(context.Background(), credential)
}

func agUsage(t *testing.T, client *cloudCodeClient) (quota.State, error) {
	t.Helper()
	return googleUsage(t, "antigravity", client, quota.Credential{AccessToken: "fixture-token"})
}

func wantWindow(t *testing.T, state quota.State, name, display string, remaining float64, reset string) {
	t.Helper()
	windows := byName(state)
	wantRemaining(t, windows, name, remaining)
	window := windows[name]
	if window.DisplayName != display {
		t.Fatalf("window %q displayName=%q want %q", name, window.DisplayName, display)
	}
	if reset == "" {
		if window.ResetAt != nil {
			t.Fatalf("window %q reset=%v want none", name, window.ResetAt)
		}
		return
	}
	if window.ResetAt == nil || window.ResetAt.Format(time.RFC3339) != reset {
		t.Fatalf("window %q reset=%v want %s", name, window.ResetAt, reset)
	}
}

func windowNames(state quota.State) []string {
	var names []string
	for _, window := range state.Windows {
		names = append(names, window.Name)
	}
	return names
}

func wantNoSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "fixture-token") {
		t.Fatalf("error exposes the credential: %v", err)
	}
}

// google.js:120-221,266-275 and antigravity-weekly.js:116-146: the paid golden
// read. Project discovery is on the production host, model and summary quota
// on the daily host; every request carries the IDE user agent, the two quota
// reads add the client name and version, and none carries the router-only
// x-request-source header (tests/unit/antigravity-usage-headers.test.js:22-33).
func TestAntigravityPinnedPaidRead(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	state, err := agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		"daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		"daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	}
	if got := client.methods(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("requests=%v", got)
	}
	for index, request := range client.requests {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer fixture-token" ||
			request.Header.Get("User-Agent") != "antigravity/ide/2.11.0 darwin/arm64" ||
			request.Header.Get("x-request-source") != "" {
			t.Fatalf("request %d headers=%v", index, request.Header)
		}
		client := request.Header.Get("X-Client-Name") == "antigravity" && request.Header.Get("X-Client-Version") == "2.11.0"
		if client != (index > 0) {
			t.Fatalf("request %d client headers=%v", index, request.Header)
		}
	}
	var load struct {
		Metadata map[string]int `json:"metadata"`
		Mode     int            `json:"mode"`
	}
	if err := json.Unmarshal([]byte(client.bodies[0]), &load); err != nil || load.Mode != 1 ||
		load.Metadata["ideType"] != 9 || load.Metadata["pluginType"] != 2 {
		t.Fatalf("loadCodeAssist body=%s", client.bodies[0])
	}
	if !strings.Contains(client.bodies[0], `"platform":`) {
		t.Fatalf("loadCodeAssist body lacks the platform enum: %s", client.bodies[0])
	}
	for _, body := range client.bodies[1:] {
		if body != `{"project":"p1"}` {
			t.Fatalf("quota body=%s", body)
		}
	}
	if state.Plan != "Pro" {
		t.Fatalf("plan=%q", state.Plan)
	}
	// Response order, then the weekly overlay; the internal model and the one
	// outside the allowlist are dropped.
	order := "gemini-3.8-flash-high,claude-opus-4-6-thinking,gemini-pro-agent,gemini_weekly,gemini_session,claude_gpt_weekly"
	if got := strings.Join(windowNames(state), ","); got != order {
		t.Fatalf("windows=%s", got)
	}
	wantWindow(t, state, "gemini-3.8-flash-high", "Gemini 3.8 Flash (High)", 0.85, "2026-09-15T00:00:00Z")
	wantWindow(t, state, "claude-opus-4-6-thinking", "Claude Opus 4.6 (Thinking)", 0.9, "2026-09-08T00:00:00Z")
	wantWindow(t, state, "gemini-pro-agent", "gemini-pro-agent", 0.8, "2026-09-08T00:00:00Z")
	wantWindow(t, state, "gemini_weekly", "Gemini (Weekly)", 0.75, "2026-09-15T00:00:00Z")
	wantWindow(t, state, "gemini_session", "Gemini (5h)", 0.9, "2026-09-09T00:00:00Z")
	wantWindow(t, state, "claude_gpt_weekly", "Claude & GPT (Weekly)", 0.5, "2026-09-14T00:00:00Z")
	if state.DeriveStatus(googleNow) != quota.StatusAvailable {
		t.Fatalf("status=%s", state.DeriveStatus(googleNow))
	}
}

// antigravity-weekly-quota.test.js:48-53,88-94: groups may sit under
// quotaSummary, and the family comes from displayName, not name.
func TestAntigravityWeeklyGroupShapes(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	summary := client.replies["retrieveUserQuotaSummary"].body
	client.replies["retrieveUserQuotaSummary"] = cloudCodeReply{status: http.StatusOK, body: `{"quotaSummary":` + summary + `}`}
	state, err := agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	wantWindow(t, state, "gemini_weekly", "Gemini (Weekly)", 0.75, "2026-09-15T00:00:00Z")
	wantWindow(t, state, "claude_gpt_weekly", "Claude & GPT (Weekly)", 0.5, "2026-09-14T00:00:00Z")

	// A group named for a family but displayed as something else is no family.
	client.replies["retrieveUserQuotaSummary"] = cloudCodeReply{status: http.StatusOK, body: `{"groups":[
		{"name":"gemini","displayName":"Unknown AI Provider","buckets":[{"window":"weekly","remainingFraction":0.5}]}]}`}
	state, err = agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byName(state)["gemini_weekly"]; ok {
		t.Fatalf("group.name matched a family: %v", windowNames(state))
	}
}

// antigravity-weekly.js:71-84,96-114 and :150-162: window detection by window
// or bucket text, first bucket per family and window wins, a disabled weekly
// bucket is dropped, a disabled session bucket is an exhausted row, a null
// fraction is zero (Number(null)), and an absent or non-numeric one is skipped.
func TestAntigravityWeeklyBucketSemantics(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	client.replies["retrieveUserQuotaSummary"] = cloudCodeReply{status: http.StatusOK, body: `{"groups":[
		{"displayName":"Gemini Models","buckets":[
			{"bucketId":"gemini-monthly","displayName":"Monthly Limit","remainingFraction":0.9},
			{"window":"WEEKLY","remainingFraction":"0.4","resetTime":"2026-09-15T00:00:00Z","disabled":"true"},
			{"window":"weekly","remainingFraction":0.2,"resetTime":"2026-09-16T00:00:00Z"},
			{"window":"5h","remainingFraction":0.6,"resetTime":"2026-09-10T05:00:00Z","disabled":true}]},
		{"displayName":"Claude and GPT models","buckets":[
			{"bucketId":"3p-weekly","displayName":"Weekly Limit Remaining","remainingFraction":0.3,"disabled":true},
			{"bucketId":"3p-five","displayName":"Five Hour Limit","remainingFraction":"not-a-number"},
			{"bucketId":"3p-five-2","displayName":"Five Hour Limit","resetTime":"2026-09-10T05:00:00Z"},
			{"bucketId":"3p-weekly-2","displayName":"Weekly Limit Remaining","remainingFraction":null,"resetTime":"2026-09-14T00:00:00Z"}]},
		{"displayName":"Imagen","buckets":[{"window":"weekly","remainingFraction":0.5}]},
		"not-a-group", null]}`}
	state, err := agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	// "disabled":"true" is not disabled === true, so the first weekly bucket wins.
	wantWindow(t, state, "gemini_weekly", "Gemini (Weekly)", 0.4, "2026-09-15T00:00:00Z")
	wantWindow(t, state, "gemini_session", "Gemini (5h)", 0, "2026-09-10T05:00:00Z")
	wantWindow(t, state, "claude_gpt_weekly", "Claude & GPT (Weekly)", 0, "2026-09-14T00:00:00Z")
	if _, ok := byName(state)["claude_gpt_session"]; ok {
		t.Fatalf("unreadable session fraction became a row: %v", windowNames(state))
	}
	if state.DeriveStatus(googleNow) != quota.StatusExhausted {
		t.Fatalf("an exhausted pool with a future reset must exhaust: %s", state.DeriveStatus(googleNow))
	}
}

// antigravity-weekly-quota.test.js:339-413 and google.js:161-169: a free-tier
// account still asks for model quota but reads only the weekly pools.
func TestAntigravityFreeTierReadsOnlyWeekly(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	client.replies["loadCodeAssist"] = cloudCodeReply{status: http.StatusOK, body: `{"cloudaicompanionProject":"p1",
		"currentTier":{"name":"Starter"},"paidTier":{"id":"free-tier","name":"Antigravity Starter Quota"}}`}
	client.replies["retrieveUserQuotaSummary"] = cloudCodeReply{status: http.StatusOK, body: `{"groups":[
		{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-weekly","displayName":"Weekly Limit Remaining","remainingFraction":1,"resetTime":"2026-09-15T00:00:00Z"}]},
		{"displayName":"Claude and GPT models","buckets":[{"bucketId":"3p-weekly","displayName":"Weekly Limit Remaining","remainingFraction":0,"resetTime":"2026-09-13T12:00:00Z"}]}]}`}
	state, err := agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 3 {
		t.Fatalf("requests=%v", client.methods())
	}
	if got := strings.Join(windowNames(state), ","); got != "gemini_weekly,claude_gpt_weekly" || state.Plan != "Starter" {
		t.Fatalf("plan=%q windows=%s", state.Plan, got)
	}
	wantWindow(t, state, "gemini_weekly", "Gemini (Weekly)", 1, "2026-09-15T00:00:00Z")
	wantWindow(t, state, "claude_gpt_weekly", "Claude & GPT (Weekly)", 0, "2026-09-13T12:00:00Z")
	if state.DeriveStatus(googleNow) != quota.StatusExhausted {
		t.Fatalf("status=%s", state.DeriveStatus(googleNow))
	}
	// A spent pool whose reset has passed no longer exhausts the account.
	if state.DeriveStatus(time.Date(2026, 9, 13, 12, 0, 1, 0, time.UTC)) != quota.StatusAvailable {
		t.Fatal("an expired exhausted window still exhausts")
	}
}

// antigravity-weekly-quota.test.js:415-490 and google.js:203,231-264: a paid
// model with no fraction is exhausted, and when every model of a family is at
// zero the 5h row follows them with the latest model reset; the weekly row is
// untouched. A family with any remaining model leaves its 5h row alone.
func TestAntigravityReconcilesSessionWithExhaustedModels(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	client.replies["fetchAvailableModels"] = cloudCodeReply{status: http.StatusOK, body: `{"models":{
		"gemini-3.8-flash-high":{"displayName":"Gemini 3.8 Flash (High)","quotaInfo":{"resetTime":"2026-09-13T12:00:00Z"}},
		"gemini-3.8-flash-low":{"quotaInfo":{"remainingFraction":0,"resetTime":"2026-09-13T10:00:00Z"}},
		"gemini-3.1-flash-image":{"quotaInfo":{"remainingFraction":0.5}},
		"claude-sonnet-4-6":{"quotaInfo":{"remainingFraction":0.4,"resetTime":"2026-09-13T12:00:00Z"}}}}`}
	client.replies["retrieveUserQuotaSummary"] = cloudCodeReply{status: http.StatusOK, body: `{"groups":[
		{"displayName":"Gemini Models","buckets":[
			{"bucketId":"gemini-weekly","displayName":"Weekly Limit Remaining","window":"weekly","remainingFraction":0.75,"resetTime":"2026-09-15T00:00:00Z"},
			{"bucketId":"gemini-5h","displayName":"Five Hour Limit Remaining","window":"5h","remainingFraction":1,"resetTime":"2026-09-13T11:00:00Z"}]},
		{"displayName":"Claude and GPT models","buckets":[
			{"window":"5h","remainingFraction":0.7,"resetTime":"2026-09-13T11:00:00Z"}]}]}`}
	state, err := agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	wantWindow(t, state, "gemini-3.8-flash-high", "Gemini 3.8 Flash (High)", 0, "2026-09-13T12:00:00Z")
	wantWindow(t, state, "gemini_session", "Gemini (5h)", 0, "2026-09-13T12:00:00Z")
	wantWindow(t, state, "gemini_weekly", "Gemini (Weekly)", 0.75, "2026-09-15T00:00:00Z")
	wantWindow(t, state, "claude_gpt_session", "Claude & GPT (5h)", 0.7, "2026-09-13T11:00:00Z")
}

// google.js:140-156,276-279: a rejected or failed model read fails the whole
// read before the weekly overlay is requested; the error never carries the
// credential. A transport timeout is a failure, not an exhausted account.
func TestAntigravityModelReadFailures(t *testing.T) {
	timeout := &url.Error{Op: "Post", URL: antigravityModelsURL, Err: context.DeadlineExceeded}
	for name, reply := range map[string]cloudCodeReply{
		"401":       {status: http.StatusUnauthorized, body: `{"error":{"code":401}}`},
		"403":       {status: http.StatusForbidden, body: `{"error":{"code":403}}`},
		"500":       {status: http.StatusInternalServerError, body: `{}`},
		"timeout":   {err: timeout},
		"malformed": {status: http.StatusOK, body: `{"models":`},
		"empty":     {status: http.StatusOK, body: ``},
	} {
		t.Run(name, func(t *testing.T) {
			client := cloudCodeFixture(t, "antigravity")
			client.replies["fetchAvailableModels"] = reply
			state, err := agUsage(t, client)
			if err == nil {
				t.Fatalf("state=%+v", state)
			}
			wantNoSecret(t, err)
			if len(client.requests) != 2 {
				t.Fatalf("weekly requested after a failed model read: %v", client.methods())
			}
		})
	}
}

// antigravity-weekly-quota.test.js:197-233,297-337: every weekly failure leaves
// the per-model rows; with nothing at all to report the read fails rather than
// returning a plan with no windows.
func TestAntigravityWeeklyFailuresKeepModels(t *testing.T) {
	timeout := &url.Error{Op: "Post", URL: antigravitySummaryURL, Err: context.DeadlineExceeded}
	for name, reply := range map[string]cloudCodeReply{
		"401":       {status: http.StatusUnauthorized, body: `{}`},
		"429":       {status: http.StatusTooManyRequests, body: `{}`},
		"timeout":   {err: timeout},
		"malformed": {status: http.StatusOK, body: `{"groups":`},
		"empty":     {status: http.StatusOK, body: `{"groups":[]}`},
		"no groups": {status: http.StatusOK, body: `{"groups":"not-array","quotaSummary":{}}`},
	} {
		t.Run(name, func(t *testing.T) {
			client := cloudCodeFixture(t, "antigravity")
			client.replies["retrieveUserQuotaSummary"] = reply
			state, err := agUsage(t, client)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(windowNames(state), ","); got != "gemini-3.8-flash-high,claude-opus-4-6-thinking,gemini-pro-agent" {
				t.Fatalf("windows=%s", got)
			}
		})
	}
	client := cloudCodeFixture(t, "antigravity")
	client.replies["fetchAvailableModels"] = cloudCodeReply{status: http.StatusOK, body: `{"models":{}}`}
	client.replies["retrieveUserQuotaSummary"] = cloudCodeReply{status: http.StatusOK, body: `{"groups":[]}`}
	if state, err := agUsage(t, client); err == nil {
		t.Fatalf("empty read reported %+v", state)
	}
}

// google.js:122-124,285-302: a failed loadCodeAssist leaves the plan Unknown,
// treats the account as free tier and sends no project. A project object is
// passed through as loadCodeAssist returned it.
func TestAntigravitySubscriptionFallbacks(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	client.replies["loadCodeAssist"] = cloudCodeReply{status: http.StatusUnauthorized, body: `{}`}
	state, err := agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan != "Unknown" || client.bodies[1] != `{}` || client.bodies[2] != `{}` {
		t.Fatalf("plan=%q bodies=%q", state.Plan, client.bodies)
	}
	if _, ok := byName(state)["gemini-3.8-flash-high"]; ok {
		t.Fatalf("unknown tier parsed per-model quota: %v", windowNames(state))
	}

	client = cloudCodeFixture(t, "antigravity")
	client.replies["loadCodeAssist"] = cloudCodeReply{status: http.StatusOK, body: `{"cloudaicompanionProject":{"id":"p2"},"paidTier":{"id":"g1-pro-tier"}}`}
	state, err = agUsage(t, client)
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan != "Unknown" || client.bodies[1] != `{"project":{"id":"p2"}}` {
		t.Fatalf("plan=%q bodies=%q", state.Plan, client.bodies)
	}
}

// google.js:22-90: the stored project is used directly, so the read is one
// request and the plan stays Free.
func TestGeminiPinnedStoredProject(t *testing.T) {
	client := cloudCodeFixture(t, "gemini-cli")
	state, err := googleUsage(t, "gemini-cli", client, quota.Credential{
		AccessToken: "fixture-token", ProviderData: map[string]string{"projectId": " cloud-code-project "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.methods(); len(got) != 1 || got[0] != "cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota" {
		t.Fatalf("requests=%v", got)
	}
	request := client.requests[0]
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer fixture-token" ||
		request.Header.Get("Content-Type") != "application/json" || client.bodies[0] != `{"project":"cloud-code-project"}` {
		t.Fatalf("request=%s %v %s", request.Method, request.Header, client.bodies[0])
	}
	if state.Plan != "Free" || len(state.Windows) != 1 {
		t.Fatalf("state=%+v", state)
	}
	wantWindow(t, state, "gemini-3-flash-preview", "", 0.75, "2026-05-25T12:00:00Z")
}

// google.js:33-37,95-115 and gemini-usage-projectid.test.js:54-74: without a
// stored project, loadCodeAssist supplies the project (object or string) and
// the tier name becomes the plan.
func TestGeminiDiscoversProjectAndTier(t *testing.T) {
	client := cloudCodeFixture(t, "gemini-cli")
	client.replies["loadCodeAssist"] = cloudCodeReply{status: http.StatusOK, body: `{"cloudaicompanionProject":{"id":"project-from-load"},"currentTier":{"name":"Standard"}}`}
	state, err := googleUsage(t, "gemini-cli", client, quota.Credential{AccessToken: "fixture-token"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cloudcode-pa.googleapis.com/v1internal:loadCodeAssist,cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	if got := strings.Join(client.methods(), ","); got != want {
		t.Fatalf("requests=%s", got)
	}
	var load struct {
		Metadata map[string]int `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(client.bodies[0]), &load); err != nil || load.Metadata["ideType"] != 9 ||
		load.Metadata["pluginType"] != 2 || !strings.Contains(client.bodies[0], `"platform":`) || strings.Contains(client.bodies[0], "mode") {
		t.Fatalf("loadCodeAssist body=%s", client.bodies[0])
	}
	if client.bodies[1] != `{"project":"project-from-load"}` || state.Plan != "Standard" {
		t.Fatalf("plan=%q body=%s", state.Plan, client.bodies[1])
	}

	// A tier without a name keeps the Free default.
	client = cloudCodeFixture(t, "gemini-cli")
	client.replies["loadCodeAssist"] = cloudCodeReply{status: http.StatusOK, body: `{"cloudaicompanionProject":"p3","currentTier":{}}`}
	state, err = googleUsage(t, "gemini-cli", client, quota.Credential{AccessToken: "fixture-token"})
	if err != nil || state.Plan != "Free" || client.bodies[1] != `{"project":"p3"}` {
		t.Fatalf("plan=%q err=%v body=%s", state.Plan, err, client.bodies[1])
	}
}

// google.js:39-44: no stored or discoverable project is actionable guidance
// before any quota request, whatever made discovery fail.
func TestGeminiMissingProjectFailsBeforeQuotaRead(t *testing.T) {
	timeout := &url.Error{Op: "Post", URL: cloudCodeLoadURL, Err: context.DeadlineExceeded}
	for name, reply := range map[string]cloudCodeReply{
		"no project": {status: http.StatusOK, body: `{}`},
		"401":        {status: http.StatusUnauthorized, body: `{}`},
		"500":        {status: http.StatusInternalServerError, body: `{}`},
		"timeout":    {err: timeout},
		"malformed":  {status: http.StatusOK, body: `{`},
		"blank id":   {status: http.StatusOK, body: `{"cloudaicompanionProject":{"id":"  "}}`},
	} {
		t.Run(name, func(t *testing.T) {
			client := cloudCodeFixture(t, "gemini-cli")
			client.replies["loadCodeAssist"] = reply
			_, err := googleUsage(t, "gemini-cli", client, quota.Credential{AccessToken: "fixture-token"})
			if err == nil || !strings.Contains(err.Error(), "reconnect") || !strings.Contains(err.Error(), "Gemini Code Assist") {
				t.Fatalf("err=%v", err)
			}
			wantNoSecret(t, err)
			if len(client.requests) != 1 {
				t.Fatalf("quota read without a project: %v", client.methods())
			}
		})
	}
}

// google.js:67-84: zero, expired, repeated and unreadable buckets.
func TestGeminiBucketSemantics(t *testing.T) {
	client := cloudCodeFixture(t, "gemini-cli")
	client.replies["retrieveUserQuota"] = cloudCodeReply{status: http.StatusOK, body: `{"buckets":[
		{"modelId":"gemini-2.5-pro","remainingFraction":0.5,"resetTime":"2026-09-11T00:00:00Z"},
		{"modelId":"gemini-2.5-flash","remainingFraction":0,"resetTime":"2026-09-11T00:00:00Z"},
		{"modelId":"gemini-2.5-flash-lite","remainingFraction":0,"resetTime":"2026-09-09T00:00:00Z"},
		{"modelId":"gemini-3-pro-preview","remainingFraction":"not-a-number"},
		{"modelId":"gemini-3-flash-preview","remainingFraction":null},
		{"modelId":"gemini-3.1-pro-preview"},
		{"remainingFraction":0.3},
		{"modelId":"gemini-2.5-pro","remainingFraction":"0.25","resetTime":"2026-09-12T00:00:00Z"}]}`}
	state, err := googleUsage(t, "gemini-cli", client, quota.Credential{
		AccessToken: "fixture-token", ProviderData: map[string]string{"projectId": "p1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(windowNames(state), ","); got != "gemini-2.5-pro,gemini-2.5-flash,gemini-2.5-flash-lite,gemini-3-pro-preview" {
		t.Fatalf("windows=%s", got)
	}
	wantWindow(t, state, "gemini-2.5-pro", "", 0.25, "2026-09-12T00:00:00Z")
	wantWindow(t, state, "gemini-3-pro-preview", "", 0, "")
	if state.DeriveStatus(googleNow) != quota.StatusExhausted {
		t.Fatalf("status=%s", state.DeriveStatus(googleNow))
	}
	// After gemini-2.5-flash resets, the unreadable bucket (Number(x) || 0, no
	// reset) still holds the account exhausted; the expired lite window never did.
	if state.DeriveStatus(time.Date(2026, 9, 11, 0, 0, 1, 0, time.UTC)) != quota.StatusExhausted {
		t.Fatal("an unreadable fraction with no reset must stay exhausted")
	}
}

// google.js:60-62,67,86-89: empty buckets keep the plan with no windows, which
// the read model shows as unknown; HTTP, transport and decode failures are
// errors that never carry the credential.
func TestGeminiQuotaReadOutcomes(t *testing.T) {
	credential := quota.Credential{AccessToken: "fixture-token", ProviderData: map[string]string{"projectId": "p1"}}
	for _, body := range []string{`{"buckets":[]}`, `{}`, `{"buckets":{"gemini-2.5-pro":{}}}`} {
		client := cloudCodeFixture(t, "gemini-cli")
		client.replies["retrieveUserQuota"] = cloudCodeReply{status: http.StatusOK, body: body}
		state, err := googleUsage(t, "gemini-cli", client, credential)
		if err != nil || state.Plan != "Free" || len(state.Windows) != 0 || state.DeriveStatus(googleNow) != quota.StatusUnknown {
			t.Fatalf("%s: state=%+v err=%v", body, state, err)
		}
	}
	timeout := &url.Error{Op: "Post", URL: geminiQuotaURL, Err: context.DeadlineExceeded}
	for name, reply := range map[string]cloudCodeReply{
		"401":       {status: http.StatusUnauthorized, body: `{}`},
		"403":       {status: http.StatusForbidden, body: `{}`},
		"429":       {status: http.StatusTooManyRequests, body: `{}`},
		"timeout":   {err: timeout},
		"malformed": {status: http.StatusOK, body: `{"buckets":`},
	} {
		t.Run(name, func(t *testing.T) {
			client := cloudCodeFixture(t, "gemini-cli")
			client.replies["retrieveUserQuota"] = reply
			state, err := googleUsage(t, "gemini-cli", client, credential)
			if err == nil {
				t.Fatalf("state=%+v", state)
			}
			wantNoSecret(t, err)
		})
	}
	client := cloudCodeFixture(t, "gemini-cli")
	if _, err := googleUsage(t, "gemini-cli", client, quota.Credential{ProviderData: map[string]string{"projectId": "p1"}}); err == nil || len(client.requests) != 0 {
		t.Fatalf("missing token: err=%v requests=%v", err, client.methods())
	}
}
