package wire

import (
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func codexReset(status int, body []byte, now time.Time) (routing.Classification, bool) {
	var document struct {
		Error struct {
			Type string          `json:"type"`
			At   json.RawMessage `json:"resets_at"`
			In   json.RawMessage `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if status != http.StatusTooManyRequests || json.Unmarshal(body, &document) != nil || document.Error.Type != "usage_limit_reached" {
		return routing.Classification{}, false
	}
	var seconds float64
	if json.Unmarshal(document.Error.At, &seconds) == nil && seconds > 0 && seconds < float64(1<<63-1)/1e9 {
		reset := time.Unix(0, int64(seconds*1e9))
		if reset.After(now) {
			return quotaReset(reset), true
		}
	}
	seconds = 0
	if json.Unmarshal(document.Error.In, &seconds) == nil && seconds > 0 && seconds < float64(1<<63-1)/1e9 {
		if delay := time.Duration(seconds * 1e9); delay > 0 {
			return quotaReset(now.Add(delay)), true
		}
	}
	return routing.Classification{}, false
}

var protobufSeconds = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,9})?s$`)

func geminiCLIRetryInfo(status int, body []byte, now time.Time) (routing.Classification, bool) {
	var document struct {
		Error struct {
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if status != http.StatusTooManyRequests || json.Unmarshal(body, &document) != nil {
		return routing.Classification{}, false
	}
	for _, raw := range document.Error.Details {
		var detail struct {
			Type  string `json:"@type"`
			Delay string `json:"retryDelay"`
		}
		if json.Unmarshal(raw, &detail) != nil || detail.Type != "type.googleapis.com/google.rpc.RetryInfo" || !protobufSeconds.MatchString(detail.Delay) {
			continue
		}
		delay, err := time.ParseDuration(detail.Delay)
		if err == nil && delay > 0 {
			return quotaReset(now.Add(delay)), true
		}
	}
	return routing.Classification{}, false
}

func quotaReset(reset time.Time) routing.Classification {
	return routing.Classification{Outcome: routing.OutcomeQuotaLock, ResetAt: reset.UTC().Format(time.RFC3339Nano)}
}
