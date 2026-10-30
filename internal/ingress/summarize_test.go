package ingress

import (
	"fmt"
	"testing"
)

func TestSummarizeResponseReadsEachProtocol(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		stream bool
		want   ResponseSummary
	}{
		{"chat", `{"choices":[{"message":{"content":"hi","reasoning_content":"think"},"finish_reason":"stop"}]}`, false, ResponseSummary{"hi", "think", "stop"}},
		{"anthropic", `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`, false, ResponseSummary{Content: "hi", FinishReason: "end_turn"}},
		{"chat stream", "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true, ResponseSummary{Content: "ab", FinishReason: "stop"}},
		{"anthropic stream", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n", true, ResponseSummary{Content: "x", FinishReason: "end_turn"}},
		{"ollama stream", "{\"message\":{\"content\":\"o\"},\"done\":false}\n{\"message\":{\"content\":\"k\"},\"done\":true,\"done_reason\":\"stop\"}\n", true, ResponseSummary{Content: "ok", FinishReason: "stop"}},
		{"not json", `<html>`, false, ResponseSummary{}},
		{"cut off stream", "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choices\":[{\"del", true, ResponseSummary{Content: "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SummarizeResponse([]byte(c.body), c.stream); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestRequestConfigKeepsTheConversationAndKnownParameters(t *testing.T) {
	got := RequestConfig([]byte(`{"model":"m","messages":[{"role":"user","content":"q"}],"max_tokens":5,"temperature":0.1,"stream":true,"tools":[],"surprise":1}`), true)
	if fmt.Sprint(got["max_tokens"]) != "5" || fmt.Sprint(got["temperature"]) != "0.1" || got["model"] != "m" || got["messages"] == nil {
		t.Fatalf("config %v", got)
	}
	if _, kept := got["surprise"]; kept {
		t.Fatalf("unknown fields must not be copied: %v", got)
	}
	if RequestConfig([]byte(`not json`), false) != nil {
		t.Fatal("an undecodable body has no config")
	}
	if got := RequestConfig([]byte(`{"input":"i"}`), false); got["input"] != "i" {
		t.Fatalf("responses input: %v", got)
	}
	if got := RequestConfig([]byte(`{"contents":[{"parts":[]}]}`), false); got["contents"] == nil {
		t.Fatalf("gemini contents: %v", got)
	}
}
