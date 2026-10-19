package api

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxFilterLength = 200

// usageStoredTime matches how usage_events.created_at is stored, so range
// bounds compare correctly as text.
const usageStoredTime = "2006-01-02T15:04:05.000Z"

// usageFilter builds a parameterized WHERE clause from the query string. Only
// the fixed column names below ever reach the SQL text; every value is bound.
// Errors name the offending parameter and never echo its value.
func usageFilter(query url.Values) (string, []any, error) {
	var clauses []string
	var args []any
	for _, f := range []struct{ param, column string }{
		{"providerId", "provider_id"}, {"modelId", "model_id"}, {"connectionId", "connection_id"}, {"apiKeyId", "api_key_id"},
	} {
		value := strings.TrimSpace(query.Get(f.param))
		if value == "" {
			continue
		}
		if len(value) > maxFilterLength {
			return "", nil, errors.New(f.param + " is too long")
		}
		clauses = append(clauses, f.column+" = ?")
		args = append(args, value)
	}
	if raw := strings.TrimSpace(query.Get("status")); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil || status < 100 || status > 599 {
			return "", nil, errors.New("status must be an HTTP status code")
		}
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	var start, end time.Time
	for _, f := range []struct {
		param, op string
		into      *time.Time
	}{{"startDate", ">=", &start}, {"endDate", "<=", &end}} {
		raw := strings.TrimSpace(query.Get(f.param))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return "", nil, errors.New(f.param + " must be an RFC 3339 timestamp")
		}
		*f.into = parsed.UTC()
		clauses = append(clauses, "created_at "+f.op+" ?")
		args = append(args, parsed.UTC().Format(usageStoredTime))
	}
	if !start.IsZero() && !end.IsZero() && start.After(end) {
		return "", nil, errors.New("startDate must not be after endDate")
	}
	if len(clauses) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}
