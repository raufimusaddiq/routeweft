package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func sseFrame(kind, data string) string { return "event: " + kind + "\ndata: " + data + "\n\n" }

const (
	codexCreated  = `{"type":"response.created","response":{"id":"resp_1","status":"in_progress","instructions":"never say model_at_capacity or server_is_overloaded"}}`
	codexDelta    = `{"type":"response.output_text.delta","delta":"Hi"}`
	codexComplete = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`
)

var (
	overloadedFrame = sseFrame("response.failed", `{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`)
	capacityFrame   = sseFrame("error", `{"type":"error","error":{"type":"invalid_request_error","code":"model_at_capacity","message":"Selected model is at capacity. Please try a different model."}}`)
)

// codexUpstream serves one scripted stream per request and records what it was sent.
type codexUpstream struct {
	*httptest.Server
	mu       sync.Mutex
	bodies   []string
	sessions []string
}

func newCodexUpstream(t *testing.T, script func(call int) (status int, contentType, body string)) *codexUpstream {
	t.Helper()
	u := &codexUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, string(raw))
		u.sessions = append(u.sessions, r.Header.Get("session_id"))
		call := len(u.bodies) - 1
		u.mu.Unlock()
		status, contentType, body := script(call)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Request-Id", "req-upstream")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *codexUpstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func codexRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(`{"model":"gpt-5","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("session_id", "thread-1")
	return request
}

// noWaiting replaces the retry delay and returns what was asked of it.
func noWaiting(t *testing.T) *[]time.Duration {
	t.Helper()
	var waited []time.Duration
	previous := codexSleep
	codexSleep = func(_ context.Context, delay time.Duration) error {
		waited = append(waited, delay)
		return nil
	}
	t.Cleanup(func() { codexSleep = previous })
	return &waited
}

func readAll(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(body)
}

func TestCodexCapacityGoesToAnotherAccountWithoutRepeating(t *testing.T) {
	waited := noWaiting(t)
	upstream := newCodexUpstream(t, func(int) (int, string, string) {
		return 200, "text/event-stream", sseFrame("response.created", codexCreated) + capacityFrame
	})
	response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+"/responses"))
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, response)
	var document struct {
		Error struct{ Message, Type, Code string }
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if response.StatusCode != 503 || document.Error.Message != "Selected model is at capacity. Please try a different model." ||
		document.Error.Type != "server_error" || document.Error.Code != "service_unavailable" {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	if upstream.calls() != 1 || len(*waited) != 0 {
		t.Fatalf("a capacity answer is repeated: %d calls, waited %v", upstream.calls(), *waited)
	}
	if response.Header.Get("Content-Type") != "application/json" || response.Header.Get("X-Request-Id") != "req-upstream" {
		t.Fatalf("headers %v", response.Header)
	}
}

func TestCodexOverloadIsRepeatedThreeTimesTwoSecondsApart(t *testing.T) {
	waited := noWaiting(t)
	upstream := newCodexUpstream(t, func(int) (int, string, string) {
		return 200, "text/event-stream", sseFrame("response.created", codexCreated) + overloadedFrame
	})
	response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+"/responses"))
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, response)
	if response.StatusCode != 503 || !strings.Contains(body, "Our servers are currently overloaded. Please try again later.") {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	if upstream.calls() != 4 {
		t.Fatalf("%d calls, want the request and three repeats", upstream.calls())
	}
	if len(*waited) != 3 || (*waited)[0] != 2*time.Second || (*waited)[2] != 2*time.Second {
		t.Fatalf("waited %v", *waited)
	}
	for i, sent := range upstream.bodies {
		if sent != `{"model":"gpt-5","stream":true}` || upstream.sessions[i] != "thread-1" {
			t.Fatalf("attempt %d was not the same request: %q %q", i, sent, upstream.sessions[i])
		}
	}
}

func TestCodexOverloadThatClearsServesTheStreamByteForByte(t *testing.T) {
	noWaiting(t)
	good := sseFrame("response.created", codexCreated) + sseFrame("response.output_text.delta", codexDelta) + sseFrame("response.completed", codexComplete)
	upstream := newCodexUpstream(t, func(call int) (int, string, string) {
		if call < 2 {
			return 200, "text/event-stream", sseFrame("response.created", codexCreated) + overloadedFrame
		}
		return 200, "text/event-stream", good
	})
	response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+"/responses"))
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, response); response.StatusCode != 200 || body != good {
		t.Fatalf("%d %q", response.StatusCode, body)
	}
	if upstream.calls() != 3 {
		t.Fatalf("%d calls", upstream.calls())
	}
}

func TestCodexStreamIsNotJudgedByTextItOnlyCarries(t *testing.T) {
	waited := noWaiting(t)
	// The phrases are in the echoed instructions and in what the model says; neither
	// is an error event.
	stream := sseFrame("response.created", codexCreated) +
		sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","delta":"the phrase server_is_overloaded and model_at_capacity"}`) +
		overloadedFrame
	upstream := newCodexUpstream(t, func(int) (int, string, string) { return 200, "text/event-stream", stream })
	response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+"/responses"))
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, response); response.StatusCode != 200 || body != stream {
		t.Fatalf("%d %q", response.StatusCode, body)
	}
	if upstream.calls() != 1 || len(*waited) != 0 {
		t.Fatalf("a stream that produced output was repeated: %d calls", upstream.calls())
	}
}

func TestCodexErrorsThatAreNotTheseArePassedOn(t *testing.T) {
	noWaiting(t)
	stream := sseFrame("response.created", codexCreated) +
		sseFrame("response.failed", `{"type":"response.failed","response":{"error":{"code":"invalid_prompt","message":"bad prompt"}}}`)
	upstream := newCodexUpstream(t, func(int) (int, string, string) { return 200, "text/event-stream", stream })
	response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+"/responses"))
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, response); response.StatusCode != 200 || body != stream || upstream.calls() != 1 {
		t.Fatalf("%d %q %d calls", response.StatusCode, body, upstream.calls())
	}
}

func TestCodexLongQuietHeadIsRelayedWhole(t *testing.T) {
	noWaiting(t)
	// More than the inspected window of reasoning with no decisive event: what was
	// read is replayed ahead of the rest, byte for byte.
	var stream strings.Builder
	stream.WriteString(sseFrame("response.created", codexCreated))
	for stream.Len() < 2*codexInspectLimit {
		stream.WriteString(sseFrame("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"thinking about it for a while"}`))
	}
	stream.WriteString(overloadedFrame) // after the window: too late to act on
	stream.WriteString(sseFrame("response.completed", codexComplete))
	upstream := newCodexUpstream(t, func(int) (int, string, string) { return 200, "text/event-stream", stream.String() })
	response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+"/responses"))
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, response); !bytes.Equal([]byte(body), []byte(stream.String())) || upstream.calls() != 1 {
		t.Fatalf("relayed %d of %d bytes in %d calls", len(body), stream.Len(), upstream.calls())
	}
}

func TestCodexCompactAndErrorStatusesAreNotJudged(t *testing.T) {
	waited := noWaiting(t)
	for name, test := range map[string]struct {
		path   string
		status int
		body   string
	}{
		"compact": {"/responses/compact", 200, `{"output":[],"note":"server_is_overloaded model_at_capacity"}`},
		"429":     {"/responses", 429, `{"error":{"type":"usage_limit_reached","message":"server_is_overloaded"}}`},
		"500":     {"/responses", 500, `model_at_capacity`},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := newCodexUpstream(t, func(int) (int, string, string) { return test.status, "application/json", test.body })
			response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+test.path))
			if err != nil {
				t.Fatal(err)
			}
			if body := readAll(t, response); response.StatusCode != test.status || body != test.body || upstream.calls() != 1 {
				t.Fatalf("%d %q %d calls", response.StatusCode, body, upstream.calls())
			}
		})
	}
	if len(*waited) != 0 {
		t.Fatalf("waited %v", *waited)
	}
}

func TestCodexRetryWaitEndsWithTheRequest(t *testing.T) {
	upstream := newCodexUpstream(t, func(int) (int, string, string) {
		return 200, "text/event-stream", sseFrame("response.created", codexCreated) + overloadedFrame
	})
	ctx, cancel := context.WithCancel(context.Background())
	previous := codexSleep
	codexSleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	t.Cleanup(func() { codexSleep = previous })
	request := codexRequest(t, upstream.URL+"/responses").WithContext(ctx)
	if _, err := CodexDo(upstream.Client(), request); err == nil {
		t.Fatal("a cancelled request was answered")
	}
	if upstream.calls() != 1 {
		t.Fatalf("a cancelled request was repeated: %d calls", upstream.calls())
	}
}

func TestCodexStreamEndingInsideTheHeadIsRelayed(t *testing.T) {
	noWaiting(t)
	stream := sseFrame("response.created", codexCreated) + "data: {\"type\":\"response.output_item.added\""
	upstream := newCodexUpstream(t, func(int) (int, string, string) { return 200, "text/event-stream", stream })
	response, err := CodexDo(upstream.Client(), codexRequest(t, upstream.URL+"/responses"))
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, response); body != stream {
		t.Fatalf("%q", body)
	}
}

func TestCodexStreamLineJudgement(t *testing.T) {
	for name, test := range map[string]struct {
		line    string
		stop    bool
		phrase  string
		account bool
	}{
		"blank":                   {"\n", false, "", false},
		"event line":              {"event: response.failed\n", false, "", false},
		"overloaded":              {`data: {"type":"response.failed","response":{"error":{"code":"server_is_overloaded"}}}` + "\n", false, "server_is_overloaded", false},
		"protection":              {`data: {"type":"error","error":{"message":"Response protection is unavailable right now"}}` + "\n", false, "response protection is unavailable", false},
		"service unavailable":     {`data: {"type":"error","error":{"type":"service_unavailable_error"}}` + "\n", false, "service_unavailable_error", false},
		"capacity wins":           {`data: {"type":"error","error":{"code":"model_at_capacity","message":"server_is_overloaded"}}` + "\n", false, "model_at_capacity", true},
		"output":                  {`data: {"type":"response.output_text.delta","delta":"x"}` + "\n", true, "", false},
		"arguments":               {`data: {"type":"response.function_call_arguments.delta","delta":"{"}` + "\n", true, "", false},
		"completed":               {`data: {"type":"response.completed","response":{}}` + "\n", true, "", false},
		"done":                    {"data: [DONE]\n", true, "", false},
		"phrase in a created":     {`data: {"type":"response.created","response":{"instructions":"model_at_capacity"}}` + "\n", false, "", false},
		"phrase in a failed echo": {`data: {"type":"response.failed","response":{"instructions":"model_at_capacity","error":{"code":"x"}}}` + "\n", false, "", false},
		"not json":                {"data: model_at_capacity\n", false, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			stop, verdict := judgeCodexLine([]byte(test.line))
			if stop != test.stop || verdict.phrase != test.phrase || verdict.account != test.account {
				t.Fatalf("stop=%v verdict=%+v", stop, verdict)
			}
		})
	}
}

// The head of every Codex stream is read once before a byte reaches the client.
func BenchmarkInspectCodexHead(b *testing.B) {
	head := sseFrame("response.created", codexCreated)
	for i := 0; i < 8; i++ {
		head += sseFrame("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`)
	}
	head += sseFrame("response.output_text.delta", codexDelta) + sseFrame("response.completed", codexComplete)
	b.SetBytes(int64(len(head)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		response := &http.Response{Body: io.NopCloser(strings.NewReader(head))}
		inspectCodexStream(response)
		io.Copy(io.Discard, response.Body)
	}
}
