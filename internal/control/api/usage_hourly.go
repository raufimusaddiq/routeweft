package api

import (
	"net/http"
	"strings"
	"time"
)

type usageHour struct {
	Hour     string `json:"hour"`
	Requests int64  `json:"requests"`
	Failed   int64  `json:"failed"`
	Tokens   int64  `json:"tokens"`
	Queried  bool   `json:"queried"`
	Observed bool   `json:"observed"`
}

func (h *Handler) handleUsageHourly(w http.ResponseWriter, r *http.Request) {
	if h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "database read models are not configured")
		return
	}
	query := r.URL.Query()
	period := periodOrDefault(strings.TrimSpace(query.Get("period")))
	duration, ok := usagePeriods[period]
	if !ok || duration == 0 {
		writeError(w, http.StatusBadRequest, "invalid_period", "period must be one of 24h, 7d, 30d")
		return
	}
	where, args, err := usageFilter(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	until := time.Now().UTC().Truncate(time.Hour)
	since := until.Add(-duration)
	if where == "" {
		where = " WHERE "
	} else {
		where += " AND "
	}
	where += "created_at >= ? AND created_at < ?"
	args = append(args, since.Format(usageStoredTime), until.Format(usageStoredTime))

	start, end := since, until
	if raw := strings.TrimSpace(query.Get("startDate")); raw != "" {
		start, _ = time.Parse(time.RFC3339, raw)
		start = start.Truncate(time.Millisecond)
	}
	if raw := strings.TrimSpace(query.Get("endDate")); raw != "" {
		end, _ = time.Parse(time.RFC3339, raw)
		end = end.Truncate(time.Millisecond)
	}
	series := make([]usageHour, int(duration/time.Hour))
	for i := range series {
		hour := since.Add(time.Duration(i) * time.Hour)
		series[i] = usageHour{
			Hour:    hour.Format(time.RFC3339),
			Queried: hour.Add(time.Hour).After(start) && !hour.After(end),
		}
	}

	rows, err := h.opts.DB.QueryContext(r.Context(), `SELECT strftime('%Y-%m-%dT%H:00:00Z',created_at) AS hour,
		COUNT(*),COALESCE(SUM(CASE WHEN status < 200 OR status >= 300 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(input_tokens + output_tokens),0)
		FROM usage_events`+where+` GROUP BY hour ORDER BY hour`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load hourly usage")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var bucket usageHour
		if err := rows.Scan(&bucket.Hour, &bucket.Requests, &bucket.Failed, &bucket.Tokens); err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed", "could not load hourly usage")
			return
		}
		hour, err := time.Parse(time.RFC3339, bucket.Hour)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed", "could not load hourly usage")
			return
		}
		index := int(hour.Sub(since) / time.Hour)
		if index >= 0 && index < len(series) {
			bucket.Queried, bucket.Observed = true, true
			series[index] = bucket
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load hourly usage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"period": period, "timezone": "UTC", "since": since.Format(time.RFC3339),
		"until": until.Format(time.RFC3339), "series": series,
	})
}
