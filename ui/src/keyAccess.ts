// Pure logic for the key access editor. Kept free of React and DOM so it can be
// unit-tested with `node --test` (see keyAccess.test.ts).

export type AccessState = { restricted: boolean; allow: string[] }
export type ChoiceKind = 'combo' | 'alias' | 'model'
export type Choice = {
  id: string
  kind: ChoiceKind
  detail: string
  // Model and alias choices carry the pair a Combo member is stored as; for an
  // alias, providerId is the alias target's provider and modelId the alias name.
  providerId?: string
  modelId?: string
  // False when the provider has no enabled connection (only listed with `all`).
  reachable: boolean
}

export type CatalogInput = {
  providers: Array<{ id: string; staticModels?: string[] }>
  connections: Array<{ providerId: string; enabled: boolean }>
  models: Array<{ providerId: string; id: string; disabled?: boolean }>
  disabled: Array<{ providerId: string; modelId: string }>
  aliases: Array<{ alias: string; providerId: string; modelId: string }>
  combos: Array<{ name: string; members: Array<{ providerId: string; modelId: string; selected?: boolean }> }>
}

const modelID = (provider: string, model: string) => `${provider}/${model}`

/**
 * Builds the names a key can be granted, limited to what a client can really
 * reach: the provider needs an enabled connection and the model must not be
 * disabled. Aliases need a reachable target; Combos need a reachable member.
 */
export function buildChoices(input: CatalogInput, options: { all?: boolean } = {}): Choice[] {
  const live = new Set(input.connections.filter(connection => connection.enabled).map(connection => connection.providerId))
  const off = new Set(input.disabled.map(item => modelID(item.providerId, item.modelId)))
  for (const model of input.models) if (model.disabled) off.add(modelID(model.providerId, model.id))
  const models = new Map<string, Choice>()
  const add = (provider: string, model: string) => {
    const id = modelID(provider, model)
    if (off.has(id) || models.has(id)) return
    if (live.has(provider) || options.all) models.set(id, { id, kind: 'model', detail: provider, providerId: provider, modelId: model, reachable: live.has(provider) })
  }
  for (const model of input.models) add(model.providerId, model.id)
  for (const provider of input.providers) for (const model of provider.staticModels ?? []) add(provider.id, model)

  const aliasTargets = new Map(input.aliases.map(alias => [alias.alias, modelID(alias.providerId, alias.modelId)]))
  const aliases: Choice[] = input.aliases
    .filter(alias => models.has(modelID(alias.providerId, alias.modelId)))
    .map(alias => ({ id: alias.alias, kind: 'alias' as const, detail: `→ ${modelID(alias.providerId, alias.modelId)}`, providerId: alias.providerId, modelId: alias.alias, reachable: models.get(modelID(alias.providerId, alias.modelId))?.reachable ?? false }))
  const reachableMember = (member: { providerId: string; modelId: string }) => {
    const target = aliasTargets.get(member.modelId)
    return models.get(target ?? modelID(member.providerId, member.modelId))?.reachable === true
  }
  const combos: Choice[] = input.combos
    .filter(combo => combo.members.some(member => member.selected !== false && reachableMember(member)))
    .map(combo => ({ id: combo.name, kind: 'combo' as const, detail: `${combo.members.filter(member => member.selected !== false).length} members`, reachable: true }))
  const order: Record<ChoiceKind, number> = { combo: 0, alias: 1, model: 2 }
  return [...combos, ...aliases, ...models.values()].sort((a, b) => order[a.kind] - order[b.kind] || a.id.localeCompare(b.id))
}

export function filterChoices(choices: Choice[], term: string): Choice[] {
  const needle = term.trim().toLowerCase()
  return needle ? choices.filter(choice => choice.id.toLowerCase().includes(needle) || choice.detail.toLowerCase().includes(needle)) : choices
}

export function toggleAllow(allow: string[], id: string, on: boolean): string[] {
  const without = allow.filter(item => item !== id)
  return on ? [...without, id].sort() : without
}

/** Entries already granted that are no longer reachable (kept visible so they can be removed). */
export function staleEntries(allow: string[], choices: Choice[]): string[] {
  const known = new Set(choices.map(choice => choice.id))
  return allow.filter(id => !known.has(id))
}

/** The exact body sent to the API: `restricted` is always explicit. */
export function accessBody(state: AccessState): { restricted: boolean; allow: string[] } {
  return state.restricted ? { restricted: true, allow: [...state.allow] } : { restricted: false, allow: [] }
}

export function sameAccess(a: AccessState, b: AccessState): boolean {
  if (a.restricted !== b.restricted) return false
  if (!a.restricted) return true
  return a.allow.length === b.allow.length && [...a.allow].sort().every((id, index) => id === [...b.allow].sort()[index])
}

/** Message for a failed key request that keeps the status and the server's wording. */
export function describeFailure(action: string, status: number, serverMessage: string | undefined): string {
  const detail = serverMessage?.trim()
  if (status === 503) return `${action}: the service is unavailable (503)${detail ? ` — ${detail}` : ''}.`
  return `${action} (${status})${detail ? `: ${detail}` : '.'}`
}

/** Names of keys whose restricted allowlist grants this Combo (an unrestricted key needs no grant). */
export function keysAllowingCombo(name: string, keys: Array<{ name: string; access?: AccessState }>): string[] {
  return keys.filter(key => key.access?.restricted && key.access.allow.includes(name)).map(key => key.name).sort()
}

export type MemberRef = { providerId: string; modelId: string }

/** Resolves a typed picker value (`provider/model` or an alias name) to a Combo member. */
export function memberFromInput(value: string, choices: Choice[]): MemberRef | undefined {
  const needle = value.trim()
  const found = choices.find(choice => choice.kind !== 'combo' && choice.id === needle)
  return found?.providerId && found.modelId ? { providerId: found.providerId, modelId: found.modelId } : undefined
}

/** Members that no longer resolve to a known, non-disabled model or alias. */
export function memberIsKnown(member: MemberRef, choices: Choice[]): boolean {
  return choices.some(choice => choice.kind !== 'combo' && choice.providerId === member.providerId && choice.modelId === member.modelId)
}

export function memberIsReachable(member: MemberRef, choices: Choice[]): boolean {
  return choices.some(choice => choice.kind !== 'combo' && choice.providerId === member.providerId && choice.modelId === member.modelId && choice.reachable)
}
