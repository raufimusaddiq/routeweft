package runtime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Combo is a compiled ordered route candidate list with strategy metadata.
type Combo struct {
	ID            string
	Name          string
	Strategy      string
	StickyLimit   uint64
	JudgeModel    string
	FusionEnabled bool
	// FusionTuning overrides the default panel timing for this Combo only
	// (v1 comboStrategies[name].fusionTuning). The zero value keeps the default.
	FusionTuning routing.FusionTuning
	Members      []ComboMember
}

// ComboMember is one ordered Combo route candidate. A member with no provider
// names another Combo (a nested Combo, served by its own strategy when reached,
// open-sse chat.js:177-229); every other member is a provider/model pair.
type ComboMember struct {
	ProviderID string
	ModelID    string
	Position   int
	Selected   bool
}

// IsCombo reports whether the member names a nested Combo.
func (m ComboMember) IsCombo() bool { return m.ProviderID == "" && m.ModelID != "" }

// MaxComboDepth bounds how deeply Combos may nest. The oracle has no bound and
// recurses until the process fails on a cycle; a cycle is rejected when the
// Combos are compiled and this limit also bounds legitimate chains.
const MaxComboDepth = 8

// Resolve returns the selected members in position order.
func (c Combo) Resolve() []ComboMember {
	members := make([]ComboMember, 0, len(c.Members))
	for _, member := range c.Members {
		if member.Selected && member.ModelID != "" && (member.ProviderID != "" || member.IsCombo()) {
			members = append(members, member)
		}
	}
	return members
}

// FusionConfig resolves this Combo's panel timing over base for its selected
// members, the way v1 resolves fusionTuning over FUSION_DEFAULTS.
func (c Combo) FusionConfig(base routing.FusionConfig) (routing.FusionConfig, error) {
	return c.FusionTuning.Apply(base, len(c.Resolve()))
}

func cloneCombos(combos map[string]Combo) map[string]Combo {
	cloned := make(map[string]Combo, len(combos))
	for key, combo := range combos {
		combo.Members = append([]ComboMember(nil), combo.Members...)
		combo.FusionTuning = combo.FusionTuning.Clone()
		cloned[key] = combo
	}
	return cloned
}

// compileCombos indexes Combos by name and validates their members. Alias
// members are canonicalized to their target provider/model so capability
// lookups and dispatch resolve uniformly (PRD-COMBO-001).
func compileCombos(combos []Combo, models map[string]Model, aliases map[string]ModelRef) (map[string]Combo, error) {
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
		for i := range combo.Members {
			member := &combo.Members[i]
			member.ProviderID = strings.TrimSpace(member.ProviderID)
			member.ModelID = strings.TrimSpace(member.ModelID)
			// An alias member resolves to its target provider/model; capability
			// and dispatch lookups then work uniformly.
			if target, ok := aliases[member.ModelID]; ok && (member.ProviderID == "" || member.ProviderID == target.ProviderID) {
				member.ProviderID, member.ModelID = target.ProviderID, target.ModelID
			}
		}
		seen := make(map[string]struct{}, len(combo.Members))
		for _, member := range combo.Members {
			key := modelKey(member.ProviderID, member.ModelID)
			if member.ModelID == "" {
				return nil, fmt.Errorf("combo %q: member provider and model are required", name)
			}
			if member.ProviderID == "" {
				// A nested Combo; the names are checked against each other below.
				key = "combo\x00" + member.ModelID
				if _, duplicate := seen[key]; duplicate {
					return nil, fmt.Errorf("combo %q: duplicate member %s", name, member.ModelID)
				}
				seen[key] = struct{}{}
				continue
			}
			if _, ok := models[key]; !ok {
				return nil, fmt.Errorf("combo %q: member %s is not a known model", name, key)
			}
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("combo %q: duplicate member %s", name, key)
			}
			seen[key] = struct{}{}
		}
		if err := combo.FusionTuning.Validate(); err != nil {
			return nil, fmt.Errorf("combo %q: %w", name, err)
		}
		// StickyLimit 0 means "inherit comboStickyRoundRobinLimit"; defaulting it
		// to 1 here would shadow the global setting the pin applies to every
		// combo (src/sse/handlers/chat.js:136).
		compiled[name] = combo
	}
	if err := checkNestedCombos(compiled); err != nil {
		return nil, err
	}
	return compiled, nil
}

// checkNestedCombos requires every nested member to name a Combo, forbids a
// Combo from reaching itself and bounds the nesting depth.
func checkNestedCombos(combos map[string]Combo) error {
	state := make(map[string]int, len(combos)) // 1 = on the current path, 2 = done
	var visit func(name string, depth int) error
	visit = func(name string, depth int) error {
		if depth > MaxComboDepth {
			return fmt.Errorf("combo %q: nested Combos are deeper than %d", name, MaxComboDepth)
		}
		state[name] = 1
		for _, member := range combos[name].Members {
			if !member.IsCombo() {
				continue
			}
			inner, ok := combos[member.ModelID]
			if !ok {
				return fmt.Errorf("combo %q: member %s is not a known model or Combo", name, member.ModelID)
			}
			switch state[inner.Name] {
			case 1:
				return fmt.Errorf("combo %q: nested Combo %q forms a cycle", name, inner.Name)
			case 0:
				if err := visit(inner.Name, depth+1); err != nil {
					return err
				}
			}
		}
		state[name] = 2
		return nil
	}
	names := make([]string, 0, len(combos))
	for name := range combos {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if state[name] == 0 {
			if err := visit(name, 1); err != nil {
				return err
			}
		}
	}
	return nil
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
	combo.FusionTuning = combo.FusionTuning.Clone()
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
		combo.FusionTuning = combo.FusionTuning.Clone()
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
	if strategy, ok := s.comboOverrides[name]; ok {
		return strategy
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
