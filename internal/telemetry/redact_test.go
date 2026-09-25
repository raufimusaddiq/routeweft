package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactRemovesCredentialShapedKeys(t *testing.T) {
	payload := map[string]any{
		"model":         "gpt-5",
		"stream":        true,
		"authorization": "Bearer secret-value",
		"headers": map[string]any{
			"X-Api-Key":      "key-123",
			"Cookie":         "session=abc",
			"Content-Type":   "application/json",
			"anthropic-beta": "prompt-caching",
		},
		"provider": map[string]any{
			"refreshToken": "rt-1",
			"name":         "openai",
		},
		"messages": []any{map[string]any{"role": "user", "content": "hello", "apiKey": "k"}},
	}
	redacted, ok := Redact(payload).(map[string]any)
	if !ok {
		t.Fatal("redact returned non-map")
	}
	if redacted["authorization"] != redactedPlaceholder {
		t.Fatalf("authorization not redacted: %v", redacted["authorization"])
	}
	headers := redacted["headers"].(map[string]any)
	if headers["X-Api-Key"] != redactedPlaceholder || headers["Cookie"] != redactedPlaceholder {
		t.Fatalf("headers not redacted: %v", headers)
	}
	if headers["Content-Type"] != "application/json" || headers["anthropic-beta"] != "prompt-caching" {
		t.Fatalf("non-sensitive header changed: %v", headers)
	}
	provider := redacted["provider"].(map[string]any)
	if provider["refreshToken"] != redactedPlaceholder || provider["name"] != "openai" {
		t.Fatalf("provider not redacted correctly: %v", provider)
	}
	message := redacted["messages"].([]any)[0].(map[string]any)
	if message["apiKey"] != redactedPlaceholder || message["content"] != "hello" {
		t.Fatalf("nested message not redacted: %v", message)
	}
	// The original payload must be untouched.
	if payload["authorization"] != "Bearer secret-value" {
		t.Fatal("Redact mutated its input")
	}
}

func TestRedactJSONRejectsUnparsable(t *testing.T) {
	if _, ok := RedactJSON([]byte("not json")); ok {
		t.Fatal("unparsable JSON must be rejected, not stored")
	}
	document := []byte(`{"authorization":"Bearer x","model":"gpt"}`)
	out, ok := RedactJSON(document)
	if !ok || strings.Contains(string(out), "Bearer x") || !strings.Contains(string(out), "gpt") {
		t.Fatalf("redacted=%s ok=%v", out, ok)
	}
}

func TestRedactHeaderValue(t *testing.T) {
	if RedactHeaderValue("Authorization", "Bearer x") != redactedPlaceholder {
		t.Fatal("authorization header not redacted")
	}
	if RedactHeaderValue("Content-Type", "application/json") != "application/json" {
		t.Fatal("non-sensitive header changed")
	}
}

func TestRedactJSONRoundTripsValidDocument(t *testing.T) {
	var decoded map[string]any
	out, ok := RedactJSON([]byte(`{"a":{"b":[1,2,3]},"token":"t"}`))
	if !ok {
		t.Fatal("expected success")
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["token"] != redactedPlaceholder {
		t.Fatalf("token=%v", decoded["token"])
	}
}
