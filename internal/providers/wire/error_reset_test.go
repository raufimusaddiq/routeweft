package wire

import (
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func TestPinnedErrorBodyResets(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, provider, body string
		delay                time.Duration
	}{
		{"codex absolute wins", "codex", `{"error":{"type":"usage_limit_reached","resets_at":1767225720,"resets_in_seconds":30}}`, 2 * time.Minute},
		{"codex relative", "codex", `{"error":{"type":"usage_limit_reached","resets_in_seconds":30.5}}`, 30500 * time.Millisecond},
		{"codex expired absolute", "codex", `{"error":{"type":"usage_limit_reached","resets_at":1,"resets_in_seconds":60}}`, time.Minute},
		{"codex mistyped absolute", "codex", `{"error":{"type":"usage_limit_reached","resets_at":"soon","resets_in_seconds":60}}`, time.Minute},
		{"codex missing", "codex", `{"error":{"type":"usage_limit_reached"}}`, 0},
		{"codex null", "codex", `{"error":{"type":"usage_limit_reached","resets_at":null,"resets_in_seconds":null}}`, 0},
		{"codex zero", "codex", `{"error":{"type":"usage_limit_reached","resets_at":0,"resets_in_seconds":0}}`, 0},
		{"codex below clock precision", "codex", `{"error":{"type":"usage_limit_reached","resets_in_seconds":1e-100}}`, 0},
		{"codex negative", "codex", `{"error":{"type":"usage_limit_reached","resets_at":-1,"resets_in_seconds":-1}}`, 0},
		{"codex string", "codex", `{"error":{"type":"usage_limit_reached","resets_in_seconds":"30"}}`, 0},
		{"codex overflow", "codex", `{"error":{"type":"usage_limit_reached","resets_at":1e100,"resets_in_seconds":1e100}}`, 0},
		{"codex other error", "codex", `{"error":{"type":"invalid_api_key","resets_in_seconds":30}}`, 0},
		{"google fractional", "gemini-cli", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30.125s"}]}}`, 30125 * time.Millisecond},
		{"google skips unrelated", "gemini-cli", `{"error":{"details":[null,42,{"@type":"other","retryDelay":"10s"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":10},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"60s"}]}}`, time.Minute},
		{"google missing", "gemini-cli", `{"error":{"details":[]}}`, 0},
		{"google null", "gemini-cli", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":null}]}}`, 0},
		{"google zero", "gemini-cli", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`, 0},
		{"google negative", "gemini-cli", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"-30s"}]}}`, 0},
		{"google units", "gemini-cli", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"1m"}]}}`, 0},
		{"google overflow", "gemini-cli", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"999999999999999999s"}]}}`, 0},
		{"google malformed", "gemini-cli", `{"error":{"details":[`, 0},
		{"codex malformed", "codex", `{"error":`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClassifyError(tc.provider, 429, []byte(tc.body), now)
			if tc.delay == 0 {
				if ok || got != (routing.Classification{}) {
					t.Fatalf("invented a reset: %+v, %v", got, ok)
				}
			} else {
				if !ok || got.Outcome != routing.OutcomeQuotaLock || got.ResetAt != now.Add(tc.delay).Format(time.RFC3339Nano) {
					t.Fatalf("reset %+v, %v; want %s", got, ok, now.Add(tc.delay))
				}
			}
			for _, status := range []int{200, 400, 401, 403, 500} {
				if got, ok := ClassifyError(tc.provider, status, []byte(tc.body), now); ok {
					t.Fatalf("status %d misclassified: %+v", status, got)
				}
			}
		})
	}
}

func BenchmarkPinnedErrorBodyResets(b *testing.B) {
	now := time.Now()
	for _, tc := range []struct{ provider, body string }{
		{"codex", `{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`},
		{"gemini-cli", `{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`},
	} {
		b.Run(tc.provider, func(b *testing.B) {
			body := []byte(tc.body)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ClassifyError(tc.provider, 429, body, now)
			}
		})
	}
}
