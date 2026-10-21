package runtime

import (
	"strconv"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

func (s *RuntimeSnapshot) TelemetryOptions() telemetry.Options { return s.telemetryOptions }
func (s *RuntimeSnapshot) DetailSizeLimit() int                { return s.detailSizeLimit }

func telemetryOptions(settings map[string]string) telemetry.Options {
	opts := telemetry.DefaultOptions()
	opts.Enabled = settings["enableObservability"] == "true"
	if value, err := strconv.Atoi(settings["observabilityMaxRecords"]); err == nil && value > 0 {
		opts.MaxRecords = value
	}
	if value, err := strconv.Atoi(settings["observabilityBatchSize"]); err == nil && value > 0 {
		opts.BatchSize = value
	}
	if value, err := strconv.Atoi(settings["observabilityFlushIntervalMs"]); err == nil && value > 0 && uint64(value) <= uint64((1<<63-1)/time.Millisecond) {
		opts.FlushInterval = time.Duration(value) * time.Millisecond
	}
	return opts
}

func observabilityMaxJSONSize(settings map[string]string) int {
	if value, err := strconv.Atoi(settings["observabilityMaxJsonSize"]); err == nil && value > 0 {
		return value
	}
	return 5 << 20
}
