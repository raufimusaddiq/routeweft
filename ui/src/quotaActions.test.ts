import assert from 'node:assert/strict'
import { test } from 'node:test'
import { canSpendResetCredit, creditsByExpiry, notUpdatedBy, freshCreditCheck, refreshTerminal, refreshText, spendConfirmation, spendOutcome, spendOutcomeUnknown, type QuotaItem, type RefreshJob } from './quotaView.ts'

const job = (status: RefreshJob['status'], extra: Partial<RefreshJob> = {}): RefreshJob => ({ id: 'j1', status, requestedAt: '2026-10-28T12:00:00Z', ...extra })
const item: QuotaItem = { connectionId: 'c', providerId: 'codex', name: 'Work', enabled: true, status: 'available' }

test('an accepted refresh never reads as a finished observation (#226)', () => {
  for (const status of ['queued', 'running'] as const) {
    const text = refreshText(job(status, { startedAt: '2026-10-28T12:00:01Z' }))
    assert.ok(!refreshTerminal(job(status)))
    assert.match(text, /still show the previous observations/)
    assert.doesNotMatch(text, /finished|reloaded/)
  }
})

test('a refresh that never finishes says so instead of claiming an update', () => {
  for (const status of ['queued', 'running'] as const) {
    const text = refreshText(job(status, { startedAt: '2026-10-28T12:00:01Z' }), true)
    assert.match(text, /previous observations; use Reload later/)
    assert.doesNotMatch(text, /finished at/)
  }
})

test('completion and failure are terminal and read differently', () => {
  const done = job('succeeded', { startedAt: '2026-10-28T12:00:01Z', finishedAt: '2026-10-28T12:00:09Z' })
  const failed = job('failed', { startedAt: '2026-10-28T12:00:01Z', finishedAt: '2026-10-28T12:00:09Z', error: 'one or more accounts could not be read' })
  assert.ok(refreshTerminal(done) && refreshTerminal(failed))
  assert.match(refreshText(done), /^Quota refresh finished at .*Rows were reloaded/)
  assert.match(refreshText(failed), /without reading every account: one or more accounts could not be read/)
  assert.notEqual(refreshText(done), refreshText(failed))
})

test('rows the finished refresh did not observe are marked stale', () => {
  const done = job('succeeded', { startedAt: '2026-10-28T12:00:00Z', finishedAt: '2026-10-28T12:00:09Z' })
  assert.equal(notUpdatedBy({ ...item, observedAt: '2026-10-28T12:00:05Z' }, done), false)
  assert.equal(notUpdatedBy({ ...item, observedAt: '2026-10-28T11:40:00Z' }, done), true)
  assert.equal(notUpdatedBy({ ...item, observedAt: undefined }, done), true)
  assert.equal(notUpdatedBy({ ...item, observedAt: '2026-10-28T11:40:00Z', enabled: false }, done), false, 'a disabled account is not refreshed')
  assert.equal(notUpdatedBy({ ...item, observedAt: '2026-10-28T11:40:00Z' }, job('running', { startedAt: '2026-10-28T12:00:00Z' })), false, 'nothing is judged before the job ends')
  assert.equal(notUpdatedBy({ ...item, observedAt: '2026-10-28T11:40:00Z' }, null), false)
})

test('reset credits list soonest expiry first, unknown expiry last', () => {
  const sorted = creditsByExpiry([
    { status: 'available' },
    { status: 'available', expiresAt: '2026-12-01T00:00:00Z' },
    { status: 'available', expiresAt: '2026-11-01T00:00:00Z' },
    { status: 'used', expiresAt: 'not a date' },
  ])
  assert.deepEqual(sorted.map(c => c.expiresAt), ['2026-11-01T00:00:00Z', '2026-12-01T00:00:00Z', undefined, 'not a date'])
})

test('only a reported credit on a Codex account can be spent', () => {
  assert.equal(canSpendResetCredit({ ...item, availableResetCredits: 2 }), true)
  assert.equal(canSpendResetCredit({ ...item, availableResetCredits: 0 }), false)
  assert.equal(canSpendResetCredit({ ...item, availableResetCredits: undefined }), false, 'an unknown count is not a credit')
  assert.equal(canSpendResetCredit({ ...item, providerId: 'claude', availableResetCredits: 2 }), false)
})

test('the confirmation names one credit, the account and the count', () => {
  assert.equal(spendConfirmation({ ...item, availableResetCredits: 3 }), 'Use 1 Codex reset credit for Work? This cannot be undone. Remaining credits: 3.')
})

test('every server error and a failed request are unknown outcomes, never spent or safe to repeat (#226)', () => {
  for (const status of [0, 500, 502, 503, 504]) {
    const outcome = spendOutcome(status, { error: { code: 'reset_failed', message: 'outcome unavailable' } })
    assert.equal(spendOutcomeUnknown(status), true, String(status))
    assert.deepEqual([outcome.tone, outcome.reload, outcome.unknown], ['error', false, true], String(status))
    assert.match(outcome.text, /may or may not have used the credit, and the request was not sent again\. The credit list below was read again; check it before trying again\./)
    assert.doesNotMatch(outcome.text, /nothing was spent|used\./)
  }
  assert.match(spendOutcome(0, null).text, /\(the request did not complete\)/)
  assert.match(spendOutcome(504, null).text, /\(HTTP 504\)/)
})

test('only a 200 is a spend, and a refusal is definite', () => {
  const spent = spendOutcome(200, { windowsReset: 2 })
  assert.deepEqual([spent.tone, spent.reload, spent.unknown], ['notice', true, undefined])
  assert.match(spent.text, /One Codex reset credit used\. 2 windows reset\./)
  for (const [status, body, text] of [
    [409, null, /nothing was spent/],
    [403, { error: { code: 'reauth_required' } }, /no reset credit was spent\. Reconnect/],
    [429, null, /refused the reset \(HTTP 429\); nothing was spent/],
    [400, { error: { code: 'confirmation_required' } }, /not confirmed/],
  ] as const) {
    const outcome = spendOutcome(status, body)
    assert.match(outcome.text, text)
    assert.equal(outcome.unknown, undefined, String(status))
    assert.equal(spendOutcomeUnknown(status), false)
  }
})

test('a spend goes ahead only on a fresh, readable credit list with a credit in it (#226)', () => {
  assert.match(freshCreditCheck(null), /could not be read from Codex, so nothing was spent\. Every spend reads the credits first/)
  assert.match(freshCreditCheck({ availableCount: 0, credits: [] }), /no reset credit to use; nothing was spent/)
  assert.match(freshCreditCheck({ availableCount: Number.NaN, credits: [] }), /no reset credit/)
  assert.equal(freshCreditCheck({ availableCount: 1, credits: [{ status: 'available' }] }), '')
  assert.match(spendOutcome(503, null).text, /The next spend reads it again first\./)
})
