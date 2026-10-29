package ingress

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// CandidateResolver returns every configured account candidate for one
// requested model. A false result means the model itself is unknown, which is
// distinct from "known but all accounts are ineligible".
type CandidateResolver func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool)

// AccountProvider resolves one selected account into a concrete upstream
// provider reference.
type AccountProvider func(*runtime.RuntimeSnapshot, string, string, string) (routing.ProviderRef, bool)

// errorBodyPeek bounds how much of an upstream error body is buffered for
// classification before it is relayed to the client unchanged (SPEC §14).
const errorBodyPeek int64 = 32 << 10

// PlanCombo orders the selected members of one Combo for a request that needs
// the hard capabilities in required (nil when it needs none). It follows the
// oracle's combo path: capacity-adapter models are added when no member covers
// every required capability, the whole list is rotated by the Combo's strategy
// (adapter models included), then models that satisfy every required capability
// float ahead, stably (chat.js:104-149, capacityAdapter.js:88-96,
// combo.js:63-82,280-294). A named Combo overrides the direct model route.
//
// access is the caller's key: an adapter model is added by the gateway, not
// chosen by the client, so a restricted key only gets the ones it may call
// directly (keyAccess.js:168-177). The Combo's own members are admitted by the
// Combo's grant.
func (h *Handler) PlanCombo(snapshot *runtime.RuntimeSnapshot, access auth.Entry, name string, required []string) ([]runtime.ComboMember, bool) {
	planned, ok := h.planCombo(snapshot, access, name, required)
	return plainMembers(planned), ok
}

// plannedMember is one ordered model of a plan. Adapter marks a model the gateway
// added; its history is fitted to ContextWindow before it is called.
type plannedMember struct {
	runtime.ComboMember
	Adapter       bool
	ContextWindow int
}

func plainMembers(planned []plannedMember) []runtime.ComboMember {
	members := make([]runtime.ComboMember, 0, len(planned))
	for _, member := range planned {
		members = append(members, member.ComboMember)
	}
	return members
}

func (h *Handler) planCombo(snapshot *runtime.RuntimeSnapshot, access auth.Entry, name string, required []string) ([]plannedMember, bool) {
	combo, ok := snapshot.ComboByName(name)
	if !ok {
		return nil, false
	}
	members := combo.Resolve()
	routed := make([]routing.Member, 0, len(members))
	for _, member := range members {
		routed = append(routed, h.routingMember(snapshot, member.ProviderID, member.ModelID, member.Position))
	}
	strategy := routing.StrategyFillFirst
	switch snapshot.ComboStrategy(name) {
	case "round-robin", "sticky-round-robin":
		// The oracle has one rotating strategy whose step is the sticky limit
		// (combo.js:208-237); a limit of 1 is plain round-robin.
		strategy = routing.StrategyStickyRR
	}
	ordered := h.orderModels(snapshot, access, "combo|"+combo.ID, strategy, snapshot.ComboStickyLimit(name), routed, required)
	return toPlanned(ordered), true
}

// PlanSolo is the oracle's single-model path: a target that lacks a required
// capability may be rescued by an adapter model (chat.js:152-169). The strategy
// is the first required capability's adapter pool (capacityAdapter.js:64-72) and
// the sticky limit is the default of 1. A false result means no adapter applies
// and the model is served as requested.
func (h *Handler) PlanSolo(snapshot *runtime.RuntimeSnapshot, access auth.Entry, providerID, modelID string, required []string) ([]runtime.ComboMember, bool) {
	planned, ok := h.planSolo(snapshot, access, providerID, modelID, required)
	return plainMembers(planned), ok
}

func (h *Handler) planSolo(snapshot *runtime.RuntimeSnapshot, access auth.Entry, providerID, modelID string, required []string) ([]plannedMember, bool) {
	solo := []routing.Member{h.routingMember(snapshot, providerID, modelID, 0)}
	augmented := h.augmentWithAdapters(snapshot, access, solo, required)
	if len(augmented) < 2 {
		return nil, false
	}
	strategy := routing.StrategyFillFirst
	for _, capability := range required {
		config := snapshot.CapacityAdapter(capability)
		if config.Enabled && len(config.Pool) > 0 {
			if config.Strategy() == routing.StrategyRoundRobin {
				strategy = routing.StrategyStickyRR
			}
			break
		}
	}
	ordered := h.orderModels(snapshot, access, "solo|"+providerID+"/"+modelID, strategy, 1, solo, required)
	return toPlanned(ordered), true
}

func toPlanned(ordered []routing.Member) []plannedMember {
	members := make([]plannedMember, 0, len(ordered))
	for index, member := range ordered {
		members = append(members, plannedMember{ComboMember: runtime.ComboMember{ProviderID: member.ProviderID, ModelID: member.ModelID, Position: index, Selected: true}, Adapter: member.Adapter, ContextWindow: member.ContextWindow})
	}
	return members
}

// routingMember resolves a model's capabilities from the same table the oracle
// reads (getCapabilitiesForModel), plus any the catalog entry names itself.
func (h *Handler) routingMember(snapshot *runtime.RuntimeSnapshot, providerID, modelID string, position int) routing.Member {
	if providerID == "" {
		// A nested Combo carries no model capabilities (getCapabilitiesForModel
		// of a bare name, combo.js:68-75), so it sorts with the incapable models.
		return routing.Member{ModelID: modelID, Position: position}
	}
	declared, _, _ := snapshot.ModelCapabilities(providerID, modelID)
	details := snapshot.ModelCapabilityDetails(providerID, modelID)
	have := map[string]bool{}
	for _, capability := range declared {
		have[capability] = true
	}
	have[capVision] = have[capVision] || details.Vision
	have[capPDF] = have[capPDF] || details.PDF
	have[capAudioInput] = have[capAudioInput] || details.AudioInput
	have[capVideoInput] = have[capVideoInput] || details.VideoInput
	var capabilities []string
	for capability := range have {
		if have[capability] {
			capabilities = append(capabilities, capability)
		}
	}
	sort.Strings(capabilities)
	return routing.Member{ProviderID: providerID, ModelID: modelID, Position: position, Capabilities: capabilities, ContextWindow: int(details.ContextWindow)}
}

func hasAll(member routing.Member, required []string) bool {
	for _, capability := range required {
		if !member.Has(capability) {
			return false
		}
	}
	return true
}

// augmentWithAdapters is augmentModelsWithCapacityAdapter
// (capacityAdapter.js:88-96) followed by filterAdapterModels: when no original
// model satisfies every required hard capability, the enabled pools of every
// capability (in the oracle's fixed order, de-duplicated) that do satisfy them
// all and are not already present go first. An enabled empty pool adds nothing.
func (h *Handler) augmentWithAdapters(snapshot *runtime.RuntimeSnapshot, access auth.Entry, models []routing.Member, required []string) []routing.Member {
	if len(required) == 0 || len(models) == 0 {
		return models
	}
	for _, model := range models {
		if hasAll(model, required) {
			return models
		}
	}
	present := make(map[string]bool, len(models))
	for _, model := range models {
		present[model.ProviderID+"/"+model.ModelID] = true
	}
	var pool []routing.Member
	for _, capability := range hardCapabilities {
		config := snapshot.CapacityAdapter(capability)
		if !config.Enabled {
			continue
		}
		for _, ref := range config.Pool {
			key := ref.ProviderID + "/" + ref.ModelID
			if present[key] {
				continue
			}
			present[key] = true
			member := h.routingMember(snapshot, ref.ProviderID, ref.ModelID, 0)
			member.Adapter = true
			if hasAll(member, required) && snapshot.KeyAllows(access, key) {
				pool = append(pool, member)
			}
		}
	}
	if len(pool) == 0 {
		return models
	}
	return append(pool, models...)
}

// orderModels adds adapter models, rotates the list by strategy and floats the
// models that satisfy every required capability (stable, never dropping one).
func (h *Handler) orderModels(snapshot *runtime.RuntimeSnapshot, access auth.Entry, rotationKey string, strategy routing.Strategy, sticky uint64, models []routing.Member, required []string) []routing.Member {
	models = h.augmentWithAdapters(snapshot, access, models, required)
	if len(models) < 2 {
		return models
	}
	rotated := models
	if strategy != routing.StrategyFillFirst && h.opts.State != nil {
		start := h.opts.State.ComboRotation(rotationKey, len(models), sticky)
		rotated = append(append([]routing.Member(nil), models[start:]...), models[:start]...)
	}
	if len(required) == 0 {
		return rotated
	}
	capable := make([]routing.Member, 0, len(rotated))
	rest := make([]routing.Member, 0, len(rotated))
	for _, model := range rotated {
		if hasAll(model, required) {
			capable = append(capable, model)
		} else {
			rest = append(rest, model)
		}
	}
	return append(capable, rest...)
}

// PlanComboRequest selects a Combo route and applies smaller-context trimming
// to opaque message units before the caller dispatches the selected member.
func (h *Handler) PlanComboRequest(snapshot *runtime.RuntimeSnapshot, access auth.Entry, name string, required []string, messages []string, budget routing.ContextBudget) ([]runtime.ComboMember, []string, bool) {
	ordered, ok := h.PlanCombo(snapshot, access, name, required)
	if !ok || len(ordered) == 0 {
		return ordered, append([]string(nil), messages...), ok
	}
	targetContext := 0
	// Any ordered candidate may be attempted on fallback, so trim to the
	// smallest context across the whole planned tier (SPEC §15.4).
	for _, member := range ordered {
		contextWindow := h.routingMember(snapshot, member.ProviderID, member.ModelID, 0).ContextWindow
		if contextWindow <= 0 {
			continue
		}
		if targetContext == 0 || contextWindow < targetContext {
			targetContext = contextWindow
		}
	}
	if targetContext > 0 && (budget.Limit <= 0 || targetContext < budget.Limit) {
		budget.Limit = targetContext
	}
	return ordered, routing.TrimHistory(messages, budget), true
}

// TrimComboHistoryForTarget applies an adapter target's context ceiling to
// opaque messages. Protocol-specific callers provide instruction/tail bounds.
func (h *Handler) TrimComboHistoryForTarget(messages []string, budget routing.ContextBudget, targetContext int) []string {
	if targetContext > 0 && (budget.Limit <= 0 || targetContext < budget.Limit) {
		budget.Limit = targetContext
	}
	return routing.TrimHistory(messages, budget)
}

func (h *Handler) planProviderAttempts(snapshot *runtime.RuntimeSnapshot, providerID, model, source string, compact bool, now time.Time) ([]routing.ProviderRef, bool) {
	if h.opts.Candidates == nil || h.opts.AccountProvider == nil {
		return nil, false
	}
	accounts, ok := h.opts.Candidates(snapshot, model)
	if !ok {
		return nil, false
	}
	state := h.opts.State
	limit := h.StickyLimitFor(snapshot, providerID)
	cursor := uint64(0)
	strategy := h.StrategyFor(snapshot, providerID)
	if state != nil && strategy != routing.StrategyFillFirst {
		cursor = state.NextCursor("provider|" + providerID)
	}
	var translated []routing.Account
	resolved := make(map[string]routing.ProviderRef, len(accounts))
	native := make([]routing.Account, 0, len(accounts))
	for _, account := range accounts {
		provider, ok := h.opts.AccountProvider(snapshot, model, account.ID, source)
		if !ok || provider.Protocol != source && !h.translationAvailable(source, provider.Protocol, compact) {
			continue
		}
		resolved[account.ID] = provider
		if provider.Protocol == source {
			native = append(native, account)
		} else {
			translated = append(translated, account)
		}
	}
	accounts = native
	options := routing.SelectOptions{
		Strategy:    strategy,
		Accounts:    accounts,
		Cursor:      cursor,
		StickyLimit: limit,
		Eligible: func(account routing.Account) bool {
			if state == nil {
				return true
			}
			if _, cooling := state.CooldownUntil(account.ID, now); cooling {
				return false
			}
			if observation, ok := state.Quota(account.ID); ok && observation.Remaining != nil && *observation.Remaining <= 0 && observation.ResetAt.After(now) {
				return false
			}
			return true
		},
	}
	selection := routing.Select(options)
	if len(translated) > 0 {
		// Native-first applies before RR/sticky rotation and the shared wire cap.
		options.Accounts = translated
		selection.Candidates = append(selection.Candidates, routing.Select(options).Candidates...)
	}
	candidates := make([]routing.ProviderRef, 0, min(len(selection.Candidates), maxAccountAttempts))
	for _, account := range selection.Candidates {
		candidates = append(candidates, resolved[account.ID])
		if len(candidates) == maxAccountAttempts {
			break
		}
	}
	return candidates, true
}

func modelProviderID(model string) string {
	if provider, _, ok := strings.Cut(model, "/"); ok {
		return provider
	}
	return model
}

// Strategy returns the configured base strategy, honoring the global strategy
// and per-provider overrides. Invalid configuration falls back to fill-first
// rather than failing the request.
func (h *Handler) Strategy(snapshot *runtime.RuntimeSnapshot) routing.Strategy {
	if h.opts.Strategy != "" {
		return h.opts.Strategy
	}
	return snapshot.ProviderStrategy()
}

// StrategyFor applies providerStrategies for one provider over the base policy.
func (h *Handler) StrategyFor(snapshot *runtime.RuntimeSnapshot, provider string) routing.Strategy {
	if strategy, ok := snapshot.ProviderStrategyOverride(provider); ok {
		return strategy
	}
	return h.Strategy(snapshot)
}

// StickyLimitFor applies the provider's stickyRoundRobinLimit override over the
// base limit (pin: auth.js:186).
func (h *Handler) StickyLimitFor(snapshot *runtime.RuntimeSnapshot, provider string) uint64 {
	if h.opts.StickyLimit != 0 {
		return routing.StickyRotations(h.opts.StickyLimit)
	}
	return snapshot.ProviderStickyLimitFor(provider)
}

// recordClassification updates RuntimeState cooldown visibility from an
// upstream response. It is intentionally memory-first (SPEC §14).
func (h *Handler) recordClassification(account string, classification routing.Classification, now time.Time) {
	if h.opts.State == nil || account == "" {
		return
	}
	deadline := routing.CooldownDeadline(classification, now, routing.DefaultFallbackBackoff)
	if deadline.IsZero() {
		return
	}
	h.opts.State.Cooldown(account, deadline)
}

// classifyResponse is the shared classification entry point for ingress retries.
func classifyResponse(status int, header http.Header) routing.Classification {
	return routing.ClassifyStatus(status, header)
}

// recordUpstreamFailure classifies one non-2xx upstream response using the
// shared provider error mapping and records cooldown visibility in RuntimeState
// (SPEC §14 "cooldown is visible immediately"). It never changes the response
// relayed to the client: only the retry classification is normalized. peek is a
// bounded prefix of the upstream error body.
func (h *Handler) recordUpstreamFailure(provider routing.ProviderRef, status int, header http.Header, peek []byte) {
	if h.opts.State == nil {
		return
	}
	classification := classifyResponse(status, header)
	info := shared.ParseError(provider.Protocol, status, peek)
	if mapped := routing.ClassifyError(info.Kind.String(), status); mapped.Outcome != "" {
		// Keep Retry-After/reset hints from the status path.
		mapped.RetryAfterSeconds = classification.RetryAfterSeconds
		mapped.ResetAt = classification.ResetAt
		classification = mapped
	}
	now := time.Now()
	h.recordClassification(provider.ConnectionID, providerClassification(provider, status, peek, classification, now), now)
}

// providerClassification applies the provider's own reading of its error body to
// the classification the status and the shared error kind produced. It only
// refines Routeweft's cooldown accounting: the status and body relayed to the
// client are never changed.
func providerClassification(provider routing.ProviderRef, status int, body []byte, classification routing.Classification, now time.Time) routing.Classification {
	if hint, ok := wire.ClassifyError(provider.ProviderID, status, body, now); ok {
		classification.Outcome = hint.Outcome
		if hint.ResetAt != "" {
			classification.ResetAt = hint.ResetAt
		}
	}
	return classification
}
