package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// observedApp is an app with observability on and its usage writer running, over
// one provider per model and an injected upstream.
func observedApp(t *testing.T, upstream *upstreamLog, models []string, settings map[string]string) (*App, string, func()) {
	t.Helper()
	a, key := multiProviderApp(t, upstream, models)
	return a, key, observe(t, a, settings)
}

// observe turns observability on for an app and returns its flush.
func observe(t *testing.T, a *App, settings map[string]string) func() {
	t.Helper()
	values := map[string]string{"enableObservability": "true", "observabilityFlushIntervalMs": "20"}
	for k, v := range settings {
		values[k] = v
	}
	if _, err := a.runtime.SetSettings(context.Background(), values, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go a.usage.Run(ctx)
	flush := func() {
		t.Helper()
		flushCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := a.usage.FlushNow(flushCtx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { a.usage.Close(); cancel(); a.usage.Wait() })
	return flush
}

func usageBody(prompt, completion int, text string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, text, prompt, completion)
}

type usageRow struct {
	Provider, Model, Requested, Selected, Account sql.NullString
	Status                                        int
	In, Out                                       int64
	TTFT, Duration                                sql.NullInt64
	Cost                                          float64
}

func lastUsage(t *testing.T, a *App) usageRow {
	t.Helper()
	var row usageRow
	err := a.store.DB().QueryRow(`SELECT provider_id,model_id,requested_model,selected_model,connection_id,status,input_tokens,output_tokens,ttft_ms,duration_ms,cost FROM usage_events ORDER BY id DESC LIMIT 1`).
		Scan(&row.Provider, &row.Model, &row.Requested, &row.Selected, &row.Account, &row.Status, &row.In, &row.Out, &row.TTFT, &row.Duration, &row.Cost)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestServedRequestIsAccountedWithNamesTTFTAndCost(t *testing.T) {
	upstream := &upstreamLog{script: func(model string, call int) (time.Duration, int, string) {
		return 30 * time.Millisecond, 200, usageBody(1000, 500, "hello")
	}}
	a, key, flush := observedApp(t, upstream, []string{"gpt-4o"}, nil)
	// An alias: the client asks for "fast", the router selects pgpt-4o/gpt-4o.
	if err := a.runtime.PutAlias(context.Background(), "fast", runtime.ModelRef{ProviderID: "pgpt-4o", ModelID: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	if w := post(a, key, fmt.Sprintf(textRequest, "fast")); w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	flush()
	row := lastUsage(t, a)
	if row.Requested.String != "fast" || row.Selected.String != "pgpt-4o/gpt-4o" || row.Model.String != "gpt-4o" || row.Provider.String != "pgpt-4o" {
		t.Fatalf("names: requested=%q selected=%q upstream=%q provider=%q", row.Requested.String, row.Selected.String, row.Model.String, row.Provider.String)
	}
	if !row.TTFT.Valid || row.TTFT.Int64 < 25 || row.TTFT.Int64 != row.Duration.Int64 {
		t.Fatalf("ttft=%v duration=%v: a non-streamed answer's TTFT is its duration", row.TTFT, row.Duration)
	}
	// gpt-4o in the default catalog: $2.50 in, $10 out per million tokens.
	if want := 1000*2.5/1e6 + 500*10.0/1e6; row.Cost < want-1e-12 || row.Cost > want+1e-12 {
		t.Fatalf("cost %v, want %v", row.Cost, want)
	}
	var day float64
	if err := a.store.DB().QueryRow(`SELECT COALESCE(SUM(cost),0) FROM usage_daily`).Scan(&day); err != nil || day != row.Cost {
		t.Fatalf("daily cost %v (%v), want %v", day, err, row.Cost)
	}
}

func detailRow(t *testing.T, a *App, where string, args ...any) (map[string]any, sql.NullString, sql.NullString, string, bool) {
	t.Helper()
	var raw string
	var provider, status sql.NullString
	err := a.store.DB().QueryRow(`SELECT detail,provider_id,status FROM request_details WHERE `+where, args...).Scan(&raw, &provider, &status)
	if err == sql.ErrNoRows {
		return nil, provider, status, "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("stored detail is not JSON: %v: %s", err, raw)
	}
	return payload, provider, status, raw, true
}

func countRows(t *testing.T, a *App, table string) int {
	t.Helper()
	var count int
	if err := a.store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A served request leaves a bounded detail with the reference's fields.
func TestServedRequestLeavesARequestDetail(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) {
		return 0, 200, usageBody(30, 12, "the answer")
	}}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, nil)
	w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"palpha/alpha","max_tokens":77,"temperature":0.2,"messages":[{"role":"user","content":"question"}],"unrelated":{"x":1}}`)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	flush()
	detail, provider, status, _, ok := detailRow(t, a, "1=1")
	if !ok || provider.String != "palpha" || status.String != "success" {
		t.Fatalf("detail row: ok=%v provider=%q status=%q", ok, provider.String, status.String)
	}
	for _, field := range []string{"provider", "model", "connectionId", "requestedModel", "selectedModel", "timestamp", "status", "latency", "tokens", "request", "providerRequest", "providerResponse", "response", "routeMode"} {
		if _, has := detail[field]; !has {
			t.Errorf("detail lacks %q: %v", field, detail)
		}
	}
	request := detail["request"].(map[string]any)
	if request["max_tokens"] != float64(77) || request["temperature"] != 0.2 || request["model"] != "palpha/alpha" {
		t.Errorf("request config: %v (a token count is a number, not a secret)", request)
	}
	if _, kept := request["unrelated"]; kept {
		t.Errorf("only the conversation and the known parameters belong in the request config: %v", request)
	}
	if messages := request["messages"].([]any); len(messages) != 1 {
		t.Errorf("messages: %v", messages)
	}
	response := detail["response"].(map[string]any)
	if response["content"] != "the answer" || response["finish_reason"] != "stop" {
		t.Errorf("response: %v", response)
	}
	tokens := detail["tokens"].(map[string]any)
	if tokens["prompt_tokens"] != float64(30) || tokens["completion_tokens"] != float64(12) {
		t.Errorf("tokens: %v", tokens)
	}
	if detail["routeMode"] != "native" || detail["requestedModel"] != "palpha/alpha" || detail["selectedModel"] != "palpha/alpha" {
		t.Errorf("route: %v", detail)
	}
	latency := detail["latency"].(map[string]any)
	if latency["total"].(float64) < 0 || latency["ttft"].(float64) != latency["total"].(float64) {
		t.Errorf("latency %v: a non-streamed answer's TTFT is its total", latency)
	}
	if countRows(t, a, "usage_events") != 1 {
		t.Error("the usage record is separate and still written")
	}
}

// Nothing is captured or stored while observability is off.
func TestNothingIsCapturedWhileObservabilityIsOff(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(3, 2, "x") }}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, map[string]string{"enableObservability": "false"})
	if w := post(a, key, fmt.Sprintf(textRequest, "palpha/alpha")); w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	flush()
	if countRows(t, a, "request_details") != 0 || countRows(t, a, "usage_events") != 0 {
		t.Fatalf("rows were written with observability off: details=%d usage=%d", countRows(t, a, "request_details"), countRows(t, a, "usage_events"))
	}
}

// PRD-OBS-002: no credential reaches a stored detail, wherever it appears.
func TestRequestDetailNeverHoldsACredential(t *testing.T) {
	// The upstream echoes the key it was called with into its error and its answer.
	var echoed string
	upstream := &upstreamLog{custom: func(w http.ResponseWriter, r *http.Request, model string) bool {
		echoed = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"your key is %s and Bearer abcdefghijklmnop"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2},"api_key":"%s","authorization":"x"}`,
			strings.TrimPrefix(echoed, "Bearer "), strings.TrimPrefix(echoed, "Bearer "))
		return true
	}}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, nil)
	body := `{"model":"palpha/alpha","api_key":"sk-in-the-body-0123456789","metadata":{"refresh_token":"rt-body-secret","note":"cookie: session=abc"},` +
		`"messages":[{"role":"user","content":"my provider key is synthetic-palpha and header Bearer zzzzzzzzzzzzzzzz1"}]}`
	if w := serveRequest(a.Handler(), key, "/v1/chat/completions", body); w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	flush()
	_, _, _, raw, ok := detailRow(t, a, "1=1")
	if !ok {
		t.Fatal("no detail")
	}
	if !strings.Contains(echoed, "synthetic-palpha") {
		t.Fatalf("the test premise failed: the upstream saw %q", echoed)
	}
	for _, leak := range []string{"synthetic-palpha", "sk-in-the-body-0123456789", "rt-body-secret", "zzzzzzzzzzzzzzzz1", "abcdefghijklmnop", key} {
		if strings.Contains(raw, leak) {
			t.Errorf("stored detail holds %q:\n%s", leak, raw)
		}
	}
	// What is not a credential is still there.
	if !strings.Contains(raw, "my provider key is [redacted]") || !strings.Contains(raw, "your key is [redacted]") {
		t.Errorf("redaction should leave the sentence around the credential:\n%s", raw)
	}
	// And the same through the API a person reads it by.
	cookie := c03Login(t, a)
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/requests", nil)
	req.AddCookie(cookie)
	listed := c03Do(a, req)
	if listed.Code != 200 {
		t.Fatalf("list status=%d %s", listed.Code, listed.Body)
	}
	for _, leak := range []string{"synthetic-palpha", "sk-in-the-body", "rt-body-secret"} {
		if strings.Contains(listed.Body.String(), leak) {
			t.Errorf("the detail list leaks %q", leak)
		}
	}
}

// Each captured field is held to the limit; one over it keeps its size and the
// first 200 characters (requestDetailsRepo.js:94-100).
func TestOversizeFieldsAreTruncatedPerField(t *testing.T) {
	big := strings.Repeat("0123456789", 100) // 1000 characters
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(3, 2, "short") }}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, map[string]string{"observabilityMaxJsonSize": "600"})
	body := fmt.Sprintf(`{"model":"palpha/alpha","messages":[{"role":"user","content":%q}]}`, big)
	if w := serveRequest(a.Handler(), key, "/v1/chat/completions", body); w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	flush()
	detail, _, _, _, ok := detailRow(t, a, "1=1")
	if !ok {
		t.Fatal("no detail")
	}
	request := detail["request"].(map[string]any)
	if request["_truncated"] != true || request["_originalSize"].(float64) < 1000 {
		t.Fatalf("request not truncated with its size: %v", request)
	}
	if preview := request["_preview"].(string); len([]rune(preview)) != 200 {
		t.Fatalf("preview has %d characters", len([]rune(preview)))
	}
	if _, truncated := detail["response"].(map[string]any)["_truncated"]; truncated {
		t.Fatal("a field within the limit must not be truncated")
	}
	if detail["providerResponse"].(map[string]any)["choices"] == nil {
		t.Fatalf("the provider response within the limit is kept whole: %v", detail["providerResponse"])
	}
}

// Retention by count: only the newest observabilityMaxDetailRecords details stay.
func TestDetailRetentionKeepsTheNewestCount(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(3, 2, "x") }}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, map[string]string{"observabilityMaxDetailRecords": "3"})
	var ids []string
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("req-%02d", i)
		ids = append(ids, id)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(textRequest, "palpha/alpha")))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("X-Request-Id", id)
		if w := c03Do(a, r); w.Code != 200 {
			t.Fatalf("status=%d", w.Code)
		}
		flush()
		time.Sleep(3 * time.Millisecond) // distinct creation times
	}
	rows, err := a.store.DB().Query(`SELECT request_id FROM request_details ORDER BY created_at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kept []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		kept = append(kept, id)
	}
	if strings.Join(kept, ",") != strings.Join(ids[4:], ",") {
		t.Fatalf("kept %v, want the newest three %v", kept, ids[4:])
	}
	// Usage accounting is not pruned by the detail limit.
	if countRows(t, a, "usage_events") != 7 {
		t.Fatalf("usage rows %d, want all 7", countRows(t, a, "usage_events"))
	}
	// Raising the limit applies to the next write without a restart.
	if _, err := a.runtime.SetSettings(context.Background(), map[string]string{"observabilityMaxDetailRecords": "5"}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 7; i < 12; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(textRequest, "palpha/alpha")))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("X-Request-Id", fmt.Sprintf("req-%02d", i))
		c03Do(a, r)
		flush()
		time.Sleep(3 * time.Millisecond)
	}
	if got := countRows(t, a, "request_details"); got != 5 {
		t.Fatalf("%d details after raising the limit to 5", got)
	}
}

func adminGet(t *testing.T, a *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(c03Login(t, a))
	return c03Do(a, req)
}

// A stream's TTFT is the first upstream byte, earlier than the whole answer.
func TestStreamedRequestRecordsTimeToFirstByte(t *testing.T) {
	upstream := &upstreamLog{custom: func(w http.ResponseWriter, r *http.Request, model string) bool {
		time.Sleep(120 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(250 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n")
		return true
	}}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, nil)
	if w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"palpha/alpha","stream":true,"messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	flush()
	row := lastUsage(t, a)
	if !row.TTFT.Valid || row.TTFT.Int64 < 100 || row.TTFT.Int64 > row.Duration.Int64-150 {
		t.Fatalf("ttft=%v duration=%v: TTFT is the first chunk (~120 ms), the duration the whole stream (~370 ms)", row.TTFT, row.Duration)
	}
	detail, _, _, _, ok := detailRow(t, a, "1=1")
	if !ok {
		t.Fatal("no detail for the stream")
	}
	if got := detail["response"].(map[string]any)["content"]; got != "hello" {
		t.Errorf("streamed response summary %v, want the joined deltas", got)
	}
	if detail["request"].(map[string]any)["stream"] != true {
		t.Errorf("request config: %v", detail["request"])
	}
}

// A dispatch that never reached an upstream still leaves an error detail, and no
// usage record (chatCore.js:441-453).
func TestFailedDispatchLeavesAnErrorDetailOnly(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(1, 1, "x") }}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, nil)
	upstream.stop()
	if w := post(a, key, fmt.Sprintf(textRequest, "palpha/alpha")); w.Code < 400 {
		t.Fatalf("status=%d, want an error", w.Code)
	}
	flush()
	detail, _, status, _, ok := detailRow(t, a, "1=1")
	if !ok || status.String != "error" {
		t.Fatalf("detail ok=%v status=%q", ok, status.String)
	}
	if detail["latency"].(map[string]any)["ttft"] != float64(0) {
		t.Errorf("an error's TTFT is 0: %v", detail["latency"])
	}
	if n := countRows(t, a, "usage_events"); n != 0 {
		t.Errorf("%d usage rows for a request no provider answered", n)
	}
}

// The cache convention: Anthropic's input excludes cached tokens, OpenAI's
// includes them, and both price the same traffic the same (usageTracking.js:147-210).
func TestCostFollowsEachProtocolsCacheConvention(t *testing.T) {
	override := func(a *App, provider, model string) {
		t.Helper()
		if _, err := a.store.DB().Exec(`INSERT INTO pricing_overrides(provider_id,model_id,input_per_mtok,output_per_mtok,cache_read_per_mtok,cache_write_per_mtok) VALUES(?,?,3,15,0.3,3.75)`, provider, model); err != nil {
			t.Fatal(err)
		}
	}
	want := 100*3.0/1e6 + 50*15.0/1e6 + 400*0.3/1e6

	openai := &upstreamLog{script: func(string, int) (time.Duration, int, string) {
		return 0, 200, `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":500,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":400}}}`
	}}
	a, key, flush := observedApp(t, openai, []string{"modelx"}, nil)
	override(a, "pmodelx", "modelx")
	if w := post(a, key, fmt.Sprintf(textRequest, "pmodelx/modelx")); w.Code != 200 {
		t.Fatalf("openai status=%d", w.Code)
	}
	flush()
	if got := lastUsage(t, a).Cost; got < want-1e-12 || got > want+1e-12 {
		t.Errorf("OpenAI-style cost %v, want %v", got, want)
	}

	anthropic := &upstreamLog{script: nativeScript(anthropicProtocol)}
	b, bkey := nativeApp(t, anthropic, anthropicProtocol, []string{"modelx"}, nil)
	bflush := observe(t, b, nil)
	override(b, "pmodelx", "modelx")
	w := serveRequest(b.Handler(), bkey, "/v1/messages", `{"model":"pmodelx/modelx","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 {
		t.Fatalf("anthropic status=%d body=%s", w.Code, w.Body)
	}
	bflush()
	if got := lastUsage(t, b).Cost; got < want-1e-12 || got > want+1e-12 {
		t.Errorf("Anthropic-style cost %v, want %v (input excludes the cache)", got, want)
	}
}

// The live view shows a request while it runs and as recent once it ends; a
// failure names its provider for a short while; the push event is emitted.
func TestLiveViewTracksInFlightAndRecentRequests(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	upstream := &upstreamLog{custom: func(w http.ResponseWriter, r *http.Request, model string) bool {
		started <- struct{}{}
		<-gate
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, usageBody(7, 3, "done"))
		return true
	}}
	a, key, flush := observedApp(t, upstream, []string{"alpha"}, nil)
	done := make(chan int, 1)
	go func() { done <- post(a, key, fmt.Sprintf(textRequest, "palpha/alpha")).Code }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream was never called")
	}
	var snapshot struct {
		ActiveRequests []struct {
			Model, Provider, Account string
			Count                    int
		} `json:"activeRequests"`
		RecentRequests []struct{ Model, Provider, Status string } `json:"recentRequests"`
		ErrorProvider  string                                     `json:"errorProvider"`
	}
	read := func() {
		t.Helper()
		w := adminGet(t, a, "/admin/v1/usage/active")
		if w.Code != 200 {
			t.Fatalf("status=%d %s", w.Code, w.Body)
		}
		snapshot.ActiveRequests, snapshot.RecentRequests = nil, nil
		if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if len(snapshot.ActiveRequests) != 1 || snapshot.ActiveRequests[0].Provider != "palpha" || snapshot.ActiveRequests[0].Model != "alpha" || snapshot.ActiveRequests[0].Count != 1 {
		t.Fatalf("while in flight: %+v", snapshot)
	}
	close(gate)
	if code := <-done; code != 200 {
		t.Fatalf("status=%d", code)
	}
	flush()
	read()
	if len(snapshot.ActiveRequests) != 0 || len(snapshot.RecentRequests) != 1 || snapshot.RecentRequests[0].Status != "ok" {
		t.Fatalf("after completion: %+v", snapshot)
	}
	// A failure is remembered by provider for the error window.
	upstream.stop()
	post(a, key, fmt.Sprintf(textRequest, "palpha/alpha"))
	read()
	if snapshot.ErrorProvider != "palpha" {
		t.Fatalf("errorProvider %q after a failed request", snapshot.ErrorProvider)
	}
}

// The operator log line carries the three names, and no credential.
func TestRequestLogLineCorrelatesTheThreeModelNames(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(3, 2, "x") }}
	a, key, flush := observedApp(t, upstream, []string{"gpt-4o"}, nil)
	if err := a.runtime.PutAlias(context.Background(), "fast", runtime.ModelRef{ProviderID: "pgpt-4o", ModelID: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	if w := post(a, key, fmt.Sprintf(textRequest, "fast")); w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	flush()
	var w *httptest.ResponseRecorder
	// The line is written off the request path.
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		w = adminGet(t, a, "/admin/v1/logs?query=request+completed")
		if strings.Contains(w.Body.String(), "request completed") || time.Now().After(deadline) {
			break
		}
	}
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{"request completed", `"requested":"fast"`, `"selected":"pgpt-4o/gpt-4o"`, `"upstream":"gpt-4o"`} {
		if !strings.Contains(body, want) {
			t.Errorf("log lacks %s:\n%s", want, body)
		}
	}
	for _, leak := range []string{key, "synthetic-pgpt-4o"} {
		if strings.Contains(body, leak) {
			t.Errorf("log leaks %q", leak)
		}
	}
}

// The usage API shows the names and cost; the detail list filters.
func TestUsageAndDetailAPIsExposeNamesCostAndFilters(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(1000, 500, "x") }}
	a, key, flush := observedApp(t, upstream, []string{"gpt-4o", "other"}, nil)
	for _, model := range []string{"pgpt-4o/gpt-4o", "pother/other"} {
		if w := post(a, key, fmt.Sprintf(textRequest, model)); w.Code != 200 {
			t.Fatalf("status=%d", w.Code)
		}
	}
	flush()
	list := adminGet(t, a, "/admin/v1/usage")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"requestedModel":"pgpt-4o/gpt-4o"`) || !strings.Contains(list.Body.String(), `"selectedModel"`) || !strings.Contains(list.Body.String(), `"cost":0.0075`) {
		t.Fatalf("usage list lacks names/cost: %d %s", list.Code, list.Body)
	}
	for query, want := range map[string]int{"providerId=pgpt-4o": 1, "modelId=other": 1, "status=success": 2, "status=error": 0, "connectionId=account-pother": 1, "startDate=2999-01-01T00:00:00Z": 0} {
		w := adminGet(t, a, "/admin/v1/requests?"+query)
		if w.Code != 200 {
			t.Fatalf("%s: status=%d %s", query, w.Code, w.Body)
		}
		var out struct{ Items []json.RawMessage }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if len(out.Items) != want {
			t.Errorf("%s: %d items, want %d: %s", query, len(out.Items), want, w.Body)
		}
	}
}

// Every Fusion leg is its own accounted request with its own id, and each carries
// the Combo the client named as its requested model.
func TestFusionLegsAreAccountedSeparatelyUnderTheComboName(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(10, 5, "leg") }}
	a, key, flush := observedApp(t, upstream, []string{"a", "b", "j"}, nil)
	setFusion(t, a, "a", "b")
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"panel","messages":[{"role":"user","content":"q"}]}`))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("X-Request-Id", "fusion-1")
	if w := c03Do(a, r); w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body)
	}
	flush()
	rows, err := a.store.DB().Query(`SELECT request_id, requested_model, selected_model FROM usage_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id, requested, selected string
		_ = rows.Scan(&id, &requested, &selected)
		if seen[id] || !strings.HasPrefix(id, "fusion-1") {
			t.Errorf("request id %q is duplicated or lost the client's id", id)
		}
		seen[id] = true
		if requested != "panel" {
			t.Errorf("leg %s requested %q, want the Combo name", id, requested)
		}
		if selected == "" {
			t.Errorf("leg %s has no selected model", id)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("%d accounted legs (%v), want 2 panel + 1 judge", len(seen), seen)
	}
	if countRows(t, a, "request_details") != 3 {
		t.Errorf("%d details, want one per leg", countRows(t, a, "request_details"))
	}
}

// The live view pushes a debounced usage.pending event when the set changes.
func TestLiveViewPublishesAPendingEvent(t *testing.T) {
	upstream := &upstreamLog{script: func(string, int) (time.Duration, int, string) { return 0, 200, usageBody(1, 1, "x") }}
	a, key, _ := observedApp(t, upstream, []string{"alpha"}, nil)
	events, cancel := a.events.Subscribe()
	defer cancel()
	if w := post(a, key, fmt.Sprintf(textRequest, "palpha/alpha")); w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == "usage.pending" {
				if !strings.Contains(fmt.Sprint(event.Data), "palpha") && !strings.Contains(fmt.Sprintf("%+v", event.Data), "alpha") {
					t.Errorf("event carries no request: %+v", event.Data)
				}
				return
			}
		case <-deadline:
			t.Fatal("no usage.pending event")
		}
	}
}
