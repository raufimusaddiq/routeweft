package proxy

import (
	"context"
	"errors"
	"math/rand/v2"
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

	// Rotation spreads a no-auth provider over the active pools.
	Rotation Rotation

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
// operator configured the proxy to hide. So does a strict pool that cannot supply
// a proxy (PARITY 6.38).
func (b *Binder) ClientFor(_ context.Context, connectionID string) *http.Client {
	if b == nil || b.Clients == nil {
		return nil
	}
	policy, ok := b.Policy(connectionID)
	if !ok {
		return nil
	}
	if policy.RelayURL != "" {
		client, err := b.Clients.Client(policy)
		if err != nil {
			return refusingClient
		}
		return client
	}
	if policy.Strict && policy.Intended && !policy.Enabled {
		return refusingClient
	}
	if !policy.Enabled || (policy.GlobalProxyURL == "" && policy.ConnectionProxyURL == "") {
		return nil
	}
	client, err := b.Clients.Client(policy)
	if err != nil {
		if policy.ConnectionOwnsNoProxy || (policy.Strict && policy.Intended) {
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
	input := ConnectionInput{PoolID: strings.TrimSpace(snapshot.ConnectionPool(connectionID))}
	if choice, ok := snapshot.NoAuthProxy(connectionID); ok {
		// A no-auth provider's providerStrategies entry names the proxy; the
		// reference's virtual account for it has no pool binding or proxy of its own.
		input.PoolID = b.noAuthPool(snapshot, choice)
	} else if legacy, ok := snapshot.LegacyConnectionProxy(connectionID); ok {
		input.LegacyEnabled, input.LegacyURL = true, legacy.URL
		input.LegacyNoProxy = strings.Join(legacy.NoProxy, ",")
	}
	// A missing, disabled or empty pool is not a hard failure unless it is strict;
	// the connection falls back to its own proxy, then to the global proxy setting,
	// the pin's order (connectionProxy.js:92-165).
	var rotationUsed bool
	if poolID := strings.TrimSpace(input.PoolID); poolID != "" && poolID != NoPool {
		if view, found := snapshot.PoolView(poolID); found {
			input.Pool = PoolInfo{Found: true, Enabled: view.Enabled, Kind: Kind(view.Kind), Strict: view.Strict, NoProxy: view.NoProxy}
			if view.Enabled {
				if member, ok := selectMember(view.Members, b.nextCursor(poolID), Strategy(view.Strategy)); ok {
					input.Pool.URL = member.URL
					rotationUsed = len(view.Members) > 1
				}
			}
		}
	}
	resolution := Resolve(input)
	binding := Binding{PoolID: resolution.PoolID, RotationUsed: rotationUsed, Strict: resolution.Strict, Intended: resolution.PoolID != ""}
	switch resolution.Source {
	case "pool":
		binding.MemberURL = resolution.ProxyURL
		binding.PoolNoProxy = splitList(resolution.NoProxy)
	case "legacy":
		binding.ConnectionProxyURL = resolution.ProxyURL
		binding.ConnectionNoProxy = splitList(resolution.NoProxy)
	default:
		if resolution.RelayURL != "" {
			binding.RelayURL = resolution.RelayURL
		}
	}
	return binding
}

// noAuthPool turns a no-auth provider's proxy choice into the pool id to bind: a
// rotation picks among the active pools (and finds none when there are none), else
// the entry's fixed pool stands.
func (b *Binder) noAuthPool(snapshot *runtime.RuntimeSnapshot, choice runtime.NoAuthProxy) string {
	if choice.Rotate == "" || choice.Rotate == "none" {
		return choice.PoolID
	}
	return b.Rotation.Pick(snapshot.RotationPools(), choice.Rotate, choice.ProviderID, rand.IntN)
}

func splitList(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
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
