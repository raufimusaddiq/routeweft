package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// StreamError is an in-band error event the gateway sent in a Messages stream.
type StreamError struct{ Raw json.RawMessage }

func (e *StreamError) Error() string { return "upstream stream reported an error: " + string(e.Raw) }

// Message is the upstream error's own message, or the raw JSON when it has none.
func (e *StreamError) Message() string {
	var failure struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Raw, &failure) == nil && failure.Message != "" {
		return failure.Message
	}
	return string(e.Raw)
}

// FoldMessagesStream folds an Anthropic Messages SSE stream into the one message
// document a non-stream client asked for. It is for an endpoint that is sent
// stream:true whatever the client wanted (Copilot's /v1/messages for Claude:
// open-sse/executors/github.js:259-263, which the pin leaves to chatCore's
// buffering of an SSE reply into one JSON body).
//
// The document is the message_start message with its content rebuilt from the
// content blocks, stop_reason and stop_sequence from message_delta, and usage
// merged over message_start's by message_delta's cumulative counts. Nothing the
// stream could carry is dropped or invented:
//
//   - a delta of a kind this fold does not know fails it, because it could carry
//     text;
//   - a stream that ends before message_stop is truncated and fails;
//   - an error event fails it as a StreamError;
//   - tool input that does not assemble into a JSON value fails it.
func FoldMessagesStream(ctx context.Context, source io.Reader) ([]byte, error) {
	var message map[string]any
	blocks := map[int64]map[string]any{}
	partial := map[int64]*strings.Builder{}
	done := false
	err := concerns.ReadEvents(ctx, source, func(kind string, event map[string]any) (bool, error) {
		switch kind {
		case "message_start":
			message = concerns.Object(event["message"])
			if message == nil {
				return false, errors.New("stream message_start carried no message")
			}
		case "content_block_start":
			block := concerns.Object(event["content_block"])
			if block == nil {
				return false, errors.New("stream content_block_start carried no block")
			}
			blocks[concerns.Int(event["index"])] = block
		case "content_block_delta":
			index := concerns.Int(event["index"])
			block, delta := blocks[index], concerns.Object(event["delta"])
			if block == nil || delta == nil {
				return false, errors.New("stream delta has no open block")
			}
			return false, applyDelta(block, partial, index, delta)
		case "content_block_stop":
			index := concerns.Int(event["index"])
			if builder := partial[index]; builder != nil && blocks[index] != nil {
				input := strings.TrimSpace(builder.String())
				if input == "" {
					input = "{}"
				}
				var value any
				if json.Unmarshal([]byte(input), &value) != nil {
					return false, errors.New("stream tool input is not JSON")
				}
				blocks[index]["input"] = json.RawMessage(input)
				delete(partial, index)
			}
		case "message_delta":
			if message == nil {
				return false, errors.New("stream message_delta before message_start")
			}
			if delta := concerns.Object(event["delta"]); delta != nil {
				for key, value := range delta {
					message[key] = value
				}
			}
			if usage := concerns.Object(event["usage"]); usage != nil {
				merged := concerns.Object(message["usage"])
				if merged == nil {
					merged = map[string]any{}
				}
				for key, value := range usage {
					merged[key] = value
				}
				message["usage"] = merged
			}
		case "message_stop":
			done = true
			return true, nil
		case "error":
			raw, _ := json.Marshal(event["error"])
			return false, &StreamError{Raw: raw}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if !done || message == nil {
		return nil, errors.New("stream ended before message_stop")
	}
	indexes := make([]int64, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	content := make([]any, 0, len(indexes))
	for position, index := range indexes {
		if index != int64(position) {
			return nil, fmt.Errorf("stream skipped content block %d", position)
		}
		content = append(content, blocks[index])
	}
	message["content"] = content
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(message); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

func applyDelta(block map[string]any, partial map[int64]*strings.Builder, index int64, delta map[string]any) error {
	switch concerns.Text(delta["type"]) {
	case "text_delta":
		block["text"] = concerns.Text(block["text"]) + concerns.Text(delta["text"])
	case "thinking_delta":
		block["thinking"] = concerns.Text(block["thinking"]) + concerns.Text(delta["thinking"])
	case "signature_delta":
		block["signature"] = concerns.Text(block["signature"]) + concerns.Text(delta["signature"])
	case "input_json_delta":
		if partial[index] == nil {
			partial[index] = &strings.Builder{}
		}
		partial[index].WriteString(concerns.Text(delta["partial_json"]))
	case "citations_delta":
		citations := concerns.Items(block["citations"])
		block["citations"] = append(citations, delta["citation"])
	default:
		return fmt.Errorf("stream carried a %q delta the fold cannot carry", concerns.Text(delta["type"]))
	}
	return nil
}
