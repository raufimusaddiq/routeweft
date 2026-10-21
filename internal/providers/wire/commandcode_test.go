package wire

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func commandCodeRef(base string) routing.ProviderRef {
	return routing.ProviderRef{ProviderID: "commandcode", Protocol: "commandcode", APIToken: "user_fixture", BaseURL: base}
}

func TestCommandCodeHeadersAndTarget(t *testing.T) {
	provider := commandCodeRef("https://api.commandcode.ai/alpha")
	got := OpenAIHeaders(provider)
	for name, want := range map[string]string{
		"Authorization": "Bearer user_fixture", "Content-Type": "application/json", "Accept": "text/event-stream",
		"X-Command-Code-Version": "0.25.7", "X-Cli-Environment": "cli",
	} {
		if got.Get(name) != want {
			t.Fatalf("%s = %q, want %q", name, got.Get(name), want)
		}
	}
	first := got.Get("X-Session-Id")
	if id, err := uuid.Parse(first); err != nil || id.Version() != 4 {
		t.Fatalf("session id %q: %v", first, err)
	}
	if first == OpenAIHeaders(provider).Get("X-Session-Id") {
		t.Fatal("session id reused across requests")
	}
	target, err := ResolveTarget(provider, "chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	built, err := target.URL(url.Parse)
	if err != nil || built.String() != "https://api.commandcode.ai/alpha/generate" {
		t.Fatalf("url = %v, %v", built, err)
	}
}

func TestCommandCodeForcesStreamOnTheEnvelope(t *testing.T) {
	body := []byte(`{"threadId":"t","params":{"model":"m","stream":false,"max_tokens":5}}`)
	out := FinalizeBody(commandCodeRef("https://x"), body)
	var envelope struct {
		Stream bool `json:"stream"`
		Params struct {
			Stream    bool `json:"stream"`
			MaxTokens int  `json:"max_tokens"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil || !envelope.Stream || !envelope.Params.Stream || envelope.Params.MaxTokens != 5 {
		t.Fatalf("%s: %+v %v", out, envelope, err)
	}
	other := commandCodeRef("https://x")
	other.Protocol = "openai-chat"
	if string(FinalizeBody(other, body)) != string(body) {
		t.Fatal("a Chat-wire body was rewritten")
	}
}

// The cases are tests/unit/commandcode-executor.test.js:31-60 plus the status
// inference ladder (executors/commandcode.js:80-138).
func TestParseCommandCodeError(t *testing.T) {
	for _, tt := range []struct {
		name, event string
		status      int
		message     string
		kind        string
	}{
		{"declared 503", `{"type":"error","error":{"type":"server_error","message":"Service temporarily unavailable. Please try again shortly.","statusCode":503,"isRetryable":true}}`, 503, "Service temporarily unavailable. Please try again shortly.", "server_error"},
		{"string message", `{"type":"error","message":"Rate limit exceeded. Please wait 30s."}`, 429, "Rate limit exceeded. Please wait 30s.", "rate_limit_error"},
		{"string error", `{"type":"error","error":"Unauthorized access"}`, 401, "Unauthorized access", "authentication_error"},
		{"numeric string status", `{"error":{"message":"x","status":"418"}}`, 418, "x", "server_error"},
		{"event status wins", `{"statusCode":502,"error":{"message":"x","statusCode":429}}`, 502, "x", "server_error"},
		{"out of range falls to text", `{"error":{"message":"Billing problem","statusCode":200}}`, 402, "Billing problem", "billing_error"},
		{"quota", `{"error":"quota exhausted"}`, 403, "quota exhausted", "permission_error"},
		{"not found", `{"error":"model not found"}`, 404, "model not found", "invalid_request_error"},
		{"overloaded", `{"error":"overloaded"}`, 503, "overloaded", "server_error"},
		{"unknown text", `{"error":"???"}`, 503, "???", "server_error"},
		{"no error field", `{"type":"error"}`, 503, "unknown", "server_error"},
		{"object without message", `{"error":{"code":1}}`, 503, `{"code":1}`, "server_error"},
	} {
		var event map[string]any
		if err := json.Unmarshal([]byte(tt.event), &event); err != nil {
			t.Fatal(err)
		}
		got := ParseCommandCodeError(event)
		if got.Status != tt.status || got.Message != tt.message || got.Type != tt.kind {
			t.Errorf("%s: %+v, want %d %q %s", tt.name, got, tt.status, tt.message, tt.kind)
		}
	}
	if got := ParseCommandCodeError(nil); got.Status != 503 || got.Message != "CommandCode upstream error" {
		t.Fatalf("nil event: %+v", got)
	}
}

type commandCodeServer struct {
	mu       sync.Mutex
	sessions []string
	bodies   []string
	headers  []http.Header
}

func (s *commandCodeServer) handler(replies ...func(http.ResponseWriter)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		index := len(s.sessions)
		s.sessions = append(s.sessions, r.Header.Get("x-session-id"))
		s.bodies = append(s.bodies, string(body))
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		if index >= len(replies) {
			index = len(replies) - 1
		}
		replies[index](w)
	})
}

func ndjson(lines ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range lines {
			io.WriteString(w, line)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

const ccUnavailable = `{"type":"error","error":{"type":"server_error","message":"Service temporarily unavailable. Please try again shortly.","statusCode":503,"isRetryable":true}}` + "\n"

func doCommandCode(t *testing.T, server *httptest.Server, ctx context.Context) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/alpha/generate", strings.NewReader(`{"params":{"stream":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-session-id", "first")
	return CommandCodeDo(server.Client(), request)
}

func noSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var delays []time.Duration
	previous := commandCodeSleep
	commandCodeSleep = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	t.Cleanup(func() { commandCodeSleep = previous })
	return &delays
}

func TestCommandCodeDoRetriesAnErrorEventThenReturnsItAsHTTP(t *testing.T) {
	delays := noSleep(t)
	for _, preamble := range [][]string{nil, {`{"type":"start"}` + "\n", `{"type":"start-step"}` + "\n"}} {
		*delays = nil
		fake := &commandCodeServer{}
		server := httptest.NewServer(fake.handler(ndjson(append(append([]string{}, preamble...), ccUnavailable)...)))
		response, err := doCommandCode(t, server, context.Background())
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		var parsed struct {
			Error struct {
				Message, Type string
				Code          int
			}
		}
		if json.Unmarshal(body, &parsed) != nil || response.StatusCode != 503 || parsed.Error.Code != 503 ||
			!strings.Contains(parsed.Error.Message, "[CommandCode error: Service temporarily unavailable") || parsed.Error.Type != "server_error" ||
			response.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("response %d %s", response.StatusCode, body)
		}
		if len(fake.sessions) != 3 || len(*delays) != 2 || (*delays)[0] != time.Second || (*delays)[1] != 2*time.Second {
			t.Fatalf("attempts %d delays %v", len(fake.sessions), *delays)
		}
		if fake.sessions[0] != "first" || fake.sessions[1] == "first" || fake.sessions[1] == fake.sessions[2] {
			t.Fatalf("session ids %v", fake.sessions)
		}
		for _, sent := range fake.bodies {
			if sent != `{"params":{"stream":true}}` {
				t.Fatalf("retry body %q", sent)
			}
		}
	}
}

func TestCommandCodeDoRecoversOnRetryAndReplaysTheStreamExactly(t *testing.T) {
	noSleep(t)
	stream := []string{
		`{"type":"start"}` + "\n",
		`{"type":"text-delta","text":"Hello"}` + "\n" + `{"type":"error","error":"late failure after content"}` + "\n",
		`{"type":"finish"}` + "\n",
	}
	fake := &commandCodeServer{}
	server := httptest.NewServer(fake.handler(ndjson(ccUnavailable), ndjson(stream...)))
	defer server.Close()
	response, err := doCommandCode(t, server, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(body) != strings.Join(stream, "") || len(fake.sessions) != 2 {
		t.Fatalf("status %d attempts %d body %q", response.StatusCode, len(fake.sessions), body)
	}
}

func TestCommandCodeDoNeverRetriesAnHTTPError(t *testing.T) {
	noSleep(t)
	for _, status := range []int{401, 429, 502, 503} {
		fake := &commandCodeServer{}
		server := httptest.NewServer(fake.handler(func(w http.ResponseWriter) {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"upstream"}`)
		}))
		response, err := doCommandCode(t, server, context.Background())
		server.Close()
		body, _ := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || string(body) != `{"error":"upstream"}` || len(fake.sessions) != 1 {
			t.Fatalf("%d: status %d attempts %d body %q err %v", status, response.StatusCode, len(fake.sessions), body, err)
		}
	}
}

func TestCommandCodeDoStatusFromEventIsNotRetriedWhenNotGateway(t *testing.T) {
	noSleep(t)
	fake := &commandCodeServer{}
	server := httptest.NewServer(fake.handler(ndjson(`{"type":"error","message":"Rate limit exceeded"}`))) // no trailing newline
	defer server.Close()
	response, err := doCommandCode(t, server, context.Background())
	if err != nil || response.StatusCode != 429 || len(fake.sessions) != 1 {
		t.Fatalf("status %d attempts %d err %v", response.StatusCode, len(fake.sessions), err)
	}
}

func TestCommandCodeDoRelaysWhenNothingDecisiveComes(t *testing.T) {
	noSleep(t)
	for _, lines := range [][]string{
		{"data: " + `{"type":"text-delta","text":"x"}` + "\n\n", "data: [DONE]\n\n"},
		{"not json\n", `{"type":"start"}` + "\n"},
		{"data: [DONE]\n", ccUnavailable}, // [DONE] ends the inspection (the pin's [DONE] branch)
	} {
		fake := &commandCodeServer{}
		server := httptest.NewServer(fake.handler(ndjson(lines...)))
		response, err := doCommandCode(t, server, context.Background())
		body, _ := io.ReadAll(response.Body)
		server.Close()
		if err != nil || response.StatusCode != 200 || string(body) != strings.Join(lines, "") || len(fake.sessions) != 1 {
			t.Fatalf("%q: status %d attempts %d body %q err %v", lines, response.StatusCode, len(fake.sessions), body, err)
		}
	}
}

func TestCommandCodeDoStopsRetryingWhenTheClientLeaves(t *testing.T) {
	previous := commandCodeSleep
	t.Cleanup(func() { commandCodeSleep = previous })
	ctx, cancel := context.WithCancel(context.Background())
	commandCodeSleep = func(c context.Context, _ time.Duration) error { cancel(); return c.Err() }
	fake := &commandCodeServer{}
	server := httptest.NewServer(fake.handler(ndjson(ccUnavailable)))
	defer server.Close()
	if _, err := doCommandCode(t, server, ctx); err == nil || len(fake.sessions) != 1 {
		t.Fatalf("attempts %d err %v", len(fake.sessions), err)
	}
}

func TestCommandCodeHookIsRegisteredWithoutForcedFold(t *testing.T) {
	hook := For("commandcode")
	if hook.Do == nil || hook.Finalize == nil || hook.OpenAIHeaders == nil || hook.Target == nil {
		t.Fatalf("incomplete hook %+v", hook)
	}
	// The wire is NDJSON, not Chat SSE: the Chat stream fold must not see it.
	if ForcesStream(routing.ProviderRef{ProviderID: "commandcode"}) {
		t.Fatal("ForceStream would route NDJSON through the Chat SSE fold")
	}
}

type countingBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *countingBody) Close() error { b.closed = true; return nil }

type bodyTransport struct {
	bodies []*countingBody
	make   func(attempt int) (http.Header, io.Reader)
}

func (t *bodyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	header, reader := t.make(len(t.bodies))
	body := &countingBody{Reader: reader}
	t.bodies = append(t.bodies, body)
	return &http.Response{StatusCode: 200, Header: header, Body: body, Request: r}, nil
}

func transportRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "https://api.commandcode.ai/alpha/generate", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestCommandCodeDoClosesEveryAttemptBody(t *testing.T) {
	// A retried attempt, and a retry cut short by the client leaving.
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		previous := commandCodeSleep
		commandCodeSleep = func(c context.Context, _ time.Duration) error {
			if cancelled {
				cancel()
				return c.Err()
			}
			return nil
		}
		transport := &bodyTransport{make: func(int) (http.Header, io.Reader) {
			return http.Header{}, strings.NewReader(ccUnavailable)
		}}
		request := transportRequest(t).WithContext(ctx)
		response, err := CommandCodeDo(&http.Client{Transport: transport}, request)
		commandCodeSleep = previous
		cancel()
		if cancelled != (err != nil) || (!cancelled && response.StatusCode != 503) {
			t.Fatalf("cancelled=%v: %v %v", cancelled, response, err)
		}
		for i, body := range transport.bodies {
			if !body.closed {
				t.Fatalf("cancelled=%v: attempt %d body leaked", cancelled, i)
			}
		}
		if want := map[bool]int{false: 3, true: 1}[cancelled]; len(transport.bodies) != want {
			t.Fatalf("cancelled=%v: %d attempts", cancelled, len(transport.bodies))
		}
	}
}

func TestCommandCodeInspectionIsBoundedByAnUnterminatedLine(t *testing.T) {
	total := 4 * commandCodeInspectLimit
	payload := strings.Repeat("x", total)
	transport := &bodyTransport{make: func(int) (http.Header, io.Reader) {
		return http.Header{}, strings.NewReader(payload)
	}}
	response, err := CommandCodeDo(&http.Client{Transport: transport}, transportRequest(t))
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("%v %v", response, err)
	}
	// Before the caller reads anything, no more than the bound was pulled in.
	if read := transport.bodies[0].read; read > commandCodeInspectLimit {
		t.Fatalf("inspection read %d bytes, bound %d", read, commandCodeInspectLimit)
	}
	relayed, _ := io.ReadAll(response.Body)
	if string(relayed) != payload {
		t.Fatalf("relayed %d bytes, want %d", len(relayed), total)
	}
}

func TestCommandCodeFailureKeepsUpstreamHeaders(t *testing.T) {
	noSleep(t)
	transport := &bodyTransport{make: func(int) (http.Header, io.Reader) {
		return http.Header{"Retry-After": {"7"}, "X-Request-Id": {"req-1"}, "Content-Type": {"text/event-stream"}, "Content-Length": {"99"}},
			strings.NewReader(ccUnavailable)
	}}
	response, err := CommandCodeDo(&http.Client{Transport: transport}, transportRequest(t))
	if err != nil || response.StatusCode != 503 {
		t.Fatalf("%v %v", response, err)
	}
	if response.Header.Get("Retry-After") != "7" || response.Header.Get("X-Request-Id") != "req-1" ||
		response.Header.Get("Content-Type") != "application/json" || response.Header.Get("Content-Length") != "" {
		t.Fatalf("headers %v", response.Header)
	}
}

func TestCommandCodeRequiresACredential(t *testing.T) {
	if !RequiresCredential("commandcode") {
		t.Fatal("an unsigned CommandCode request would be sent and its 401 blamed on the account")
	}
}
