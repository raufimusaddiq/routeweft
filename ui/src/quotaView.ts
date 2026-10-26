// Pure formatting for the Quota Tracker. The read model sends `remaining` as a
// fraction in [0,1] (SPEC PRD-QUOTA-001); every percentage on the page goes
// through percent() so a raw fraction is never printed as a count.

export type Credit = { currency: string; state: 'positive' | 'zero' | 'negative' | 'missing' | 'malformed'; total?: number; granted?: number; toppedUp?: number }
export type QuotaWindow = { name: string; remaining?: number; resetAt?: string; unlimited: boolean; credit?: Credit }
export type QuotaStatus = 'available' | 'exhausted' | 'cooldown' | 'error' | 'unknown' | 'unlimited' | 'not-reported'
export type QuotaItem = {
  connectionId: string; providerId: string; name: string; enabled: boolean; status: QuotaStatus | string
  remaining?: number; resetAt?: string; nextResetAt?: string; observedAt?: string; stale?: boolean; cooldownUntil?: string
  plan?: string; windows?: QuotaWindow[]; windowsObservedAt?: string; availableResetCredits?: number; creditAvailable?: boolean; error?: boolean
}

/** A remaining fraction as a whole percentage, the way the pin rounds (utils.js getRemainingPercentage). */
export function percent(fraction: number | undefined): string | null {
  if (typeof fraction !== 'number' || !Number.isFinite(fraction)) return null
  const clamped = Math.min(1, Math.max(0, fraction))
  return `${Math.round(clamped * 100)}%`
}

const statusLabels: Record<string, string> = {
  available: 'available', exhausted: 'exhausted', cooldown: 'cooldown', error: 'read failed',
  unknown: 'limit unknown', unlimited: 'unlimited', 'not-reported': 'not reported',
}

export function statusLabel(status: string): string { return statusLabels[status] ?? status }

export function statusTone(status: string): string {
  if (status === 'available' || status === 'unlimited') return 'state-good'
  if (status === 'exhausted' || status === 'cooldown' || status === 'error') return 'state-warn'
  return ''
}

/** A credit amount in its own currency; never a percentage and never summed. */
export function money(amount: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency, maximumFractionDigits: 2 }).format(amount)
  } catch {
    return `${amount.toFixed(2)} ${currency}`
  }
}

const creditStates: Record<Credit['state'], string> = { positive: '', zero: '', negative: ' (negative)', missing: 'balance not reported', malformed: 'balance unreadable' }

/** One window's value: a percentage, unlimited, a credit balance, or not reported. */
export function windowValue(window: QuotaWindow): string {
  if (window.credit) {
    const { credit } = window
    if (typeof credit.total !== 'number') return `${credit.currency}: ${creditStates[credit.state] || 'balance not reported'}`
    return `${money(credit.total, credit.currency)} credit${creditStates[credit.state]}`
  }
  if (window.unlimited) return 'unlimited'
  const value = percent(window.remaining)
  return value ? `${value} remaining` : 'limit not reported'
}

/** The account's summary limit: the most constrained window, or why there is none. */
export function remainingText(item: QuotaItem): string {
  const value = percent(item.remaining)
  if (value) return `${value} remaining`
  if (item.status === 'unlimited') return 'unlimited'
  if (item.status === 'not-reported') return 'not reported'
  return 'limit not reported'
}

/** An absolute time with an explicit zone, so a reset reads the same to every operator. */
export function zoned(value: string | undefined, timeZone?: string): string | null {
  if (!value) return null
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return null
  return new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', timeZoneName: 'short', timeZone }).format(date)
}

export function until(value: string | undefined, now = Date.now()): string | null {
  if (!value) return null
  const time = Date.parse(value)
  if (Number.isNaN(time)) return null
  const minutes = Math.ceil((time - now) / 60000)
  if (minutes <= 0) return 'passed'
  if (minutes < 60) return `in ${minutes} min`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `in ${hours} h ${minutes % 60} min`
  return `in ${Math.floor(hours / 24)} d ${hours % 24} h`
}

export const QUOTA_SORTS = [
  ['default', 'Provider order'], ['reset', 'Next reset first'], ['exhausted', 'Exhausted first'], ['error', 'Read failures first'],
] as const

export const QUOTA_PAGE_SIZE = 50

export function quotaQuery(page: number, sort: string, provider: string, search: string): string {
  const params = new URLSearchParams({ page: String(page), pageSize: String(QUOTA_PAGE_SIZE), sort })
  if (provider) params.set('provider', provider)
  if (search.trim()) params.set('q', search.trim())
  return params.toString()
}
