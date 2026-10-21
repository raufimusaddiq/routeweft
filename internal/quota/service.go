package quota

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// CredentialSource supplies the live credential for one connection. The
// credentials.Registry satisfies it, so quota reads reuse the same durable,
// singleflight-refreshed credential that inference uses.
type CredentialSource interface {
	Secret(ctx context.Context, connectionID string) (credentials.Secret, error)
}

// ConnectionSeeder loads a provider's durable connections into the credential
// registry before they are read. Without this, Credential on an unseeded
// registry returns ErrNotFound and every quota read fails.
type ConnectionSeeder interface {
	Load(ctx context.Context, providerID string) ([]credentials.Connection, error)
}

// ConnectionSource lists the enabled connections for a provider.
type ConnectionSource interface {
	ListConnections(ctx context.Context, providerID string) ([]credentials.Connection, error)
}

// Service refreshes quota for configured provider accounts and publishes each
// normalized observation to RuntimeState. It is the production call site for
// Observer: the app drives it on an interval and on explicit operator refresh
// (PRD-QUOTA-001: refreshing must not block normal inference).
type Service struct {
	Connections ConnectionSource
	Credentials CredentialSource
	// Seeder registers a provider's connections with the credential source before
	// the first read. Optional, but required for a memory-first registry that is
	// not otherwise seeded.
	Seeder   ConnectionSeeder
	Observer Observer

	mu       sync.Mutex
	lastErr  map[string]error
	lastDone time.Time
	states   map[string]State
}

// NewService builds a quota refresh service.
func NewService(connections ConnectionSource, credentials CredentialSource, observer Observer) *Service {
	return &Service{Connections: connections, Credentials: credentials, Observer: observer, lastErr: make(map[string]error), states: make(map[string]State)}
}

// WithSeeder attaches the connection seeder and returns the service.
func (s *Service) WithSeeder(seeder ConnectionSeeder) *Service {
	s.Seeder = seeder
	return s
}

// RefreshProvider reads and publishes quota for one provider's enabled
// connections. A single account's read failure is isolated: it is published as
// an error observation and does not stop the others.
func (s *Service) RefreshProvider(ctx context.Context, providerID string) error {
	if s.Connections == nil {
		return errors.New("quota service requires a connection source")
	}
	connections, err := s.Connections.ListConnections(ctx, providerID)
	if err != nil {
		return err
	}
	// Seed the credential source from durable state before reading, so a
	// memory-first registry has an entry for every connection.
	if s.Seeder != nil {
		if _, err := s.Seeder.Load(ctx, providerID); err != nil {
			return err
		}
	}
	var firstErr error
	for _, connection := range connections {
		if !connection.Enabled {
			continue
		}
		// ErrNotConfigured means this connection has nothing readable, not that the
		// refresh failed. Propagating it as firstErr made one cookie-only connection
		// report the whole provider as failed.
		if err := s.refreshConnection(ctx, connection); err != nil && !errors.Is(err, ErrNotConfigured) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RefreshAll refreshes every provider that has a registered usage client.
func (s *Service) RefreshAll(ctx context.Context) error {
	providers := make([]string, 0, len(s.Observer.Clients))
	for providerID := range s.Observer.Clients {
		providers = append(providers, providerID)
	}
	sort.Strings(providers)
	var firstErr error
	for _, providerID := range providers {
		if err := s.RefreshProvider(ctx, providerID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// LastError reports the most recent refresh error for one connection, if any.
func (s *Service) LastError(connectionID string) (error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err, ok := s.lastErr[connectionID]
	return err, ok
}

// LastRefresh reports when the last refresh attempt completed.
func (s *Service) LastRefresh() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDone
}

// Run refreshes on the supplied interval until the context is cancelled. A
// refresh error is recorded, not fatal, so a temporarily unavailable provider
// endpoint cannot take the process down.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RefreshAll(ctx)
		}
	}
}

func (s *Service) refreshConnection(ctx context.Context, connection credentials.Connection) error {
	if s.Credentials == nil {
		return errors.New("quota service requires a credential source")
	}
	// ListConnections resolves each connection's provider from its node.
	if _, ok := s.Observer.Clients[connection.ProviderID]; !ok {
		return nil
	}
	secret, err := s.Credentials.Secret(ctx, connection.ID)
	if err != nil {
		s.record(connection.ID, err)
		return err
	}
	credential := Credential{
		AccessToken:  secret.AccessToken,
		Cookie:       secret.Cookie,
		ProviderData: secret.ProviderData,
	}
	// Every usage reader authenticates with an access token or API key; none reads
	// a session cookie. A cookie-only connection therefore has no readable
	// credential. It is reported as ErrNotConfigured so the caller can skip it, and
	// deliberately not recorded: lastErr exists to surface read failures, and a
	// provider with no usage endpoint has not failed.
	if strings.TrimSpace(credential.AccessToken) == "" {
		return ErrNotConfigured
	}
	_, err = s.observe(ctx, connection.ProviderID, connection.ID, credential)
	return err
}

func (s *Service) observe(ctx context.Context, providerID, connectionID string, credential Credential) (State, error) {
	state, err := s.Observer.Observe(ctx, providerID, connectionID, credential)
	s.mu.Lock()
	// A failed read yields a near-empty State. Keep the last good detail so an
	// outage shows stale named windows instead of wiping them; the failure is
	// still surfaced through record() and the observer's error status.
	if _, known := s.states[connectionID]; err == nil || !known {
		s.states[connectionID] = state
	}
	s.mu.Unlock()
	s.record(connectionID, err)
	return state, err
}

func (s *Service) LastState(connectionID string) (State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[connectionID]
	state.Windows = append([]Window(nil), state.Windows...)
	for i := range state.Windows {
		if value := state.Windows[i].Remaining; value != nil {
			copy := *value
			state.Windows[i].Remaining = &copy
		}
		if value := state.Windows[i].ResetAt; value != nil {
			copy := *value
			state.Windows[i].ResetAt = &copy
		}
	}
	if state.AvailableReset != nil {
		copy := *state.AvailableReset
		state.AvailableReset = &copy
	}
	return state, ok
}

func (s *Service) record(connectionID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.lastErr, connectionID)
	} else {
		s.lastErr[connectionID] = err
	}
	s.lastDone = time.Now()
}
