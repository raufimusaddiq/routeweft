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

	// installMu serializes every credential install (durable write + memory
	// publish) for one connection. The generation check and its durable write
	// happen under this lock, so a refresh can never pass the staleness check and
	// then write over an import that landed in the check-to-write window.
	installMu sync.Mutex
	mu        sync.Mutex
	// generation increments on every explicit credential install (import or
	// register). A refresh captures it before the exchange and refuses to commit
	// or publish if it changed, so an import that lands mid-refresh is never
	// overwritten by the older exchange result.
	generation uint64
	current    Secret
	seeded     bool
	inflight   *refreshCall
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

// ErrSuperseded means a refresh result was discarded because a newer credential
// install (import or register) landed while the exchange was in flight.
var ErrSuperseded = errors.New("credential refresh was superseded by a newer install")

// Register seeds one connection into memory-first state. An already-registered
// connection is replaced, which is how an import or edit installs new material.
func (r *Registry) Register(connection Connection, exchanger Exchanger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.entries[connection.ID]; ok {
		existing.installMu.Lock()
		existing.mu.Lock()
		existing.connection = connection
		existing.current = connection.Secret
		existing.seeded = true
		existing.exchanger = exchanger
		existing.generation++
		existing.mu.Unlock()
		existing.installMu.Unlock()
		return
	}
	r.entries[connection.ID] = &entry{connection: connection, exchanger: exchanger, current: connection.Secret, seeded: true, generation: 1}
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
	generation := current.generation
	current.mu.Unlock()

	secret, err := r.refresh(ctx, connectionID, stored, generation, current, exchanger)
	if errors.Is(err, ErrSuperseded) {
		// A newer install won while this exchange ran. Serve the newer credential
		// instead of a stale or failed one, and do not clear the inflight marker's
		// result as an error for other waiters.
		current.mu.Lock()
		secret, err = current.current, nil
		current.inflight = nil
		call.secret, call.err = secret, nil
		current.mu.Unlock()
		close(call.done)
		return secret, nil
	}
	if err != nil {
		secret = Secret{}
	}
	current.mu.Lock()
	// Memory publication already happened inside refresh(), under the install
	// lock and generation check, so a newer import can never be overwritten here.
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
//
// The staleness check, the durable write, and the in-memory publication all
// execute under the connection's install lock, so an import that lands during
// the exchange either wins before this write (making it a no-op) or waits until
// after it. Either order leaves the newer install as both the durable and the
// published credential; they can never diverge.
func (r *Registry) refresh(ctx context.Context, connectionID string, stored Secret, generation uint64, current *entry, exchanger Exchanger) (Secret, error) {
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
	current.installMu.Lock()
	defer current.installMu.Unlock()
	// Still current under the install lock: nothing has replaced this generation
	// since the exchange started.
	if r.superseded(current, generation) {
		return Secret{}, ErrSuperseded
	}
	if r.store != nil {
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := r.store.RotateSecret(commitCtx, connectionID, refreshed)
		cancel()
		if err != nil {
			return Secret{}, fmt.Errorf("persist rotated credential: %w", err)
		}
	}
	// Publish in memory in the same critical section as the durable write so an
	// import cannot interleave and leave memory older than storage. installMu is
	// still held and the generation was validated above, so nothing can have
	// replaced this credential since.
	current.mu.Lock()
	current.current = refreshed
	current.seeded = true
	current.mu.Unlock()
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
	// The generation bump and the durable write are one critical section under the
	// install lock, so a refresh cannot interleave between them and overwrite this
	// import with a stale rotated secret.
	current.installMu.Lock()
	current.mu.Lock()
	current.generation++
	current.mu.Unlock()
	if err := r.store.RotateSecret(ctx, connectionID, secret); err != nil {
		current.installMu.Unlock()
		return err
	}
	if err := r.store.RecordCredentialEvent(ctx, connectionID, "import", ""); err != nil {
		current.installMu.Unlock()
		return err
	}
	current.mu.Lock()
	current.current = secret
	current.seeded = true
	current.mu.Unlock()
	current.installMu.Unlock()
	return nil
}

// superseded reports whether a newer install replaced the credential generation
// a refresh started from.
func (r *Registry) superseded(current *entry, generation uint64) bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.generation != generation
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
