package promptcache

import (
	"encoding/json"
	"testing"
)

func TestParseAnthropicCacheUsage(t *testing.T) {
	body, err := json.Marshal(map[string]any{"usage": map[string]int{"input_tokens": 120, "output_tokens": 24, "cache_read_input_tokens": 80, "cache_creation_input_tokens": 30}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := ParseAnthropicUsage(body)
	want := Usage{InputTokens: 120, OutputTokens: 24, CacheReadTokens: 80, CacheWriteTokens: 30}
	if !ok || got != want {
		t.Fatalf("usage=%+v ok=%v", got, ok)
	}
	for _, malformed := range [][]byte{nil, []byte("not json"), []byte("{}")} {
		if _, ok := ParseAnthropicUsage(malformed); ok {
			t.Errorf("reported usage for %q", malformed)
		}
	}
	partial, ok := ParseAnthropicUsage([]byte(`{"usage":{"input_tokens":1}}`))
	if !ok || partial.CacheReadTokens != 0 || partial.CacheWriteTokens != 0 {
		t.Fatalf("partial=%+v ok=%v", partial, ok)
	}
	started, ok := ParseAnthropicUsage([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":8,"cache_creation_input_tokens":2}}}`))
	if !ok || started.CacheReadTokens != 8 || started.CacheWriteTokens != 2 {
		t.Fatalf("stream start usage=%+v ok=%v", started, ok)
	}
	delta, ok := ParseAnthropicUsage([]byte(`{"type":"message_delta","usage":{"output_tokens":7}}`))
	if !ok || delta.OutputTokens != 7 {
		t.Fatalf("stream delta usage=%+v ok=%v", delta, ok)
	}
	merged := started.Merge(delta)
	if merged.CacheReadTokens != 8 || merged.CacheWriteTokens != 2 || merged.OutputTokens != 7 {
		t.Fatalf("merged=%+v", merged)
	}
}
