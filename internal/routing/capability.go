package routing

import "sort"

// Member is one Combo route candidate with resolved capability metadata.
type Member struct {
	ProviderID    string
	ModelID       string
	Position      int
	Capabilities  []string
	ContextWindow int
}

// CapabilityRequirement names a hard capability an active request needs.
type CapabilityRequirement struct {
	Name     string
	Strategy Strategy
	// AdapterEnabled mirrors the per-capability adapter enable flag.
	AdapterEnabled bool
	// AdapterPool is the configured capacity-adapter pool. An enabled empty
	// pool is a deliberate no-op (PRD-COMBO-004).
	AdapterPool []Member
	// AdapterContextWindow is the selected adapter's context ceiling.
	AdapterContextWindow int
}

// Has reports whether a member declares a capability.
func (m Member) Has(capability string) bool {
	for _, candidate := range m.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// OrderCombo stably prioritizes members that satisfy every required hard
// capability, without dropping original fallback candidates (SPEC §15.3).
func OrderCombo(members []Member, requirements []CapabilityRequirement) []Member {
	if len(requirements) == 0 {
		return append([]Member(nil), members...)
	}
	capable := make([]Member, 0, len(members))
	rest := make([]Member, 0, len(members))
	for _, member := range members {
		if satisfiesAll(member, requirements) {
			capable = append(capable, member)
		} else {
			rest = append(rest, member)
		}
	}
	capable = append(capable, rest...)
	return capable
}

func satisfiesAll(member Member, requirements []CapabilityRequirement) bool {
	for _, requirement := range requirements {
		if !member.Has(requirement.Name) {
			return false
		}
	}
	return true
}

// AdapterCandidates returns eligible capacity-adapter members to prepend when
// no original candidate satisfies a required capability. Disabled adapters and
// empty pools yield no candidates, preserving the no-op contract.
func AdapterCandidates(requirements []CapabilityRequirement, stratify func([]Member) []Member) []Member {
	var adapters []Member
	for _, requirement := range requirements {
		if !requirement.AdapterEnabled || len(requirement.AdapterPool) == 0 {
			continue
		}
		for _, member := range requirement.AdapterPool {
			if member.Has(requirement.Name) {
				adapters = append(adapters, member)
			}
		}
	}
	if len(adapters) == 0 {
		return nil
	}
	if stratify != nil {
		return stratify(adapters)
	}
	// Adapter ordering is local to the adapter tier; caller prepends this
	// result without mixing it into original Combo RR state.
	return OrderAdapters(adapters, requirements)
}

// OrderAdapters groups candidates by required capability and applies that
// capability's fallback/RR strategy independently.
func OrderAdapters(adapters []Member, requirements []CapabilityRequirement) []Member {
	ordered := make([]Member, 0, len(adapters))
	for _, requirement := range requirements {
		var group []Member
		for _, member := range adapters {
			if member.Has(requirement.Name) {
				group = append(group, member)
			}
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].Position < group[j].Position })
		if (requirement.Strategy == StrategyRoundRobin || requirement.Strategy == StrategyStickyRR) && len(group) > 1 {
			group = append(group[1:], group[:1]...)
		}
		ordered = append(ordered, group...)
	}
	return ordered
}

// ContextBudget carries the instruction head, active tail and target context
// limit needed for adapter history trimming.
type ContextBudget struct {
	Head, Tail, Limit int
}

// TrimHistory applies a context budget using opaque message units.
func TrimHistory(messages []string, budget ContextBudget) []string {
	return TrimHistoryForContext(messages, budget.Head, budget.Tail, budget.Limit)
}

// TrimHistoryForContext keeps the instruction head and the active tail, dropping
// older middle turns first when an adapter target has a smaller context
// (SPEC §15.4). Messages are opaque units; callers keep protocol semantics.
func TrimHistoryForContext(messages []string, head, tail, limit int) []string {
	if limit <= 0 || len(messages) <= limit {
		return append([]string(nil), messages...)
	}
	head = min(max(head, 0), len(messages))
	tail = min(max(tail, 0), len(messages)-head)
	if head+tail > limit {
		// Preserve the active tail when both protected regions cannot fit.
		head = limit - min(tail, limit)
	}
	keepTail := min(tail, limit-head)
	middle := min(limit-head-keepTail, len(messages)-head-tail)
	result := append([]string(nil), messages[:head]...)
	result = append(result, messages[head:head+middle]...)
	return append(result, messages[len(messages)-keepTail:]...)
}
