// Pure formatting for the Quota Tracker. The read model sends `remaining` as a
// fraction in [0,1] (SPEC PRD-QUOTA-001); every percentage on the page goes
// through percent() so a raw fraction is never printed as a count.

export type Credit = { currency: string; state: 'positive' | 'zero' | 'negative' | 'missing' | 'malformed'; total?: number; granted?: number; toppedUp?: number }
export type QuotaWindow = { name: string; displayName?: string; remaining?: number; resetAt?: string; unlimited: boolean; credit?: Credit; observedAt?: string }
export type QuotaStatus = 'available' | 'exhausted' | 'cooldown' | 'error' | 'unknown' | 'unlimited' | 'not-reported'
export type QuotaItem = {
  connectionId: string; providerId: string; name: string; authKind?: string; enabled: boolean; status: QuotaStatus | string
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
// windowLabel prefers the provider's own label; name is the stable key (#221).
export function windowLabel(window: QuotaWindow): string { return window.displayName || window.name }

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

/** When a window was served from a reader's cache, the time it was read upstream; null for a fresh window (#240). */
export function cachedAt(window: QuotaWindow): string | null {
  if (!window.observedAt || Number.isNaN(Date.parse(window.observedAt))) return null
  return window.observedAt
}

/** The account's summary limit: the most constrained window, or why there is none. */
export function remainingText(item: QuotaItem): string {
  const value = percent(item.remaining)
  if (value) return `${value} remaining`
  if (item.status === 'unlimited') return 'unlimited'
  if (item.status === 'not-reported') return 'not reported'
  return 'limit not reported'
}

/** One account's quota for the sign-in result (#215), or what to expect when the
 * background read has not reached it yet: a new account usually has none. */
export function quotaLine(item: QuotaItem | undefined): string {
  if (!item || item.status === 'not-reported') return 'Quota not read yet. It is read in the background; use Refresh quota on the Quota page to ask for it now.'
  return [statusLabel(item.status), remainingText(item), item.plan ? `plan ${item.plan}` : ''].filter(Boolean).join(' · ')
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

// Operator actions on the Quota page (#226): the refresh job, and Codex reset
// credits. Pure, so every state the page can show is tested without a browser.

// A refresh request is a job. 202 means accepted; only a terminal job means the
// rows were read again, and even then each row keeps its own observedAt.
export type RefreshStatus = 'queued' | 'running' | 'succeeded' | 'failed'
export type RefreshJob = { id: string; status: RefreshStatus; requestedAt: string; startedAt?: string; finishedAt?: string; error?: string }

/** How often the page asks for the job, and how long before it stops asking. */
export const REFRESH_POLL_MS = 1000
export const REFRESH_POLL_LIMIT = 90

export function refreshTerminal(job: RefreshJob | null | undefined): boolean {
  return job?.status === 'succeeded' || job?.status === 'failed'
}

/** What the page says about the latest refresh; never that rows were updated before the job ends. */
export function refreshText(job: RefreshJob, gaveUp = false): string {
  const at = (value?: string) => zoned(value) ?? 'an unknown time'
  switch (job.status) {
    case 'queued':
      return gaveUp ? 'Quota refresh is still queued. The rows show the previous observations; use Reload later.' : 'Quota refresh queued. It runs in the background and never blocks inference; the rows still show the previous observations.'
    case 'running':
      return gaveUp ? `Quota refresh has been running since ${at(job.startedAt)} and has not finished. The rows show the previous observations; use Reload later.` : `Quota refresh running since ${at(job.startedAt)}. The rows still show the previous observations.`
    case 'succeeded':
      return `Quota refresh finished at ${at(job.finishedAt)}. Rows were reloaded; each shows when it was observed.`
    case 'failed':
      return `Quota refresh finished at ${at(job.finishedAt)} without reading every account: ${job.error || 'a read failed'}. Rows were reloaded; accounts that could not be read show read failed or an older observation.`
  }
}

/** A row the finished refresh did not observe: no observation, or one older than the job's start. */
export function notUpdatedBy(item: QuotaItem, job: RefreshJob | null | undefined): boolean {
  if (!job || !refreshTerminal(job) || !job.startedAt || !item.enabled) return false
  const started = Date.parse(job.startedAt)
  if (Number.isNaN(started)) return false
  if (!item.observedAt) return true
  const observed = Date.parse(item.observedAt)
  return Number.isNaN(observed) || observed < started
}

// Codex reset credits. Spending one is irreversible, so the action is one
// credit, confirmed, and an unknown outcome is never sent again.
export type ResetCredit = { status: string; grantedAt?: string; expiresAt?: string }
export type ResetCredits = { availableCount: number; credits: ResetCredit[] }

/** Soonest expiry first; a credit without one sorts last (pin ProviderLimits/index.js:342-347). */
export function creditsByExpiry(credits: ResetCredit[]): ResetCredit[] {
  const time = (value?: string) => {
    const parsed = value ? Date.parse(value) : Number.NaN
    return Number.isNaN(parsed) ? Number.POSITIVE_INFINITY : parsed
  }
  return [...credits].sort((a, b) => time(a.expiresAt) - time(b.expiresAt))
}

/** A Codex account with a reported credit to spend; an unknown count is not one (pin :1162). */
export function canSpendResetCredit(item: QuotaItem): boolean {
  return item.providerId === 'codex' && typeof item.availableResetCredits === 'number' && item.availableResetCredits > 0
}

/** Why a spend cannot go ahead on this live read of the credit list, or '' when it can. No read is never a go-ahead. */
export function freshCreditCheck(fresh: ResetCredits | null): string {
  if (!fresh) return 'The credit list could not be read from Codex, so nothing was spent. Every spend reads the credits first; try again once they can be read.'
  if (!(fresh.availableCount > 0)) return 'Codex reports no reset credit to use; nothing was spent.'
  return ''
}

/** The confirmation text: one credit, irreversible, and the count before spending (pin :1469). */
export function spendConfirmation(item: QuotaItem): string {
  return `Use 1 Codex reset credit for ${item.name || 'this account'}? This cannot be undone. Remaining credits: ${item.availableResetCredits ?? 0}.`
}

// unknown marks an outcome that may have spent a credit: the page reads the
// credit list again before anything else is offered.
export type SpendOutcome = { tone: 'notice' | 'error'; text: string; reload: boolean; unknown?: boolean }

/** A spend whose request failed in the browser (status 0) or on a server (any 5xx): it may have been spent. */
export function spendOutcomeUnknown(status: number): boolean { return status === 0 || status >= 500 }

/** What a spend response means. Only a 200 is known to have spent a credit; a 5xx or a failed request is unknown and is never retried. */
export function spendOutcome(status: number, body: { windowsReset?: number; error?: { code?: string; message?: string } } | null): SpendOutcome {
  if (status === 200) {
    const windows = typeof body?.windowsReset === 'number' && body.windowsReset > 0 ? ` ${body.windowsReset} window${body.windowsReset === 1 ? '' : 's'} reset.` : ''
    return { tone: 'notice', text: `One Codex reset credit used.${windows} Quota reloaded.`, reload: true }
  }
  if (status === 409) return { tone: 'error', text: 'Codex reports no reset credit to use; nothing was spent.', reload: true }
  if (spendOutcomeUnknown(status)) {
    const how = status === 0 ? 'the request did not complete' : `HTTP ${status}`
    return { tone: 'error', text: `The outcome is unknown (${how}): Codex may or may not have used the credit, and the request was not sent again. The credit list below was read again; check it before trying again. The next spend reads it again first.`, reload: false, unknown: true }
  }
  if (body?.error?.code === 'reauth_required') return { tone: 'error', text: 'Codex rejected the account credential; no reset credit was spent. Reconnect the account.', reload: false }
  if (status === 400 && body?.error?.code === 'confirmation_required') return { tone: 'error', text: 'The reset was not confirmed; nothing was spent.', reload: false }
  if (status >= 400 && status < 500) return { tone: 'error', text: `Codex refused the reset (HTTP ${status}); nothing was spent.`, reload: false }
  return { tone: 'error', text: body?.error?.message || `The reset could not be requested (HTTP ${status}).`, reload: false }
}

// ---- Quota warming opt-in (#297) --------------------------------------------
// The pin shows a per-account toggle in its usage view for OAuth Claude and Codex
// accounts (ProviderLimits/index.js:1193-1200) and keeps the opt-ins in the
// claudeAutoPing / codexAutoPing settings as { connections: { <id>: true } }
// (:57-60,543-555,557-577). Warming is usage-increasing, so it is off for every
// account until someone turns it on (default {"connections":{}}).

export type WarmingProvider = 'claude' | 'codex'
export type WarmingMaps = Record<WarmingProvider, Record<string, boolean>>

export const WARMING_SETTINGS: Record<WarmingProvider, string> = { claude: 'claudeAutoPing', codex: 'codexAutoPing' }

/** The pin's tooltips (index.js:62-65), shown as visible text rather than on hover only. */
export const WARMING_DESCRIPTIONS: Record<WarmingProvider, string> = {
  claude: 'When your 5h quota runs out, auto-sends a request the moment it resets so a new window starts right away.',
  codex: 'Auto-starts the next 5h Codex window after reset by sending a tiny gpt-5.6-terra request. Consumes a small amount of quota.',
}

/** Warming applies to OAuth Claude and Codex accounts only (index.js:1193). */
export function warmingProvider(item: Pick<QuotaItem, 'providerId' | 'authKind'>): WarmingProvider | null {
  return (item.providerId === 'claude' || item.providerId === 'codex') && item.authKind === 'oauth' ? item.providerId : null
}

function connectionsOf(raw: string | undefined): Record<string, boolean> {
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw) as { connections?: unknown }
    const map = parsed?.connections
    if (!map || typeof map !== 'object' || Array.isArray(map)) return {}
    const out: Record<string, boolean> = {}
    for (const [id, on] of Object.entries(map as Record<string, unknown>)) if (on === true) out[id] = true
    return out
  } catch { return {} }
}

/** The opt-in maps out of GET /admin/v1/settings, whose object values are JSON text. Anything unreadable is off. */
export function parseWarming(settings: Record<string, string> | undefined): WarmingMaps {
  return { claude: connectionsOf(settings?.claudeAutoPing), codex: connectionsOf(settings?.codexAutoPing) }
}

/** Turning an account on adds it; turning it off removes the entry, so the stored map holds only opted-in accounts. */
export function withWarming(map: Record<string, boolean>, connectionId: string, on: boolean): Record<string, boolean> {
  const next = { ...map }
  if (on) next[connectionId] = true
  else delete next[connectionId]
  return next
}

/** The PATCH /admin/v1/settings body that stores one provider's opt-ins. */
export function warmingPatch(provider: WarmingProvider, map: Record<string, boolean>): { set: Record<string, string> } {
  return { set: { [WARMING_SETTINGS[provider]]: JSON.stringify({ connections: map }) } }
}

/** Status in words, so it never rides on colour alone. */
export function warmingStatusText(on: boolean, saving: boolean): string {
  return saving ? 'Saving…' : on ? 'Warming on' : 'Warming off'
}
