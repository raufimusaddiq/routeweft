// Package anthropic owns Anthropic Messages wire adapters.
package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const MessagesProtocol = "anthropic-messages"

// MaxMessagesRequestBytes mirrors the compatibility body limit enforced by ingress.
const MaxMessagesRequestBytes int64 = 128 << 20

// MessagesRequest is the retained Anthropic Messages request view. Raw keeps the
// exact bytes so tool_use/tool_result ordering, thinking, and cache_control
// fields reach native Messages providers unchanged.
type MessagesRequest struct {
	Model  string
	Stream bool
	Raw    []byte
	Fields map[string]json.RawMessage
	// AnthropicBeta records a client anthropic-beta header value carried in the
	// parsed request for the native transport to forward.
	AnthropicBeta string
}

func ParseMessagesRequest(body []byte) (*MessagesRequest, error) {
	if len(body) == 0 || int64(len(body)) > MaxMessagesRequestBytes {
		return nil, errors.New("request body is empty or exceeds the configured limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, errors.New("request body must be one JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON object")
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil || strings.TrimSpace(model) == "" {
		return nil, errors.New("model is required")
	}
	var messages []json.RawMessage
	if raw, ok := fields["messages"]; !ok || json.Unmarshal(raw, &messages) != nil || messages == nil || len(messages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}
	var stream bool
	if raw, ok := fields["stream"]; ok && json.Unmarshal(raw, &stream) != nil {
		return nil, errors.New("stream must be a boolean")
	}
	return &MessagesRequest{Model: model, Stream: stream, Raw: append([]byte(nil), body...), Fields: fields}, nil
}

func (r *MessagesRequest) MarshalBody(upstreamModel string) ([]byte, error) {
	if upstreamModel == "" || upstreamModel == r.Model {
		return append([]byte(nil), r.Raw...), nil
	}
	fields := make(map[string]json.RawMessage, len(r.Fields))
	for key, value := range r.Fields {
		fields[key] = append(json.RawMessage(nil), value...)
	}
	model, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("encode upstream model: %w", err)
	}
	fields["model"] = model
	return json.Marshal(fields)
}
