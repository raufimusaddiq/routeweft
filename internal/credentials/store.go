package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/routing"
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
	ID     string
	NodeID string
	// ProviderID is the owning node's provider identity, resolved on read so
	// consumers (routing, quota) do not need a second node lookup.
	ProviderID  string
	Name        string
	AuthKind    AuthKind
	Identity    string
	Settings    routing.ProviderSettings
	Secret      Secret
	Enabled     bool
	Priority    int
	ProxyPoolID string
	AccountID   string
	// CredentialRevision is how many times the stored credential was replaced
	// by a different one (v13; a reseal of the same credential or a refresh
	// rotation is not a replacement), read in the same row as Secret. A validation records its result
	// only against the revision it tested (RecordValidation). It is not
	// configuration: a save ignores it and the store keeps the count.
	CredentialRevision int64
	// Lifecycle is what a save that writes a new secret records about that
	// secret, in the same statement (v13), so no other save or check can land
	// between the credential and its status. A save without a new secret never
	// changes the stored lifecycle. Not configuration.
	Lifecycle Lifecycle
}

// Lifecycle is the validation state a credential is saved with, applied in the
// save's own statement. StatusActive (a provider issued or accepted it) applies
// to any save of a credential, the same one included, and clears the last
// error, as the reference's activation does (connectionsRepo.js:64-75).
// StatusUntested (no provider has confirmed it) applies only when the
// credential is replaced by a different one. Every replacement clears the last
// test, error and model count, which describe the credential it replaced. An
// empty Status keeps an active status (Xiaomi's soft-validated duplicate,
// xiaomi-mimo/api-key/route.js:79) and turns a failed one untested on a
// replacement; a new row starts untested. An empty Origin keeps the stored
// origin. Reset makes the save a replacement even when the credential is the
// same one: an operator's relink starts the credential over (untested, no last
// test, error or model count, and a running check's result refused), whatever
// it typed.
type Lifecycle struct {
	Status string
	Origin string
	Reset  bool
}

// Secret is one decryptable credential payload. Fields are optional per auth
// kind: API keys use AccessToken, OAuth uses AccessToken/RefreshToken/Expiry,
// cookie auth uses Cookie, and imports may additionally carry IDToken.
//
// ProviderData carries the provider-specific connection fields LiteRouter
// persists alongside the credential (copilot token, Google project id, Cursor
// machine id, Kiro profile ARN, device id, org id). It lives inside the sealed
// payload; only the typed provider-settings projection can enter a snapshot.
// The raw map and credentials must never reach the snapshot or browser.
type Secret struct {
	AccessToken  string            `json:"accessToken,omitempty"`
	RefreshToken string            `json:"refreshToken,omitempty"`
	IDToken      string            `json:"idToken,omitempty"`
	Cookie       string            `json:"cookie,omitempty"`
	Expiry       time.Time         `json:"expiry,omitempty"`
	ProviderData map[string]string `json:"providerData,omitempty"`
	// AuthType is the reference's authType for a connection whose sign-in path
	// sets one apart from its auth kind: a Codex token import or pasted raw token
	// is AuthTypeAccessToken, never matched as a duplicate, while a Codex sign-in
	// or bulk item is AuthTypeOAuth (#241). It is written only by those paths,
	// carried across refresh, import and edits, and empty on every connection
	// stored before it existed. It is sealed with the credential; no request
	// sets it.
	AuthType string `json:"authType,omitempty"`
}

// The reference authTypes a Codex connection records (Secret.AuthType).
const (
	AuthTypeOAuth       = "oauth"
	AuthTypeAccessToken = "access_token"
)

// Empty reports whether the secret holds no credential material at all. A
// refresh must never replace a durable credential with an empty payload
// (PRD-AUTH-003).
func (s Secret) Empty() bool {
	return strings.TrimSpace(s.AccessToken) == "" && strings.TrimSpace(s.RefreshToken) == "" && strings.TrimSpace(s.Cookie) == ""
}

// Store owns durable provider node/connection records. Every write is a single
// short transaction and no transaction spans network work (SPEC §4).
type Store struct {
	db      *sql.DB
	sealer  *Sealer
	now     func() time.Time
	publish Publisher
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
	change := func(c *Configuration) error {
		for i := range c.Nodes {
			if c.Nodes[i].ID == node.ID {
				c.Nodes[i] = node
				return nil
			}
		}
		c.Nodes = append(c.Nodes, node)
		return nil
	}
	if err := s.exec(ctx, change, upsert, node.ID, string(node.Kind), node.ProviderID, node.Name, nullable(node.Prefix), nullable(node.BaseURL), transports); err != nil {
		return Node{}, fmt.Errorf("persist provider node %q: %w", node.Name, err)
	}
	return node, nil
}

// ListNodes returns provider definitions in stable catalog order without
// decrypting or exposing connection credentials.
func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,provider_id,name,COALESCE(prefix,''),COALESCE(base_url,''),COALESCE(transports,'[]') FROM provider_nodes ORDER BY provider_id,name,id`)
	if err != nil {
		return nil, fmt.Errorf("list provider nodes: %w", err)
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var node Node
		var transports string
		if err := rows.Scan(&node.ID, &node.Kind, &node.ProviderID, &node.Name, &node.Prefix, &node.BaseURL, &transports); err != nil {
			return nil, err
		}
		node.Transports, err = decodeTransports(transports)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

// GetNode returns one definition without opening any connection secret.
func (s *Store) GetNode(ctx context.Context, id string) (Node, error) {
	var node Node
	var transports string
	err := s.db.QueryRowContext(ctx, `SELECT id,kind,provider_id,name,COALESCE(prefix,''),COALESCE(base_url,''),COALESCE(transports,'[]') FROM provider_nodes WHERE id=?`, id).Scan(&node.ID, &node.Kind, &node.ProviderID, &node.Name, &node.Prefix, &node.BaseURL, &transports)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	if err != nil {
		return Node{}, err
	}
	node.Transports, err = decodeTransports(transports)
	return node, err
}

// DeleteGenericNode refuses to remove built-in provider definitions. Explicit
// Generic Provider deletion cascades to its connections and is audited by the
// caller as a destructive operator action.
func (s *Store) DeleteGenericNode(ctx context.Context, id string) error {
	change := func(c *Configuration) error {
		for i, node := range c.Nodes {
			if node.ID != id || node.Kind != NodeGeneric {
				continue
			}
			c.Nodes = append(c.Nodes[:i], c.Nodes[i+1:]...)
			kept := c.Connections[:0]
			for _, connection := range c.Connections {
				if connection.NodeID != id {
					kept = append(kept, connection)
				}
			}
			c.Connections = kept
			return nil
		}
		return ErrNotFound
	}
	err := s.exec(ctx, change, "DELETE FROM provider_nodes WHERE id=? AND kind=?", id, string(NodeGeneric))
	if err != nil {
		return fmt.Errorf("delete generic provider node: %w", err)
	}
	return nil
}

// PutConnection creates or replaces one connection, sealing the secret. Writes
// never persist the plaintext payload and never clear a stored blob with an
// empty secret (PRD-AUTH-003). A blank identity is stored as none (v11): the
// store does not decide who may omit one, the API does.
func (s *Store) PutConnection(ctx context.Context, connection Connection) (Connection, error) {
	if strings.TrimSpace(connection.NodeID) == "" {
		return Connection{}, errors.New("connection node id is required")
	}
	if strings.TrimSpace(connection.Name) == "" {
		return Connection{}, errors.New("connection name is required")
	}
	switch connection.AuthKind {
	case AuthAPIKey, AuthOAuth, AuthCookie, AuthNone:
	default:
		return Connection{}, errors.New("unsupported auth kind")
	}
	switch connection.Lifecycle.Status {
	case "", StatusActive, StatusUntested:
	default:
		return Connection{}, errors.New("a saved credential is active or untested")
	}
	if connection.ID == "" {
		connection.ID = uuid.NewString()
	}
	connection.Settings = projectSettings(connection.Secret.ProviderData)
	blob, err := s.sealSecret(connection.Secret)
	if err != nil {
		return Connection{}, err
	}
	enabled := 0
	if connection.Enabled {
		enabled = 1
	}
	// The expiry column follows the sealed secret: it changes only when a new
	// secret is written (v13).
	var expiry any
	if blob != nil {
		expiry = expiryColumn(connection.Secret)
	}
	// A save that reseals the stored credential (an edit merges it back in) is
	// not a replacement: ?15, the credential's digest, decides in the statement
	// itself whether the revision counts and the lifecycle resets. ?14 is
	// whether this save names a status for a replacement (Lifecycle).
	var digest any
	if blob != nil {
		digest = s.sealer.Digest(credentialMaterial(connection.Secret))
		if err := s.initDigest(ctx, connection.ID); err != nil {
			return Connection{}, err
		}
	}
	named, active, reset := 0, 0, 0
	if connection.Lifecycle.Status != "" {
		named = 1
	}
	if connection.Lifecycle.Reset {
		reset = 1
	}
	if connection.Lifecycle.Status == StatusActive {
		active = 1
	}
	// "Replaced" below is a new secret whose digest differs from the stored one,
	// or any secret a save marks Reset (?17);
	// only a replacement counts in the revision, and every replacement clears
	// the last test, error and model count, which describe the credential it
	// replaced, whether or not the save names a status. A replacement that names
	// none keeps an active status, as the reference keeps existing.testStatus
	// (xiaomi-mimo/api-key/route.js:79), but not a failed one: that failure was
	// the old credential's, so the new one is untested. "Accepted" is any save of a credential a provider just issued
	// or accepted (?16): it is active and clears the last error even when the
	// provider returned the same credential, as the reference's activation does
	// (connectionsRepo.js:64-75,282,347). SET expressions read the row as it was
	// before this update.
	const upsert = `INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,secret_blob,enabled,priority,proxy_pool_id,account_id,credential_expires_at,test_status,auth_origin,credential_digest)
VALUES(?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,COALESCE(NULLIF(?12,''),'untested'),?13,?15)
ON CONFLICT(id) DO UPDATE SET node_id=excluded.node_id,name=excluded.name,auth_kind=excluded.auth_kind,identity=excluded.identity,secret_blob=COALESCE(excluded.secret_blob,provider_connections.secret_blob),enabled=excluded.enabled,priority=excluded.priority,proxy_pool_id=excluded.proxy_pool_id,account_id=excluded.account_id,credential_expires_at=CASE WHEN excluded.secret_blob IS NULL THEN provider_connections.credential_expires_at ELSE excluded.credential_expires_at END,
credential_revision=CASE WHEN excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) THEN provider_connections.credential_revision+1 ELSE provider_connections.credential_revision END,
credential_digest=CASE WHEN excluded.secret_blob IS NULL THEN provider_connections.credential_digest ELSE ?15 END,
test_status=CASE WHEN ?14 AND excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) THEN excluded.test_status WHEN ?16 AND excluded.secret_blob IS NOT NULL THEN 'active' WHEN excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) AND provider_connections.test_status='failed' THEN 'untested' ELSE provider_connections.test_status END,
auth_origin=CASE WHEN ?13<>'' AND (excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) OR ?16 AND excluded.secret_blob IS NOT NULL) THEN ?13 ELSE provider_connections.auth_origin END,
last_tested_at=CASE WHEN excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) THEN NULL ELSE provider_connections.last_tested_at END,
last_error=CASE WHEN excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) OR ?16 AND excluded.secret_blob IS NOT NULL THEN NULL ELSE provider_connections.last_error END,
last_error_at=CASE WHEN excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) OR ?16 AND excluded.secret_blob IS NOT NULL THEN NULL ELSE provider_connections.last_error_at END,
models_available=CASE WHEN excluded.secret_blob IS NOT NULL AND (provider_connections.credential_digest IS NOT ?15 OR ?17) THEN NULL ELSE provider_connections.models_available END,
updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	change := func(c *Configuration) error {
		metadata := connection
		// The runtime configuration carries metadata only: the credential, its
		// lifecycle and its revision counter are store state, and a counter
		// read with the row would differ from the configuration's (which never
		// selects it) and republish the snapshot on a credential-only save.
		metadata.Secret, metadata.Lifecycle, metadata.CredentialRevision = Secret{}, Lifecycle{}, 0
		for i := range c.Connections {
			if c.Connections[i].ID == connection.ID {
				if blob == nil {
					metadata.Settings = c.Connections[i].Settings
					connection.Settings = metadata.Settings
				}
				c.Connections[i] = metadata
				return nil
			}
		}
		c.Connections = append(c.Connections, metadata)
		return nil
	}
	if err := s.exec(ctx, change, upsert, connection.ID, connection.NodeID, connection.Name, string(connection.AuthKind), nullable(connection.Identity), blob, enabled, connection.Priority, nullable(connection.ProxyPoolID), connection.AccountID, expiry, connection.Lifecycle.Status, connection.Lifecycle.Origin, named, digest, active, reset); err != nil {
		return Connection{}, fmt.Errorf("persist provider connection %q: %w", connection.Name, err)
	}
	return connection, nil
}

// sealSecret encodes the credential payload. A nil result means "leave the
// stored blob unchanged", which is how an enable/disable edit keeps the secret.
func (s *Store) sealSecret(secret Secret) (any, error) {
	if secret.Empty() && secret.IDToken == "" && len(secret.ProviderData) == 0 {
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
	return s.exec(ctx, editConnection(connectionID, func(c *Connection) {
		c.Settings = projectSettings(secret.ProviderData)
	}), "UPDATE provider_connections SET secret_blob=?,credential_expires_at=?,credential_digest=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", sealed, expiryColumn(secret), s.sealer.Digest(credentialMaterial(secret)), connectionID)
}

// GetConnection returns one connection with its decrypted secret.
func (s *Store) GetConnection(ctx context.Context, id string) (Connection, error) {
	const query = "SELECT c.id,c.node_id,n.provider_id,c.name,c.auth_kind,COALESCE(c.identity,''),COALESCE(c.secret_blob,''),c.enabled,c.priority,COALESCE(c.proxy_pool_id,''),c.account_id,c.credential_revision FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id WHERE c.id=?"
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
	const query = `SELECT c.id,c.node_id,n.provider_id,c.name,c.auth_kind,COALESCE(c.identity,''),COALESCE(c.secret_blob,''),c.enabled,c.priority,COALESCE(c.proxy_pool_id,''),c.account_id,c.credential_revision
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

// ConnectionCreationOrder returns a provider's connection ids oldest first: by
// creation time, then by row order for connections created in one millisecond.
func (s *Store) ConnectionCreationOrder(ctx context.Context, providerID string) ([]string, error) {
	const query = `SELECT c.id FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id
WHERE n.provider_id=? ORDER BY c.created_at,c.rowid`
	rows, err := s.db.QueryContext(ctx, query, providerID)
	if err != nil {
		return nil, fmt.Errorf("list provider connection creation order: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetConnectionEnabled toggles a connection without touching its secret.
func (s *Store) SetConnectionEnabled(ctx context.Context, id string, enabled bool) error {
	flag := 0
	if enabled {
		flag = 1
	}
	err := s.exec(ctx, editConnection(id, func(c *Connection) { c.Enabled = enabled }), "UPDATE provider_connections SET enabled=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", flag, id)
	if err != nil {
		return fmt.Errorf("update provider connection: %w", err)
	}
	return nil
}

// SetConnectionPriority changes stable account order without loading secrets.
func (s *Store) SetConnectionPriority(ctx context.Context, id string, priority int) error {
	err := s.exec(ctx, editConnection(id, func(c *Connection) { c.Priority = priority }), "UPDATE provider_connections SET priority=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", priority, id)
	if err != nil {
		return fmt.Errorf("update provider connection priority: %w", err)
	}
	return nil
}

// MoveConnection swaps one account with its adjacent sibling in stable routing
// order. A no-op at either end keeps the user action bounded and deterministic.
func (s *Store) MoveConnection(ctx context.Context, id string, direction int) error {
	if direction != -1 && direction != 1 {
		return errors.New("connection move direction must be -1 or 1")
	}
	var ordered []Connection
	change := func(c *Configuration) error {
		nodeID := ""
		for _, connection := range c.Connections {
			if connection.ID == id {
				nodeID = connection.NodeID
				break
			}
		}
		if nodeID == "" {
			return ErrNotFound
		}
		for _, connection := range c.Connections {
			if connection.NodeID == nodeID {
				ordered = append(ordered, connection)
			}
		}
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].Priority != ordered[j].Priority {
				return ordered[i].Priority < ordered[j].Priority
			}
			if ordered[i].Name != ordered[j].Name {
				return ordered[i].Name < ordered[j].Name
			}
			return ordered[i].ID < ordered[j].ID
		})
		for i := range ordered {
			if ordered[i].ID == id {
				next := i + direction
				if next < 0 || next >= len(ordered) {
					return errNoChange
				}
				ordered[i], ordered[next] = ordered[next], ordered[i]
				break
			}
		}
		for i := range ordered {
			ordered[i].Priority = i
			if err := editConnection(ordered[i].ID, func(item *Connection) { item.Priority = i })(c); err != nil {
				return err
			}
		}
		return nil
	}
	return s.update(ctx, change, func(tx *sql.Tx) error {
		for _, item := range ordered {
			if err := requireAffected(tx.ExecContext(ctx, "UPDATE provider_connections SET priority=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", item.Priority, item.ID)); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetConnectionProxy changes a connection's proxy-pool binding without
// exposing its sealed credential.
func (s *Store) SetConnectionProxy(ctx context.Context, id, poolID string) error {
	err := s.exec(ctx, editConnection(id, func(c *Connection) { c.ProxyPoolID = poolID }), "UPDATE provider_connections SET proxy_pool_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", nullable(poolID), id)
	if err != nil {
		return fmt.Errorf("update provider connection proxy: %w", err)
	}
	return nil
}

// DeleteConnection removes one connection. Disabling is preferred for routing
// continuity, so callers should treat this as an explicit destructive action.
func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	change := func(c *Configuration) error {
		for i := range c.Connections {
			if c.Connections[i].ID == id {
				c.Connections = append(c.Connections[:i], c.Connections[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	}
	err := s.exec(ctx, change, "DELETE FROM provider_connections WHERE id=?", id)
	if err != nil {
		return fmt.Errorf("delete provider connection: %w", err)
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
	if err := scan(&connection.ID, &connection.NodeID, &connection.ProviderID, &connection.Name, &connection.AuthKind, &connection.Identity, &blob, &enabled, &connection.Priority, &connection.ProxyPoolID, &connection.AccountID, &connection.CredentialRevision); err != nil {
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
	connection.Settings = projectSettings(connection.Secret.ProviderData)
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
