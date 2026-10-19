// Package openai owns OpenAI wire adapters. PR 7 delivers the Chat
// Completions adapter: native passthrough first, canonical translation hooks
// for later provider PRs (SPEC sections 9-10, PRD-API-005/007).
package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxChatRequestBytes mirrors the compatibility body limit enforced by App.
const MaxChatRequestBytes int64 = 128 << 20

// ChatProtocol identifies the OpenAI Chat Completions wire protocol.
const ChatProtocol = "openai-chat"

// ChatRequest is the minimal parsed view of an OpenAI Chat Completions body.
// Raw keeps the exact client bytes so native paths can forward unknown
// forward-compatible fields untouched.
type ChatRequest struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
	Stream   bool            `json:"stream"`

	Raw    []byte
	Fields map[string]json.RawMessage
}

// ParseChatRequest validates the required shape and records the untouched bytes.
func ParseChatRequest(body []byte) (*ChatRequest, error) {
	if len(body) == 0 {
		return nil, errors.New("request body is required")
	}
	if int64(len(body)) > MaxChatRequestBytes {
		return nil, errors.New("request body exceeds the configured limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request ChatRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON object")
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("model is required")
	}
	var messages []json.RawMessage
	if len(request.Messages) == 0 || json.Unmarshal(request.Messages, &messages) != nil || messages == nil || len(messages) == 0 {
		return nil, errors.New("messages must be an array")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	request.Raw = append([]byte(nil), body...)
	request.Fields = fields
	return &request, nil
}

// Clone returns a copy whose Raw bytes can be safely mutated by transforms.
func (r *ChatRequest) Clone() *ChatRequest {
	cloned := *r
	cloned.Raw = append([]byte(nil), r.Raw...)
	cloned.Fields = make(map[string]json.RawMessage, len(r.Fields))
	for key, value := range r.Fields {
		cloned.Fields[key] = append(json.RawMessage(nil), value...)
	}
	return &cloned
}

// MarshalBody returns the exact outbound body for the native path, applying a
// model remap only when the resolved upstream model differs.
func (r *ChatRequest) MarshalBody(upstreamModel string) ([]byte, error) {
	if upstreamModel == "" || upstreamModel == r.Model {
		return append([]byte(nil), r.Raw...), nil
	}
	fields := make(map[string]json.RawMessage, len(r.Fields))
	for key, value := range r.Fields {
		fields[key] = value
	}
	remapped, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, err
	}
	fields["model"] = remapped
	return json.Marshal(fields)
}

// TerminalMarker is the exact stream sentinel OpenAI clients expect once.
const TerminalMarker = "[DONE]"

// ValidateStreamTerminal asserts exactly one terminal marker at stream end.
func ValidateStreamTerminal(body string) error {
	count := strings.Count(body, "data: "+TerminalMarker)
	if count != 1 {
		return fmt.Errorf("expected exactly one %s terminal marker, found %d", TerminalMarker, count)
	}
	if !strings.HasSuffix(body, "data: "+TerminalMarker+"\n\n") {
		return errors.New("terminal marker must be the final SSE event")
	}
	return nil
}
