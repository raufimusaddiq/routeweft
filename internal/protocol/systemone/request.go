package systemone

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const Protocol = "systemone"

type Request struct {
	Model  string
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
	if _, ok := fields["state"]; !ok {
		return nil, errors.New("state is required")
	}
	if _, ok := fields["questions"]; !ok {
		return nil, errors.New("questions is required")
	}
	return &Request{model, append([]byte(nil), body...), fields}, nil
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
