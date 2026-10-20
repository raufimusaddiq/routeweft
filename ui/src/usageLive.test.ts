import { test } from 'node:test'
import assert from 'node:assert/strict'
import { addLive, freshLive, parseLiveRow, LIVE_CAP, type LiveRow } from './usageLive.ts'

const frame = (over: Record<string, unknown> = {}) => JSON.stringify({ requestId: 'r1', providerId: 'p', modelId: 'm', status: 200, durationMs: 40, inputTokens: 12, outputTokens: 3, createdAt: '2026-07-01T10:00:00.000Z', ...over })
const row = (requestId: string): LiveRow => ({ requestId, status: 200, inputTokens: 1, outputTokens: 1, createdAt: '2026-07-01T10:00:00.000Z' })

test('parses a completed-request frame and keeps only metadata fields', () => {
  const parsed = parseLiveRow(frame({ prompt: 'secret text', authorization: 'Bearer x' }))
  assert.equal(parsed?.requestId, 'r1')
  assert.equal(parsed?.inputTokens, 12)
  assert.deepEqual(Object.keys(parsed!).filter(key => ['prompt', 'authorization'].includes(key)), [])
})

test('drops malformed frames instead of rendering them', () => {
  for (const bad of ['', 'not json', 'null', '[]', frame({ requestId: '' }), frame({ status: '200' }), frame({ createdAt: 'yesterday' }), frame({ createdAt: undefined })]) {
    assert.equal(parseLiveRow(bad), null, bad)
  }
})

test('coerces absent or hostile token counts to zero', () => {
  const parsed = parseLiveRow(frame({ inputTokens: -5, outputTokens: 'many' }))
  assert.equal(parsed?.inputTokens, 0)
  assert.equal(parsed?.outputTokens, 0)
})

test('adds newest first, replaces a repeated request id, and stays bounded', () => {
  let rows = addLive([], row('a'))
  rows = addLive(rows, row('b'))
  assert.deepEqual(rows.map(r => r.requestId), ['b', 'a'])
  rows = addLive(rows, row('a'))
  assert.deepEqual(rows.map(r => r.requestId), ['a', 'b'])
  for (let i = 0; i < LIVE_CAP * 4; i++) rows = addLive(rows, row(`r${i}`))
  assert.equal(rows.length, LIVE_CAP)
  assert.equal(rows[0].requestId, `r${LIVE_CAP * 4 - 1}`)
})

test('a reconnect that reloads history does not show a request twice', () => {
  const live = [row('a'), row('b'), row('c')]
  assert.deepEqual(freshLive(live, [{ requestId: 'b' }, { requestId: 'z' }]).map(r => r.requestId), ['a', 'c'])
  assert.deepEqual(freshLive(live, []).length, 3)
  assert.deepEqual(freshLive([], [{ requestId: 'b' }]), [])
})

test('a frame flagged usageUnobserved carries no token counts', () => {
  const parsed = parseLiveRow(frame({ inputTokens: 0, outputTokens: 0, usageUnobserved: true, termination: 'client_abandoned' }))
  assert.equal(parsed?.inputTokens, undefined)
  assert.equal(parsed?.outputTokens, undefined)
  assert.equal(parsed?.usageUnobserved, true)
  assert.equal(parsed?.termination, 'client_abandoned')
  assert.equal(parseLiveRow(frame({ inputTokens: 0, outputTokens: 0 }))?.inputTokens, 0)
})
