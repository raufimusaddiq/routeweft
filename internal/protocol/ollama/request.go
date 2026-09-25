package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const Protocol = "ollama"

type Request struct {
	Model  string
	Stream bool
	Raw    []byte
	Fields map[string]json.RawMessage
}

func ParseRequest(body []byte) (*Request, error) {
	if len(body) == 0 {
		return nil, errors.New("request body is required")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	if err := d.Decode(&fields); err != nil || fields == nil {
		return nil, errors.New("request body must be one JSON object")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON object")
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return nil, errors.New("model is required")
	}
	var messages []json.RawMessage
	if raw, ok := fields["messages"]; !ok || json.Unmarshal(raw, &messages) != nil || len(messages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}
	stream := true
	if raw, ok := fields["stream"]; ok && json.Unmarshal(raw, &stream) != nil {
		return nil, errors.New("stream must be a boolean")
	}
	return &Request{model, stream, append([]byte(nil), body...), fields}, nil
}

func (r *Request) MarshalBody(model string) ([]byte, error) {
	if model == "" || model == r.Model {
		return append([]byte(nil), r.Raw...), nil
	}
	fields := make(map[string]json.RawMessage, len(r.Fields))
	for k, v := range r.Fields {
		fields[k] = append(json.RawMessage(nil), v...)
	}
	b, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	fields["model"] = b
	return json.Marshal(fields)
}
