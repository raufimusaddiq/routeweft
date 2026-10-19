package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

func TestUsageHourly(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	base := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	event := func(at time.Time, status int) telemetry.Event {
		return telemetry.Event{
			RequestID: "hourly", ProviderID: "openai", ModelID: "gpt-5",
			ConnectionID: "c1", APIKeyID: "key-a", Status: status,
			InputTokens: 10, OutputTokens: 5, CacheRead: 4, CacheWrite: 3, CreatedAt: at,
		}
	}
	first := event(base.Add(59*time.Minute+59*time.Second), 200)
	second := event(base.Add(time.Hour), 302)
	other := event(base.Add(10*time.Minute), 500)
	other.ProviderID, other.ModelID, other.ConnectionID, other.APIKeyID = "anthropic", "claude", "c2", "key-b"
	utcDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	offsetAt, err := time.Parse(time.RFC3339, utcDay+"T19:15:00+07:00")
	if err != nil {
		t.Fatal(err)
	}
	offset := event(offsetAt, 200)
	dateFilter := "startDate=" + url.QueryEscape(base.In(time.FixedZone("UTC+7", 7*60*60)).Format(time.RFC3339)) +
		"&endDate=" + url.QueryEscape(base.Add(time.Hour).Format(time.RFC3339))
	cases := []struct {
		name, query string
		events      []telemetry.Event
		want        map[string][3]int64
	}{
		{"empty table", "", nil, nil},
		{"single request", "period=7d", []telemetry.Event{first}, map[string][3]int64{base.Format(time.RFC3339): {1, 0, 15}}},
		{"hour boundary", "period=7d", []telemetry.Event{first, second}, map[string][3]int64{base.Format(time.RFC3339): {1, 0, 15}, base.Add(time.Hour).Format(time.RFC3339): {1, 1, 15}}},
		{"provider and model", "providerId=openai&modelId=gpt-5", []telemetry.Event{first, second, other}, map[string][3]int64{base.Format(time.RFC3339): {1, 0, 15}, base.Add(time.Hour).Format(time.RFC3339): {1, 1, 15}}},
		{"connection and key and status", "connectionId=c2&apiKeyId=key-b&status=500", []telemetry.Event{first, other}, map[string][3]int64{base.Format(time.RFC3339): {1, 1, 15}}},
		{"filter without matches", "providerId=nobody", []telemetry.Event{first}, nil},
		{"SQL injection is inert", "providerId=" + url.QueryEscape("openai' OR '1'='1"), []telemetry.Event{first}, nil},
		{"UTC instant from offset zone", "period=7d", []telemetry.Event{offset}, map[string][3]int64{utcDay + "T12:00:00Z": {1, 0, 15}}},
		{"inclusive date filters with offset", dateFilter, []telemetry.Event{first, second, other, event(base.Add(-time.Millisecond), 200), event(base.Add(time.Hour+time.Millisecond), 200)}, map[string][3]int64{base.Format(time.RFC3339): {2, 1, 30}, base.Add(time.Hour).Format(time.RFC3339): {1, 1, 15}}},
		{"window edges", "period=24h", []telemetry.Event{event(base.Add(-21*time.Hour), 200), event(base.Add(-21*time.Hour-time.Millisecond), 200), event(base.Add(3*time.Hour), 200)}, map[string][3]int64{base.Add(-21 * time.Hour).Format(time.RFC3339): {1, 0, 15}}},
		{"30 day bound", "period=30d", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.DB().Exec("DELETE FROM usage_events"); err != nil {
				t.Fatal(err)
			}
			for i := range tc.events {
				tc.events[i].RequestID = fmt.Sprintf("hourly-%d", i)
			}
			if err := telemetry.NewSQLiteSink(store.DB()).WriteUsageEvents(context.Background(), tc.events); err != nil {
				t.Fatal(err)
			}
			recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/hourly?"+tc.query, "")
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Period, Timezone, Since, Until string
				Series                         []usageHour
			}
			decodeBody(t, recorder, &body)
			count := 168
			if body.Period == "24h" {
				count = 24
			} else if body.Period == "30d" {
				count = 720
			}
			if body.Timezone != "UTC" || len(body.Series) != count {
				t.Fatalf("timezone=%q buckets=%d want %d", body.Timezone, len(body.Series), count)
			}
			since, err := time.Parse(time.RFC3339, body.Since)
			if err != nil {
				t.Fatal(err)
			}
			if body.Until != since.Add(time.Duration(count)*time.Hour).Format(time.RFC3339) {
				t.Fatalf("window=%s..%s", body.Since, body.Until)
			}
			seen := 0
			for i, bucket := range body.Series {
				if bucket.Hour != since.Add(time.Duration(i)*time.Hour).Format(time.RFC3339) {
					t.Fatalf("bucket %d is not consecutive UTC: %+v", i, bucket)
				}
				want, observed := tc.want[bucket.Hour]
				queried := true
				if tc.query == dateFilter {
					queried = bucket.Hour == base.Format(time.RFC3339) || bucket.Hour == base.Add(time.Hour).Format(time.RFC3339)
				}
				if bucket.Requests != want[0] || bucket.Failed != want[1] || bucket.Tokens != want[2] || bucket.Observed != observed || bucket.Queried != queried {
					t.Fatalf("bucket=%+v want=%v observed=%t queried=%t", bucket, want, observed, queried)
				}
				if observed {
					seen++
				}
			}
			if seen != len(tc.want) {
				t.Fatalf("found %d observed hours, want %d", seen, len(tc.want))
			}
		})
	}
}

func TestUsageHourlyErrors(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	if got := doJSON(t, mux, nil, http.MethodGet, "/admin/v1/usage/hourly", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", got.Code)
	}
	for _, query := range []string{"period=all", "period=eternity", "status=abc", "startDate=yesterday", "endDate=tomorrow", "startDate=2026-10-05T00:00:00Z&endDate=2026-10-01T00:00:00Z"} {
		t.Run(query, func(t *testing.T) {
			got := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/hourly?"+query, "")
			if got.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
			}
		})
	}
	handler.opts.DB = nil
	if got := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/hourly", ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status=%d", got.Code)
	}
	handler.opts.DB = store.DB()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/hourly", ""); got.Code != http.StatusInternalServerError {
		t.Fatalf("closed database status=%d", got.Code)
	}
}
