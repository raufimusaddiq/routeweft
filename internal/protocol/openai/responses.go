package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const ResponsesProtocol = "openai-responses"

type ResponsesRequest struct {
	Model   string
	Stream  bool
	Compact bool
	Raw     []byte
	Fields  map[string]json.RawMessage
}

// ParseResponsesRequest preserves source bytes for native dispatch. Compact is
// an internal flag and never becomes an upstream JSON property.
func ParseResponsesRequest(body []byte, compact bool) (*ResponsesRequest, error) {
	if len(body) == 0 || int64(len(body)) > MaxChatRequestBytes {
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
	input, ok := fields["input"]
	if !ok || bytes.Equal(bytes.TrimSpace(input), []byte("null")) {
		return nil, errors.New("input is required")
	}
	var stream bool
	if raw, ok := fields["stream"]; ok && json.Unmarshal(raw, &stream) != nil {
		return nil, errors.New("stream must be a boolean")
	}
	var outbound []byte
	if compact {
		delete(fields, "_compact")
		outbound, _ = json.Marshal(fields)
	} else {
		outbound = append([]byte(nil), body...)
	}
	return &ResponsesRequest{Model: model, Stream: stream, Compact: compact, Raw: outbound, Fields: fields}, nil
}

func (r *ResponsesRequest) MarshalBody(upstreamModel string) ([]byte, error) {
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
