// Readiness is the build's immutable capability for a provider (SPEC 36). It is
// independent of the account enable toggles and of stored credentials; an absent
// or contradictory value is "unknown" and never rendered as ready.
export type ReadinessStatus = 'ready' | 'experimental' | 'unavailable' | 'unknown'
export type Readiness = { status: ReadinessStatus; served?: boolean; reason?: string }

export const READINESS_LABEL: Record<ReadinessStatus, string> = { ready: 'Ready', experimental: 'Experimental', unavailable: 'Unavailable', unknown: 'Readiness unknown' }

// A status outside the label set, or one whose served flag contradicts it, is
// unknown (#229): the page claims nothing the build did not declare.
export function readinessStatus(provider: { readiness?: Readiness }): ReadinessStatus {
  const readiness = provider.readiness
  if (!readiness || typeof readiness.status !== 'string' || !Object.hasOwn(READINESS_LABEL, readiness.status)) return 'unknown'
  const served = readiness.status === 'ready' || readiness.status === 'experimental'
  if (readiness.served !== undefined && readiness.served !== served) return 'unknown'
  return readiness.status
}

// readinessNote is the provider page's explanation of a non-ready status, or
// undefined for a ready one. Unavailable means no Chat, Responses or Messages
// client is routed to the provider; it does not say every wire is unserved,
// because TypeSafe's System One requests are, and its reason says so.
export function readinessNote(provider: { readiness?: Readiness }): string | undefined {
  const status = readinessStatus(provider)
  if (status === 'ready') return undefined
  const lead = status === 'unknown'
    ? 'This build does not say whether it can serve this provider.'
    : status === 'unavailable'
      ? 'This build does not route Chat, Responses or Messages requests to this provider yet. You can still add and enable accounts, but those requests will not be sent here.'
      : 'This build serves this provider, but it is not yet qualified for production.'
  const reason = provider.readiness?.reason ? ` ${provider.readiness.reason}.` : ''
  return `${lead}${reason} Readiness is set by the build; the Enable and Disable buttons below control accounts only.`
}
