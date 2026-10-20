package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func stream(t *testing.T, ctx context.Context, upstream string) (events []map[string]any, done bool, err error) {
	t.Helper()
	var out bytes.Buffer
	err = CursorStreamToChat(ctx, strings.NewReader(upstream), &out)
	for _, block := range strings.Split(strings.TrimSpace(out.String()), "\n\n") {
		payload, ok := strings.CutPrefix(block, "data: ")
		switch {
		case !ok:
		case payload == "[DONE]":
			done = true
		default:
			var value map[string]any
			if jsonErr := json.Unmarshal([]byte(payload), &value); jsonErr != nil {
				t.Fatalf("bad event %q", payload)
			}
			events = append(events, value)
		}
	}
	return events, done, err
}

const (
	textChunk   = `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"
	thinkChunk  = `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"hm"},"finish_reason":null}]}` + "\n\n"
	callChunk   = `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n"
	stopChunk   = `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	toolsFinish = `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	doneMarker  = "data: [DONE]\n\n"
)

// The leg is the identity (cursor-to-openai.js:13-28): what the transport emits
// is what the client reads, and the leg makes up no usage.
func TestStreamIsPassedThroughWithoutUsage(t *testing.T) {
	events, done, err := stream(t, context.Background(), thinkChunk+textChunk+callChunk+toolsFinish+doneMarker)
	if err != nil || !done || len(events) != 4 {
		t.Fatalf("err=%v done=%v events=%d", err, done, len(events))
	}
	for _, event := range events {
		if _, present := event["usage"]; present {
			t.Errorf("a usage was made up: %v", event)
		}
	}
	choice := concerns.Object(concerns.Items(events[3]["choices"])[0])
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish = %v", choice["finish_reason"])
	}
	// Usage the transport itself reports is not removed.
	withUsage := strings.Replace(stopChunk, `"finish_reason":"stop"}]`, `"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}`, 1)
	events, _, err = stream(t, context.Background(), textChunk+withUsage+doneMarker)
	if err != nil || concerns.Object(events[1]["usage"])["total_tokens"] != float64(7) {
		t.Errorf("reported usage must survive: %v %v", err, events)
	}
}

func TestStreamFailures(t *testing.T) {
	for name, c := range map[string]struct{ upstream, want string }{
		"error frame":          {textChunk + `data: {"error":{"message":"rate limited","type":"rate_limit_error"}}` + "\n\n" + doneMarker, "rate_limit_error"},
		"error string":         {`data: {"error":"boom"}` + "\n\n", "boom"},
		"no finish, then DONE": {textChunk + doneMarker, "upstream_incomplete"},
		"no finish, no DONE":   {textChunk, "upstream_incomplete"},
		"finish, no DONE":      {textChunk + stopChunk, "upstream_incomplete"},
		"tool finish, no call": {textChunk + toolsFinish + doneMarker, "upstream_incomplete"},
		"truncated":            {textChunk[:30], "upstream_incomplete"},
		"empty":                {"", "upstream_incomplete"},
	} {
		t.Run(name, func(t *testing.T) {
			events, done, err := stream(t, context.Background(), c.upstream)
			if err == nil || !done {
				t.Fatalf("a failed stream must end with an error and the marker: err=%v done=%v", err, done)
			}
			last, _ := json.Marshal(events[len(events)-1])
			if !strings.Contains(string(last), `"error"`) || !strings.Contains(string(last), c.want) {
				t.Errorf("terminal = %s, want error mentioning %s", last, c.want)
			}
		})
	}
	// Finish reasons other than a tool finish pass, with or without calls.
	for name, upstream := range map[string]string{"calls, stop": callChunk + stopChunk + doneMarker, "no calls, stop": textChunk + stopChunk + doneMarker} {
		if _, done, err := stream(t, context.Background(), upstream); err != nil || !done {
			t.Errorf("%s: err=%v done=%v", name, err, done)
		}
	}
}

func TestCancelledClientGetsNoTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events, done, err := stream(t, ctx, textChunk+stopChunk+doneMarker)
	if !errors.Is(err, context.Canceled) || done || len(events) != 0 {
		t.Errorf("err=%v done=%v events=%v", err, done, events)
	}
}

func TestBufferedBody(t *testing.T) {
	ok := `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1}}`
	if got, err := CursorResponseToChat([]byte(ok)); err != nil || string(got) != ok {
		t.Errorf("the body is returned as received: %v %s", err, got)
	}
	noUsage := strings.Replace(ok, `,"usage":{"prompt_tokens":1}`, "", 1)
	if got, _ := CursorResponseToChat([]byte(noUsage)); strings.Contains(string(got), "usage") {
		t.Errorf("a usage was made up: %s", got)
	}
	for name, body := range map[string]string{
		"error object":        `{"error":{"message":"x","type":"api_error"}}`,
		"error string":        `{"error":"x"}`,
		"no choices":          `{"object":"chat.completion","choices":[]}`,
		"no message":          `{"choices":[{"index":0,"finish_reason":"stop"}]}`,
		"tool finish no call": `{"choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"tool_calls"}]}`,
		"not json":            `nope`,
		"two objects":         ok + ok,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CursorResponseToChat([]byte(body)); err == nil {
				t.Error("want an error")
			}
		})
	}
	call := `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	if _, err := CursorResponseToChat([]byte(call)); err != nil {
		t.Errorf("a tool finish with a call is fine: %v", err)
	}
	var failure *concerns.StreamFailure
	if _, err := CursorResponseToChat([]byte(`{"error":{"message":"x"}}`)); !errors.As(err, &failure) {
		t.Errorf("an upstream error object is the upstream's own failure: %v", err)
	}
}
