package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLiteSink persists usage events into the usage_events table and maintains
// the usage_daily rollup in one transaction per batch (SPEC §21, §4 schema).
// The batcher is the only writer, so a single connection suffices.
type SQLiteSink struct {
	db *sql.DB
}

// NewSQLiteSink wraps the durable store.
func NewSQLiteSink(db *sql.DB) *SQLiteSink { return &SQLiteSink{db: db} }

const insertUsageEvent = `INSERT INTO usage_events ` +
	`(request_id,provider_id,model_id,connection_id,api_key_id,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,ttft_ms,created_at) ` +
	`VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`

const upsertUsageDaily = `INSERT INTO usage_daily ` +
	`(day,provider_id,model_id,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) ` +
	`VALUES (?,?,?,1,?,?,?,?) ` +
	`ON CONFLICT(day,provider_id,model_id) DO UPDATE SET ` +
	`requests=requests+1, input_tokens=input_tokens+excluded.input_tokens, ` +
	`output_tokens=output_tokens+excluded.output_tokens, cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens, ` +
	`cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens`

// WriteUsageEvents commits one batch atomically. An empty batch is a no-op.
func (s *SQLiteSink) WriteUsageEvents(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin usage batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, event := range events {
		createdAt := event.CreatedAt.UTC()
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		stamp := createdAt.Format("2006-01-02T15:04:05.000Z")
		if _, err := tx.ExecContext(ctx, insertUsageEvent,
			event.RequestID, nullable(event.ProviderID), nullable(event.ModelID), nullable(event.ConnectionID), nullable(event.APIKeyID),
			event.Status, event.InputTokens, event.OutputTokens, event.CacheRead, event.CacheWrite,
			nullableInt(event.DurationMS), nullableInt(event.TTFTMS), stamp,
		); err != nil {
			return fmt.Errorf("insert usage event: %w", err)
		}
		// Only attributed successes contribute to the daily rollup, which is a
		// per provider/model accounting view rather than a failure log.
		if event.ProviderID == "" || event.ModelID == "" || event.Status < 200 || event.Status >= 300 {
			continue
		}
		if _, err := tx.ExecContext(ctx, upsertUsageDaily,
			createdAt.Format("2006-01-02"), event.ProviderID, event.ModelID,
			event.InputTokens, event.OutputTokens, event.CacheRead, event.CacheWrite,
		); err != nil {
			return fmt.Errorf("upsert usage daily: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage batch: %w", err)
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
