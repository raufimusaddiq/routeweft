package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

type servingProvider struct {
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
		result[connection.ID] = servingProvider{node: node, endpoints: endpoints, account: routing.Account{ID: connection.ID, Enabled: connection.Enabled, Priority: connection.Priority}, name: connection.Name}
	}
	return result, nil
}

// UpdateProviders uses the same compile/commit/publish lock as other config.
func (m *Manager) UpdateProviders(ctx context.Context, change func(*credentials.Configuration) error, persist func(*sql.Tx) error) error {
	_, err := m.update(ctx, func(candidate *Candidate) error {
		if err := change(&candidate.Providers); err != nil {
			return err
		}
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

func (s *RuntimeSnapshot) ProviderCandidates(requested string) ([]routing.Account, bool) {
	model, ok := s.ServingModel(requested)
	if !ok {
		return nil, false
	}
	return append([]routing.Account(nil), s.providerAccounts[model.ProviderID]...), true
}

func (s *RuntimeSnapshot) AccountProvider(requested, account, source string, canTranslate func(string, string) bool) (routing.ProviderRef, bool) {
	model, ok := s.ServingModel(requested)
	if !ok {
		return routing.ProviderRef{}, false
	}
	config, ok := s.providers[account]
	if !ok || config.node.ProviderID != model.ProviderID || !config.account.Enabled || len(config.node.Transports) == 0 || config.node.BaseURL == "" {
		return routing.ProviderRef{}, false
	}
	protocol := config.node.Transports[0]
	if source != "" {
		if slices.Contains(config.node.Transports, source) {
			protocol = source
		} else {
			protocol = ""
			for _, target := range config.node.Transports {
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
	return routing.ProviderRef{ProviderID: model.ProviderID, ConnectionID: account, BaseURL: config.node.BaseURL, Protocol: protocol, UpstreamModel: model.ID, Endpoint: config.endpoints[registry.Protocol(protocol)]}, true
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
	var overrides map[string]string
	json.Unmarshal([]byte(s.settings["providerStrategies"]), &overrides)
	s.providerStrategies = make(map[string]routing.Strategy)
	for provider, raw := range overrides {
		if strategy, err := routing.ParseStrategy(raw); err == nil {
			s.providerStrategies[provider] = strategy
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
func (s *RuntimeSnapshot) ProviderStickyLimit() uint64 { return s.providerStickyLimit }
