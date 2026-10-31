// The Usage page's provider topology (#275). The live view (/admin/v1/usage/active) is the oracle's
// getActiveRequests: requests in flight per provider/account/model and a recent ring (usageRepo.js:172-259).
// Pure so node grouping and ordering are unit-tested.
export type BreakdownRow = { key: string; requests: number }
export type ActiveRequest = { model: string; provider: string; account: string; count: number }
export type RecentRequest = { timestamp: string; model: string; provider: string; promptTokens: number; completionTokens: number; status: string }
export type UsageActive = { activeRequests: ActiveRequest[]; recentRequests: RecentRequest[]; errorProvider: string }

export type TopologyNode = { provider: string; active: number; models: string[]; accounts: string[]; error: boolean }

/** One node per provider with anything live, recent, or in the period's breakdown; most active first. */
export function topologyNodes(active: ActiveRequest[], recent: RecentRequest[], providers: BreakdownRow[], errorProvider: string, limit = 8): TopologyNode[] {
  const nodes = new Map<string, TopologyNode>()
  const node = (provider: string) => {
    if (!provider) return null
    let found = nodes.get(provider)
    if (!found) {
      found = { provider, active: 0, models: [], accounts: [], error: provider === errorProvider }
      nodes.set(provider, found)
    }
    return found
  }
  for (const item of active) {
    const found = node(item.provider)
    if (!found) continue
    found.active += item.count
    if (item.model && !found.models.includes(item.model)) found.models.push(item.model)
    if (item.account && !found.accounts.includes(item.account)) found.accounts.push(item.account)
  }
  for (const item of recent) {
    const found = node(item.provider)
    if (found && item.model && !found.models.includes(item.model)) found.models.push(item.model)
  }
  for (const item of providers) node(item.key)
  return [...nodes.values()]
    .sort((a, b) => b.active - a.active || a.provider.localeCompare(b.provider))
    .slice(0, limit)
}

/** The in-flight count across every active row. */
export function inFlight(active: ActiveRequest[]) {
  return active.reduce((sum, item) => sum + item.count, 0)
}
