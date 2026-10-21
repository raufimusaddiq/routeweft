package runtime

import (
	"testing"
	"time"
)

func TestTelemetryOptionsMapSettings(t *testing.T) {
	opts := telemetryOptions(map[string]string{
		"enableObservability":          "true",
		"observabilityMaxRecords":      "250",
		"observabilityBatchSize":       "50",
		"observabilityFlushIntervalMs": "1500",
	})
	if !opts.Enabled || opts.MaxRecords != 250 || opts.BatchSize != 50 || opts.FlushInterval != 1500*time.Millisecond {
		t.Fatalf("options=%+v", opts)
	}
	// Invalid values fall back to defaults.
	opts = telemetryOptions(map[string]string{"observabilityMaxRecords": "nope"})
	if opts.MaxRecords != 1000 || opts.Enabled {
		t.Fatalf("fallback options=%+v", opts)
	}
}

func TestObservabilityIntervalOverflowFallsBack(t *testing.T) {
	opts := telemetryOptions(map[string]string{"observabilityFlushIntervalMs": "9223372036854775807"})
	if opts.FlushInterval != 5*time.Second {
		t.Fatalf("invalid interval=%s", opts.FlushInterval)
	}
	if err := validateSettingValue("observabilityFlushIntervalMs", "9223372036854775807"); err == nil {
		t.Fatal("accepted overflowing interval")
	}
}
