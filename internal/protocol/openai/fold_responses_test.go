package openai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// event is one SSE frame as the Codex endpoint writes it: an event line and a
// data line whose JSON repeats the type.
func event(kind, data string) string {
	return "event: " + kind + "\ndata: " + data + "\n\n"
}

func foldResponses(t *testing.T, stream string) (map[string]any, error) {
	t.Helper()
	folded, err := FoldResponsesStream(strings.NewReader(stream))
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(folded.Body, &document); err != nil {
		t.Fatalf("folded body is not JSON: %v: %s", err, folded.Body)
	}
	return document, nil
}

const codexAnswer = `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`

func TestFoldResponsesStreamFillsAnEmptyOutputFromTheItems(t *testing.T) {
	// The Codex endpoint ends with a response.completed whose output is empty: the
	// items were already sent.
	document, err := foldResponses(t, ""+
		event("response.created", `{"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"gpt-5","instructions":"be brief"}}`)+
		event("response.output_item.done", codexAnswer)+
		event("response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi <b> & bye"}]}}`)+
		event("response.completed", `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5","output":[],"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":8},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":15}}}`))
	if err != nil {
		t.Fatal(err)
	}
	output := document["output"].([]any)
	if len(output) != 2 || output[0].(map[string]any)["type"] != "reasoning" || output[1].(map[string]any)["id"] != "msg_1" {
		t.Fatalf("output %v", output)
	}
	if text := output[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; text != "Hi <b> & bye" {
		t.Fatalf("text %q", text)
	}
	// The terminal response is the document: id, model and the usage detail the
	// endpoint reported, including the cached tokens a cost report needs.
	usage := document["usage"].(map[string]any)
	if document["id"] != "resp_1" || document["model"] != "gpt-5" || document["status"] != "completed" ||
		usage["input_tokens_details"].(map[string]any)["cached_tokens"] != float64(8) || usage["total_tokens"] != float64(15) {
		t.Fatalf("document %v", document)
	}
}

func TestFoldResponsesStreamKeepsAnOutputTheTerminalEventCarries(t *testing.T) {
	document, err := foldResponses(t, ""+
		event("response.output_item.done", codexAnswer)+
		event("response.completed", `{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"id":"msg_9","type":"message","content":[]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	output := document["output"].([]any)
	if len(output) != 1 || output[0].(map[string]any)["id"] != "msg_9" {
		t.Fatalf("the terminal output was replaced: %v", output)
	}
}

func TestFoldResponsesStreamDoesNotInventUsage(t *testing.T) {
	// A usage nobody reported is unknown, not zero: the key stays absent.
	document, err := foldResponses(t, event("response.completed", `{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := document["usage"]; present {
		t.Fatalf("usage invented: %v", document["usage"])
	}
}

func TestFoldResponsesStreamIncompleteIsATerminalResponse(t *testing.T) {
	document, err := foldResponses(t, event("response.incomplete", `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if document["status"] != "incomplete" || document["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("document %v", document)
	}
}

func TestFoldResponsesStreamReportsInBandFailures(t *testing.T) {
	for name, stream := range map[string]string{
		"response.failed": event("response.failed", `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded."}}}`),
		"error":           event("error", `{"type":"error","error":{"code":"rate_limit_exceeded","message":"Our servers are currently overloaded."}}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := foldResponses(t, stream)
			var inband *StreamError
			if !errors.As(err, &inband) || inband.Message() != "Our servers are currently overloaded." {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestFoldResponsesStreamRefusesWhatItWouldHaveToInvent(t *testing.T) {
	for name, stream := range map[string]string{
		"truncated": event("response.output_item.done", codexAnswer),
		"empty":     "",
		"gap":       event("response.output_item.done", strings.Replace(codexAnswer, `"output_index":0`, `"output_index":1`, 1)) + event("response.completed", `{"type":"response.completed","response":{"id":"r","output":[]}}`),
		"not json":  "data: {oops\n\n",
		"no body":   event("response.completed", `{"type":"response.completed"}`),
		"no index":  event("response.output_item.done", `{"type":"response.output_item.done","item":{}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if document, err := foldResponses(t, stream); err == nil {
				t.Fatalf("folded %v", document)
			}
		})
	}
}

func TestFoldResponsesStreamReadsFramingVariants(t *testing.T) {
	for name, stream := range map[string]string{
		"CRLF":             strings.ReplaceAll(event("response.completed", `{"type":"response.completed","response":{"id":"r","output":[]}}`), "\n", "\r\n"),
		"no event line":    "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[]}}\n\n",
		"no final blank":   "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[]}}",
		"DONE after":       event("response.completed", `{"type":"response.completed","response":{"id":"r","output":[]}}`) + "data: [DONE]\n\n",
		"comment lines":    ": keep-alive\n\n" + event("response.completed", `{"type":"response.completed","response":{"id":"r","output":[]}}`),
		"data without gap": "data:{\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[]}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			document, err := foldResponses(t, stream)
			if err != nil || document["id"] != "r" {
				t.Fatalf("%v %v", document, err)
			}
		})
	}
}

func TestFoldResponsesStreamBoundsWhatItReads(t *testing.T) {
	huge := strings.Repeat("a", MaxTranslatedResponseBytes)
	_, err := FoldResponsesStream(strings.NewReader("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"t\":\"" + huge + "\"}}\n\n"))
	if err == nil {
		t.Fatal("an oversized stream was folded")
	}
}

func TestFoldResponsesStreamWithNoOutputIsAValidResponseWithAnEmptyOutput(t *testing.T) {
	for name, terminal := range map[string]string{
		"omitted": `{"type":"response.completed","response":{"id":"r1","status":"completed"}}`,
		"empty":   `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			document, err := foldResponses(t, event("response.completed", terminal))
			output, present := document["output"].([]any)
			if err != nil || !present || len(output) != 0 || document["id"] != "r1" {
				t.Fatalf("%v %v", document, err)
			}
			// A translated client turns it into an empty assistant message, not a 502.
			folded, _ := FoldResponsesStream(strings.NewReader(event("response.completed", terminal)))
			if _, err := ResponsesResponseToChat(folded.Body); err != nil {
				t.Fatalf("the translator refused it: %v", err)
			}
		})
	}
}
