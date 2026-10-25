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
		Calls int     `json:"calls"`
		Waits []int64 `json:"waits"`
		Final struct {
			Status int    `json:"status"`
			Error  string `json:"error"`
		} `json:"final"`
	} `json:"scenarios"`
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

// pinNow fixes the clock at the instant the pin's was fixed at.
func pinNow(t *testing.T, milliseconds int64) time.Time {
	t.Helper()
	now := time.UnixMilli(milliseconds)
	previous := antigravityNow
	antigravityNow = func() time.Time { return now }
	t.Cleanup(func() { antigravityNow = previous })
	return now
}

// recordSleeps replaces the retry wait with a record of it.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	previous := antigravitySleep
	antigravitySleep = func(ctx context.Context, delay time.Duration) error {
		waits = append(waits, delay)
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

func antigravityRequest(ctx context.Context, t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://daily.example.invalid/v1internal:streamGenerateContent?alt=sse",
		strings.NewReader(`{"project":"p","request":{"contents":[]}}`))
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
			for index, body := range server.bodies {
				if body != server.bodies[0] {
					t.Errorf("request %d sent a different body", index)
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

// Where the Go reading of a header is deliberately not V8's. Each is a Retry-After
// the pin's legacy Date.parse reads as a date and so stops at, where this goes on to
// the next header; none is a form a server sends (antigravity_retry.go).
var antigravityLegacyDateForms = map[string]bool{"0.": true, "0.5": true, "- 5": true}

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
			if _, companion := c.Headers["x-ratelimit-reset-after"]; companion && antigravityLegacyDateForms[c.Headers["retry-after"]] {
				if !found || got != 8000 {
					t.Errorf("%v: %d %v, want the next header's 8000", c.Headers, got, found)
				}
				continue
			}
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
		case "agent", "null", "absent":
			if refused {
				t.Fatalf("%s: %v", c.Name, err)
			}
		default:
			// Image generation and search are not served here (PARITY 6.9 C1).
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

// Security: a repeat is the same request through the same client, so the transport's
// own checks (address policy, redirect policy) apply to it again, and nothing is added.
func TestAntigravityRepeatIsTheSameRequestThroughTheSameClient(t *testing.T) {
	recordSleeps(t)
	server, _, err := sendAntigravity(t, context.Background(), upstreamStep{status: 502}, upstreamStep{status: 502}, upstreamStep{status: 200})
	if err != nil || len(server.requests) != 3 {
		t.Fatalf("%v, %d requests", err, len(server.requests))
	}
	first := server.requests[0]
	for index, request := range server.requests {
		if request.Method != first.Method || request.URL.String() != first.URL.String() || !reflect.DeepEqual(request.Header, first.Header) || server.bodies[index] != server.bodies[0] {
			t.Errorf("request %d differs: %s %s %v", index, request.Method, request.URL, request.Header)
		}
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

// Only Antigravity is repeated, and a connection probe stays plain.
func TestAntigravityIsTheOnlyCloudCodeWireThatRepeats(t *testing.T) {
	if For("antigravity").Do == nil || Exchanges("antigravity") {
		t.Errorf("do=%v exchanges=%v", For("antigravity").Do != nil, Exchanges("antigravity"))
	}
	if For("gemini-cli").Do != nil {
		t.Error("gemini-cli repeats; its pin executor has no such hook")
	}
}
