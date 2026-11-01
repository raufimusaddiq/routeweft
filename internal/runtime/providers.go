package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/cloudflare"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

type servingProvider struct {
	metadata  routing.ConnectionMetadata
	node      credentials.Node
	endpoints map[registry.Protocol]string
	account   routing.Account
	name      string
}

func compileProviders(config credentials.Configuration) (map[string]servingProvider, error) {
	specs := make(map[string]registry.Spec)
	for _, spec := range registry.Builtins() {
		specs[spec.ID] = spec
	}
	nodes := make(map[string]credentials.Node)
	result := make(map[string]servingProvider)
	for _, node := range config.Clone().Nodes {
		if node.ID == "" || node.ProviderID == "" {
			return nil, fmt.Errorf("provider node id and identity are required")
		}
		if _, exists := nodes[node.ID]; exists {
			return nil, fmt.Errorf("duplicate provider node %q", node.ID)
		}
		if spec, ok := specs[node.ProviderID]; ok && node.Kind == credentials.NodeBuiltin {
			if node.BaseURL == "" {
				node.BaseURL = spec.DefaultBaseURL
			}
			if len(node.Transports) == 0 {
				for _, protocol := range spec.Transports {
					node.Transports = append(node.Transports, string(protocol))
				}
			}
		}
		nodes[node.ID] = node
	}
	for _, connection := range config.Connections {
		node, ok := nodes[connection.NodeID]
		if !ok {
			return nil, fmt.Errorf("connection %q references an unknown provider node", connection.ID)
		}
		if _, exists := result[connection.ID]; exists {
			return nil, fmt.Errorf("duplicate connection %q", connection.ID)
		}
		endpoints := map[registry.Protocol]string(nil)
		if node.Kind == credentials.NodeBuiltin {
			endpoints = specs[node.ProviderID].TransportEndpoints
		}
		result[connection.ID] = servingProvider{metadata: routing.ConnectionMetadata{
			NodeID: node.ID, NodeKind: string(node.Kind), NodeName: node.Name, NodePrefix: node.Prefix,
			Name: connection.Name, AuthKind: string(connection.AuthKind), Identity: connection.Identity,
			AccountID: connection.AccountID, ProxyPoolID: connection.ProxyPoolID, Settings: connection.Settings,
		}, node: node, endpoints: endpoints, account: routing.Account{ID: connection.ID, Enabled: connection.Enabled, Priority: connection.Priority}, name: connection.Name}
	}
	return result, nil
}

// UpdateProviders uses the same compile/commit/publish lock as other config.
func (m *Manager) UpdateProviders(ctx context.Context, change func(*credentials.Configuration) error, persist func(*sql.Tx) error) error {
	// Sealed settings and their snapshot must serialize with configuration writes.
	// Only local compile/SQLite work holds this gate; refresh HTTP runs before it.
	if err := m.lockMutation(ctx); err != nil {
		return err
	}
	defer m.unlockMutation()
	current, err := m.Load()
	if err != nil {
		return err
	}
	next := current.providerConfig.Clone()
	if err := change(&next); err != nil {
		return err
	}
	// Token-only renewals keep the compiled configuration and revision unchanged.
	if reflect.DeepEqual(next, current.providerConfig) {
		if persist == nil {
			return nil
		}
		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := persist(tx); err != nil {
			return err
		}
		return tx.Commit()
	}
	_, err = m.updateLocked(ctx, func(candidate *Candidate) error {
		candidate.Providers = next
		candidate.PoolBindings = make(map[string]string)
		for _, connection := range candidate.Providers.Connections {
			if connection.ProxyPoolID != "" {
				candidate.PoolBindings[connection.ID] = connection.ProxyPoolID
			}
		}
		return nil
	}, persist)
	return err
}

// ServingModel resolves aliases and qualified IDs. Ambiguous bare IDs are not
// silently sent to a provider chosen by map iteration order.
func (s *RuntimeSnapshot) ServingModel(requested string) (Model, bool) {
	if target, ok := s.aliases[requested]; ok {
		return s.ResolveModel(target.ProviderID, target.ModelID)
	}
	if provider, model, ok := strings.Cut(requested, "/"); ok {
		if resolved, known := s.ResolveModel(provider, model); known {
			return resolved, true
		}
	}
	found := s.bareModels[requested]
	if found.ProviderID == "" {
		return Model{}, false
	}
	return s.ResolveModel(found.ProviderID, found.ModelID)
}

// ProbeModel keeps native account IDs ahead of aliases, including disabled targets.
func (s *RuntimeSnapshot) ProbeModel(provider, requested string) (string, error) {
	target := ModelRef{ProviderID: provider, ModelID: requested}
	if _, native := s.models[modelKey(provider, requested)]; !native {
		if prefix, id, qualified := strings.Cut(requested, "/"); qualified && prefix == provider {
			_, known := s.models[modelKey(provider, id)]
			_, alias := s.aliases[id]
			if known || alias {
				target.ModelID = id
			}
		}
		if _, native := s.models[modelKey(provider, target.ModelID)]; !native {
			if alias, ok := s.aliases[target.ModelID]; ok {
				target = alias
			}
		}
	}
	if target.ProviderID != provider {
		return "", fmt.Errorf("model resolves to a different provider")
	}
	if _, disabled := s.disabledModels[modelKey(target.ProviderID, target.ModelID)]; disabled {
		return "", fmt.Errorf("model is disabled")
	}
	return target.ModelID, nil
}

func (s *RuntimeSnapshot) ProviderCandidates(requested string) ([]routing.Account, bool) {
	model, ok := s.ServingModel(requested)
	if !ok {
		return nil, false
	}
	return append([]routing.Account(nil), s.providerAccounts[model.ProviderID]...), true
}

// ModelKind derives typed catalog membership without changing durable model metadata.
func (s *RuntimeSnapshot) ModelKind(model Model) string {
	if model.Source == "alias" {
		if resolved, ok := s.ResolveModel(model.ProviderID, model.ID); ok {
			model = resolved
		}
	}
	if model.ProviderID == "typesafe" || (model.ProviderID == "cloudflare-ai" && (model.ID == cloudflare.Clef || model.ID == cloudflare.ClefFlash)) {
		return string(registry.TransportSystemOne)
	}
	for _, node := range s.providerConfig.Nodes {
		if node.ProviderID == model.ProviderID && len(node.Transports) == 1 && node.Transports[0] == string(registry.TransportSystemOne) {
			return string(registry.TransportSystemOne)
		}
	}
	if model.Kind != "" {
		return model.Kind
	}
	return "llm"
}

func (s *RuntimeSnapshot) AccountProvider(requested, account, source string, canTranslate func(string, string) bool) (routing.ProviderRef, bool) {
	model, ok := s.ServingModel(requested)
	if !ok {
		return routing.ProviderRef{}, false
	}
	config, ok := s.providers[account]
	if !ok {
		return routing.ProviderRef{}, false
	}
	// The base is wire.NodeBase's rule: the node's own, else the one the connection
	// names (Azure OpenAI declares none); a connection with neither is not selectable.
	baseURL := wire.NodeBase(config.node.ProviderID, config.node.BaseURL, "", config.metadata.Settings)
	if config.node.ProviderID != model.ProviderID || !config.account.Enabled || len(config.node.Transports) == 0 || baseURL == "" {
		return routing.ProviderRef{}, false
	}
	transports := config.node.Transports
	if forced := wire.ForcedTransport(model.ProviderID, model.ID); forced != "" {
		if !slices.Contains(transports, forced) {
			return routing.ProviderRef{}, false
		}
		transports = []string{forced}
	}
	protocol := transports[0]
	if source != "" {
		if slices.Contains(transports, source) {
			protocol = source
		} else {
			protocol = ""
			for _, target := range transports {
				if canTranslate != nil && canTranslate(source, target) {
					protocol = target
					break
				}
			}
			if protocol == "" {
				return routing.ProviderRef{}, false
			}
		}
	}
	endpoint := config.endpoints[registry.Protocol(protocol)]
	return routing.ProviderRef{Connection: config.metadata, ProviderID: model.ProviderID, ConnectionID: account, BaseURL: baseURL, Protocol: protocol, UpstreamModel: model.ID, Endpoint: endpoint}, true
}

func (s *RuntimeSnapshot) compileServingIndexes() {
	s.providerAccounts = make(map[string][]routing.Account)
	ordered := make([]servingProvider, 0, len(s.providers))
	for _, provider := range s.providers {
		ordered = append(ordered, provider)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].account.Priority != ordered[j].account.Priority {
			return ordered[i].account.Priority < ordered[j].account.Priority
		}
		if ordered[i].name != ordered[j].name {
			return ordered[i].name < ordered[j].name
		}
		return ordered[i].account.ID < ordered[j].account.ID
	})
	for _, provider := range ordered {
		s.providerAccounts[provider.node.ProviderID] = append(s.providerAccounts[provider.node.ProviderID], provider.account)
	}
	s.bareModels = make(map[string]ModelRef)
	for _, model := range s.models {
		if _, disabled := s.disabledModels[modelKey(model.ProviderID, model.ID)]; disabled {
			continue
		}
		if _, exists := s.bareModels[model.ID]; exists {
			s.bareModels[model.ID] = ModelRef{}
		} else {
			s.bareModels[model.ID] = ModelRef{ProviderID: model.ProviderID, ModelID: model.ID}
		}
	}
	s.providerStrategy, _ = routing.ParseStrategy(s.settings["providerStrategy"])
	if s.providerStrategy == "" {
		s.providerStrategy = routing.StrategyFillFirst
	}
	s.providerStrategies = make(map[string]routing.Strategy)
	s.providerSticky = make(map[string]uint64)
	for provider, override := range strategyOverridesTolerant(s.settings["providerStrategies"], false) {
		if override.Strategy != "" {
			if strategy, err := routing.ParseStrategy(override.Strategy); err == nil {
				s.providerStrategies[provider] = strategy
			}
		}
		if override.StickyLimit > 0 {
			s.providerSticky[provider] = override.StickyLimit
		}
	}
	s.comboOverrides = make(map[string]string)
	for name, override := range strategyOverridesTolerant(s.settings["comboStrategies"], true) {
		if override.Strategy != "" {
			if parsed, err := parseComboStrategy(override.Strategy); err == nil {
				s.comboOverrides[name] = parsed
			}
		}
	}
	if value, err := strconv.ParseUint(strings.TrimSpace(s.settings["stickyRoundRobinLimit"]), 10, 64); err == nil {
		s.providerStickyLimit = routing.StickyRotations(value)
	}
}

func (s *RuntimeSnapshot) ProviderStrategy() routing.Strategy { return s.providerStrategy }
func (s *RuntimeSnapshot) ProviderStrategyOverride(provider string) (routing.Strategy, bool) {
	strategy, ok := s.providerStrategies[provider]
	return strategy, ok
}

// ProviderStickyLimitFor resolves the per-provider stickyRoundRobinLimit
// override over the global limit, as the pin does at auth.js:186
// (`override.stickyRoundRobinLimit || settings.stickyRoundRobinLimit || 3`):
// an override of 0 or absent inherits.
func (s *RuntimeSnapshot) ProviderStickyLimitFor(provider string) uint64 {
	if limit, ok := s.providerSticky[provider]; ok {
		return limit
	}
	return s.providerStickyLimit
}
