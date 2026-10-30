// Combo members the Combos page can now offer (#278, U09). Pure and DOM-free so node can test it.
//
// A member with no provider and a model id names another Combo (a nested Combo, served by its own
// strategy when reached; the oracle's ModelSelectModal lists combos beside models, and a card
// aggregates their capabilities: src/shared/components/ModelSelectModal.js:120-128,411-417,
// src/app/(dashboard)/dashboard/combos/page.js:309-331). The server refuses a Combo that reaches
// itself, names no Combo, or nests deeper than MAX_COMBO_DEPTH (internal/runtime/combo.go:218-262);
// nestedProblem repeats that rule so the picker only offers what will save.

export type MemberRef = { providerId: string; modelId: string; selected?: boolean }
export type ComboRefs = { name: string; members: MemberRef[] }

/** internal/runtime/combo.go MaxComboDepth: the oracle has no bound and recurses until it fails on a cycle. */
export const MAX_COMBO_DEPTH = 8

/** Capabilities a Combo is described by: the four a capacity adapter can supply (open-sse/providers/capabilities.js:595-625 ORs them across members). */
export const COMBO_CAPABILITIES = ['vision', 'pdf', 'audio-input', 'video-input'] as const

export function isNestedMember(member: Pick<MemberRef, 'providerId' | 'modelId'>): boolean {
  return member.providerId === '' && member.modelId !== ''
}

/**
 * Why this set of Combos cannot be saved, or undefined. Mirrors checkNestedCombos: every nested member must
 * name a Combo, a Combo may not reach itself, and the longest chain below a Combo (counting itself) may not
 * exceed MAX_COMBO_DEPTH.
 */
export function nestedProblem(combos: ComboRefs[]): string | undefined {
  const byName = new Map(combos.map(combo => [combo.name, combo]))
  const height = new Map<string, number>()
  const onPath = new Set<string>()
  const visit = (name: string): number | string => {
    const known = height.get(name)
    if (known !== undefined) return known
    if (onPath.has(name)) return `Combo “${name}” would reach itself`
    onPath.add(name)
    let deepest = 0
    for (const member of byName.get(name)?.members ?? []) {
      if (!isNestedMember(member)) continue
      if (!byName.has(member.modelId)) return `Combo “${name}”: “${member.modelId}” is not a known model or Combo`
      const inner = visit(member.modelId)
      if (typeof inner === 'string') return inner
      deepest = Math.max(deepest, inner)
    }
    onPath.delete(name)
    height.set(name, deepest + 1)
    return deepest + 1 > MAX_COMBO_DEPTH ? `Combo “${name}” would nest deeper than ${MAX_COMBO_DEPTH} Combos` : deepest + 1
  }
  for (const name of [...byName.keys()].sort()) {
    const result = visit(name)
    if (typeof result === 'string') return result
  }
  return undefined
}

/** Replace (or add) the Combo being edited with its draft members, leaving every other Combo as saved. */
function withDraft(editing: string, draft: MemberRef[], combos: ComboRefs[]): ComboRefs[] {
  return [...combos.filter(combo => combo.name !== editing), { name: editing, members: draft }]
}

/**
 * The Combos that can be added to the one being edited right now: not itself, not already a member, and
 * (judged on the whole set with the draft in place of the saved version) not forming a cycle or going too deep.
 */
export function nestedCandidates(editing: string, draft: MemberRef[], combos: ComboRefs[]): string[] {
  const name = editing.trim()
  const taken = new Set(draft.filter(isNestedMember).map(member => member.modelId))
  return combos
    .map(combo => combo.name)
    .filter(candidate => candidate !== name && !taken.has(candidate))
    .filter(candidate => nestedProblem(withDraft(name, [...draft, { providerId: '', modelId: candidate }], combos)) === undefined)
    .sort()
}

/** Why a typed name cannot be added as a nested Combo, in words for the member error line. */
export function nestedRefusal(value: string, editing: string, draft: MemberRef[], combos: ComboRefs[]): string {
  const needle = value.trim()
  const name = editing.trim()
  if (!combos.some(combo => combo.name === needle)) return `“${needle}” is not a known model, alias or Combo. Pick one from the list.`
  if (needle === name) return 'A Combo cannot contain itself.'
  if (draft.some(member => isNestedMember(member) && member.modelId === needle)) return 'That member is already in this Combo.'
  return nestedProblem(withDraft(name, [...draft, { providerId: '', modelId: needle }], combos)) ?? 'That Combo cannot be added.'
}

/** What a member shows as: the provider and model, or the nested Combo's name. */
export function memberLabel(member: Pick<MemberRef, 'providerId' | 'modelId'>): string {
  return isNestedMember(member) ? member.modelId : `${member.providerId} / ${member.modelId}`
}

export type CapabilityCatalog = {
  /** `provider/model` to the capability names the model supports. */
  models: Map<string, string[]>
  /** An alias name to the model it points at. */
  aliases: Map<string, { providerId: string; modelId: string }>
  combos: Map<string, ComboRefs>
}

/**
 * The capabilities a Combo can satisfy: the union over its selected members, a nested Combo contributing its own.
 * A member that is deselected, unknown, or deeper than MAX_COMBO_DEPTH contributes nothing.
 */
export function aggregateCapabilities(combo: ComboRefs, catalog: CapabilityCatalog, depth = 0, seen: Set<string> = new Set()): string[] {
  if (depth >= MAX_COMBO_DEPTH || seen.has(combo.name)) return []
  const found = new Set<string>()
  const path = new Set(seen).add(combo.name)
  for (const member of combo.members) {
    if (member.selected === false) continue
    if (isNestedMember(member)) {
      const inner = catalog.combos.get(member.modelId)
      if (inner) for (const capability of aggregateCapabilities(inner, catalog, depth + 1, path)) found.add(capability)
      continue
    }
    const target = catalog.aliases.get(member.modelId)
    const key = target && (member.providerId === '' || member.providerId === target.providerId) ? `${target.providerId}/${target.modelId}` : `${member.providerId}/${member.modelId}`
    for (const capability of catalog.models.get(key) ?? []) found.add(capability)
  }
  return (COMBO_CAPABILITIES as readonly string[]).filter(capability => found.has(capability))
}

/** Per-Combo Fusion timing overrides as typed: a blank field keeps the default. */
export type FusionTuningInput = { minPanel: string; stragglerGraceMs: string; panelHardTimeoutMs: string }
export type FusionTuning = { minPanel?: number; stragglerGraceMs?: number; panelHardTimeoutMs?: number }

/** v1 FUSION_DEFAULTS (open-sse/services/combo.js:472-476). */
export const FUSION_DEFAULTS = { minPanel: 2, stragglerGraceMs: 8000, panelHardTimeoutMs: 90000 } as const
/** internal/routing/fusion_tuning.go MaxFusionTuningMs: SPEC 16 caps the panel timing at ten minutes. */
export const MAX_FUSION_TUNING_MS = 10 * 60 * 1000

export const BLANK_TUNING: FusionTuningInput = { minPanel: '', stragglerGraceMs: '', panelHardTimeoutMs: '' }

export function tuningInput(tuning: FusionTuning | undefined): FusionTuningInput {
  return {
    minPanel: tuning?.minPanel === undefined ? '' : String(tuning.minPanel),
    stragglerGraceMs: tuning?.stragglerGraceMs === undefined ? '' : String(tuning.stragglerGraceMs),
    panelHardTimeoutMs: tuning?.panelHardTimeoutMs === undefined ? '' : String(tuning.panelHardTimeoutMs),
  }
}

const whole = (text: string) => /^\d+$/.test(text.trim())

/**
 * The tuning to send and the first thing wrong with it. A blank field is omitted (the default applies); the rules are
 * the server's (fusion_tuning.go:33-46): minPanel a whole number (below 2 still means 2), grace 0..10 minutes,
 * hard timeout 1 ms..10 minutes.
 */
export function tuningBody(input: FusionTuningInput): { tuning?: FusionTuning; problem?: string } {
  const tuning: FusionTuning = {}
  if (input.minPanel.trim() !== '') {
    if (!whole(input.minPanel)) return { problem: 'Minimum panel must be a whole number.' }
    tuning.minPanel = Number(input.minPanel)
  }
  if (input.stragglerGraceMs.trim() !== '') {
    if (!whole(input.stragglerGraceMs) || Number(input.stragglerGraceMs) > MAX_FUSION_TUNING_MS) return { problem: `Straggler grace must be a whole number of milliseconds from 0 to ${MAX_FUSION_TUNING_MS}.` }
    tuning.stragglerGraceMs = Number(input.stragglerGraceMs)
  }
  if (input.panelHardTimeoutMs.trim() !== '') {
    if (!whole(input.panelHardTimeoutMs) || Number(input.panelHardTimeoutMs) < 1 || Number(input.panelHardTimeoutMs) > MAX_FUSION_TUNING_MS) return { problem: `Panel hard timeout must be a whole number of milliseconds from 1 to ${MAX_FUSION_TUNING_MS}.` }
    tuning.panelHardTimeoutMs = Number(input.panelHardTimeoutMs)
  }
  return Object.keys(tuning).length > 0 ? { tuning } : {}
}
