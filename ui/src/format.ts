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
  if (abs < 1e4) return numberFormat.format(Math.round(value))
  const units: [string, number][] = [['B', 1e9], ['M', 1e6], ['k', 1e3]]
  const render = (suffix: string, divisor: number) => {
    const scaled = abs / divisor
    const text = scaled.toFixed(scaled >= 10 ? 0 : 1)
    return { text, rounded: Number(text), suffix }
  }
  let index = units.findIndex(([, divisor]) => abs >= divisor)
  let result = render(units[index][0], units[index][1])
  // 999,500 rounds to 1000k: promote to the next unit instead of printing it.
  if (result.rounded >= 1000 && index > 0) { index -= 1; result = render(units[index][0], units[index][1]) }
  return `${value < 0 ? '−' : ''}${result.text}${result.suffix}`
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

// One classification for every status mark and chart segment: success, redirect (neutral), client error, server error.
export type Tone = 'good' | 'warn' | 'bad' | 'idle'
export function statusTone(status: number | string): Tone {
  const code = typeof status === 'number' ? status : Number.parseInt(status, 10)
  if (code >= 200 && code < 300) return 'good'
  if (code >= 400 && code < 500) return 'warn'
  if (code >= 500 && code < 600) return 'bad'
  return 'idle'
}

export function sum(days: Day[], key: keyof Day) {
  return days.reduce((total, day) => total + (day[key] as number), 0)
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

// The provider mark and the error figure share one rule so they cannot disagree.
export function providerTone(errors: number, requests: number, fleet: number): Tone {
  return badRate(errors, requests, fleet) ? 'bad' : 'good'
}

// Width of a share bar in percent, safe when the largest value is zero.
export function share(value: number, largest: number) {
  return Math.min(100, (value / Math.max(1, largest)) * 100)
}

// The busiest providers by requests, regardless of how the list on screen is sorted.
export function topByRequests<T extends { id: string; requests: number }>(rows: T[], count: number) {
  return [...rows].sort((a, b) => b.requests - a.requests || a.id.localeCompare(b.id)).slice(0, count)
}

// A failure is any response that is not a 2xx, exactly the API's definition of `errors`
// (internal/control/api/usage_summary.go: status < 200 OR status >= 300), so class breakdowns and the totals agree.
// Redirects therefore count as failures here even though their mark is neutral (see statusTone).
export function isFailure(status: number | string) {
  const code = typeof status === 'number' ? status : Number.parseInt(status, 10)
  return !(code >= 200 && code < 300)
}

export function failureStatuses<T extends { key: string; requests: number }>(statuses: T[]) {
  return statuses.filter(item => isFailure(item.key)).sort((a, b) => b.requests - a.requests)
}

// ---- calendar days ----
// The daily series is GROUP BY day over usage_daily, so a day without attributed 2xx traffic is absent, not zero.
// Windows (this week, the week before, averages) must be calendar windows, so absent days are filled with zeros.
export const utcDay = (date = new Date()) => date.toISOString().slice(0, 10)

function shiftDay(day: string, offset: number) {
  const date = new Date(`${day}T00:00:00Z`)
  date.setUTCDate(date.getUTCDate() + offset)
  return date.toISOString().slice(0, 10)
}

// Whole calendar days a period covers, ending today (inclusive). 'all' runs from the first recorded day.
export function periodDays(period: string, series: Day[], today: string) {
  if (period === '24h') return 1
  if (period === '7d') return 7
  if (period === '30d') return 30
  if (series.length === 0) return 1
  const first = new Date(`${series[0].day}T00:00:00Z`).getTime()
  const last = new Date(`${today}T00:00:00Z`).getTime()
  return Math.max(1, Math.round((last - first) / 86400000) + 1)
}

// Internal: `count` consecutive days ending on `endDay` (or on the latest recorded day, if that is later), zero-filled.
function denseDays(series: Day[], count: number, endDay: string): Day[] {
  const byDay = new Map(series.map(day => [day.day, day]))
  const latest = series.reduce((best, day) => (day.day > best ? day.day : best), endDay)
  return Array.from({ length: count }, (_, index) => {
    const day = shiftDay(latest, index - (count - 1))
    return byDay.get(day) ?? { day, requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 }
  })
}

// ---- observed days ----
// The API omits a day with no attributed 2xx traffic, so the series cannot tell "nothing happened" from "we were not
// recording yet". Anything that COMPARES days (averages, week-over-week, ghost ticks, weekend ratios) must therefore see
// only observed days: from the first recorded day on, where a missing day really is zero. This is the only way to
// build an `Observed`, and every comparison below takes one and nothing else, so a comparison over zero-filled days
// that predate the install cannot be written without the compiler objecting (see format.typecheck.ts).
declare const observedBrand: unique symbol
export type Observed = {
  readonly [observedBrand]: true
  /** A calendar window for DRAWING: zero-filled, so the axis is continuous. Never divide by its length. */
  readonly drawn: readonly Day[]
  /** The days at or after the first recorded day: the only days a comparison may use. */
  readonly days: readonly Day[]
  readonly firstRecorded: string | null
}

export function observeSeries(series: Day[], count: number, today: string): Observed {
  const firstRecorded = series.reduce<string | null>((first, day) => (first === null || day.day < first ? day.day : first), null)
  const drawn = series.length > 0 ? denseDays(series, count, today) : []
  const days = firstRecorded === null ? [] : drawn.filter(day => day.day >= firstRecorded)
  return { drawn, days, firstRecorded } as unknown as Observed
}

// Mean requests per observed day; 0 when nothing is observed.
export function observedAverage(observed: Observed) {
  return observed.days.length > 0 ? sum(observed.days as Day[], 'requests') / observed.days.length : 0
}

// Total over the latest 7 drawn days. A count, not a comparison, so zero-filled days are fine here.
export function latestWeekTotal(observed: Observed) {
  return sum(observed.drawn.slice(-7) as Day[], 'requests')
}

// Week-over-week needs two full observed weeks. `days` is the tail of `drawn` from the first recorded day, so
// 14 observed days means the latest 14 drawn days are all observed. Otherwise null, and the caller falls back to a plain count.
export function weekOverWeek(observed: Observed) {
  if (observed.days.length < 14) return null
  const last14 = observed.drawn.slice(-14)
  const lastWeek = sum(last14.slice(0, 7) as Day[], 'requests')
  const thisWeek = sum(last14.slice(7) as Day[], 'requests')
  return { thisWeek, lastWeek, change: delta(thisWeek, lastWeek) }
}

// The same day a week earlier, for the latest 7 drawn days; only when week-over-week itself is defined.
export function sameDayLastWeek(observed: Observed): (index: number) => number | null {
  const comparable = weekOverWeek(observed) !== null
  const length = observed.drawn.length
  return index => (comparable && index >= length - 7 ? observed.drawn[index - 7].requests : null)
}

// How much lighter weekend days run than weekdays, in percent. null unless the observed days include both kinds
// and weekdays have traffic: a ratio needs both sides, so a young install shows a dash, not a claim.
export function weekendLighter(observed: Observed) {
  const weekend = observed.days.filter(day => isWeekend(day.day))
  const weekday = observed.days.filter(day => !isWeekend(day.day))
  if (weekend.length === 0 || weekday.length === 0) return null
  const average = (rows: readonly Day[]) => rows.reduce((total, day) => total + day.requests, 0) / rows.length
  const base = average(weekday)
  return base > 0 ? 100 - (average(weekend) / base) * 100 : null
}

// One rule for every share bar: the baseline is the largest value actually present, never an assumed first row.
export function largest(values: number[]) {
  return values.reduce((best, value) => (value > best ? value : best), 0)
}
