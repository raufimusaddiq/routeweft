import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ago, badRate, compact, delta, isWeekend, outlier, pct, priorWeek, providerTone, rate, failureStatuses, share, statusTone, topByRequests, weekendLighter, type Day, type Summary } from './format.ts'

test('compact keeps small numbers exact and abbreviates large ones', () => {
  assert.equal(compact(950), '950')
  assert.equal(compact(12_345), '12k')
  assert.equal(compact(6_800_000), '6.8M')
  assert.equal(compact(21_000_000), '21M')
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

test('priorWeek needs two full weeks of days', () => {
  const day = (n: number) => ({ day: `2026-10-${String(n).padStart(2, '0')}`, requests: n, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 })
  assert.equal(priorWeek([1, 2, 3].map(day)), null)
  const fourteen = Array.from({ length: 14 }, (_, i) => day(i + 1))
  assert.deepEqual(priorWeek(fourteen)?.map(d => d.requests), [1, 2, 3, 4, 5, 6, 7])
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

test('weekendLighter is a ratio of two groups, so it needs both', () => {
  // 2026-10-17 and 18 are Saturday and Sunday; 19 and 20 are Monday and Tuesday
  assert.equal(weekendLighter([day('2026-10-19', 100), day('2026-10-20', 100)]), null)
  assert.equal(weekendLighter([day('2026-10-17', 50), day('2026-10-18', 50)]), null)
  assert.equal(weekendLighter([day('2026-10-17', 0), day('2026-10-19', 0)]), null)
  assert.equal(weekendLighter([day('2026-10-17', 50), day('2026-10-18', 50), day('2026-10-19', 100), day('2026-10-20', 100)]), 50)
  assert.equal(weekendLighter([day('2026-10-17', 150), day('2026-10-19', 100)]), -50)
})

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

test('failureStatuses counts client and server errors, not redirects', () => {
  const rows = [{ key: '200', requests: 900 }, { key: '302', requests: 50 }, { key: '429', requests: 30 }, { key: '503', requests: 20 }]
  assert.deepEqual(failureStatuses(rows).map(row => row.key), ['429', '503'])
})
