package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/pricing"
	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
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
	`(request_id,provider_id,model_id,connection_id,api_key_id,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,ttft_ms,created_at,termination,usage_observed,requested_model,selected_model,cost) ` +
	`VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

const upsertUsageDaily = `INSERT INTO usage_daily ` +
	`(day,provider_id,model_id,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost) ` +
	`VALUES (?,?,?,?,?,?,?,?,?) ` +
	`ON CONFLICT(day,provider_id,model_id) DO UPDATE SET ` +
	`requests=requests+excluded.requests, input_tokens=input_tokens+excluded.input_tokens, ` +
	`output_tokens=output_tokens+excluded.output_tokens, cache_read_tokens=cache_read_tokens+excluded.cache_read_tokens, ` +
	`cache_write_tokens=cache_write_tokens+excluded.cache_write_tokens, cost=cost+excluded.cost`

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
	prices, err := loadPriceOverrides(ctx, tx)
	if err != nil {
		return err
	}
	for _, event := range events {
		createdAt := event.CreatedAt.UTC()
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		stamp := createdAt.Format("2006-01-02T15:04:05.000Z")
		cost := eventCost(prices, event)
		if _, err := tx.ExecContext(ctx, insertUsageEvent,
			event.RequestID, nullable(event.ProviderID), nullable(event.ModelID), nullable(event.ConnectionID), nullable(event.APIKeyID),
			event.Status, event.InputTokens, event.OutputTokens, event.CacheRead, event.CacheWrite,
			nullableInt(event.DurationMS), nullableInt(event.TTFTMS), stamp, event.Termination, !event.UsageUnobserved,
			nullable(event.RequestedModel), nullable(event.SelectedModel), cost,
		); err != nil {
			return fmt.Errorf("insert usage event: %w", err)
		}
		// Only attributed successes contribute to the daily rollup, which is a
		// per provider/model accounting view rather than a failure log.
		if event.ProviderID == "" || event.ModelID == "" || event.Status < 200 || event.Status >= 300 {
			continue
		}
		// A terminated request (client abandoned, upstream truncated) is not a
		// successful request, so it adds no request to the rollup; the tokens
		// the upstream billed for it are still counted.
		requests := 1
		if event.Termination != "" {
			requests = 0
		}
		if _, err := tx.ExecContext(ctx, upsertUsageDaily,
			createdAt.Format("2006-01-02"), event.ProviderID, event.ModelID, requests,
			event.InputTokens, event.OutputTokens, event.CacheRead, event.CacheWrite, cost,
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

// priceKey names one operator price override.
type priceKey struct{ provider, model string }

// loadPriceOverrides reads the operator's price overrides once per batch. They are
// few and the writer is the only reader on this path; the request path never
// touches them.
func loadPriceOverrides(ctx context.Context, tx *sql.Tx) (map[priceKey]pricing.Rates, error) {
	rows, err := tx.QueryContext(ctx, "SELECT provider_id,model_id,input_per_mtok,output_per_mtok,cache_read_per_mtok,cache_write_per_mtok FROM pricing_overrides")
	if err != nil {
		return nil, fmt.Errorf("read price overrides: %w", err)
	}
	defer rows.Close()
	prices := map[priceKey]pricing.Rates{}
	for rows.Next() {
		var provider, model string
		var input, output, cacheRead, cacheWrite sql.NullFloat64
		if err := rows.Scan(&provider, &model, &input, &output, &cacheRead, &cacheWrite); err != nil {
			return nil, err
		}
		prices[priceKey{provider, model}] = pricing.Rates{Input: input.Float64, Output: output.Float64, Cached: cacheRead.Float64, CacheCreation: cacheWrite.Float64}
	}
	return prices, rows.Err()
}

// eventCost prices one event in dollars: the operator's override for its
// provider and model, else the oracle's default catalog, else nothing
// (calculateCost in usageRepo.js:154-170 gives 0 for a model with no price). A
// request whose upstream never reported usage has no tokens to price.
func eventCost(overrides map[priceKey]pricing.Rates, event Event) float64 {
	if event.UsageUnobserved || event.ProviderID == "" || event.ModelID == "" {
		return 0
	}
	rates, ok := overrides[priceKey{event.ProviderID, event.ModelID}]
	if !ok {
		rates, ok = pricing.Lookup(capabilities.ProviderKeys(event.ProviderID), event.ModelID)
	}
	if !ok {
		return 0
	}
	input := event.InputTokens
	if event.CacheExclusive {
		input += event.CacheRead + event.CacheWrite
	}
	return pricing.Cost(pricing.Tokens{Input: input, Output: event.OutputTokens, Cached: event.CacheRead, CacheCreation: event.CacheWrite}, rates)
}
