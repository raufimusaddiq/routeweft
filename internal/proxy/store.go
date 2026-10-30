package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Strategy is a supported pool-selection strategy.
type Strategy string

const (
	StrategyRoundRobin Strategy = "round_robin"
	StrategyFillFirst  Strategy = "fill_first"
)

// Pool is one durable proxy pool.
type Pool struct {
	ID       string
	Name     string
	Enabled  bool
	Strategy Strategy
	Members  []Member
	// Kind is what the members are: forward proxies (the default) or relays that
	// carry a request in x-relay-target/x-relay-path headers (PARITY 6.38).
	Kind Kind
	// Strict makes a connection bound to the pool fail rather than leave over the
	// direct address when the pool cannot supply a proxy.
	Strict bool
	// NoProxy is a comma-separated list of hosts that bypass the pool.
	NoProxy string
	// TestStatus is "unknown" until a connectivity test, then "active" or "error".
	TestStatus   string
	LastTestedAt string
	LastError    string
}

// Kind is the type of endpoint a pool's members are.
type Kind string

const (
	KindHTTP       Kind = "http"
	KindVercel     Kind = "vercel"
	KindCloudflare Kind = "cloudflare"
	KindDeno       Kind = "deno"
	KindNetlify    Kind = "netlify"
)

// Valid reports whether the kind is one the store accepts.
func (k Kind) Valid() bool {
	switch k {
	case KindHTTP, KindVercel, KindCloudflare, KindDeno, KindNetlify:
		return true
	}
	return false
}

// Relay reports whether members of this kind are relays rather than proxies.
func (k Kind) Relay() bool { return k.Valid() && k != KindHTTP }

// Member is one proxy endpoint inside a pool.
type Member struct {
	ID       string
	PoolID   string
	Position int
	URL      string
	Enabled  bool
}

// Store owns durable proxy pools and their members.
type Store struct{ db *sql.DB }

// NewStore builds a proxy store.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("proxy store requires a database")
	}
	return &Store{db: db}, nil
}

// ErrNotFound reports a missing pool or member.
var ErrNotFound = errors.New("proxy pool record not found")

// ErrInvalid wraps every caller-correctable validation failure so control
// handlers can answer 400 without string matching.
var ErrInvalid = errors.New("invalid proxy pool")

// ErrConflict reports a pool name that is already in use.
var ErrConflict = errors.New("proxy pool name already exists")

// ErrInUse reports a pool that provider connections are still bound to.
var ErrInUse = errors.New("proxy pool is bound to connections")

// ValidateURL applies the store's proxy URL rules without persisting anything.
func ValidateURL(raw string) (string, error) { return validateProxyURL(raw) }

// PutPool creates or replaces one pool and its members in a single transaction.
// Member URLs are validated and de-duplicated so an invalid entry cannot reach
// the outbound transport.
func (s *Store) PutPool(ctx context.Context, pool Pool) (Pool, error) {
	pool.Name = strings.TrimSpace(pool.Name)
	if pool.Name == "" {
		return Pool{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	switch pool.Strategy {
	case "":
		pool.Strategy = StrategyRoundRobin
	case StrategyRoundRobin, StrategyFillFirst:
	default:
		return Pool{}, fmt.Errorf("%w: unsupported strategy", ErrInvalid)
	}
	if pool.Kind == "" {
		pool.Kind = KindHTTP
	}
	if !pool.Kind.Valid() {
		return Pool{}, fmt.Errorf("%w: unsupported pool type", ErrInvalid)
	}
	pool.NoProxy = strings.TrimSpace(pool.NoProxy)
	if pool.ID == "" {
		pool.ID = uuid.NewString()
	}
	members, err := normalizeMembers(pool.ID, pool.Kind, pool.Members)
	if err != nil {
		return Pool{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Pool{}, fmt.Errorf("begin proxy pool transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	enabled := 0
	if pool.Enabled {
		enabled = 1
	}
	strict := 0
	if pool.Strict {
		strict = 1
	}
	// The connectivity-test columns are not written here: replacing a pool's
	// configuration keeps what its last test found until the next test.
	const upsertPool = `INSERT INTO proxy_pools(id,name,enabled,strategy,kind,strict,no_proxy) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,strategy=excluded.strategy,kind=excluded.kind,strict=excluded.strict,no_proxy=excluded.no_proxy,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	if _, err := tx.ExecContext(ctx, upsertPool, pool.ID, pool.Name, enabled, string(pool.Strategy), string(pool.Kind), strict, pool.NoProxy); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: proxy_pools.name") {
			return Pool{}, ErrConflict
		}
		return Pool{}, fmt.Errorf("persist proxy pool %q: %w", pool.Name, err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_pool_members WHERE pool_id=?", pool.ID); err != nil {
		return Pool{}, fmt.Errorf("replace proxy pool members: %w", err)
	}
	for position, member := range members {
		flag := 0
		if member.Enabled {
			flag = 1
		}
		const insert = "INSERT INTO proxy_pool_members(id,pool_id,position,proxy_url,enabled) VALUES(?,?,?,?,?)"
		if _, err := tx.ExecContext(ctx, insert, member.ID, pool.ID, position, member.URL, flag); err != nil {
			return Pool{}, fmt.Errorf("persist proxy pool member: %w", err)
		}
		members[position].Position = position
	}
	if err := tx.Commit(); err != nil {
		return Pool{}, fmt.Errorf("commit proxy pool: %w", err)
	}
	pool.Members = members
	_ = s.db.QueryRowContext(ctx, "SELECT test_status,COALESCE(last_tested_at,''),COALESCE(last_error,'') FROM proxy_pools WHERE id=?", pool.ID).Scan(&pool.TestStatus, &pool.LastTestedAt, &pool.LastError)
	return pool, nil
}

// GetPool returns one pool with its ordered members.
func (s *Store) GetPool(ctx context.Context, id string) (Pool, error) {
	var pool Pool
	var enabled, strict int
	if err := s.db.QueryRowContext(ctx, "SELECT "+poolColumns+" FROM proxy_pools WHERE id=?", id).Scan(poolDest(&pool, &enabled, &strict)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Pool{}, ErrNotFound
		}
		return Pool{}, err
	}
	pool.Enabled, pool.Strict = enabled != 0, strict != 0
	members, err := s.listMembers(ctx, id)
	if err != nil {
		return Pool{}, err
	}
	pool.Members = members
	return pool, nil
}

// ListPools returns every pool ordered by name.
func (s *Store) ListPools(ctx context.Context) ([]Pool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+poolColumns+" FROM proxy_pools ORDER BY name,id")
	if err != nil {
		return nil, fmt.Errorf("list proxy pools: %w", err)
	}
	defer rows.Close()
	var pools []Pool
	for rows.Next() {
		var pool Pool
		var enabled, strict int
		if err := rows.Scan(poolDest(&pool, &enabled, &strict)...); err != nil {
			return nil, err
		}
		pool.Enabled, pool.Strict = enabled != 0, strict != 0
		pools = append(pools, pool)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range pools {
		members, err := s.listMembers(ctx, pools[i].ID)
		if err != nil {
			return nil, err
		}
		pools[i].Members = members
	}
	return pools, nil
}

const poolColumns = "id,name,enabled,strategy,kind,strict,no_proxy,test_status,COALESCE(last_tested_at,''),COALESCE(last_error,'')"

func poolDest(pool *Pool, enabled, strict *int) []any {
	return []any{&pool.ID, &pool.Name, enabled, &pool.Strategy, &pool.Kind, strict, &pool.NoProxy, &pool.TestStatus, &pool.LastTestedAt, &pool.LastError}
}

// RecordTest stores the outcome of a connectivity test and, as the reference does,
// enables the pool when the test passed and disables it when it failed
// (src/app/api/proxy-pools/[id]/test/route.js:51-56).
func (s *Store) RecordTest(ctx context.Context, id string, ok bool, testedAt time.Time, failure string) error {
	status, enabled := "error", 0
	if ok {
		status, enabled = "active", 1
	}
	var lastError any
	if !ok {
		lastError = failure
	}
	result, err := s.db.ExecContext(ctx, "UPDATE proxy_pools SET test_status=?,last_tested_at=?,last_error=?,enabled=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?",
		status, testedAt.UTC().Format(time.RFC3339Nano), lastError, enabled, id)
	if err != nil {
		return fmt.Errorf("record proxy pool test: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePool removes one pool; bound connections have their binding cleared by
// the schema's ON DELETE SET NULL.
func (s *Store) DeletePool(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM proxy_pools WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete proxy pool: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePoolIfUnbound removes a pool only while no provider connection is bound
// to it. The check and the delete are one statement, so a binding created
// concurrently cannot be silently unbound by the foreign key. When refused it
// returns ErrInUse and the number of bound connections.
func (s *Store) DeletePoolIfUnbound(ctx context.Context, id string) (int, error) {
	// A providerStrategies entry that names the pool (no-auth rotation) binds it
	// too (src/lib/db/repos/proxyPoolsRepo.js:114-124). The setting is read inside
	// the same transaction as the delete.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin proxy pool delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='providerStrategies'").Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read provider strategies: %w", err)
	}
	if strategyNamesPool(raw.String, id) {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM proxy_pools WHERE id=?", id).Scan(&exists); err != nil {
			return 0, err
		}
		if exists == 0 {
			return 0, ErrNotFound
		}
		var bound int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_connections WHERE proxy_pool_id=?", id).Scan(&bound); err != nil {
			return 0, err
		}
		return bound, ErrInUse
	}
	const del = "DELETE FROM proxy_pools WHERE id=? AND NOT EXISTS (SELECT 1 FROM provider_connections WHERE proxy_pool_id=?)"
	result, err := tx.ExecContext(ctx, del, id, id)
	if err != nil {
		return 0, fmt.Errorf("delete proxy pool: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected > 0 {
		return 0, tx.Commit()
	}
	var bound int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM provider_connections WHERE proxy_pool_id=?", id).Scan(&bound); err != nil {
		return 0, err
	}
	if bound > 0 {
		return bound, ErrInUse
	}
	return 0, ErrNotFound
}

// strategyNamesPool reports whether a providerStrategies value has an entry whose
// proxyPoolId is the pool.
func strategyNamesPool(raw, id string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var entries map[string]struct {
		ProxyPoolID string `json:"proxyPoolId"`
	}
	if json.Unmarshal([]byte(raw), &entries) != nil {
		return false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ProxyPoolID) == id {
			return true
		}
	}
	return false
}

func (s *Store) listMembers(ctx context.Context, poolID string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,pool_id,position,proxy_url,enabled FROM proxy_pool_members WHERE pool_id=? ORDER BY position,id", poolID)
	if err != nil {
		return nil, fmt.Errorf("list proxy pool members: %w", err)
	}
	defer rows.Close()
	var members []Member
	for rows.Next() {
		var member Member
		var enabled int
		if err := rows.Scan(&member.ID, &member.PoolID, &member.Position, &member.URL, &enabled); err != nil {
			return nil, err
		}
		member.Enabled = enabled != 0
		members = append(members, member)
	}
	return members, rows.Err()
}

// normalizeMembers validates each proxy URL and drops duplicates while sorting
// by the caller's declared position so pool order is stable.
func normalizeMembers(poolID string, kind Kind, members []Member) ([]Member, error) {
	ordered := append([]Member(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
	seen := make(map[string]struct{}, len(ordered))
	normalized := make([]Member, 0, len(ordered))
	for _, member := range ordered {
		validate := validateProxyURL
		if kind.Relay() {
			validate = validateRelayURL
		}
		url, err := validate(member.URL)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[url]; duplicate {
			continue
		}
		seen[url] = struct{}{}
		if member.ID == "" {
			member.ID = uuid.NewString()
		}
		member.PoolID = poolID
		member.URL = url
		normalized = append(normalized, member)
	}
	return normalized, nil
}

// ValidateRelayURL applies the rules for a relay endpoint without persisting.
func ValidateRelayURL(raw string) (string, error) { return validateRelayURL(raw) }

// validateRelayURL accepts the http(s) URL of a relay function. A relay is not a
// proxy: it takes the request as an ordinary HTTP call, so only http and https
// are meaningful, and the URL carries no credentials (a relay that needs one
// takes it in the request, not in its address).
func validateRelayURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: relay URL is required", ErrInvalid)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: relay URL must include a host", ErrInvalid)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: a relay URL must be http or https", ErrInvalid)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: a relay URL carries no credentials or fragment", ErrInvalid)
	}
	return parsed.String(), nil
}

// validateProxyURL rejects non-proxy schemes and credential-free/blank hosts.
// Credentials embedded in the URL are allowed because some pools require them;
// they are never logged.
func validateProxyURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: proxy URL is required", ErrInvalid)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: proxy URL must include a host", ErrInvalid)
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("%w: unsupported proxy URL scheme", ErrInvalid)
	}
	return parsed.String(), nil
}
