import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ago, badRate, compact, delta, isWeekend, outlier, pct, priorWeek, rate, statusClass, type Summary } from './format.ts'

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

test('statusClass separates success, client and server failures', () => {
  assert.equal(statusClass(200), 'good')
  assert.equal(statusClass(429), 'warn')
  assert.equal(statusClass(503), 'bad')
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
