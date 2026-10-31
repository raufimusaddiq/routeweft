package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// testdata/antigravity_pin.json is what the pinned executor does, recorded by
// compat/parity/generate-antigravity-retry.mjs from executors/base.js and
// executors/antigravity.js at 1c3b9f47, with the network, clock and timers scripted.
// Nothing here calls a provider.
type antigravityPin struct {
	Now       int64 `json:"now"`
	Scenarios []struct {
		Name  string `json:"name"`
		Steps []struct {
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
			Network string            `json:"network"`
		} `json:"steps"`
		Sent  []string `json:"sent"`
		Calls int      `json:"calls"`
		Waits []int64  `json:"waits"`
		Final struct {
			Status int    `json:"status"`
			Error  string `json:"error"`
		} `json:"final"`
	} `json:"scenarios"`
	IDs []struct {
		Name      string  `json:"name"`
		RequestID *string `json:"requestId"`
		SessionID string  `json:"sessionId"`
		Contents  int     `json:"contents"`
		Model     string  `json:"model"`
		ID        string  `json:"id"`
	} `json:"ids"`
	Dates  [][2]any `json:"dates"`
	Delays []struct {
		Name    string            `json:"name"`
		Status  int               `json:"status"`
		Attempt int               `json:"attempt"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Delay   json.RawMessage   `json:"delay"`
	} `json:"delays"`
	Parsed []struct {
		Kind    string            `json:"kind"`
		Headers map[string]string `json:"headers"`
		Message string            `json:"message"`
		Body    string            `json:"body"`
		Value   json.RawMessage   `json:"value"`
	} `json:"parsed"`
	Rewrites []struct {
		In      string           `json:"in"`
		Parts   []map[string]any `json:"parts"`
		Content string           `json:"content"`
	} `json:"rewrites"`
	RequestTypes []struct {
		Name    string `json:"name"`
		RootHas bool   `json:"rootHas"`
	} `json:"requestTypes"`
}

func loadAntigravityPin(t *testing.T) antigravityPin {
	t.Helper()
	raw, err := os.ReadFile("testdata/antigravity_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin antigravityPin
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	return pin
}

// testClock is the clock the retry code reads; a recorded wait moves it, as the pin's
// scripted one moves.
var testClock time.Time

// pinNow fixes the clock at the instant the pin's was fixed at.
func pinNow(t *testing.T, milliseconds int64) time.Time {
	t.Helper()
	testClock = time.UnixMilli(milliseconds)
	previous := antigravityNow
	antigravityNow = func() time.Time { return testClock }
	t.Cleanup(func() { antigravityNow = previous })
	return testClock
}

// recordSleeps replaces the retry wait with a record of it.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	previous := antigravitySleep
	antigravitySleep = func(ctx context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		testClock = testClock.Add(delay)
		return ctx.Err()
	}
	t.Cleanup(func() { antigravitySleep = previous })
	return &waits
}

type upstreamStep struct {
	status  int
	headers map[string]string
	body    string
	network string
}

// upstream plays its steps in order and repeats the last, as the pin's script does.
type upstream struct {
	steps    []upstreamStep
	requests []*http.Request
	bodies   []string
	closed   atomic.Int32
	opened   atomic.Int32
}

func (u *upstream) RoundTrip(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	request.Body.Close()
	u.requests = append(u.requests, request)
	u.bodies = append(u.bodies, string(body))
	step := u.steps[min(len(u.requests)-1, len(u.steps)-1)]
	if step.network != "" {
		return nil, errors.New(step.network)
	}
	header := http.Header{}
	for name, value := range step.headers {
		header.Set(name, value)
	}
	u.opened.Add(1)
	return &http.Response{StatusCode: step.status, Header: header, Request: request,
		Body: &countedBody{Reader: strings.NewReader(step.body), closed: &u.closed}}, nil
}

type countedBody struct {
	io.Reader
	closed *atomic.Int32
}

func (b *countedBody) Close() error { b.closed.Add(1); return nil }

// antigravityEnvelope is what the generator sends the pin's executor: three turns in
// the session -4242, under the translator's own request id.
const antigravityEnvelope = `{"model":"gemini-3.8-flash-high","project":"synthetic-project","requestId":"agent-synthetic",` +
	`"request":{"sessionId":"-4242","contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hello"}]},{"role":"user","parts":[{"text":"hello"}]}]}}`

func antigravityRequest(ctx context.Context, t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://daily.example.invalid/v1internal:streamGenerateContent?alt=sse",
		strings.NewReader(antigravityEnvelope))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-token")
	request.Header.Set("User-Agent", antigravityUserAgent)
	return request
}

var antigravityRef = routing.ProviderRef{ProviderID: "antigravity", Protocol: gemini.AntigravityProtocol}

func sendAntigravity(t *testing.T, ctx context.Context, steps ...upstreamStep) (*upstream, *http.Response, error) {
	t.Helper()
	server := &upstream{steps: steps}
	response, err := Send(antigravityRef, &http.Client{Transport: server}, antigravityRequest(ctx, t))
	return server, response, err
}

// executors/base.js:127-181 with antigravity.js:421-446: the number of requests, the
// waits between them and the answer, for the same sequence of upstream answers.
func TestAntigravityRetriesAsThePinDoes(t *testing.T) {
	pin := loadAntigravityPin(t)
	pinNow(t, pin.Now)
	if len(pin.Scenarios) < 40 {
		t.Fatalf("only %d recorded scenarios", len(pin.Scenarios))
	}
	for _, scenario := range pin.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			testClock = time.UnixMilli(pin.Now)
			waits := recordSleeps(t)
			steps := make([]upstreamStep, len(scenario.Steps))
			for index, step := range scenario.Steps {
				steps[index] = upstreamStep{step.Status, step.Headers, step.Body, step.Network}
			}
			server, response, err := sendAntigravity(t, context.Background(), steps...)
			if len(server.requests) != scenario.Calls {
				t.Errorf("%d requests, the pin sent %d", len(server.requests), scenario.Calls)
			}
			var got []int64
			for _, wait := range *waits {
				got = append(got, wait.Milliseconds())
			}
			if !reflect.DeepEqual(got, append([]int64(nil), scenario.Waits...)) && (len(got) != 0 || len(scenario.Waits) != 0) {
				t.Errorf("waits %v, the pin waited %v", got, scenario.Waits)
			}
			switch {
			case scenario.Final.Error != "":
				if err == nil || !strings.Contains(err.Error(), scenario.Final.Error) {
					t.Errorf("error %v, the pin threw %q", err, scenario.Final.Error)
				}
			case err != nil:
				t.Errorf("error %v, the pin answered %d", err, scenario.Final.Status)
			case response.StatusCode != scenario.Final.Status:
				t.Errorf("status %d, the pin answered %d", response.StatusCode, scenario.Final.Status)
			}
			// Each attempt carries the id the pin built for it, from the clock at that moment;
			// the rest of the body does not change.
			for index, body := range server.bodies {
				var sent, pinSent map[string]json.RawMessage
				if err := json.Unmarshal([]byte(body), &sent); err != nil || index >= len(scenario.Sent) {
					t.Fatalf("request %d: %v (the pin sent %d)", index, err, len(scenario.Sent))
				}
				_ = json.Unmarshal([]byte(scenario.Sent[index]), &pinSent)
				if string(sent["requestId"]) != string(pinSent["requestId"]) {
					t.Errorf("request %d: id %s, the pin sent %s", index, sent["requestId"], pinSent["requestId"])
				}
				var first map[string]json.RawMessage
				_ = json.Unmarshal([]byte(server.bodies[0]), &first)
				delete(sent, "requestId")
				delete(first, "requestId")
				if !reflect.DeepEqual(sent, first) {
					t.Errorf("request %d changed more than its id", index)
				}
			}
		})
	}
}

// computeRetryDelay on its own, for every retried status and attempt.
func TestAntigravityRetryDelayIsThePins(t *testing.T) {
	pin := loadAntigravityPin(t)
	now := pinNow(t, pin.Now)
	for _, c := range pin.Delays {
		header := http.Header{}
		for name, value := range c.Headers {
			header.Set(name, value)
		}
		got, repeat := antigravityRetryDelay(c.Status, header, []byte(c.Body), c.Attempt, now)
		if string(c.Delay) == "false" {
			if repeat {
				t.Errorf("%d attempt %d %s: waits %v, the pin gives up", c.Status, c.Attempt, c.Name, got)
			}
			continue
		}
		var want int64
		if err := json.Unmarshal(c.Delay, &want); err != nil {
			t.Fatal(err)
		}
		if !repeat || got.Milliseconds() != want {
			t.Errorf("%d attempt %d %s: %v %v, the pin waits %dms", c.Status, c.Attempt, c.Name, got, repeat, want)
		}
	}
}

func TestAntigravityParsersAreThePins(t *testing.T) {
	pin := loadAntigravityPin(t)
	now := pinNow(t, pin.Now)
	for _, c := range pin.Parsed {
		switch c.Kind {
		case "headers":
			header := http.Header{}
			for name, value := range c.Headers {
				header.Set(name, value)
			}
			got, found := parseAntigravityRetryHeaders(header, now)
			if string(c.Value) == "null" {
				if found {
					t.Errorf("%v: %d, the pin has none", c.Headers, got)
				}
				continue
			}
			var want int64
			_ = json.Unmarshal(c.Value, &want)
			if !found || got != want {
				t.Errorf("%v: %d %v, the pin has %d", c.Headers, got, found, want)
			}
		case "message":
			got, found := parseAntigravityResetMessage(c.Message)
			if string(c.Value) == "null" {
				if found {
					t.Errorf("%q: %d, the pin has none", c.Message, got)
				}
				continue
			}
			var want int64
			_ = json.Unmarshal(c.Value, &want)
			// A duration past maxSaturated is read as maxSaturated: either is far over the cap.
			want = min(want, maxSaturated)
			if !found || got != want {
				t.Errorf("%q: %d %v, the pin has %d", c.Message, got, found, want)
			}
		case "text":
			var want string
			_ = json.Unmarshal(c.Value, &want)
			if got := antigravityErrorText([]byte(c.Body)); got != want {
				t.Errorf("%q: %q, the pin has %q", c.Body, got, want)
			}
		}
	}
}

func finalizeAntigravity(t *testing.T, protocol, text string) (parts []map[string]any, content string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "m", "request": map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": text}, map[string]any{"text": "second " + text}, map[string]any{"notText": 1}, map[string]any{"text": 5}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Request struct {
			SystemInstruction struct {
				Parts []map[string]any `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		} `json:"request"`
	}
	if err := json.Unmarshal(FinalizeBodyAt(routing.ProviderRef{ProviderID: "antigravity", Protocol: protocol}, "", body), &sent); err != nil {
		t.Fatal(err)
	}
	return sent.Request.SystemInstruction.Parts, sent.Request.Contents[0].Parts[0].Text
}

// config/appConstants.js:175-183 applied at executors/antigravity.js:265-274.
func TestAntigravityRewritesTheSystemPromptAsThePinDoes(t *testing.T) {
	pin := loadAntigravityPin(t)
	if len(pin.Rewrites) < 50 {
		t.Fatalf("only %d recorded prompts", len(pin.Rewrites))
	}
	for _, c := range pin.Rewrites {
		parts, content := finalizeAntigravity(t, gemini.AntigravityProtocol, c.In)
		if !reflect.DeepEqual(parts, c.Parts) {
			t.Errorf("%q: system parts %q, the pin has %q", c.In, parts, c.Parts)
		}
		if content != c.Content || content != c.In {
			t.Errorf("%q: the conversation became %q; only the system prompt is rewritten", c.In, content)
		}
	}
}

func TestAntigravityRewriteAppliesToAntigravityOnly(t *testing.T) {
	text := "You are Hermes. OpenCode"
	for _, protocol := range []string{gemini.GeminiCLIProtocol, "gemini", "openai-chat", ""} {
		if parts, _ := finalizeAntigravity(t, protocol, text); parts[0]["text"] != text {
			t.Errorf("%q wire rewrote the prompt: %v", protocol, parts[0])
		}
	}
	for _, id := range []string{"gemini-cli", "vertex"} {
		body := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"OpenCode"}]}}}`)
		if got := FinalizeBodyAt(routing.ProviderRef{ProviderID: id, Protocol: gemini.AntigravityProtocol}, "", body); !bytes.Equal(got, body) {
			t.Errorf("%s: %s", id, got)
		}
	}
}

func TestAntigravityRewriteLeavesAnUnrewrittenBodyAlone(t *testing.T) {
	for name, body := range map[string]string{
		"nothing to rewrite": `{"request":{"systemInstruction":{"parts":[{"text":"be brief"}]},"n":1.0}}`,
		"no instruction":     `{"request":{"contents":[]}}`,
		"no parts":           `{"request":{"systemInstruction":{}}}`,
		"parts not a list":   `{"request":{"systemInstruction":{"parts":"opencode"}}}`,
		"request not object": `{"request":"opencode"}`,
		"not an object":      `[1]`,
	} {
		if got := FinalizeBodyAt(routing.ProviderRef{ProviderID: "antigravity", Protocol: gemini.AntigravityProtocol}, "", []byte(body)); string(got) != body {
			t.Errorf("%s: %s", name, got)
		}
	}
}

// A rewrite keeps every other field of the body as it was, numbers included.
func TestAntigravityRewriteKeepsTheRestOfTheBody(t *testing.T) {
	body := `{"model":"m","project":"p","request":{"generationConfig":{"seed":12345678901234567890,"temperature":1.50},` +
		`"systemInstruction":{"role":"user","parts":[{"text":"OpenCode"},{"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"sessionId":"-1"}}`
	got := FinalizeBodyAt(routing.ProviderRef{ProviderID: "antigravity", Protocol: gemini.AntigravityProtocol}, "", []byte(body))
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	var request map[string]json.RawMessage
	_ = json.Unmarshal(decoded["request"], &request)
	if string(request["generationConfig"]) != `{"seed":12345678901234567890,"temperature":1.50}` || string(request["sessionId"]) != `"-1"` {
		t.Errorf("rest of the body changed: %s", got)
	}
	var instruction struct {
		Role  string           `json:"role"`
		Parts []map[string]any `json:"parts"`
	}
	_ = json.Unmarshal(request["systemInstruction"], &instruction)
	if instruction.Role != "user" || instruction.Parts[0]["text"] != "Antigravity" || instruction.Parts[1]["inlineData"] == nil {
		t.Errorf("instruction: %+v", instruction)
	}
}

// executors/antigravity.js:296-302 deletes requestType from the envelope root. This
// product builds the envelope itself: a client's requestType never reaches the wire.
func TestAntigravityEnvelopeCarriesNoRequestType(t *testing.T) {
	pin := loadAntigravityPin(t)
	inner := `"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	for _, c := range pin.RequestTypes {
		if c.RootHas {
			t.Fatalf("%s: the pin keeps requestType at the root", c.Name)
		}
		var value string
		switch c.Name {
		case "absent":
			value = ``
		case "null":
			value = `"requestType":null,`
		default:
			value = `"requestType":"` + strings.ReplaceAll(c.Name, "empty", "") + `",`
		}
		chat, err := gemini.AntigravityRequestToChat(mustObject(t, `{`+value+inner+`}`), "gemini-3.8-flash-high", true)
		refused := err != nil
		switch c.Name {
		case "agent", "null", "absent", "image_gen":
			// image_gen is the routed model's to decide, not the client's (PARITY 6.26).
			if refused {
				t.Fatalf("%s: %v", c.Name, err)
			}
		default:
			// Search is not served here (PARITY 6.9 C1).
			if !refused {
				t.Errorf("%s: served", c.Name)
			}
			continue
		}
		var request map[string]any
		_ = json.Unmarshal(chat, &request)
		out, _, err := gemini.ChatRequestToAntigravity(request, gemini.RequestOptions{Model: "gemini-3.8-flash-high"}, gemini.CloudCodeOptions{Project: "p"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "requestType") {
			t.Errorf("%s: the envelope names requestType: %s", c.Name, out)
		}
	}
}

func mustObject(t *testing.T, text string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(text), &object); err != nil {
		t.Fatal(err)
	}
	return object
}

// The wait ends with the request, not after it (the pin's setTimeout does not).
func TestAntigravityWaitEndsWhenTheClientLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{}, 1)
	server := &upstream{steps: []upstreamStep{{status: 429, headers: map[string]string{"Retry-After": "10"}}, {status: 200}}}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := server.RoundTrip(request)
		select {
		case served <- struct{}{}:
		default:
		}
		return response, err
	})}
	done := make(chan error, 1)
	go func() {
		_, err := Send(antigravityRef, client, antigravityRequest(ctx, t))
		done <- err
	}()
	<-served
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting 2s after the client left a 10s wait")
	}
	if len(server.requests) != 1 {
		t.Errorf("%d requests; the repeat must not be sent for a client that left", len(server.requests))
	}
	if server.closed.Load() != server.opened.Load() {
		t.Errorf("%d answers opened, %d closed", server.opened.Load(), server.closed.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// A request cancelled while it is being sent is not a failure to repeat (base.js:170).
func TestAntigravityDoesNotRepeatARequestTheClientCancelled(t *testing.T) {
	waits := recordSleeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})}
	if _, err := Send(antigravityRef, client, antigravityRequest(ctx, t)); !errors.Is(err, context.Canceled) {
		t.Errorf("error %v", err)
	}
	if calls != 1 || len(*waits) != 0 {
		t.Errorf("%d requests, waits %v", calls, *waits)
	}
}

// Waits are bounded whatever the upstream asks: no wait is over the cap of its
// kind, and no request is sent more than four times.
func TestAntigravityWaitsAreBounded(t *testing.T) {
	pinNow(t, 1_700_000_000_000)
	for _, status := range []int{429, 500, 502, 503, 504} {
		for _, header := range []map[string]string{
			nil,
			{"Retry-After": "99999999999999999999"},
			{"Retry-After": "10"},
			{"X-Ratelimit-Reset-After": "9"},
			{"X-Ratelimit-Reset": "99999999999999999999"},
		} {
			waits := recordSleeps(t)
			server, _, _ := sendAntigravity(t, context.Background(), upstreamStep{status: status, headers: header})
			if len(server.requests) > 4 {
				t.Errorf("%d with %v: %d requests", status, header, len(server.requests))
			}
			for _, wait := range *waits {
				if wait > 15*time.Second || (status == 429 && wait > 10*time.Second) {
					t.Errorf("%d with %v: waited %v", status, header, wait)
				}
			}
		}
	}
}

// The hint is looked for in the head of the error body only, and the client still
// gets the whole body.
func TestAntigravityReadsABoundedHeadOfTheErrorBody(t *testing.T) {
	pinNow(t, 1_700_000_000_000)
	waits := recordSleeps(t)
	tail := strings.Repeat("x", antigravityErrorLimit) + " reset after 7s"
	server, response, err := sendAntigravity(t, context.Background(), upstreamStep{status: 400, body: tail})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(got) != tail || len(server.requests) != 1 {
		t.Errorf("a non-retried answer lost its body: %d bytes of %d, %d requests", len(got), len(tail), len(server.requests))
	}
	_, _, _ = sendAntigravity(t, context.Background(), upstreamStep{status: 429, body: tail}, upstreamStep{status: 200})
	if len(*waits) != 1 || (*waits)[0] != 2*time.Second {
		t.Errorf("waits %v: a hint past the limit was read", *waits)
	}
	// A final answer that was read for its hint is whole.
	_, response, _ = sendAntigravity(t, context.Background(), upstreamStep{status: 429, body: `{"error":{"message":"gone"}}`})
	got, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 429 || string(got) != `{"error":{"message":"gone"}}` {
		t.Errorf("%d %s", response.StatusCode, got)
	}
}

// A retried answer is closed so its connection returns to the pool.
func TestAntigravityClosesTheAnswersItRepeats(t *testing.T) {
	recordSleeps(t)
	server, response, err := sendAntigravity(t, context.Background(), upstreamStep{status: 503}, upstreamStep{status: 503}, upstreamStep{status: 200})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.closed.Load(); got != 2 {
		t.Errorf("%d answers closed before the last, want 2", got)
	}
	response.Body.Close()
}

// Security: a repeat is the same request through the same client, apart from the id the
// pin rebuilds, so the transport's own checks (address policy, redirect policy) apply to
// it again, and nothing is added.
func TestAntigravityRepeatIsTheSameRequestThroughTheSameClient(t *testing.T) {
	pinNow(t, 1_700_000_000_000)
	recordSleeps(t)
	server, _, err := sendAntigravity(t, context.Background(), upstreamStep{status: 502}, upstreamStep{status: 502}, upstreamStep{status: 200})
	if err != nil || len(server.requests) != 3 {
		t.Fatalf("%v, %d requests", err, len(server.requests))
	}
	first := server.requests[0]
	ids := map[string]bool{}
	for index, request := range server.requests {
		var sent map[string]json.RawMessage
		_ = json.Unmarshal([]byte(server.bodies[index]), &sent)
		ids[string(sent["requestId"])] = true
		delete(sent, "requestId")
		var firstSent map[string]json.RawMessage
		_ = json.Unmarshal([]byte(server.bodies[0]), &firstSent)
		delete(firstSent, "requestId")
		if request.Method != first.Method || request.URL.String() != first.URL.String() || !reflect.DeepEqual(request.Header, first.Header) || !reflect.DeepEqual(sent, firstSent) {
			t.Errorf("request %d differs: %s %s %v", index, request.Method, request.URL, request.Header)
		}
	}
	if len(ids) != 3 {
		t.Errorf("%d distinct request ids over 3 attempts; the pin builds one per attempt", len(ids))
	}
	if first.Header.Get("Authorization") != "Bearer synthetic-token" || first.Header.Get("User-Agent") != antigravityUserAgent {
		t.Errorf("headers: %v", first.Header)
	}
}

// A request body that cannot be replayed from GetBody is read once and replayed.
func TestAntigravityReplaysABodyWithoutGetBody(t *testing.T) {
	recordSleeps(t)
	server := &upstream{steps: []upstreamStep{{status: 500}, {status: 500}, {status: 200}}}
	request := antigravityRequest(context.Background(), t)
	request.Body, request.GetBody = io.NopCloser(strings.NewReader("payload")), nil
	response, err := Send(antigravityRef, &http.Client{Transport: server}, request)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("%v", err)
	}
	if !reflect.DeepEqual(server.bodies, []string{"payload", "payload", "payload"}) {
		t.Errorf("bodies %q", server.bodies)
	}
}

// Antigravity repeats through its own override; gemini-cli repeats through the
// base executor (base.js:100-180). Both are plain probes, never credential exchanges.
func TestCloudCodeWiresRepeatThroughTheirOwnSeam(t *testing.T) {
	for _, id := range []string{"antigravity", "gemini-cli"} {
		if For(id).Do == nil || Exchanges(id) {
			t.Errorf("%s: do=%v exchanges=%v", id, For(id).Do != nil, Exchanges(id))
		}
	}
}

// buildIdeRequestId: the same session, model and turns under the same clock give the pin's id.
func TestAntigravityRequestIDIsTheIDEFormThePinBuilds(t *testing.T) {
	pin := loadAntigravityPin(t)
	now := time.UnixMilli(pin.Now)
	for _, c := range pin.IDs {
		contents := make([]string, c.Contents)
		for index := range contents {
			contents[index] = `{"role":"user","parts":[{"text":"hello"}]}`
		}
		envelope := map[string]any{"model": c.Model, "request": json.RawMessage(`{"sessionId":"` + c.SessionID + `","contents":[` + strings.Join(contents, ",") + `]}`)}
		if c.RequestID != nil {
			envelope["requestId"] = *c.RequestID
		}
		payload, _ := json.Marshal(envelope)
		var sent struct {
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(withIDERequestID(payload, antigravityRef, now), &sent); err != nil || sent.RequestID != c.ID {
			t.Errorf("%s: %q (%v), the pin builds %q", c.Name, sent.RequestID, err, c.ID)
		}
	}
}

func TestAntigravityRequestIDFailsOpen(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	for name, payload := range map[string]string{"not json": "payload", "array": "[1]", "null": "null", "empty": ""} {
		if got := withIDERequestID([]byte(payload), antigravityRef, now); string(got) != payload {
			t.Errorf("%s: %q", name, got)
		}
	}
	// With no session in the envelope the connection stands for it, as the pin's chain ends (:105).
	withConnection := routing.ProviderRef{ProviderID: "antigravity", ConnectionID: "synthetic-connection"}
	a := withIDERequestID([]byte(`{"model":"m","request":{"contents":[]}}`), withConnection, now)
	b := withIDERequestID([]byte(`{"model":"m","request":{"contents":[]}}`), antigravityRef, now)
	if bytes.Equal(a, b) || !strings.Contains(string(a), "agent/") || !strings.Contains(string(b), "agent/") {
		t.Errorf("%s %s", a, b)
	}
}

// Date.parse, as V8 answers it for the strings the generator drew, plus the forms the
// ticket names (0.5, 0., "- 5", RFC 850 with a two-digit year).
func TestAntigravityDateParseIsV8s(t *testing.T) {
	pin := loadAntigravityPin(t)
	if len(pin.Dates) < 3000 {
		t.Fatalf("only %d recorded dates", len(pin.Dates))
	}
	named := map[string]bool{}
	for _, c := range pin.Dates {
		text := c[0].(string)
		got, ok := jsDateParse(text)
		want, wantOK := 0.0, c[1] != nil
		if wantOK {
			want = c[1].(float64)
		}
		if ok != wantOK || (ok && got != want) {
			t.Errorf("%q: %v %v, V8 %v %v", text, got, ok, want, wantOK)
		}
		named[text] = true
	}
	for _, text := range []string{"0.5", "0.", "- 5", "Tuesday, 14-Nov-49 22:13:25 GMT", "Tuesday, 14-Nov-50 22:13:25 GMT", "Tuesday, 14-Nov-68 22:13:25 GMT"} {
		if !named[text] {
			t.Errorf("%q is not in the recorded dates", text)
		}
	}
}
