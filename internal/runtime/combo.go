package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Combo is a compiled ordered route candidate list with strategy metadata.
type Combo struct {
	ID            string
	Name          string
	Strategy      string
	StickyLimit   uint64
	JudgeModel    string
	FusionEnabled bool
	Members       []ComboMember
}

// ComboMember is one ordered Combo route candidate.
type ComboMember struct {
	ProviderID string
	ModelID    string
	Position   int
	Selected   bool
}

// Resolve returns the selected members in position order.
func (c Combo) Resolve() []ComboMember {
	members := make([]ComboMember, 0, len(c.Members))
	for _, member := range c.Members {
		if member.Selected && member.ProviderID != "" && member.ModelID != "" {
			members = append(members, member)
		}
	}
	return members
}

func cloneCombos(combos map[string]Combo) map[string]Combo {
	cloned := make(map[string]Combo, len(combos))
	for key, combo := range combos {
		combo.Members = append([]ComboMember(nil), combo.Members...)
		cloned[key] = combo
	}
	return cloned
}

// compileCombos indexes Combos by name and validates their members.
func compileCombos(combos []Combo, models map[string]Model) (map[string]Combo, error) {
	compiled := make(map[string]Combo, len(combos))
	for _, combo := range combos {
		name := strings.TrimSpace(combo.Name)
		if name == "" {
			return nil, errors.New("combo name is required")
		}
		if strings.TrimSpace(combo.ID) == "" {
			return nil, fmt.Errorf("combo %q: id is required", name)
		}
		if _, exists := compiled[name]; exists {
			return nil, fmt.Errorf("duplicate combo %q", name)
		}
		if _, err := parseComboStrategy(combo.Strategy); err != nil {
			return nil, fmt.Errorf("combo %q: %w", name, err)
		}
		combo.Members = append([]ComboMember(nil), combo.Members...)
		seen := make(map[string]struct{}, len(combo.Members))
		for _, member := range combo.Members {
			key := modelKey(member.ProviderID, member.ModelID)
			if member.ProviderID == "" || member.ModelID == "" {
				return nil, fmt.Errorf("combo %q: member provider and model are required", name)
			}
			if _, ok := models[key]; !ok {
				return nil, fmt.Errorf("combo %q: member %s is not a known model", name, key)
			}
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("combo %q: duplicate member %s", name, key)
			}
			seen[key] = struct{}{}
		}
		if combo.StickyLimit == 0 {
			combo.StickyLimit = 1
		}
		compiled[name] = combo
	}
	return compiled, nil
}

func parseComboStrategy(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", "fallback":
		return "fallback", nil
	case "round-robin":
		return "round-robin", nil
	case "sticky-round-robin":
		return "sticky-round-robin", nil
	default:
		return "", errors.New("unsupported combo strategy")
	}
}

// ComboByName resolves one Combo plus its selected ordered members.
func (s *RuntimeSnapshot) ComboByName(name string) (Combo, bool) {
	if s == nil {
		return Combo{}, false
	}
	combo, ok := s.combos[name]
	if !ok {
		return Combo{}, false
	}
	combo.Members = append([]ComboMember(nil), combo.Members...)
	return combo, true
}

// Combos returns the compiled Combos as a defensive copy.
func (s *RuntimeSnapshot) Combos() []Combo {
	if s == nil {
		return nil
	}
	list := make([]Combo, 0, len(s.combos))
	for _, combo := range s.combos {
		combo.Members = append([]ComboMember(nil), combo.Members...)
		list = append(list, combo)
	}
	return list
}

// ComboStrategy resolves per-name, then per-Combo, then global strategy.
func (s *RuntimeSnapshot) ComboStrategy(name string) string {
	combo, ok := s.ComboByName(name)
	if !ok {
		return "fallback"
	}
	var overrides map[string]string
	_ = json.Unmarshal([]byte(s.settings["comboStrategies"]), &overrides)
	if strategy, ok := overrides[name]; ok {
		if parsed, err := parseComboStrategy(strategy); err == nil {
			return parsed
		}
	}
	if combo.Strategy != "" {
		return combo.Strategy
	}
	if strategy, err := parseComboStrategy(s.settings["comboStrategy"]); err == nil {
		return strategy
	}
	return "fallback"
}

// ComboStickyLimit resolves per-Combo sticky limit, then the global setting.
func (s *RuntimeSnapshot) ComboStickyLimit(name string) uint64 {
	if combo, ok := s.ComboByName(name); ok && combo.StickyLimit > 0 {
		return combo.StickyLimit
	}
	var limit uint64
	if _, err := fmt.Sscan(s.settings["comboStickyRoundRobinLimit"], &limit); err != nil || limit == 0 {
		return 1
	}
	return limit
}

// ModelCapabilities resolves catalog capabilities for one provider/model pair.
func (s *RuntimeSnapshot) ModelCapabilities(provider, model string) ([]string, int, bool) {
	if s == nil {
		return nil, 0, false
	}
	if target, ok := s.aliases[model]; ok && provider == "" {
		provider, model = target.ProviderID, target.ModelID
	}
	entry, ok := s.models[modelKey(provider, model)]
	if !ok {
		return nil, 0, false
	}
	return append([]string(nil), entry.Capabilities...), entry.ContextWindow, true
}
