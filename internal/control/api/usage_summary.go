package api

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// usagePeriods bounds the selectable Usage windows (PRD-OBS-001). "all" scans
// without a lower bound; the others use a rolling window from now.
var usagePeriods = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"all": 0,
}

func usageWindow(period string, now time.Time) (string, bool) {
	if period == "" {
		period = "7d"
	}
	duration, ok := usagePeriods[period]
	if !ok {
		return "", false
	}
	if duration == 0 {
		return "", true
	}
	return now.Add(-duration).UTC().Format("2006-01-02T15:04:05.000Z"), true
}

// handleUsageSummary aggregates bounded Usage totals, a daily series and
// provider/model/status breakdowns from the durable usage tables (PRD-OBS-001).
func (h *Handler) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "database read models are not configured")
		return
	}
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	since, ok := usageWindow(period, time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_period", "period must be one of 24h, 7d, 30d, all")
		return
	}
	ctx := r.Context()

	where, args := "", []any(nil)
	if since != "" {
		where = " WHERE created_at >= ?"
		args = append(args, since)
	}
	totals := map[string]any{"requests": int64(0), "inputTokens": int64(0), "outputTokens": int64(0), "cacheReadTokens": int64(0), "cacheWriteTokens": int64(0), "errors": int64(0)}
	row := h.opts.DB.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_write_tokens),0),COALESCE(SUM(CASE WHEN status < 200 OR status >= 300 THEN 1 ELSE 0 END),0),COALESCE(SUM(duration_ms),0),COALESCE(SUM(ttft_ms),0),COALESCE(SUM(CASE WHEN duration_ms IS NOT NULL THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN ttft_ms IS NOT NULL THEN 1 ELSE 0 END),0) FROM usage_events"+where, args...)
	var requests, input, output, cacheRead, cacheWrite, errors, durationSum, ttftSum, timed, ttftCount int64
	if err := row.Scan(&requests, &input, &output, &cacheRead, &cacheWrite, &errors, &durationSum, &ttftSum, &timed, &ttftCount); err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load usage totals")
		return
	}
	totals["requests"], totals["inputTokens"], totals["outputTokens"] = requests, input, output
	totals["cacheReadTokens"], totals["cacheWriteTokens"], totals["errors"] = cacheRead, cacheWrite, errors
	if timed > 0 {
		totals["avgDurationMs"] = durationSum / timed
	}
	if ttftCount > 0 {
		totals["avgTtftMs"] = ttftSum / ttftCount
	}

	series, err := h.usageDailySeries(ctx, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load usage history")
		return
	}
	providers, err := h.usageBreakdown(ctx, "provider_id", where, args)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load usage breakdown")
		return
	}
	models, err := h.usageBreakdown(ctx, "provider_id || '/' || model_id", where, args)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load usage breakdown")
		return
	}
	statuses, err := h.usageBreakdown(ctx, "CAST(status AS TEXT)", where, args)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load usage breakdown")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"period": periodOrDefault(period), "since": since, "totals": totals, "series": series, "providers": providers, "models": models, "statuses": statuses})
}

func periodOrDefault(period string) string {
	if period == "" {
		return "7d"
	}
	return period
}

// usageDailySeries returns per-day request/token totals, capped at a bounded
// number of days so the response cannot grow without limit. usage_daily is keyed
// (day, provider_id, model_id), so the rows must be aggregated to one row per
// day *before* the LIMIT bounds days; the newest days are then presented
// oldest-first for charting.
func (h *Handler) usageDailySeries(ctx context.Context, since string) ([]map[string]any, error) {
	query := "SELECT day,COALESCE(SUM(requests),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_write_tokens),0) FROM (SELECT day,SUM(requests) AS requests,SUM(input_tokens) AS input_tokens,SUM(output_tokens) AS output_tokens,SUM(cache_read_tokens) AS cache_read_tokens,SUM(cache_write_tokens) AS cache_write_tokens FROM usage_daily"
	var args []any
	if since != "" {
		query += " WHERE day >= ?"
		args = append(args, since[:10])
	}
	query += " GROUP BY day ORDER BY day DESC LIMIT 366) GROUP BY day ORDER BY day"
	rows, err := h.opts.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := make([]map[string]any, 0, 32)
	for rows.Next() {
		var day string
		var requests, input, output, cacheRead, cacheWrite int64
		if err := rows.Scan(&day, &requests, &input, &output, &cacheRead, &cacheWrite); err != nil {
			return nil, err
		}
		series = append(series, map[string]any{"day": day, "requests": requests, "inputTokens": input, "outputTokens": output, "cacheReadTokens": cacheRead, "cacheWriteTokens": cacheWrite})
	}
	return series, rows.Err()
}

// usageBreakdown groups by one expression. Null/empty keys are labelled so the
// UI can show unattributed requests without hiding them.
func (h *Handler) usageBreakdown(ctx context.Context, expression, where string, args []any) ([]map[string]any, error) {
	query := "SELECT COALESCE(NULLIF(" + expression + ",''),'(unattributed)') AS k,COUNT(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(CASE WHEN status < 200 OR status >= 300 THEN 1 ELSE 0 END),0) FROM usage_events" + where + " GROUP BY k ORDER BY COUNT(*) DESC, k LIMIT 50"
	rows, err := h.opts.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, 16)
	for rows.Next() {
		var key string
		var count, input, output, errors int64
		if err := rows.Scan(&key, &count, &input, &output, &errors); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"key": key, "requests": count, "inputTokens": input, "outputTokens": output, "errors": errors})
	}
	return items, rows.Err()
}
