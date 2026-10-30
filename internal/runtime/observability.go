package runtime

import (
	"strconv"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

func (s *RuntimeSnapshot) TelemetryOptions() telemetry.Options { return s.telemetryOptions }
func (s *RuntimeSnapshot) DetailSizeLimit() int                { return s.detailSizeLimit }

// DetailMaxRecords is how many request details are kept; the oldest beyond it are
// deleted as new ones are written. It is the reference's observabilityMaxRecords
// (requestDetailsRepo.js:141-147), which here is the setting
// observabilityMaxDetailRecords because observabilityMaxRecords bounds the live
// queue (PARITY D17).
func (s *RuntimeSnapshot) DetailMaxRecords() int { return s.detailMaxRecords }

func telemetryOptions(settings map[string]string) telemetry.Options {
	opts := telemetry.DefaultOptions()
	opts.Enabled = settings["enableObservability"] == "true"
	if value, err := strconv.Atoi(settings["observabilityMaxRecords"]); err == nil && value > 0 {
		opts.MaxRecords = value
	}
	if value, err := strconv.Atoi(settings["observabilityBatchSize"]); err == nil && value > 0 {
		opts.BatchSize = value
	}
	if opts.BatchSize > opts.MaxRecords {
		opts.BatchSize = opts.MaxRecords
	}
	if value, err := strconv.Atoi(settings["observabilityFlushIntervalMs"]); err == nil && value > 0 && uint64(value) <= uint64((1<<63-1)/time.Millisecond) {
		opts.FlushInterval = time.Duration(value) * time.Millisecond
	}
	return opts
}

func observabilityMaxDetailRecords(settings map[string]string) int {
	if value, err := strconv.Atoi(settings["observabilityMaxDetailRecords"]); err == nil && value > 0 {
		return value
	}
	return telemetry.DefaultMaxDetailRows
}

func observabilityMaxJSONSize(settings map[string]string) int {
	if value, err := strconv.Atoi(settings["observabilityMaxJsonSize"]); err == nil && value > 0 {
		return value
	}
	return 5 << 20
}
