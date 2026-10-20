package api

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

// C07 part 1: the throttle reports whole seconds, rounded up, never zero while
// locked (pin src/lib/auth/loginLimiter.js:28 Math.ceil(remaining/1000)).
func TestThrottlerRetryAfterIsCeilSeconds(t *testing.T) {
	th := newThrottler()
	now := time.Unix(1000, 0)
	th.now = func() time.Time { return now }
	for i := 0; i < loginMaxFailures; i++ {
		if got := th.retryAfter("k"); got != 0 {
			t.Fatalf("locked after %d failures: %d", i, got)
		}
		th.fail("k")
	}
	if got := th.retryAfter("k"); got != int(loginWindow/time.Second) {
		t.Fatalf("retryAfter=%d want %d", got, int(loginWindow/time.Second))
	}
	now = now.Add(loginWindow - 1500*time.Millisecond)
	if got := th.retryAfter("k"); got != 2 {
		t.Fatalf("sub-second remainder must round up: got %d want 2", got)
	}
	now = now.Add(time.Second)
	if got := th.retryAfter("k"); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	now = now.Add(time.Second)
	if got := th.retryAfter("k"); got != 0 {
		t.Fatalf("lock must expire: got %d", got)
	}
}

// C07 part 2: sanitization keeps requested and upstream model attribution
// while still redacting secrets logged beside it.
func TestLogSanitizerRetainsModelAttribution(t *testing.T) {
	buffer := NewLogBuffer(4)
	var sink bytes.Buffer
	logger := slog.New(LogHandler(slog.NewTextHandler(&sink, nil), buffer))
	logger.Info("model probe",
		"requestedModel", "combo/fast",
		"upstreamModel", "accounts/fireworks/models/llama-v3p1-70b",
		"servedModel", "hf:org/model@cf",
		"model", "gpt-5",
		"apiKey", "sk-live-123")
	items, _ := buffer.list(page{Number: 1, Size: 1}, "", "")
	attrs := items[0].Attributes
	for key, want := range map[string]string{
		"requestedModel": "combo/fast",
		"upstreamModel":  "accounts/fireworks/models/llama-v3p1-70b",
		"servedModel":    "hf:org/model@cf",
		"model":          "gpt-5",
	} {
		if attrs[key] != want {
			t.Fatalf("%s=%v want %q (sanitizer stripped attribution): %+v", key, attrs[key], want, attrs)
		}
		if !strings.Contains(sink.String(), want) {
			t.Fatalf("downstream handler lost %s=%q: %s", key, want, sink.String())
		}
	}
	if attrs["apiKey"] != "[redacted]" || strings.Contains(sink.String(), "sk-live-123") {
		t.Fatalf("secret beside model leaked: %+v %s", attrs, sink.String())
	}
}

// C07 part 3: the pin has no X-Sync-Secret implementation and Routeweft has no
// sync-secret ingress, so the only way such input reaches a log is as a header
// or attribute. Assert every sanitizer on those paths redacts it.
func TestSyncSecretNeverReachesLogs(t *testing.T) {
	const secret = "sync-s3cr3t-value-9f2"
	buffer := NewLogBuffer(4)
	var sink bytes.Buffer
	logger := slog.New(LogHandler(slog.NewTextHandler(&sink, nil), buffer))
	logger.Info("sync request",
		"X-Sync-Secret", secret,
		"x_sync_secret", secret,
		"headers", slog.GroupValue(slog.String("X-Sync-Secret", secret)))
	logger.Warn("upstream said X-Sync-Secret: " + secret)
	items, _ := buffer.list(page{Number: 1, Size: 4}, "", "")
	if len(items) != 2 {
		t.Fatalf("items=%d", len(items))
	}
	if strings.Contains(sink.String(), secret) || strings.Contains(toString(items), secret) {
		t.Fatalf("sync secret reached console log: %s / %s", sink.String(), toString(items))
	}

	// Request-detail path: header values and JSON bodies.
	if got := telemetry.RedactHeaderValue("X-Sync-Secret", secret); got == secret {
		t.Fatal("telemetry header redaction leaked the sync secret")
	}
	out, ok := telemetry.RedactJSON([]byte(`{"headers":{"X-Sync-Secret":"` + secret + `"},"syncSecret":"` + secret + `"}`))
	if !ok || strings.Contains(string(out), secret) {
		t.Fatalf("telemetry body redaction leaked: ok=%v %s", ok, out)
	}
}
