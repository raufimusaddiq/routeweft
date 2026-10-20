package shared

import "testing"

func TestSystemOneUsageNativeAndCloudflare(t *testing.T) {
	for _, body := range []string{
		`{"answers":{"q":true},"usage":{"input_tokens":18,"output_tokens":2,"cache_read_input_tokens":4,"cache_creation_input_tokens":1}}`,
		`{"result":{"answers":{"q":true},"usage":{"input_tokens":18,"output_tokens":2,"cache_read_input_tokens":4,"cache_creation_input_tokens":1}},"success":true}`,
	} {
		usage, ok := ParseUsage(FamilySystemOne, []byte(body))
		if !ok || usage.InputTokens != 18 || usage.OutputTokens != 2 || usage.CacheReadTokens != 4 || usage.CacheWriteTokens != 1 {
			t.Errorf("usage=%+v present=%v", usage, ok)
		}
	}
	for _, body := range []string{`{"usage":{"input_tokens":18,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}`, `{"usage":{"prompt_tokens":18,"completion_tokens":2,"cached_tokens":4}}`} {
		usage, ok := ParseUsage(FamilySystemOne, []byte(body))
		if !ok || usage.InputTokens != 18 || usage.OutputTokens != 2 || usage.CacheReadTokens != 4 {
			t.Errorf("compatibility usage=%+v present=%v", usage, ok)
		}
	}
	for _, body := range []string{`{"result":null}`, `{"result":[]}`, `{"result":{"answers":{}}}`, `{"usage":{"input_tokens":"bad"}}`, `{"result":`, `not JSON`} {
		if _, ok := ParseUsage(FamilySystemOne, []byte(body)); ok {
			t.Errorf("malformed or absent usage accepted: %s", body)
		}
	}
}

func TestParseUsagePerProtocolFamily(t *testing.T) {
	cases := []struct {
		name   string
		family string
		body   string
		input  int64
		output int64
		cache  int64
	}{
		{"openai chat", FamilyOpenAIChat, `{"usage":{"prompt_tokens":10,"completion_tokens":4}}`, 10, 4, 0},
		{"openai chat responses-style", FamilyOpenAIChat, `{"usage":{"input_tokens":7,"output_tokens":3,"input_tokens_details":{"cached_tokens":5}}}`, 7, 3, 5},
		{"openai responses", FamilyOpenAIResponses, `{"usage":{"input_tokens":9,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}`, 9, 2, 4},
		{"responses nested", FamilyOpenAIResponses, `{"response":{"usage":{"input_tokens":6,"output_tokens":1}}}`, 6, 1, 0},
		{"gemini", FamilyGemini, `{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"cachedContentTokenCount":3}}`, 11, 5, 3},
		{"ollama", FamilyOllama, `{"prompt_eval_count":8,"eval_count":6}`, 8, 6, 0},
		{"ollama cached subset of the prompt", FamilyOllama, `{"prompt_eval_count":8,"prompt_eval_cached_count":5,"eval_count":6}`, 8, 6, 5},
		{"anthropic", FamilyAnthropic, `{"usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}`, 12, 7, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseUsage(tc.family, []byte(tc.body))
			if !ok || got.InputTokens != tc.input || got.OutputTokens != tc.output || got.CacheReadTokens != tc.cache {
				t.Fatalf("usage=%+v ok=%v", got, ok)
			}
		})
	}
}

func TestParseUsageReportsAbsentUsage(t *testing.T) {
	for _, tc := range []struct {
		family string
		body   string
	}{
		{FamilyOpenAIChat, `{}`},
		{FamilyOpenAIChat, `not json`},
		{FamilyOpenAIResponses, `{}`},
		{FamilyGemini, `{}`},
		{FamilyOllama, `{}`},
		{FamilySystemOne, `{}`},
		{"unknown-family", `{}`},
	} {
		if _, ok := ParseUsage(tc.family, []byte(tc.body)); ok {
			t.Fatalf("family=%q body=%q reported usage", tc.family, tc.body)
		}
	}
}

func TestParseUsageNestedResponsesReadsCachedTokens(t *testing.T) {
	body := `{"type":"response.completed","response":{"usage":{"input_tokens":80,"output_tokens":9,"input_tokens_details":{"cached_tokens":48}}}}`
	got, ok := ParseUsage(FamilyOpenAIResponses, []byte(body))
	if !ok || got.InputTokens != 80 || got.OutputTokens != 9 || got.CacheReadTokens != 48 {
		t.Fatalf("usage=%+v ok=%v", got, ok)
	}
}

// The upstream's own figures are emitted as reported. total_tokens disagrees
// with its parts here on purpose and must change nothing; reasoning_tokens is
// already inside the reported output and is not read separately (see #119).
func TestParseUsageEmitsReportedFiguresWithoutReconciling(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"prompt_tokens":100,"completion_tokens":30,"total_tokens":999,"completion_tokens_details":{"reasoning_tokens":25}}}`,
		`{"response":{"usage":{"input_tokens":100,"output_tokens":30,"total_tokens":999,"output_tokens_details":{"reasoning_tokens":25}}}}`,
	} {
		got, ok := ParseUsage(FamilyOpenAIResponses, []byte(body))
		if !ok || got.InputTokens != 100 || got.OutputTokens != 30 || got.CacheReadTokens != 0 {
			t.Fatalf("body=%s usage=%+v ok=%v", body, got, ok)
		}
	}
}
