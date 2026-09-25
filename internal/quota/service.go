package quota

import (
	"context"
	"errors"
	"fmt"
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
	Credential(ctx context.Context, connectionID string) (string, error)
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
}

// NewService builds a quota refresh service.
func NewService(connections ConnectionSource, credentials CredentialSource, observer Observer) *Service {
	return &Service{Connections: connections, Credentials: credentials, Observer: observer, lastErr: make(map[string]error)}
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
		if err := s.refreshConnection(ctx, connection); err != nil && firstErr == nil {
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
	credential, err := s.Credentials.Credential(ctx, connection.ID)
	if err != nil {
		s.record(connection.ID, err)
		return err
	}
	if strings.TrimSpace(credential) == "" {
		err := fmt.Errorf("connection %q has no credential for quota read", connection.Name)
		s.record(connection.ID, err)
		return err
	}
	_, err = s.Observer.Observe(ctx, connection.ProviderID, connection.ID, credential)
	s.record(connection.ID, err)
	return err
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
