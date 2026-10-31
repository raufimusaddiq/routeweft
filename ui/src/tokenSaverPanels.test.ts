import assert from 'node:assert/strict'
import { test } from 'node:test'
import { helperReason, helperState, logState, newestLogs, statsRows, type PXPIPELog } from './tokenSaverPanels.ts'

const log = (overrides: Partial<PXPIPELog> = {}): PXPIPELog => ({ step: 'pxpipe', status: 'ok', skipped: false, attempted: true, succeeded: true, inputChars: 30000, outputChars: 9000, durationMs: 120, ...overrides })

test('a probe is available, unconfigured, or unavailable exactly as the API reports it', () => {
  assert.deepEqual(helperState({ healthy: true, latencyMs: 4 }), { tone: 'good', label: 'Available' })
  assert.deepEqual(helperState({ healthy: false, class: 'not_configured', message: 'no helper URL is configured', latencyMs: 0 }), { tone: 'idle', label: 'Not configured' })
  assert.deepEqual(helperState({ healthy: false, class: 'refused', message: 'helper refused the connection', latencyMs: 1 }), { tone: 'bad', label: 'Unavailable' })
})

test('the reason is the API message, with the status code bad_status reports separately', () => {
  assert.equal(helperReason({ healthy: true, latencyMs: 12 }), 'Probe answered in 12 ms.')
  assert.equal(helperReason({ healthy: false, class: 'refused', message: 'helper refused the connection', latencyMs: 1 }), 'helper refused the connection.')
  assert.equal(helperReason({ healthy: false, class: 'bad_status', message: 'helper answered its health endpoint with an error status', statusCode: 503, latencyMs: 2 }), 'helper answered its health endpoint with an error status (HTTP 503).')
  assert.equal(helperReason({ healthy: false, latencyMs: 0 }), 'No reason was reported.')
})

test('a log entry is classified from what it attempted, never from colour alone', () => {
  assert.deepEqual(logState(log()), { tone: 'good', label: 'Succeeded' })
  assert.deepEqual(logState(log({ succeeded: false, attempted: false, skipped: true, status: 'skipped' })), { tone: 'idle', label: 'Skipped' })
  assert.deepEqual(logState(log({ succeeded: false, status: 'failed' })), { tone: 'bad', label: 'Failed' })
  assert.deepEqual(logState(log({ succeeded: false, attempted: false, status: 'queued' })), { tone: 'warn', label: 'Queued' })
  assert.deepEqual(logState(log({ succeeded: false, attempted: false, status: '' })), { tone: 'warn', label: 'Unrecorded' })
})

test('logs are shown newest first and capped to the tail', () => {
  const entries = Array.from({ length: 60 }, (_, index) => log({ durationMs: index }))
  const shown = newestLogs(entries, 50)
  assert.equal(shown.length, 50)
  assert.equal(shown[0].durationMs, 59)
  assert.equal(shown.at(-1)?.durationMs, 10)
  assert.equal(entries[0].durationMs, 0, 'the source order is untouched')
  assert.deepEqual(newestLogs([]), [])
})

test('counters are reported as the API sends them', () => {
  assert.deepEqual(statsRows({ attempts: 12, succeeded: 9, failed: 2, skipped: 1 }), [
    { label: 'Attempts', value: 12 },
    { label: 'Succeeded', value: 9 },
    { label: 'Failed', value: 2 },
    { label: 'Skipped', value: 1 },
  ])
})
