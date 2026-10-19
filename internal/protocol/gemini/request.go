package gemini

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const Protocol = "gemini"

type Request struct {
	Model  string
	Stream bool
	Raw    []byte
	Fields map[string]json.RawMessage
}

func ParseRequest(model string, stream bool, body []byte) (*Request, error) {
	if strings.TrimSpace(model) == "" || len(body) == 0 {
		return nil, errors.New("model and JSON body are required")
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
	var contents []json.RawMessage
	if raw, ok := fields["contents"]; !ok || json.Unmarshal(raw, &contents) != nil || len(contents) == 0 {
		return nil, errors.New("contents must be a non-empty array")
	}
	return &Request{Model: model, Stream: stream, Raw: append([]byte(nil), body...), Fields: fields}, nil
}

func (r *Request) MarshalBody(model string) ([]byte, error) {
	if model == "" || model == r.Model {
		return append([]byte(nil), r.Raw...), nil
	}
	fields := make(map[string]json.RawMessage, len(r.Fields))
	for key, value := range r.Fields {
		fields[key] = append(json.RawMessage(nil), value...)
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	fields["model"] = encoded
	return json.Marshal(fields)
}
