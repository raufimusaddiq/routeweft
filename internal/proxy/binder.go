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
// policy, or nil when no proxy applies so ingress keeps its default client.
func (b *Binder) ClientFor(_ context.Context, connectionID string) *http.Client {
	if b == nil || b.Clients == nil || b.Snapshot == nil {
		return nil
	}
	snapshot := b.Snapshot()
	if snapshot == nil {
		return nil
	}
	global := b.global(snapshot)
	binding := b.binding(snapshot, connectionID)
	resolver := Resolver{Global: global}
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

func (b *Binder) binding(snapshot *runtime.RuntimeSnapshot, connectionID string) Binding {
	if connectionID == "" {
		return Binding{}
	}
	poolID := strings.TrimSpace(snapshot.ConnectionPool(connectionID))
	if poolID == "" {
		return Binding{}
	}
	strategy, members, ok := snapshot.ProxyPool(poolID)
	if !ok {
		// A missing or disabled pool is not a hard failure; the connection falls
		// back to the global proxy setting.
		return Binding{}
	}
	cursor := b.nextCursor(poolID)
	member, ok := selectMember(members, cursor, Strategy(strategy))
	if !ok {
		return Binding{}
	}
	return Binding{PoolID: poolID, MemberURL: member.URL, RotationUsed: len(members) > 1}
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
	settings := snapshot.Settings()
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
