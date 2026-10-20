package ingress

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

type outcomeLog struct {
	mu   sync.Mutex
	list []RequestOutcome
}

func (l *outcomeLog) add(o RequestOutcome) {
	l.mu.Lock()
	l.list = append(l.list, o)
	l.mu.Unlock()
}

func (l *outcomeLog) wait(t *testing.T) []RequestOutcome {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		n := len(l.list)
		l.mu.Unlock()
		if n > 0 {
			time.Sleep(20 * time.Millisecond) // a second record would show up here
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]RequestOutcome(nil), l.list...)
}

const (
	chatRequest      = `{"model":"fixture-model","messages":[{"role":"user","content":"hi"}]%s}`
	responsesRequest = `{"model":"fixture-model","input":"hi"%s}`
)

// nativeGateway serves one native provider of the given protocol in front of
// upstream and returns the gateway URL plus the outcomes it published.
func nativeGateway(t *testing.T, protocol string, upstream http.Handler) (string, *outcomeLog) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	log := &outcomeLog{}
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "native-usage")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{
		AllowPrivateUpstreams: true,
		OnRequestComplete:     log.add,
		ProviderResolver: func(_ *runtime.RuntimeSnapshot, model string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "native", UpstreamModel: model, Protocol: protocol, BaseURL: up.URL + "/v1"}, true
		},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	gateway := httptest.NewServer(bearer(mux, key))
	t.Cleanup(gateway.Close)
	return gateway.URL, log
}

func post(t *testing.T, gateway, key, path, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, gateway+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func fixedBody(contentType, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, body)
	})
}

// Totals below deliberately disagree with their parts (total_tokens 99): the
// record carries the reported parts, never a recomputed or reconciled figure.
// reasoning_tokens is present upstream and is already inside completion_tokens.
const (
	chatBodyWithUsage = `{"id":"c1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"{\"usage\":{\"prompt_tokens\":1}}"}}],"usage":{"prompt_tokens":120,"completion_tokens":40,"total_tokens":99,"prompt_tokens_details":{"cached_tokens":64},"completion_tokens_details":{"reasoning_tokens":12}},"system_fingerprint":"fp"}`
	responsesBody     = `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":200,"output_tokens":50,"total_tokens":99,"input_tokens_details":{"cached_tokens":128},"output_tokens_details":{"reasoning_tokens":20}},"metadata":{}}`
)

func TestNativeNonStreamEmitsExactlyOneReportedRecord(t *testing.T) {
	cases := []struct {
		name, protocol, path, request, body string
		in, out, cached                     int64
	}{
		{"chat", openaiadapter.ChatProtocol, "/v1/chat/completions", chatRequest, chatBodyWithUsage, 120, 40, 64},
		{"responses", openaiadapter.ResponsesProtocol, "/v1/responses", responsesRequest, responsesBody, 200, 50, 128},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway, log := nativeGateway(t, tc.protocol, fixedBody("application/json", tc.body))
			response := post(t, gateway, "", tc.path, strings.Replace(tc.request, "%s", "", 1))
			got, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(got) != tc.body {
				t.Fatalf("body altered: %s", got)
			}
			outcomes := log.wait(t)
			if len(outcomes) != 1 {
				t.Fatalf("outcomes=%d want exactly 1", len(outcomes))
			}
			o := outcomes[0]
			if o.InputTokens != tc.in || o.OutputTokens != tc.out || o.CacheRead != tc.cached || o.UsageUnobserved || o.Termination != "" || o.Stream {
				t.Fatalf("outcome %+v want in=%d out=%d cached=%d observed", o, tc.in, tc.out, tc.cached)
			}
		})
	}
}

func TestNativeNonStreamWithoutUsageIsUnobservedNotZero(t *testing.T) {
	gateway, log := nativeGateway(t, openaiadapter.ChatProtocol, fixedBody("application/json", `{"id":"c1","choices":[]}`))
	post(t, gateway, "", "/v1/chat/completions", strings.Replace(chatRequest, "%s", "", 1)).Body.Close()
	outcomes := log.wait(t)
	if len(outcomes) != 1 || !outcomes[0].UsageUnobserved || outcomes[0].Termination != "" || outcomes[0].InputTokens != 0 {
		t.Fatalf("outcomes %+v", outcomes)
	}
}

const (
	chatStreamWithUsage = "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}],\"usage\":null}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":7,\"total_tokens\":99,\"prompt_tokens_details\":{\"cached_tokens\":16}}}\n\n" +
		"data: [DONE]\n\n"
	chatStreamNoUsage = "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\n"
	responsesStream   = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"usage\":null}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":80,\"output_tokens\":9,\"total_tokens\":99,\"input_tokens_details\":{\"cached_tokens\":48},\"output_tokens_details\":{\"reasoning_tokens\":3}}}}\n\n"
)

func TestNativeStreamEmitsOneRecordFromFinalChunk(t *testing.T) {
	cases := []struct {
		name, protocol, path, request, body string
		in, out, cached                     int64
	}{
		{"chat", openaiadapter.ChatProtocol, "/v1/chat/completions", chatRequest, chatStreamWithUsage, 30, 7, 16},
		{"responses", openaiadapter.ResponsesProtocol, "/v1/responses", responsesRequest, responsesStream, 80, 9, 48},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway, log := nativeGateway(t, tc.protocol, fixedBody("text/event-stream", tc.body))
			response := post(t, gateway, "", tc.path, strings.Replace(tc.request, "%s", `,"stream":true`, 1))
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			outcomes := log.wait(t)
			if len(outcomes) != 1 {
				t.Fatalf("outcomes=%d want exactly 1", len(outcomes))
			}
			o := outcomes[0]
			if o.InputTokens != tc.in || o.OutputTokens != tc.out || o.CacheRead != tc.cached || o.UsageUnobserved || o.Termination != "" || !o.Stream {
				t.Fatalf("outcome %+v want in=%d out=%d cached=%d", o, tc.in, tc.out, tc.cached)
			}
		})
	}
}

func TestNativeChatStreamWithoutUsageChunkIsCompleteButUnobserved(t *testing.T) {
	gateway, log := nativeGateway(t, openaiadapter.ChatProtocol, fixedBody("text/event-stream", chatStreamNoUsage))
	response := post(t, gateway, "", "/v1/chat/completions", strings.Replace(chatRequest, "%s", `,"stream":true`, 1))
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	outcomes := log.wait(t)
	if len(outcomes) != 1 || !outcomes[0].UsageUnobserved || outcomes[0].Termination != "" {
		t.Fatalf("outcomes %+v", outcomes)
	}
}

func TestNativeStreamUpstreamTruncatedIsMarked(t *testing.T) {
	truncated := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"
	gateway, log := nativeGateway(t, openaiadapter.ChatProtocol, fixedBody("text/event-stream", truncated))
	response := post(t, gateway, "", "/v1/chat/completions", strings.Replace(chatRequest, "%s", `,"stream":true`, 1))
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	outcomes := log.wait(t)
	if len(outcomes) != 1 || outcomes[0].Termination != TerminationUpstreamTruncated || !outcomes[0].UsageUnobserved {
		t.Fatalf("outcomes %+v", outcomes)
	}
}

// holdOpen writes first, flushes, then blocks until the gateway drops the
// upstream connection, which is how a cancelled client reaches the upstream.
func holdOpen(first string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
}

func abandonAfterFirstLine(t *testing.T, gateway, path, body string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, gateway+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
}

func TestNativeStreamAbandonedBeforeUsageIsMarkedAndUnobserved(t *testing.T) {
	gateway, log := nativeGateway(t, openaiadapter.ChatProtocol, holdOpen("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
	abandonAfterFirstLine(t, gateway, "/v1/chat/completions", strings.Replace(chatRequest, "%s", `,"stream":true`, 1))
	outcomes := log.wait(t)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes=%d want exactly 1", len(outcomes))
	}
	o := outcomes[0]
	if o.Termination != TerminationClientAbandoned || !o.UsageUnobserved || o.InputTokens != 0 || o.OutputTokens != 0 || o.Status != http.StatusOK {
		t.Fatalf("outcome %+v", o)
	}
}

func TestNativeStreamAbandonedAfterUsageKeepsReportedUsage(t *testing.T) {
	usageChunk := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":16}}}\n\n"
	gateway, log := nativeGateway(t, openaiadapter.ChatProtocol, holdOpen(usageChunk))
	abandonAfterFirstLine(t, gateway, "/v1/chat/completions", strings.Replace(chatRequest, "%s", `,"stream":true`, 1))
	outcomes := log.wait(t)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes=%d want exactly 1", len(outcomes))
	}
	o := outcomes[0]
	if o.Termination != TerminationClientAbandoned || o.UsageUnobserved || o.InputTokens != 30 || o.OutputTokens != 7 || o.CacheRead != 16 {
		t.Fatalf("outcome %+v", o)
	}
}

func TestNativeResponsesStreamAbandonedIsMarked(t *testing.T) {
	gateway, log := nativeGateway(t, openaiadapter.ResponsesProtocol, holdOpen("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
	abandonAfterFirstLine(t, gateway, "/v1/responses", strings.Replace(responsesRequest, "%s", `,"stream":true`, 1))
	outcomes := log.wait(t)
	if len(outcomes) != 1 || outcomes[0].Termination != TerminationClientAbandoned || !outcomes[0].UsageUnobserved {
		t.Fatalf("outcomes %+v", outcomes)
	}
}

func TestTopLevelUsageCapture(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"plain", `{"a":1,"usage":{"prompt_tokens":1}}`, `{"prompt_tokens":1}`},
		{"braces and key text in strings", `{"t":"{\"usage\":{\"x\":1}} \" }","usage":{"n":{"m":2},"s":"}"},"z":{"usage":{"bad":1}}}`, `{"n":{"m":2},"s":"}"}`},
		{"nested usage ignored", `{"response":{"usage":{"a":1}}}`, ``},
		{"null usage", `{"usage":null,"x":1}`, ``},
		{"spaced", "{ \"usage\" :\n { \"a\" : 1 } }", "{ \"a\" : 1 }"},
	}
	for _, tc := range cases {
		for _, chunk := range []int{1, 3, len(tc.body)} {
			c := &topLevelUsageCapture{}
			for i := 0; i < len(tc.body); i += chunk {
				end := i + chunk
				if end > len(tc.body) {
					end = len(tc.body)
				}
				_, _ = c.Write([]byte(tc.body[i:end]))
			}
			got, ok := c.value()
			if tc.want == "" && ok || tc.want != "" && (!ok || string(got) != tc.want) {
				t.Fatalf("%s chunk=%d: got %q ok=%v want %q", tc.name, chunk, got, ok, tc.want)
			}
		}
	}
}

func TestTopLevelUsageCaptureIsBounded(t *testing.T) {
	c := &topLevelUsageCapture{}
	_, _ = c.Write([]byte(`{"usage":{"pad":"`))
	chunk := []byte(strings.Repeat("x", 4096))
	for i := 0; i < 64; i++ {
		_, _ = c.Write(chunk)
	}
	if _, ok := c.value(); ok || len(c.buf) > maxCapturedUsageBytes {
		t.Fatalf("capture not bounded: ok=%v len=%d", ok, len(c.buf))
	}
}

// The response, not the request flag, decides how usage is read. An upstream
// that ignores stream:true and answers a JSON document is a normal success.
func TestNativeStreamRequestAnsweredWithJSONIsAccountedFromTheBody(t *testing.T) {
	body := `{"id":"c1","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}`
	gateway, log := nativeGateway(t, openaiadapter.ChatProtocol, fixedBody("application/json", body))
	response := post(t, gateway, "", "/v1/chat/completions", strings.Replace(chatRequest, "%s", `,"stream":true`, 1))
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	outcomes := log.wait(t)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes=%d want exactly 1", len(outcomes))
	}
	o := outcomes[0]
	if o.Termination != "" || o.UsageUnobserved || o.InputTokens != 11 || o.OutputTokens != 3 || o.Stream || o.Status != http.StatusOK {
		t.Fatalf("outcome %+v want a completed non-stream success with 11/3", o)
	}
}

func TestNativeNonStreamRequestAnsweredWithSSEIsAccountedFromTheStream(t *testing.T) {
	gateway, log := nativeGateway(t, openaiadapter.ChatProtocol, fixedBody("text/event-stream", chatStreamWithUsage))
	response := post(t, gateway, "", "/v1/chat/completions", strings.Replace(chatRequest, "%s", "", 1))
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	outcomes := log.wait(t)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes=%d want exactly 1", len(outcomes))
	}
	o := outcomes[0]
	if o.Termination != "" || o.UsageUnobserved || o.InputTokens != 30 || o.OutputTokens != 7 || o.CacheRead != 16 || !o.Stream {
		t.Fatalf("outcome %+v", o)
	}
}

func TestNativeResponsesStreamRequestAnsweredWithJSON(t *testing.T) {
	gateway, log := nativeGateway(t, openaiadapter.ResponsesProtocol, fixedBody("application/json", responsesBody))
	response := post(t, gateway, "", "/v1/responses", strings.Replace(responsesRequest, "%s", `,"stream":true`, 1))
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	outcomes := log.wait(t)
	if len(outcomes) != 1 || outcomes[0].Termination != "" || outcomes[0].InputTokens != 200 || outcomes[0].CacheRead != 128 {
		t.Fatalf("outcomes %+v", outcomes)
	}
}
