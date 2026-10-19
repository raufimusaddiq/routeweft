package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

type translationFixture struct {
	Source           string          `json:"source"`
	Request          json.RawMessage `json:"request"`
	TargetRequest    json.RawMessage `json:"target_request"`
	UpstreamResponse json.RawMessage `json:"upstream_response"`
	Response         json.RawMessage `json:"response"`
}

func translationFixtures(t testing.TB) []translationFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/chat-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []translationFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func equalJSON(t testing.TB, got, want []byte) {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		t.Fatalf("invalid JSON: %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &right); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestChatResponsesTranslationFixtures(t *testing.T) {
	for _, fixture := range translationFixtures(t) {
		t.Run(fixture.Source, func(t *testing.T) {
			var request, response []byte
			var err error
			if fixture.Source == ChatProtocol {
				parsed, parseErr := ParseChatRequest(fixture.Request)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				parsed.Model = "vendor/model"
				request, err = ChatToResponses(parsed)
				if err != nil {
					t.Fatal(err)
				}
				response, err = ResponsesResponseToChat(fixture.UpstreamResponse)
			} else {
				parsed, parseErr := ParseResponsesRequest(fixture.Request, false)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				parsed.Model = "vendor/model"
				request, err = ResponsesToChat(parsed)
				if err != nil {
					t.Fatal(err)
				}
				response, err = ChatResponseToResponses(fixture.UpstreamResponse, CustomToolNames(parsed))
			}
			if err != nil {
				t.Fatal(err)
			}
			equalJSON(t, request, fixture.TargetRequest)
			equalJSON(t, response, fixture.Response)
		})
	}
}

func TestChatResponsesStreamingFixtures(t *testing.T) {
	for _, fixture := range translationFixtures(t) {
		t.Run(fixture.Source, func(t *testing.T) {
			name := "chat-upstream.sse"
			if fixture.Source == ChatProtocol {
				name = "responses-upstream.sse"
			}
			upstream, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if fixture.Source == ChatProtocol {
				err = ResponsesStreamToChat(context.Background(), bytes.NewReader(upstream), &output, "vendor/model")
			} else {
				parsed, _ := ParseResponsesRequest(fixture.Request, false)
				err = ChatStreamToResponses(context.Background(), bytes.NewReader(upstream), &output, "vendor/model", CustomToolNames(parsed))
			}
			if err != nil {
				t.Fatal(err)
			}
			if fixture.Source == ResponsesProtocol {
				terminals, sequence := 0, int64(0)
				deltas := map[string]string{}
				err = readEvents(context.Background(), &output, func(kind string, value map[string]any) (bool, error) {
					if integer(value["sequence_number"]) != sequence+1 {
						t.Fatalf("unordered event: %v", value)
					}
					sequence++
					if kind == "response.function_call_arguments.delta" {
						deltas[text(value["item_id"])] += text(value["delta"])
					}
					if kind == "response.completed" {
						terminals++
						body, _ := json.Marshal(value["response"])
						equalJSON(t, body, fixture.Response)
						if deltas["item_resp_chat_fixture_2"] != `{"city":"Oslo"}` {
							t.Fatalf("duplicated/missing arguments: %v", deltas)
						}
					}
					return false, nil
				})
				if !errors.Is(err, io.ErrUnexpectedEOF) || terminals != 1 {
					t.Fatalf("terminal count=%d err=%v", terminals, err)
				}
				return
			}
			if err := ValidateStreamTerminal(output.String()); err != nil {
				t.Fatal(err)
			}
			message := map[string]any{"role": "assistant", "content": "", "reasoning_content": ""}
			calls := map[int64]map[string]any{}
			var final map[string]any
			finished := 0
			err = readEvents(context.Background(), &output, func(kind string, value map[string]any) (bool, error) {
				if kind == "[DONE]" {
					return true, nil
				}
				choice := object(items(value["choices"])[0])
				delta := object(choice["delta"])
				for _, key := range []string{"content", "reasoning_content"} {
					message[key] = text(message[key]) + text(delta[key])
				}
				for _, raw := range items(delta["tool_calls"]) {
					call := object(raw)
					index := integer(call["index"])
					if calls[index] == nil {
						calls[index] = map[string]any{"type": "function", "function": map[string]any{"arguments": ""}}
					}
					stored := calls[index]
					copyFields(stored, call, "id")
					function := object(stored["function"])
					copyFields(function, object(call["function"]), "name")
					function["arguments"] = text(function["arguments"]) + text(object(call["function"])["arguments"])
				}
				if choice["finish_reason"] != nil {
					final = value
					finished++
				}
				return false, nil
			})
			if err != nil || finished != 1 || len(calls) != 2 {
				t.Fatalf("finish=%d calls=%d err=%v", finished, len(calls), err)
			}
			message["tool_calls"] = []any{calls[0], calls[1]}
			final["object"] = "chat.completion"
			object(items(final["choices"])[0])["message"] = message
			delete(object(items(final["choices"])[0]), "delta")
			body, _ := json.Marshal(final)
			equalJSON(t, body, fixture.Response)
		})
	}
}

func TestTranslatedStreamFailureAndCancellation(t *testing.T) {
	for _, source := range []string{ChatProtocol, ResponsesProtocol} {
		for _, upstream := range []string{"", "data: invalid\n\n", "data: {}\n\n", "data: " + strings.Repeat("x", (4<<20)+1)} {
			var output bytes.Buffer
			var err error
			if source == ChatProtocol {
				err = ResponsesStreamToChat(context.Background(), strings.NewReader(upstream), &output, "m")
			} else {
				err = ChatStreamToResponses(context.Background(), strings.NewReader(upstream), &output, "m", nil)
			}
			if err == nil || strings.Contains(output.String(), `"status":"completed"`) || strings.Count(output.String(), "upstream_incomplete") != 1 {
				t.Fatalf("%s err=%v output=%s", source, err, &output)
			}
			if source == ChatProtocol && ValidateStreamTerminal(output.String()) != nil {
				t.Fatal("failure missing terminal marker")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var output bytes.Buffer
		var err error
		if source == ChatProtocol {
			err = ResponsesStreamToChat(ctx, strings.NewReader("data: {}\n\n"), &output, "m")
		} else {
			err = ChatStreamToResponses(ctx, strings.NewReader("data: {}\n\n"), &output, "m", nil)
		}
		if !errors.Is(err, context.Canceled) || output.Len() != 0 {
			t.Fatalf("cancel %s err=%v output=%s", source, err, &output)
		}
	}
}

func TestTranslationInvalidShapes(t *testing.T) {
	for _, body := range []string{`{"model":"m","messages":[null]}`, `{"model":"m","messages":[{"role":"user","content":42}]}`, `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema"}}`} {
		request, err := ParseChatRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ChatToResponses(request); err == nil {
			t.Fatalf("invalid shape accepted: %s", body)
		}
	}
	for _, input := range []string{`42`, `[null]`, `[{"type":"message","role":"user","content":42}]`} {
		request, err := ParseResponsesRequest([]byte(`{"model":"m","input":`+input+`}`), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ResponsesToChat(request); err == nil {
			t.Fatalf("invalid input accepted: %s", input)
		}
	}
}

func TestTranslatedResponseTerminals(t *testing.T) {
	chat := []byte(`{"id":"c","choices":[{"message":{"role":"assistant","content":null,"refusal":"No"},"finish_reason":"content_filter"}]}`)
	converted, err := ChatResponseToResponses(chat, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := wireObject(converted)
	if response["status"] != "incomplete" || object(response["incomplete_details"])["reason"] != "content_filter" || !bytes.Contains(converted, []byte(`"refusal":"No"`)) {
		t.Fatalf("refusal/incomplete lost: %s", converted)
	}
	converted, err = ResponsesResponseToChat(converted)
	if err != nil || !bytes.Contains(converted, []byte(`"finish_reason":"content_filter"`)) || !bytes.Contains(converted, []byte(`"refusal":"No"`)) {
		t.Fatalf("reverse refusal/incomplete err=%v: %s", err, converted)
	}
	for _, source := range []string{ChatProtocol, ResponsesProtocol} {
		var output bytes.Buffer
		if source == ChatProtocol {
			err = ResponsesStreamToChat(context.Background(), strings.NewReader("event: response.incomplete\r\ndata: {\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[]}}\r\n\r\n"), &output, "m")
			if err != nil || !strings.Contains(output.String(), `"finish_reason":"length"`) || ValidateStreamTerminal(output.String()) != nil {
				t.Fatalf("incomplete Chat err=%v: %s", err, &output)
			}
		} else {
			err = ChatStreamToResponses(context.Background(), strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"), &output, "m", nil)
			if err != nil || strings.Count(output.String(), "event: response.incomplete\n") != 1 || strings.Contains(output.String(), "event: response.completed") {
				t.Fatalf("incomplete Responses err=%v: %s", err, &output)
			}
		}
		output.Reset()
		if source == ChatProtocol {
			err = ResponsesStreamToChat(context.Background(), strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"synthetic_failure\"}}}\n\n"), &output, "m")
		} else {
			err = ChatStreamToResponses(context.Background(), strings.NewReader("data: {\"error\":{\"code\":\"synthetic_failure\"}}\n\n"), &output, "m", nil)
		}
		if err == nil || strings.Count(output.String(), "synthetic_failure") != 1 || strings.Contains(output.String(), "upstream_incomplete") {
			t.Fatalf("upstream error lost/duplicated for %s err=%v: %s", source, err, &output)
		}
	}
}

func TestResponsesStreamDoneOnlyAndCustomTools(t *testing.T) {
	upstream := "event: response.output_item.added\ndata: {\"item\":{\"id\":\"custom\",\"type\":\"custom_tool_call\",\"name\":\"exec\",\"call_id\":\"call_1\"}}\n\n" +
		"data: {\"type\":\"response.custom_tool_call_input.delta\",\"item_id\":\"custom\",\"delta\":\"echo \"}\n\n" +
		"data: {\"type\":\"response.custom_tool_call_input.done\",\"item_id\":\"custom\",\"input\":\"echo hello\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"function\",\"type\":\"function_call\",\"name\":\"lookup\",\"call_id\":\"call_2\",\"arguments\":\"{}\"}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"reasoning\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"Plan\"}],\"encrypted_content\":\"synthetic-continuity\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"
	var output bytes.Buffer
	if err := ResponsesStreamToChat(context.Background(), strings.NewReader(upstream), &output, "m"); err != nil {
		t.Fatal(err)
	}
	if ValidateStreamTerminal(output.String()) != nil || strings.Count(output.String(), `"arguments":"{\"input\":\"echo hello\"}"`) != 1 || strings.Count(output.String(), `"arguments":"{}"`) != 1 || strings.Count(output.String(), "synthetic-continuity") != 1 || !strings.Contains(output.String(), `"reasoning_content":"Plan"`) {
		t.Fatalf("done-only/custom conversion: %s", &output)
	}
}

type failedStreamWriter struct{}

func (failedStreamWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTranslatedStreamWriteFailure(t *testing.T) {
	for _, source := range []string{ChatProtocol, ResponsesProtocol} {
		var err error
		if source == ChatProtocol {
			err = ResponsesStreamToChat(context.Background(), strings.NewReader("data: {\"type\":\"response.created\"}\n\n"), failedStreamWriter{}, "m")
		} else {
			err = ChatStreamToResponses(context.Background(), strings.NewReader("data: {\"choices\":[{\"delta\":{}}]}\n\n"), failedStreamWriter{}, "m", nil)
		}
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("write failure %s: %v", source, err)
		}
	}
}

func TestTranslatedStreamTotalLimit(t *testing.T) {
	for _, line := range []string{":commentxx\n", "\n", "\r\n"} {
		upstream := strings.NewReader(strings.Repeat(line, MaxTranslatedResponseBytes/len(line)+1))
		var output bytes.Buffer
		err := ChatStreamToResponses(context.Background(), upstream, &output, "m", nil)
		if err == nil || !strings.Contains(err.Error(), "exceeds limit") || strings.Count(output.String(), "event: response.failed\n") != 1 {
			t.Fatalf("limit err=%v output=%s", err, &output)
		}
	}
}

func TestTranslationRejectsTrailingJSON(t *testing.T) {
	for _, body := range []string{`{} {}`, `{} garbage`, `null`} {
		if _, err := ResponsesResponseToChat([]byte(body)); err == nil {
			t.Fatalf("accepted invalid response: %s", body)
		}
	}
}

func TestResponsesStreamRetainsUnstreamedContentParts(t *testing.T) {
	upstream := "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"content_index\":0,\"delta\":\"hello\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"msg\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"},{\"type\":\"refusal\",\"refusal\":\"no\"}]}]}}\n\n"
	var output bytes.Buffer
	if err := ResponsesStreamToChat(context.Background(), strings.NewReader(upstream), &output, "m"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), `"content":"hello"`) != 1 || strings.Count(output.String(), `"refusal":"no"`) != 1 || ValidateStreamTerminal(output.String()) != nil {
		t.Fatalf("lost or replayed content part: %s", &output)
	}
}
