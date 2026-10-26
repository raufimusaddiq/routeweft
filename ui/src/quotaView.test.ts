import assert from 'node:assert/strict'
import { test } from 'node:test'
import { percent, quotaQuery, remainingText, statusLabel, until, windowLabel, windowValue, zoned, type QuotaItem } from './quotaView.ts'

test('remaining fractions render as whole percentages (#213)', () => {
  assert.equal(percent(0), '0%')
  assert.equal(percent(0.5), '50%')
  assert.equal(percent(0.85), '85%')
  assert.equal(percent(1), '100%')
  assert.equal(percent(0.004), '0%')
  assert.equal(percent(0.995), '100%')
})

test('out-of-contract remaining values never print as counts', () => {
  assert.equal(percent(42), '100%')
  assert.equal(percent(-0.2), '0%')
  assert.equal(percent(undefined), null)
  assert.equal(percent(Number.NaN), null)
})

test('the four limit states read differently', () => {
  const base: QuotaItem = { connectionId: 'c', providerId: 'p', name: 'n', enabled: true, status: 'unknown' }
  const texts = [
    remainingText({ ...base, status: 'not-reported' }),
    remainingText({ ...base, status: 'unknown' }),
    remainingText({ ...base, status: 'unlimited' }),
    remainingText({ ...base, status: 'exhausted', remaining: 0 }),
  ]
  assert.deepEqual(texts, ['not reported', 'limit not reported', 'unlimited', '0% remaining'])
  const labels = ['not-reported', 'unknown', 'unlimited', 'exhausted'].map(statusLabel)
  assert.equal(new Set(labels).size, 4)
})

test('windows carry their own unit', () => {
  assert.equal(windowValue({ name: 'session', remaining: 0.85, unlimited: false }), '85% remaining')
  assert.equal(windowValue({ name: 'daily', unlimited: true }), 'unlimited')
  assert.equal(windowValue({ name: 'weekly', unlimited: false }), 'limit not reported')
  const usd = windowValue({ name: 'Balance (USD)', unlimited: false, credit: { currency: 'USD', state: 'positive', total: 12.5 } })
  assert.match(usd, /12\.50/)
  assert.doesNotMatch(usd, /%/)
  const cny = windowValue({ name: 'Balance (CNY)', unlimited: false, credit: { currency: 'CNY', state: 'zero', total: 0 } })
  assert.match(cny, /0\.00/)
  assert.doesNotMatch(cny, /unlimited|%/)
  assert.equal(windowValue({ name: 'Balance (EUR)', unlimited: false, credit: { currency: 'EUR', state: 'missing' } }), 'EUR: balance not reported')
  assert.match(windowValue({ name: 'Balance (EUR)', unlimited: false, credit: { currency: 'EUR', state: 'negative', total: -1.5 } }), /negative/)
})

test('windows show the provider label and keep the stable key (#221)', () => {
  assert.equal(windowLabel({ name: 'gemini_weekly', displayName: 'Gemini (Weekly)', unlimited: false }), 'Gemini (Weekly)')
  assert.equal(windowLabel({ name: 'gemini-2.5-pro', unlimited: false }), 'gemini-2.5-pro')
})

test('reset times name their zone and count down', () => {
  assert.match(zoned('2026-09-26T01:00:00Z', 'UTC') ?? '', /UTC/)
  assert.equal(zoned('not a date'), null)
  const now = Date.parse('2026-09-26T00:00:00Z')
  assert.equal(until('2026-09-26T00:30:00Z', now), 'in 30 min')
  assert.equal(until('2026-09-26T03:15:00Z', now), 'in 3 h 15 min')
  assert.equal(until('2026-09-28T02:00:00Z', now), 'in 2 d 2 h')
  assert.equal(until('2026-09-25T23:00:00Z', now), 'passed')
})

test('the page query carries paging, sort, provider and search', () => {
  assert.equal(quotaQuery(3, 'reset', 'groq', '  team a '), 'page=3&pageSize=50&sort=reset&provider=groq&q=team+a')
  assert.equal(quotaQuery(1, 'default', '', ''), 'page=1&pageSize=50&sort=default')
})
