import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ago, badRate, compact, delta, isWeekend, largest, latestWeekTotal, observeSeries, observedAverage, outlier, pct, providerTone, rate, failureStatuses, isFailure, periodDays, periodStart, sameDayLastWeek, utcDay, share, statusTone, topByRequests, weekOverWeek, weekendLighter, type Day, type Summary } from './format.ts'

test('compact keeps small numbers exact and abbreviates large ones', () => {
  assert.equal(compact(950), '950')
  assert.equal(compact(12_345), '12k')
  assert.equal(compact(6_800_000), '6.8M')
  assert.equal(compact(21_000_000), '21M')
  assert.equal(compact(999_499), '999k')
  assert.equal(compact(999_500), '1.0M')
  assert.equal(compact(999_999_999), '1.0B')
  assert.equal(compact(-12_345), '−12k')
})

test('rate and delta never divide by zero', () => {
  assert.equal(rate(5, 0), 0)
  assert.equal(pct(rate(81, 2962)), '2.7%')
  assert.equal(delta(10, 0), null)
  assert.equal(delta(10, undefined), null)
  assert.equal(Math.round((delta(130, 100) ?? 0)), 30)
  assert.equal(Math.round((delta(50, 100) ?? 0)), -50)
})

test('ago reads in seconds, minutes, hours and days', () => {
  const now = Date.parse('2026-10-19T12:00:00Z')
  assert.equal(ago('2026-10-19T11:59:30Z', now), '30 s ago')
  assert.equal(ago('2026-10-19T11:30:00Z', now), '30 min ago')
  assert.equal(ago('2026-10-19T06:00:00Z', now), '6 h ago')
  assert.equal(ago('2026-10-15T12:00:00Z', now), '4 d ago')
  assert.equal(ago('', now), 'never')
  assert.equal(ago('not a date', now), 'unknown')
})

test('statusTone is one classification: success, redirect neutral, client warn, server bad', () => {
  assert.equal(statusTone(200), 'good')
  assert.equal(statusTone('204'), 'good')
  assert.equal(statusTone(302), 'idle')
  assert.equal(statusTone(429), 'warn')
  assert.equal(statusTone('503'), 'bad')
  assert.equal(statusTone('weird'), 'idle')
})

test('outlier ignores low-traffic providers and reports the multiple of the fleet rate', () => {
  const summary: Summary = {
    period: '7d', since: '', series: [], models: [], statuses: [],
    totals: { requests: 1000, errors: 30, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 },
    providers: [
      { key: 'tiny', requests: 10, errors: 9, inputTokens: 0, outputTokens: 0 },
      { key: 'openai', requests: 600, errors: 12, inputTokens: 0, outputTokens: 0 },
      { key: 'deepseek', requests: 200, errors: 14, inputTokens: 0, outputTokens: 0 },
    ],
  }
  const worst = outlier(summary)
  assert.equal(worst?.key, 'deepseek')
  assert.equal(worst?.times.toFixed(1), '2.3')
  assert.equal(outlier({ ...summary, providers: [] }), null)
})

test('badRate flags only clear outliers with enough traffic', () => {
  assert.equal(badRate(14, 200, 3), true)
  assert.equal(badRate(9, 10, 3), false)
  assert.equal(badRate(6, 200, 3), false)
})

test('isWeekend reads the calendar day, not the timezone', () => {
  assert.equal(isWeekend('2026-10-17'), true)
  assert.equal(isWeekend('2026-10-19'), false)
})

const day = (date: string, requests: number): Day => ({ day: date, requests, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 })

test('providerTone follows the same rule as the error figure', () => {
  assert.equal(providerTone(1, 2, 3), 'good')
  assert.equal(providerTone(14, 200, 3), 'bad')
  assert.equal(providerTone(6, 200, 3), 'good')
})

test('share never divides by zero or exceeds 100', () => {
  assert.equal(share(0, 0), 0)
  assert.equal(share(5, 0), 100)
  assert.equal(share(50, 200), 25)
})

test('topByRequests ignores the display order of the list', () => {
  const rows = [{ id: 'gemini', requests: 868 }, { id: 'openai', requests: 2816 }, { id: 'anthropic', requests: 2403 }, { id: 'cohere', requests: 46 }]
  const byName = [...rows].sort((a, b) => a.id.localeCompare(b.id))
  const byAccounts = [...rows].reverse()
  assert.deepEqual(topByRequests(rows, 2).map(row => row.id), ['openai', 'anthropic'])
  assert.deepEqual(topByRequests(byName, 2).map(row => row.id), ['openai', 'anthropic'])
  assert.deepEqual(topByRequests(byAccounts, 2).map(row => row.id), ['openai', 'anthropic'])
  assert.deepEqual(topByRequests([{ id: 'b', requests: 5 }, { id: 'a', requests: 5 }], 1).map(row => row.id), ['a'])
})

test('a failure is any non-2xx response, the same set the API counts as errors', () => {
  const rows = [{ key: '200', requests: 900 }, { key: '302', requests: 50 }, { key: '429', requests: 30 }, { key: '503', requests: 20 }]
  const failures = failureStatuses(rows)
  assert.deepEqual(failures.map(row => row.key), ['302', '429', '503'])
  const failed = failures.reduce((total, row) => total + row.requests, 0)
  assert.equal(failed, 100)
  assert.equal(100 - (failed / 1000) * 100, 90)
  assert.equal(isFailure(204), false)
  assert.equal(isFailure('199'), true)
  assert.equal(isFailure(302), true)
})

test('periodDays counts calendar days, including a quiet history for all time', () => {
  assert.equal(periodDays('24h', [], '2026-10-19'), 1)
  assert.equal(periodDays('7d', [], '2026-10-19'), 7)
  assert.equal(periodDays('30d', [], '2026-10-19'), 30)
  assert.equal(periodDays('today', [], '2026-10-19'), 1)
  assert.equal(periodDays('60d', [], '2026-10-19'), 60)
  assert.equal(periodDays('all', [day('2026-10-10', 5)], '2026-10-19'), 10)
  assert.equal(periodDays('all', [], '2026-10-19'), 1)
  assert.match(utcDay(new Date('2026-10-19T23:30:00Z')), /^2026-10-19$/)
})

test('periodStart matches the API windows: UTC midnight for today, rolling otherwise, none for all time', () => {
  const now = new Date('2026-10-19T15:30:00Z')
  assert.equal(periodStart('today', now), '2026-10-19T00:00:00.000Z')
  assert.equal(periodStart('24h', now), '2026-10-18T15:30:00.000Z')
  assert.equal(periodStart('60d', now), '2026-08-20T15:30:00.000Z')
  assert.equal(periodStart('all', now), null)
})

// ---- observed days ----
// 2026-10-19 is a Monday. `steady(first, today, n)` is a series that starts on `first` and records `n` requests every day to `today`.
const TODAY = '2026-10-19'
const shift = (date: string, offset: number) => { const d = new Date(`${date}T00:00:00Z`); d.setUTCDate(d.getUTCDate() + offset); return d.toISOString().slice(0, 10) }
const steady = (recordedDays: number, requests = 100, today = TODAY): Day[] => Array.from({ length: recordedDays }, (_, i) => day(shift(today, i - (recordedDays - 1)), requests))

test('observeSeries: an empty series has nothing drawn and nothing observed', () => {
  const observed = observeSeries([], 30, TODAY)
  assert.equal(observed.drawn.length, 0)
  assert.equal(observed.days.length, 0)
  assert.equal(observed.firstRecorded, null)
  assert.equal(observedAverage(observed), 0)
  assert.equal(weekOverWeek(observed), null)
  assert.equal(weekendLighter(observed), null)
  assert.equal(sameDayLastWeek(observed)(29), null)
})

test('observeSeries: a series starting yesterday observes yesterday and today, and draws a full window', () => {
  const observed = observeSeries([day(shift(TODAY, -1), 80)], 30, TODAY)
  assert.equal(observed.drawn.length, 30)
  assert.deepEqual(observed.days.map(d => d.day), [shift(TODAY, -1), TODAY])
  assert.deepEqual(observed.days.map(d => d.requests), [80, 0])
  assert.equal(observedAverage(observed), 40)
  assert.equal(weekOverWeek(observed), null)
})

test('observeSeries: a series starting 40 days ago is observed across the whole 30-day window', () => {
  const observed = observeSeries(steady(30), 30, TODAY)
  assert.equal(observed.days.length, 30)
  const longer = observeSeries([day(shift(TODAY, -40), 100), ...steady(30)], 30, TODAY)
  assert.equal(longer.days.length, 30)
  assert.equal(longer.firstRecorded, shift(TODAY, -40))
})

test('a 10-day install at a steady 100 a day makes no week-over-week claim', () => {
  const observed = observeSeries(steady(10), 30, TODAY)
  assert.equal(latestWeekTotal(observed), 700)        // a count: fine
  assert.equal(observedAverage(observed), 100)        // 10 observed days, not 30 drawn
  assert.equal(weekOverWeek(observed), null)          // the old code compared 700 with 300 and printed +133.3%
  assert.equal(sameDayLastWeek(observed)(29), null)   // and drew white ticks at zero
})

test('week-over-week matches hand-computed arithmetic once two full weeks are observed', () => {
  const series = steady(30).map((d, i) => (i >= 23 ? { ...d, requests: 140 } : d))   // last 7 days at 140, the week before at 100
  const observed = observeSeries(series, 30, TODAY)
  const wow = weekOverWeek(observed)
  assert.deepEqual([wow?.thisWeek, wow?.lastWeek], [980, 700])
  assert.equal(wow?.change?.toFixed(1), '40.0')
  assert.equal(sameDayLastWeek(observed)(29), 100)    // today against the same day a week earlier
  assert.equal(sameDayLastWeek(observed)(10), null)   // only the latest 7 days carry a tick
})

test('week-over-week needs exactly 14 observed days: 13 is a dash, 14 is a number', () => {
  assert.equal(weekOverWeek(observeSeries(steady(13), 30, TODAY)), null)
  assert.equal(weekOverWeek(observeSeries(steady(14), 30, TODAY))?.change, 0)
})

// The structural guard: a comparison may not depend on how many zero-filled days precede the first recorded day.
// Drawing the same observed data in a 30-day and a 60-day window changes only the zero-filled prefix, so every
// comparison must return the same answer. Any comparison that divides by `drawn.length` fails this for k < 30.
test('no comparison depends on the length of the zero-filled window', () => {
  for (let recorded = 1; recorded <= 40; recorded += 1) {
    const series = steady(recorded).map((d, i) => ({ ...d, requests: 60 + ((i * 37) % 90) }))   // deterministic uneven traffic
    const short = observeSeries(series, 30, TODAY)
    // the average is the mean of the recorded values in the window, never diluted by zero-filled days before the first record
    const inWindow = series.slice(-30)
    const mean = inWindow.reduce((total, d) => total + d.requests, 0) / inWindow.length
    assert.ok(Math.abs(observedAverage(short) - mean) < 1e-9, `average equals the mean of ${inWindow.length} recorded days`)
    assert.equal(short.days.length, inWindow.length)
    if (recorded > 30) continue
    // while every recorded day fits in both windows, a longer window only adds zero-filled prefix and must change nothing
    const long = observeSeries(series, 60, TODAY)
    assert.equal(observedAverage(short), observedAverage(long), `average, ${recorded} recorded days`)
    assert.equal(weekendLighter(short), weekendLighter(long), `weekend ratio, ${recorded} recorded days`)
    assert.deepEqual(weekOverWeek(short), weekOverWeek(long), `week-over-week, ${recorded} recorded days`)
    assert.equal(sameDayLastWeek(short)(29), sameDayLastWeek(long)(59), `ghost tick, ${recorded} recorded days`)
  }
})

test('weekendLighter needs weekend and weekday days, and ignores days before the first record', () => {
  // 2026-10-17 and 18 are Saturday and Sunday; 19 and 20 are Monday and Tuesday
  assert.equal(weekendLighter(observeSeries([day('2026-10-19', 100), day('2026-10-20', 100)], 30, '2026-10-20')), null)
  const mixed = [day('2026-10-17', 50), day('2026-10-18', 50), day('2026-10-19', 100), day('2026-10-20', 100)]
  assert.equal(weekendLighter(observeSeries(mixed, 30, '2026-10-20')), 50)
  assert.equal(weekendLighter(observeSeries([day('2026-10-17', 150), day('2026-10-19', 100)], 30, '2026-10-19')), 25)  // Sunday is an observed zero: weekend (150+0)/2 = 75 against weekday 100
})

test('observeSeries keeps a day recorded after today instead of dropping it', () => {
  const observed = observeSeries([day('2026-10-20', 9)], 2, TODAY)
  assert.deepEqual(observed.drawn.map(d => d.day), ['2026-10-19', '2026-10-20'])
  assert.equal(observed.days.length, 1)
})

test('largest is the biggest value present, not the first', () => {
  assert.equal(largest([]), 0)
  assert.equal(largest([3, 9, 4]), 9)
})
