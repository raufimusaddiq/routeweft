package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// DetailRetentionDays bounds how long request details are kept. It is a fixed
// policy constant rather than a tuned setting: PRD-OBS-002 requires details to be
// bounded, and a fixed short window satisfies that without inventing a new
// operator knob. ponytail: constant ceiling, promote to a setting if operators
// need longer history.
const DetailRetentionDays = 7

// Detail is one bounded, redacted request diagnostic record (SPEC §21
// diagnostic class, PRD-OBS-002). Payload is already redacted by the caller via
// RedactJSON; the sink re-checks nothing beyond size.
type Detail struct {
	RequestID string
	RouteMode string
	// Payload is a decoded JSON object safe to persist. Credential-shaped keys
	// are removed before it reaches this type.
	Payload   map[string]any
	CreatedAt time.Time
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
// request id so a route decision can be replaced by its final outcome.
func (s *SQLiteDetailStore) WriteRequestDetail(ctx context.Context, detail Detail) error {
	if detail.RequestID == "" {
		return nil
	}
	redacted, err := json.Marshal(Redact(detail.Payload))
	if err != nil {
		return fmt.Errorf("encode request detail: %w", err)
	}
	if len(redacted) > s.MaxJSONSize {
		// Over the cap: store only the attribution, never an unredacted prefix.
		redacted, _ = json.Marshal(map[string]string{"omitted": "detail exceeded configured size limit"})
	}
	createdAt := detail.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	const upsert = `INSERT INTO request_details (request_id,route_mode,detail,created_at) VALUES (?,?,?,?) ` +
		`ON CONFLICT(request_id) DO UPDATE SET route_mode=excluded.route_mode, detail=excluded.detail`
	if _, err := s.db.ExecContext(ctx, upsert, detail.RequestID, nullable(detail.RouteMode), string(redacted), createdAt.Format("2006-01-02T15:04:05.000Z")); err != nil {
		return fmt.Errorf("persist request detail: %w", err)
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
