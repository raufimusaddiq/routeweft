package credentials

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// RefreshLead is the default window before expiry in which a rotating access
// token is refreshed proactively.
const RefreshLead = 60 * time.Second

// Exchanger performs the provider-specific refresh/token exchange. It is the
// only provider-specific code the registry needs, so every provider group reuses
// one singleflight/persist implementation (SPEC §19, PRD-AUTH-003).
type Exchanger func(ctx context.Context, current Secret) (Secret, error)

// TokenProvider returns one usable credential payload for a connection. Both
// static API keys and rotating OAuth credentials satisfy it.
type TokenProvider interface {
	// Credential returns the current access token/cookie value.
	Credential(ctx context.Context) (string, error)
	// Secret returns the full payload, for flows that need the refresh token.
	Secret(ctx context.Context) (Secret, error)
}

// Registry holds memory-first credential state for every active connection and
// serializes refresh through one durable commit path.
type Registry struct {
	store *Store
	now   func() time.Time
	lead  time.Duration

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	connection Connection
	exchanger  Exchanger

	mu       sync.Mutex
	current  Secret
	seeded   bool
	inflight *refreshCall
}

type refreshCall struct {
	done   chan struct{}
	secret Secret
	err    error
}

// NewRegistry builds an empty credential registry over the durable store.
func NewRegistry(store *Store, now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{store: store, now: now, lead: RefreshLead, entries: make(map[string]*entry)}
}

// ErrNoExchanger means a rotating connection has no refresh implementation.
var ErrNoExchanger = errors.New("connection has no credential refresh implementation")

// Register seeds one connection into memory-first state. An already-registered
// connection is replaced, which is how an import or edit installs new material.
func (r *Registry) Register(connection Connection, exchanger Exchanger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.entries[connection.ID]; ok {
		existing.mu.Lock()
		existing.connection = connection
		existing.current = connection.Secret
		existing.seeded = true
		existing.exchanger = exchanger
		existing.mu.Unlock()
		return
	}
	r.entries[connection.ID] = &entry{connection: connection, exchanger: exchanger, current: connection.Secret, seeded: true}
}

// Forget drops one connection's in-memory credential state.
func (r *Registry) Forget(connectionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, connectionID)
}

// Load seeds every connection for one provider from durable storage.
func (r *Registry) Load(ctx context.Context, providerID string) ([]Connection, error) {
	if r.store == nil {
		return nil, ErrKeyRequired
	}
	connections, err := r.store.ListConnections(ctx, providerID)
	if err != nil {
		return nil, err
	}
	for _, connection := range connections {
		r.Register(connection, nil)
	}
	return connections, nil
}

// Credential returns a currently valid access token for one connection,
// refreshing once under singleflight when needed.
func (r *Registry) Credential(ctx context.Context, connectionID string) (string, error) {
	secret, err := r.Secret(ctx, connectionID)
	if err != nil {
		return "", err
	}
	if secret.AccessToken != "" {
		return secret.AccessToken, nil
	}
	return secret.Cookie, nil
}

// Secret returns the full credential payload, refreshing when the access token
// is missing or within the refresh lead.
func (r *Registry) Secret(ctx context.Context, connectionID string) (Secret, error) {
	current, err := r.lookup(connectionID)
	if err != nil {
		return Secret{}, err
	}
	current.mu.Lock()
	if r.validLocked(current) {
		secret := current.current
		current.mu.Unlock()
		return secret, nil
	}
	if call := current.inflight; call != nil {
		current.mu.Unlock()
		select {
		case <-call.done:
			return call.secret, call.err
		case <-ctx.Done():
			return Secret{}, ctx.Err()
		}
	}
	exchanger := current.exchanger
	if exchanger == nil {
		// A static credential has nothing to refresh; return what is stored so a
		// missing static key surfaces as a configuration problem, not a hang.
		secret := current.current
		current.mu.Unlock()
		return secret, nil
	}
	call := &refreshCall{done: make(chan struct{})}
	current.inflight = call
	stored := current.current
	current.mu.Unlock()

	secret, err := r.refresh(ctx, connectionID, stored, exchanger)
	if err != nil {
		secret = Secret{}
	}
	current.mu.Lock()
	if err == nil {
		current.current = secret
	}
	current.inflight = nil
	call.secret, call.err = secret, err
	current.mu.Unlock()
	close(call.done)
	return secret, err
}

// refresh runs the provider exchange and durably commits the rotated payload
// before a successful refresh becomes visible to any caller. The commit is
// bounded and detached so an already-rotated upstream token is not dropped when
// the caller disappears between exchange and commit (SPEC §19).
func (r *Registry) refresh(ctx context.Context, connectionID string, stored Secret, exchanger Exchanger) (Secret, error) {
	refreshed, err := exchanger(ctx, stored)
	if err != nil {
		return Secret{}, err
	}
	// A refresh that yields no usable access token/cookie is a failure, never a
	// silently empty credential.
	if strings.TrimSpace(refreshed.AccessToken) == "" && strings.TrimSpace(refreshed.Cookie) == "" {
		return Secret{}, errors.New("credential refresh returned an unusable payload")
	}
	// Providers may omit the refresh token when it did not rotate; keep the
	// known durable value rather than losing it (PRD-AUTH-003).
	if strings.TrimSpace(refreshed.RefreshToken) == "" {
		refreshed.RefreshToken = stored.RefreshToken
	}
	if r.store != nil {
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := r.store.RotateSecret(commitCtx, connectionID, refreshed)
		cancel()
		if err != nil {
			return Secret{}, fmt.Errorf("persist rotated credential: %w", err)
		}
	}
	return refreshed, nil
}

// Import installs operator-supplied credential material through the same durable
// path as OAuth refresh, validating the identity before overwriting an existing
// connection (SPEC §35).
func (r *Registry) Import(ctx context.Context, connectionID, identity string, secret Secret) error {
	r.mu.Lock()
	current, ok := r.entries[connectionID]
	r.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	identity = strings.TrimSpace(identity)
	current.mu.Lock()
	storedIdentity := current.connection.Identity
	current.mu.Unlock()
	if identity != "" && storedIdentity != "" && identity != storedIdentity {
		return fmt.Errorf("imported identity %q does not match connection identity", identity)
	}
	if secret.Empty() {
		return errors.New("import requires credential material")
	}
	if r.store == nil {
		return ErrKeyRequired
	}
	if err := r.store.RotateSecret(ctx, connectionID, secret); err != nil {
		return err
	}
	if err := r.store.RecordCredentialEvent(ctx, connectionID, "import", ""); err != nil {
		return err
	}
	current.mu.Lock()
	current.current = secret
	current.seeded = true
	current.mu.Unlock()
	return nil
}

func (r *Registry) lookup(connectionID string) (*entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.entries[connectionID]
	if !ok {
		return nil, ErrNotFound
	}
	return current, nil
}

func (r *Registry) validLocked(current *entry) bool {
	if !current.seeded {
		return false
	}
	secret := current.current
	if secret.AccessToken == "" && secret.Cookie == "" {
		return false
	}
	if secret.Expiry.IsZero() {
		return true
	}
	return r.now().Add(r.lead).Before(secret.Expiry)
}
