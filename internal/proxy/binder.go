package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// SnapshotSource returns the active immutable runtime snapshot. Proxy selection
// reads only the snapshot, so it never synchronously queries SQLite on the
// request path (SPEC §5, §20).
type SnapshotSource func() *runtime.RuntimeSnapshot

// Binder compiles and caches the per-connection outbound client that honors a
// bound proxy pool (PRD-ROUTE-005, SPEC §20). It is the production wiring behind
// ingress Options.ClientFor.
type Binder struct {
	Snapshot SnapshotSource
	Clients  *transport.PooledClients

	// allowLocal mirrors the trusted-local operator policy so proxy tests can
	// reach a LAN proxy during validation.
	AllowLocal bool

	mu sync.Mutex
	// cursors are memory-first round-robin positions per pool (SPEC §13).
	cursors map[string]uint64
}

// NewBinder builds a proxy binder with a fresh pooled-client cache.
func NewBinder(snapshot SnapshotSource) *Binder {
	return &Binder{Snapshot: snapshot, Clients: transport.NewPooledClients(), cursors: make(map[string]uint64)}
}

// ClientFor returns the pooled client for one connection's compiled proxy
// policy, or nil when no proxy applies so ingress keeps its default client. A
// per-connection proxy whose stored URL is unusable yields a client that fails
// every request: dialing directly instead would leave over the address the
// operator configured the proxy to hide.
func (b *Binder) ClientFor(_ context.Context, connectionID string) *http.Client {
	if b == nil || b.Clients == nil {
		return nil
	}
	policy, ok := b.Policy(connectionID)
	if !ok || !policy.Enabled || (policy.GlobalProxyURL == "" && policy.ConnectionProxyURL == "") {
		return nil
	}
	client, err := b.Clients.Client(policy)
	if err != nil {
		if policy.ConnectionOwnsNoProxy {
			return refusingClient
		}
		return nil
	}
	return client
}

// Policy compiles one connection's proxy policy from the active snapshot,
// advancing its pool's rotation cursor as an attempt does. ok is false when
// there is no snapshot.
func (b *Binder) Policy(connectionID string) (policy transport.ProxyPolicy, ok bool) {
	if b == nil || b.Snapshot == nil {
		return transport.ProxyPolicy{}, false
	}
	snapshot := b.Snapshot()
	if snapshot == nil {
		return transport.ProxyPolicy{}, false
	}
	resolver := Resolver{Global: b.global(snapshot)}
	return resolver.Policy(b.binding(snapshot, connectionID)), true
}

// TransportFor is ClientFor's transport, for a transport.ConnectionRouter on
// the side paths (usage, refresh, tests) that are not inference.
func (b *Binder) TransportFor(connectionID string) http.RoundTripper {
	if client := b.ClientFor(context.Background(), connectionID); client != nil {
		return client.Transport
	}
	return nil
}

var errConnectionProxyInvalid = errors.New("connection proxy URL is invalid")

var refusingClient = &http.Client{Transport: refusingTransport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type refusingTransport struct{}

func (refusingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request != nil && request.Body != nil {
		_ = request.Body.Close()
	}
	return nil, errConnectionProxyInvalid
}

func (b *Binder) binding(snapshot *runtime.RuntimeSnapshot, connectionID string) Binding {
	if connectionID == "" {
		return Binding{}
	}
	if binding, ok := b.poolBinding(snapshot, connectionID); ok {
		return binding
	}
	// A missing, disabled or empty pool is not a hard failure; the connection
	// falls back to its own proxy, then to the global proxy setting, the pin's
	// order (connectionProxy.js:92-165).
	if legacy, ok := snapshot.LegacyConnectionProxy(connectionID); ok {
		return Binding{ConnectionProxyURL: legacy.URL, ConnectionNoProxy: legacy.NoProxy}
	}
	return Binding{}
}

func (b *Binder) poolBinding(snapshot *runtime.RuntimeSnapshot, connectionID string) (Binding, bool) {
	poolID := strings.TrimSpace(snapshot.ConnectionPool(connectionID))
	if poolID == "" {
		return Binding{}, false
	}
	strategy, members, ok := snapshot.ProxyPool(poolID)
	if !ok {
		return Binding{}, false
	}
	cursor := b.nextCursor(poolID)
	member, ok := selectMember(members, cursor, Strategy(strategy))
	if !ok {
		return Binding{}, false
	}
	return Binding{PoolID: poolID, MemberURL: member.URL, RotationUsed: len(members) > 1}, true
}

// selectMember chooses from compiled snapshot members. It mirrors SelectMember
// but operates on the snapshot's member view, keeping the hot path free of the
// durable Store type.
func selectMember(members []runtime.ProxyMember, cursor uint64, strategy Strategy) (runtime.ProxyMember, bool) {
	eligible := make([]runtime.ProxyMember, 0, len(members))
	for _, member := range members {
		if member.Enabled && strings.TrimSpace(member.URL) != "" {
			eligible = append(eligible, member)
		}
	}
	if len(eligible) == 0 {
		return runtime.ProxyMember{}, false
	}
	if strategy == StrategyFillFirst {
		return eligible[0], true
	}
	return eligible[cursor%uint64(len(eligible))], true
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

func (b *Binder) global(snapshot *runtime.RuntimeSnapshot) GlobalConfig {
	if snapshot == nil {
		if b.AllowLocal {
			return GlobalConfig{AllowLocal: true}
		}
		return GlobalConfig{}
	}
	enabled, proxyURL, noProxy := snapshot.OutboundProxy()
	return GlobalConfig{
		Enabled:    enabled,
		ProxyURL:   proxyURL,
		NoProxy:    noProxy,
		AllowLocal: b.AllowLocal,
	}
}
