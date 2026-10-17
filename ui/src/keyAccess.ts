// Pure logic for the key access editor. Kept free of React and DOM so it can be
// unit-tested with `node --test` (see keyAccess.test.ts).

export type AccessState = { restricted: boolean; allow: string[] }
export type ChoiceKind = 'combo' | 'alias' | 'model'
export type Choice = { id: string; kind: ChoiceKind; detail: string }

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
export function buildChoices(input: CatalogInput): Choice[] {
  const live = new Set(input.connections.filter(connection => connection.enabled).map(connection => connection.providerId))
  const off = new Set(input.disabled.map(item => modelID(item.providerId, item.modelId)))
  for (const model of input.models) if (model.disabled) off.add(modelID(model.providerId, model.id))
  const models = new Map<string, Choice>()
  const add = (provider: string, model: string) => {
    const id = modelID(provider, model)
    if (live.has(provider) && !off.has(id) && !models.has(id)) models.set(id, { id, kind: 'model', detail: provider })
  }
  for (const model of input.models) add(model.providerId, model.id)
  for (const provider of input.providers) for (const model of provider.staticModels ?? []) add(provider.id, model)

  const aliasTargets = new Map(input.aliases.map(alias => [alias.alias, modelID(alias.providerId, alias.modelId)]))
  const aliases: Choice[] = input.aliases
    .filter(alias => models.has(modelID(alias.providerId, alias.modelId)))
    .map(alias => ({ id: alias.alias, kind: 'alias' as const, detail: `→ ${modelID(alias.providerId, alias.modelId)}` }))
  const reachableMember = (member: { providerId: string; modelId: string }) => {
    const target = aliasTargets.get(member.modelId)
    return models.has(target ?? modelID(member.providerId, member.modelId))
  }
  const combos: Choice[] = input.combos
    .filter(combo => combo.members.some(member => member.selected !== false && reachableMember(member)))
    .map(combo => ({ id: combo.name, kind: 'combo' as const, detail: `${combo.members.filter(member => member.selected !== false).length} members` }))
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
