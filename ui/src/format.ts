// Pure formatting and derivation helpers shared by the editorial pages. No React, so they are unit-tested.

export type Totals = { requests: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number; errors: number; avgDurationMs?: number; avgTtftMs?: number }
export type Day = { day: string; requests: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number }
export type Breakdown = { key: string; requests: number; inputTokens: number; outputTokens: number; errors: number }
export type Summary = { period: string; since: string; totals: Totals; series: Day[]; providers: Breakdown[]; models: Breakdown[]; statuses: Breakdown[] }
export type UsageEvent = { id: number; requestId: string; status: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number; createdAt: string; providerId?: string; modelId?: string; connectionId?: string; apiKeyId?: string; durationMs?: number; ttftMs?: number }

const numberFormat = new Intl.NumberFormat('en-US')
export const num = (value: number) => numberFormat.format(Math.round(value))

export function compact(value: number) {
  const abs = Math.abs(value)
  if (abs >= 1e9) return `${(value / 1e9).toFixed(abs >= 1e10 ? 0 : 1)}B`
  if (abs >= 1e6) return `${(value / 1e6).toFixed(abs >= 1e7 ? 0 : 1)}M`
  if (abs >= 1e4) return `${(value / 1e3).toFixed(0)}k`
  return numberFormat.format(Math.round(value))
}

export const pct = (value: number, digits = 1) => `${value.toFixed(digits)}%`
export const rate = (errors: number, requests: number) => (requests > 0 ? (errors / requests) * 100 : 0)

// Relative change in percent; null when there is nothing to compare against.
export function delta(now: number, before: number | undefined | null) {
  if (before === undefined || before === null || before === 0) return null
  return ((now - before) / before) * 100
}

export function ago(iso: string, now = Date.now()) {
  if (!iso) return 'never'
  const stamp = new Date(iso).getTime()
  if (Number.isNaN(stamp)) return 'unknown'
  const seconds = Math.max(0, Math.round((now - stamp) / 1000))
  if (seconds < 90) return `${seconds} s ago`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min ago`
  if (seconds < 129600) return `${Math.round(seconds / 3600)} h ago`
  return `${Math.round(seconds / 86400)} d ago`
}

export function clock(iso: string) {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  const two = (value: number) => String(value).padStart(2, '0')
  return `${two(date.getHours())}:${two(date.getMinutes())}:${two(date.getSeconds())}`
}

export function statusClass(status: number): 'good' | 'warn' | 'bad' {
  if (status >= 200 && status < 300) return 'good'
  if (status >= 500) return 'bad'
  return 'warn'
}

export function sum(days: Day[], key: keyof Day) {
  return days.reduce((total, day) => total + (day[key] as number), 0)
}

// The 7 days before the latest 7, from a daily series (the summary API has no date-range totals).
export function priorWeek(series: Day[]): Day[] | null {
  return series.length >= 14 ? series.slice(-14, -7) : null
}

// The provider furthest above the fleet error rate, ignoring providers with too little traffic to mean anything.
export function outlier(summary: Summary, minRequests = 50) {
  const fleet = rate(summary.totals.errors, summary.totals.requests)
  const worst = summary.providers
    .filter(provider => provider.requests >= minRequests)
    .map(provider => ({ ...provider, err: rate(provider.errors, provider.requests) }))
    .sort((a, b) => b.err - a.err)[0]
  return worst ? { ...worst, fleet, times: worst.err / Math.max(0.01, fleet) } : null
}

// A provider's error rate is "bad" when it is clearly above the fleet rate, not merely non-zero.
export function badRate(errors: number, requests: number, fleet: number) {
  const value = rate(errors, requests)
  return requests >= 50 && value > fleet * 1.5 && value > 3
}

export function isWeekend(day: string) {
  return [0, 6].includes(new Date(`${day}T12:00:00`).getDay())
}
