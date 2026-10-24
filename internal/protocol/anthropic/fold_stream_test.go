package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func sse(events ...string) string {
	var out strings.Builder
	for _, event := range events {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(event), &head)
		out.WriteString("event: " + head.Type + "\ndata: " + event + "\n\n")
	}
	return out.String()
}

const (
	startEvent = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4.6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":20,"cache_read_input_tokens":8,"cache_creation_input_tokens":2,"output_tokens":1}}}`
	stopEvent  = `{"type":"message_stop"}`
)

func fold(t *testing.T, events ...string) (map[string]any, error) {
	t.Helper()
	body, err := FoldMessagesStream(context.Background(), strings.NewReader(sse(events...)))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	return out, nil
}

func TestFoldMessagesStreamRebuildsTextThinkingAndToolUse(t *testing.T) {
	got, err := fold(t,
		startEvent,
		`{"type":"ping"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hel"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo <&>"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_2","name":"noargs","input":{}}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}`,
		stopEvent,
	)
	if err != nil {
		t.Fatal(err)
	}
	blocks := got["content"].([]any)
	if len(blocks) != 4 {
		t.Fatalf("%v", got)
	}
	thinking, text, tool, noargs := blocks[0].(map[string]any), blocks[1].(map[string]any), blocks[2].(map[string]any), blocks[3].(map[string]any)
	if thinking["thinking"] != "hm" || thinking["signature"] != "sig" || text["text"] != "hello <&>" {
		t.Fatalf("%v", blocks)
	}
	if input := tool["input"].(map[string]any); input["q"] != "x" || tool["id"] != "toolu_1" || tool["name"] != "lookup" {
		t.Fatalf("%v", tool)
	}
	if len(noargs["input"].(map[string]any)) != 0 {
		t.Fatalf("%v", noargs)
	}
	usage := got["usage"].(map[string]any)
	if got["stop_reason"] != "tool_use" || got["id"] != "msg_1" || got["model"] != "claude-opus-4.6" ||
		usage["output_tokens"] != float64(9) || usage["input_tokens"] != float64(20) || usage["cache_read_input_tokens"] != float64(8) || usage["cache_creation_input_tokens"] != float64(2) {
		t.Fatalf("%v", got)
	}
}

func TestFoldMessagesStreamFailsRatherThanLosingAnything(t *testing.T) {
	text := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	for name, events := range map[string][]string{
		"no message_stop":        {startEvent, text},
		"empty":                  {},
		"unknown delta":          {startEvent, text, `{"type":"content_block_delta","index":0,"delta":{"type":"audio_delta","data":"x"}}`, stopEvent},
		"delta without a block":  {startEvent, `{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"x"}}`, stopEvent},
		"skipped block index":    {startEvent, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"x"}}`, stopEvent},
		"broken tool input":      {startEvent, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"n","input":{}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`, `{"type":"content_block_stop","index":0}`, stopEvent},
		"message_delta too soon": {`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, stopEvent},
	} {
		if _, err := fold(t, events...); err == nil {
			t.Errorf("%s: folded", name)
		}
	}
}

func TestFoldMessagesStreamReportsAnInBandError(t *testing.T) {
	_, err := fold(t, startEvent, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	var inband *StreamError
	if !errors.As(err, &inband) || inband.Message() != "Overloaded" {
		t.Fatalf("%v", err)
	}
}

func TestFoldMessagesStreamStopsWhenTheRequestIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FoldMessagesStream(ctx, strings.NewReader(sse(startEvent, stopEvent))); err == nil {
		t.Fatal("a cancelled request folded")
	}
}
