package proxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

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
}

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

// PutPool creates or replaces one pool and its members in a single transaction.
// Member URLs are validated and de-duplicated so an invalid entry cannot reach
// the outbound transport.
func (s *Store) PutPool(ctx context.Context, pool Pool) (Pool, error) {
	pool.Name = strings.TrimSpace(pool.Name)
	if pool.Name == "" {
		return Pool{}, errors.New("proxy pool name is required")
	}
	switch pool.Strategy {
	case "":
		pool.Strategy = StrategyRoundRobin
	case StrategyRoundRobin, StrategyFillFirst:
	default:
		return Pool{}, errors.New("unsupported proxy pool strategy")
	}
	if pool.ID == "" {
		pool.ID = uuid.NewString()
	}
	members, err := normalizeMembers(pool.ID, pool.Members)
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
	const upsertPool = `INSERT INTO proxy_pools(id,name,enabled,strategy) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,strategy=excluded.strategy,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	if _, err := tx.ExecContext(ctx, upsertPool, pool.ID, pool.Name, enabled, string(pool.Strategy)); err != nil {
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
	return pool, nil
}

// GetPool returns one pool with its ordered members.
func (s *Store) GetPool(ctx context.Context, id string) (Pool, error) {
	var pool Pool
	var enabled int
	const query = "SELECT id,name,enabled,strategy FROM proxy_pools WHERE id=?"
	if err := s.db.QueryRowContext(ctx, query, id).Scan(&pool.ID, &pool.Name, &enabled, &pool.Strategy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Pool{}, ErrNotFound
		}
		return Pool{}, err
	}
	pool.Enabled = enabled != 0
	members, err := s.listMembers(ctx, id)
	if err != nil {
		return Pool{}, err
	}
	pool.Members = members
	return pool, nil
}

// ListPools returns every pool ordered by name.
func (s *Store) ListPools(ctx context.Context) ([]Pool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,enabled,strategy FROM proxy_pools ORDER BY name,id")
	if err != nil {
		return nil, fmt.Errorf("list proxy pools: %w", err)
	}
	defer rows.Close()
	var pools []Pool
	for rows.Next() {
		var pool Pool
		var enabled int
		if err := rows.Scan(&pool.ID, &pool.Name, &enabled, &pool.Strategy); err != nil {
			return nil, err
		}
		pool.Enabled = enabled != 0
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
func normalizeMembers(poolID string, members []Member) ([]Member, error) {
	ordered := append([]Member(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
	seen := make(map[string]struct{}, len(ordered))
	normalized := make([]Member, 0, len(ordered))
	for _, member := range ordered {
		url, err := validateProxyURL(member.URL)
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

// validateProxyURL rejects non-proxy schemes and credential-free/blank hosts.
// Credentials embedded in the URL are allowed because some pools require them;
// they are never logged.
func validateProxyURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("proxy URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", errors.New("proxy URL must include a host")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", errors.New("unsupported proxy URL scheme")
	}
	return parsed.String(), nil
}
