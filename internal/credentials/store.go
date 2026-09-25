package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AuthKind mirrors the registry credential modes. It is duplicated locally so
// this package does not import the provider registry for a string enum.
type AuthKind string

const (
	AuthAPIKey AuthKind = "api-key"
	AuthOAuth  AuthKind = "oauth"
	AuthCookie AuthKind = "cookie"
	AuthNone   AuthKind = "none"
)

// NodeKind distinguishes a catalog-backed provider node from an operator-created
// Generic Provider node (PRD-GEN-001).
type NodeKind string

const (
	NodeBuiltin NodeKind = "builtin"
	NodeGeneric NodeKind = "generic"
)

// Node is one provider definition. Generic nodes carry the operator-supplied
// prefix, base URL and transports; builtin nodes reference the compiled catalog.
type Node struct {
	ID         string
	Kind       NodeKind
	ProviderID string
	Name       string
	Prefix     string
	BaseURL    string
	Transports []string
}

// Connection is one account/credential record against a node. Secret carries the
// raw credential material only in memory; the store seals it before persisting
// and callers must never log it.
type Connection struct {
	ID          string
	NodeID      string
	Name        string
	AuthKind    AuthKind
	Identity    string
	Secret      Secret
	Enabled     bool
	Priority    int
	ProxyPoolID string
}

// Secret is one decryptable credential payload. Fields are optional per auth
// kind: API keys use AccessToken, OAuth uses AccessToken/RefreshToken/Expiry,
// cookie auth uses Cookie, and imports may additionally carry IDToken.
type Secret struct {
	AccessToken  string    `json:"accessToken,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	IDToken      string    `json:"idToken,omitempty"`
	Cookie       string    `json:"cookie,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// Empty reports whether the secret holds no credential material at all. A
// refresh must never replace a durable credential with an empty payload
// (PRD-AUTH-003).
func (s Secret) Empty() bool {
	return strings.TrimSpace(s.AccessToken) == "" && strings.TrimSpace(s.RefreshToken) == "" && strings.TrimSpace(s.Cookie) == ""
}

// Store owns durable provider node/connection records. Every write is a single
// short transaction and no transaction spans network work (SPEC §4).
type Store struct {
	db     *sql.DB
	sealer *Sealer
	now    func() time.Time
}

// NewStore builds the credential store. A nil sealer is rejected because
// unsealed credential material must never reach SQLite.
func NewStore(db *sql.DB, sealer *Sealer) (*Store, error) {
	if db == nil {
		return nil, errors.New("credential store requires a database")
	}
	if sealer == nil {
		return nil, ErrKeyRequired
	}
	return &Store{db: db, sealer: sealer, now: time.Now}, nil
}

// ErrNotFound reports a missing node or connection.
var ErrNotFound = errors.New("credential record not found")

// PutNode creates or replaces one provider node. A blank ID allocates a new one.
func (s *Store) PutNode(ctx context.Context, node Node) (Node, error) {
	if strings.TrimSpace(node.ProviderID) == "" {
		return Node{}, errors.New("provider id is required")
	}
	if strings.TrimSpace(node.Name) == "" {
		return Node{}, errors.New("node name is required")
	}
	switch node.Kind {
	case NodeBuiltin, NodeGeneric:
	default:
		return Node{}, errors.New("unsupported node kind")
	}
	if node.ID == "" {
		node.ID = uuid.NewString()
	}
	transports, err := encodeTransports(node.Transports)
	if err != nil {
		return Node{}, err
	}
	const upsert = `INSERT INTO provider_nodes(id,kind,provider_id,name,prefix,base_url,transports) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,provider_id=excluded.provider_id,name=excluded.name,prefix=excluded.prefix,base_url=excluded.base_url,transports=excluded.transports,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	if _, err := s.db.ExecContext(ctx, upsert, node.ID, string(node.Kind), node.ProviderID, node.Name, nullable(node.Prefix), nullable(node.BaseURL), transports); err != nil {
		return Node{}, fmt.Errorf("persist provider node %q: %w", node.Name, err)
	}
	return node, nil
}

// PutConnection creates or replaces one connection, sealing the secret. Writes
// never persist the plaintext payload and never clear a stored blob with an
// empty secret (PRD-AUTH-003).
func (s *Store) PutConnection(ctx context.Context, connection Connection) (Connection, error) {
	if strings.TrimSpace(connection.NodeID) == "" {
		return Connection{}, errors.New("connection node id is required")
	}
	if strings.TrimSpace(connection.Name) == "" {
		return Connection{}, errors.New("connection name is required")
	}
	if strings.TrimSpace(connection.Identity) == "" {
		return Connection{}, errors.New("connection identity is required")
	}
	switch connection.AuthKind {
	case AuthAPIKey, AuthOAuth, AuthCookie, AuthNone:
	default:
		return Connection{}, errors.New("unsupported auth kind")
	}
	if connection.ID == "" {
		connection.ID = uuid.NewString()
	}
	blob, err := s.sealSecret(connection.Secret)
	if err != nil {
		return Connection{}, err
	}
	enabled := 0
	if connection.Enabled {
		enabled = 1
	}
	const upsert = `INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,secret_blob,enabled,priority,proxy_pool_id) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET node_id=excluded.node_id,name=excluded.name,auth_kind=excluded.auth_kind,identity=excluded.identity,secret_blob=COALESCE(excluded.secret_blob,provider_connections.secret_blob),enabled=excluded.enabled,priority=excluded.priority,proxy_pool_id=excluded.proxy_pool_id,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	if _, err := s.db.ExecContext(ctx, upsert, connection.ID, connection.NodeID, connection.Name, string(connection.AuthKind), connection.Identity, blob, enabled, connection.Priority, nullable(connection.ProxyPoolID)); err != nil {
		return Connection{}, fmt.Errorf("persist provider connection %q: %w", connection.Name, err)
	}
	return connection, nil
}

// sealSecret encodes the credential payload. A nil result means "leave the
// stored blob unchanged", which is how an enable/disable edit keeps the secret.
func (s *Store) sealSecret(secret Secret) (any, error) {
	if secret.Empty() {
		return nil, nil
	}
	payload, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("encode credential payload: %w", err)
	}
	sealed, err := s.sealer.Seal(payload)
	if err != nil {
		return nil, err
	}
	return sealed, nil
}

// RotateSecret durably replaces one connection's secret after a successful
// refresh. It is the single durable credential update path shared by OAuth
// refresh and provider import (SPEC §35) and refuses to drop the last known
// durable credential for an empty payload.
func (s *Store) RotateSecret(ctx context.Context, connectionID string, secret Secret) error {
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		return errors.New("connection id is required")
	}
	if secret.Empty() {
		return errors.New("refusing to persist an empty credential payload")
	}
	payload, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("encode credential payload: %w", err)
	}
	sealed, err := s.sealer.Seal(payload)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE provider_connections SET secret_blob=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", sealed, connectionID)
	if err != nil {
		return fmt.Errorf("persist rotated credential: %w", err)
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

// GetConnection returns one connection with its decrypted secret.
func (s *Store) GetConnection(ctx context.Context, id string) (Connection, error) {
	const query = "SELECT id,node_id,name,auth_kind,identity,COALESCE(secret_blob,''),enabled,priority,COALESCE(proxy_pool_id,'') FROM provider_connections WHERE id=?"
	row := s.db.QueryRowContext(ctx, query, id)
	connection, err := s.scanConnection(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	return connection, err
}

// ListConnections returns a provider's connections in routing order: priority
// ascending, then name, so selection is deterministic across restarts.
func (s *Store) ListConnections(ctx context.Context, providerID string) ([]Connection, error) {
	const query = `SELECT c.id,c.node_id,c.name,c.auth_kind,c.identity,COALESCE(c.secret_blob,''),c.enabled,c.priority,COALESCE(c.proxy_pool_id,'')
FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id
WHERE n.provider_id=? ORDER BY c.priority,c.name,c.id`
	rows, err := s.db.QueryContext(ctx, query, providerID)
	if err != nil {
		return nil, fmt.Errorf("list provider connections: %w", err)
	}
	defer rows.Close()
	var connections []Connection
	for rows.Next() {
		connection, err := s.scanConnection(rows.Scan)
		if err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

// SetConnectionEnabled toggles a connection without touching its secret.
func (s *Store) SetConnectionEnabled(ctx context.Context, id string, enabled bool) error {
	flag := 0
	if enabled {
		flag = 1
	}
	result, err := s.db.ExecContext(ctx, "UPDATE provider_connections SET enabled=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", flag, id)
	if err != nil {
		return fmt.Errorf("update provider connection: %w", err)
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

// DeleteConnection removes one connection. Disabling is preferred for routing
// continuity, so callers should treat this as an explicit destructive action.
func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM provider_connections WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete provider connection: %w", err)
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

// RecordCredentialEvent writes one non-secret audit row. Detail must never
// contain credential material; callers pass event metadata only.
func (s *Store) RecordCredentialEvent(ctx context.Context, connectionID, event, detail string) error {
	if strings.TrimSpace(event) == "" {
		return errors.New("credential event is required")
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO credential_events(connection_id,event,detail) VALUES(?,?,?)", nullable(connectionID), event, nullable(detail)); err != nil {
		return fmt.Errorf("persist credential event: %w", err)
	}
	return nil
}

func (s *Store) scanConnection(scan func(...any) error) (Connection, error) {
	var connection Connection
	var blob string
	var enabled int
	if err := scan(&connection.ID, &connection.NodeID, &connection.Name, &connection.AuthKind, &connection.Identity, &blob, &enabled, &connection.Priority, &connection.ProxyPoolID); err != nil {
		return Connection{}, err
	}
	connection.Enabled = enabled != 0
	if blob != "" {
		plaintext, err := s.sealer.Open(blob)
		if err != nil {
			return Connection{}, fmt.Errorf("open credential for connection %q: %w", connection.Name, err)
		}
		if err := json.Unmarshal(plaintext, &connection.Secret); err != nil {
			return Connection{}, fmt.Errorf("decode credential for connection %q: %w", connection.Name, err)
		}
	}
	return connection, nil
}

func encodeTransports(transports []string) (any, error) {
	if len(transports) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(transports))
	seen := make(map[string]struct{}, len(transports))
	for _, transport := range transports {
		trimmed := strings.TrimSpace(transport)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode node transports: %w", err)
	}
	return string(encoded), nil
}

func decodeTransports(encoded string) ([]string, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, nil
	}
	var transports []string
	if err := json.Unmarshal([]byte(encoded), &transports); err != nil {
		return nil, fmt.Errorf("decode node transports: %w", err)
	}
	return transports, nil
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
