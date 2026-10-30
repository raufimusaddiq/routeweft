package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// DetailRetentionDays bounds how long request details are kept, on top of the row
// count (DetailStore.MaxRows). It is a fixed policy constant rather than a tuned
// setting: PRD-OBS-002 requires details to be bounded, and a fixed short window
// satisfies that without inventing another operator knob.
const DetailRetentionDays = 7

// DefaultMaxDetailRows is how many request details are kept when no limit is set.
const DefaultMaxDetailRows = 1000

// previewChars is how much of an over-limit field is kept (requestDetailsRepo.js:94-100).
const previewChars = 200

// Detail is one bounded, redacted request diagnostic record (SPEC §21
// diagnostic class, PRD-OBS-002). Payload is already redacted by the builder; the
// store redacts it again (idempotently) and checks its size.
type Detail struct {
	RequestID string
	RouteMode string
	// Provider, Model, Connection and Status are what a list of details is
	// filtered by (requestDetailsRepo.js:209-240); Status is "success" or "error".
	Provider, Model, Connection, Status string
	// Payload is a decoded JSON object safe to persist. Credential-shaped keys
	// are removed before it reaches this type.
	Payload   map[string]any
	CreatedAt time.Time
}

// TruncateField bounds one captured field the way the reference does
// (requestDetailsRepo.js:94-100): a field whose JSON is longer than max is
// replaced by its original size and the first 200 characters of its JSON. Redact
// before truncating, so the preview can hold nothing the field could not.
func TruncateField(value any, max int) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"_truncated": true, "_error": "field could not be encoded"}
	}
	if len(encoded) <= max {
		return value
	}
	preview := []rune(string(encoded))
	if len(preview) > previewChars {
		preview = preview[:previewChars]
	}
	return map[string]any{"_truncated": true, "_originalSize": len(encoded), "_preview": string(preview)}
}

// DetailStore persists and prunes request details.
type DetailStore interface {
	WriteRequestDetail(ctx context.Context, detail Detail) error
	PruneRequestDetails(ctx context.Context, before time.Time) (int64, error)
}

// SQLiteDetailStore persists request details into the request_details table and
// prunes rows past the retention window.
type SQLiteDetailStore struct {
	db *sql.DB
	// MaxJSONSize bounds the stored detail document; larger payloads are dropped
	// rather than silently truncated into invalid JSON.
	MaxJSONSize int
	SizeLimit   func() int
	// MaxRows is how many details are kept: after every write the oldest rows past
	// it are deleted in the same transaction (requestDetailsRepo.js:141-147,
	// 296-299). Nil or non-positive means DefaultMaxDetailRows.
	MaxRows func() int
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// NewSQLiteDetailStore wraps the durable store with the configured detail cap.
func NewSQLiteDetailStore(db *sql.DB, maxJSONSize int) *SQLiteDetailStore {
	if maxJSONSize <= 0 {
		maxJSONSize = 5 << 20
	}
	return &SQLiteDetailStore{db: db, MaxJSONSize: maxJSONSize}
}

// WriteRequestDetail redacts, size-checks and upserts one detail row keyed by
// request id so a route decision can be replaced by its final outcome, then
// deletes the oldest rows beyond the row limit in the same transaction.
func (s *SQLiteDetailStore) WriteRequestDetail(ctx context.Context, detail Detail) error {
	if detail.RequestID == "" {
		return nil
	}
	redacted, err := json.Marshal(DetailRedactor{}.Redact(detail.Payload))
	if err != nil {
		return fmt.Errorf("encode request detail: %w", err)
	}
	// Each captured field is already bounded by the limit; the document as a whole
	// is held to four fields' worth plus its own fixed fields.
	limit := s.MaxJSONSize
	if s.SizeLimit != nil {
		limit = s.SizeLimit()
	}
	if len(redacted) > 4*limit+2<<10 {
		// Over the cap: store only the attribution, never an unredacted prefix.
		redacted, _ = json.Marshal(map[string]string{"omitted": "detail exceeded configured size limit"})
	}
	createdAt := detail.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin request detail: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	const upsert = `INSERT INTO request_details (request_id,route_mode,detail,created_at,provider_id,model_id,connection_id,status) VALUES (?,?,?,?,?,?,?,?) ` +
		`ON CONFLICT(request_id) DO UPDATE SET route_mode=excluded.route_mode, detail=excluded.detail, created_at=excluded.created_at, ` +
		`provider_id=excluded.provider_id, model_id=excluded.model_id, connection_id=excluded.connection_id, status=excluded.status`
	if _, err := tx.ExecContext(ctx, upsert, detail.RequestID, nullable(detail.RouteMode), string(redacted), createdAt.Format("2006-01-02T15:04:05.000Z"),
		nullable(detail.Provider), nullable(detail.Model), nullable(detail.Connection), nullable(detail.Status)); err != nil {
		return fmt.Errorf("persist request detail: %w", err)
	}
	maxRows := DefaultMaxDetailRows
	if s.MaxRows != nil {
		if value := s.MaxRows(); value > 0 {
			maxRows = value
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM request_details WHERE request_id IN (SELECT request_id FROM request_details ORDER BY created_at ASC, rowid ASC LIMIT MAX((SELECT COUNT(*) FROM request_details) - ?, 0))`, maxRows); err != nil {
		return fmt.Errorf("limit request details: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit request detail: %w", err)
	}
	return nil
}

// PruneRequestDetails deletes rows created before the cutoff and reports how
// many were removed (PRD-OBS-002 bounded retention).
func (s *SQLiteDetailStore) PruneRequestDetails(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM request_details WHERE created_at < ?", before.UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return 0, fmt.Errorf("prune request details: %w", err)
	}
	return result.RowsAffected()
}

// PruneNow prunes using the configured retention window.
func (s *SQLiteDetailStore) PruneNow(ctx context.Context) (int64, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return s.PruneRequestDetails(ctx, now().AddDate(0, 0, -DetailRetentionDays))
}
