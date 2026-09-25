package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// SettingsSource supplies the compiled outbound-proxy settings
// (outboundProxyEnabled/outboundProxyUrl/noProxy) from the active snapshot.
type SettingsSource func() map[string]string

// BindingSource resolves one connection's bound proxy pool id.
type BindingSource interface {
	ConnectionPool(ctx context.Context, connectionID string) (string, error)
}

// SnapshotBindingSource reads pool bindings from the immutable runtime snapshot,
// so the request path never opens SQLite (SPEC §5). Bindings are loaded into the
// snapshot by the control mutation that edits a connection.
type SnapshotBindingSource struct {
	Snapshot func() *runtime.RuntimeSnapshot
}

// ConnectionPool reports the pool id bound to a connection, if any.
func (s SnapshotBindingSource) ConnectionPool(_ context.Context, connectionID string) (string, error) {
	if s.Snapshot == nil {
		return "", nil
	}
	snapshot := s.Snapshot()
	if snapshot == nil {
		return "", nil
	}
	return snapshot.ConnectionPool(connectionID), nil
}

// Binder compiles and caches the per-connection outbound client that honors a
// bound proxy pool (PRD-ROUTE-005, SPEC §20). It is the production wiring behind
// ingress Options.ClientFor.
type Binder struct {
	Pools    *Store
	Settings SettingsSource
	Bindings BindingSource
	Clients  *transport.PooledClients

	// allowLocal mirrors the trusted-local operator policy so proxy tests can
	// reach a LAN proxy during validation.
	AllowLocal bool

	mu sync.Mutex
	// cursors are memory-first round-robin positions per pool (SPEC §13).
	cursors map[string]uint64
}

// NewBinder builds a proxy binder with a fresh pooled-client cache.
func NewBinder(pools *Store, settings SettingsSource, bindings BindingSource) *Binder {
	return &Binder{Pools: pools, Settings: settings, Bindings: bindings, Clients: transport.NewPooledClients(), cursors: make(map[string]uint64)}
}

// ClientFor returns the pooled client for one connection's compiled proxy
// policy, or nil when no proxy applies so ingress keeps its default client.
func (b *Binder) ClientFor(ctx context.Context, connectionID string) *http.Client {
	if b == nil || b.Clients == nil {
		return nil
	}
	global := b.global()
	binding := b.binding(ctx, connectionID)
	resolver := Resolver{Global: global, Store: b.Pools}
	policy := resolver.Policy(binding)
	if !policy.Enabled || (policy.GlobalProxyURL == "" && policy.ConnectionProxyURL == "") {
		return nil
	}
	client, err := b.Clients.Client(policy)
	if err != nil {
		return nil
	}
	return client
}

func (b *Binder) binding(ctx context.Context, connectionID string) Binding {
	if b.Bindings == nil || b.Pools == nil || connectionID == "" {
		return Binding{}
	}
	poolID, err := b.Bindings.ConnectionPool(ctx, connectionID)
	if err != nil || strings.TrimSpace(poolID) == "" {
		return Binding{}
	}
	pool, err := b.Pools.GetPool(ctx, poolID)
	if err != nil || !pool.Enabled {
		// A missing or disabled pool is not a hard failure; the connection falls
		// back to the global proxy setting.
		return Binding{}
	}
	cursor := b.nextCursor(poolID)
	member, ok := SelectMember(pool, cursor, pool.Strategy)
	if !ok {
		return Binding{}
	}
	return Binding{PoolID: poolID, MemberURL: member.URL, RotationUsed: len(pool.Members) > 1}
}

func (b *Binder) nextCursor(poolID string) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cursors == nil {
		b.cursors = make(map[string]uint64)
	}
	value := b.cursors[poolID]
	b.cursors[poolID] = value + 1
	return value
}

func (b *Binder) global() GlobalConfig {
	if b.Settings == nil {
		if b.AllowLocal {
			return GlobalConfig{AllowLocal: true}
		}
		return GlobalConfig{}
	}
	settings := b.Settings()
	enabled, _ := strconv.ParseBool(strings.TrimSpace(settings["outboundProxyEnabled"]))
	return GlobalConfig{
		Enabled:    enabled,
		ProxyURL:   strings.TrimSpace(settings["outboundProxyUrl"]),
		NoProxy:    parseNoProxy(settings["noProxy"]),
		AllowLocal: b.AllowLocal,
	}
}

func parseNoProxy(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var entries []string
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return nil
	}
	return entries
}
