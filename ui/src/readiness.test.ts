import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readinessNote, readinessStatus, type Readiness } from './readiness.ts'

const of = (readiness?: Readiness) => ({ readiness })

test('a declared status is rendered as declared', () => {
  assert.equal(readinessStatus(of({ status: 'ready', served: true })), 'ready')
  assert.equal(readinessStatus(of({ status: 'experimental', served: true, reason: 'why' })), 'experimental')
  assert.equal(readinessStatus(of({ status: 'unavailable', served: false, reason: 'why' })), 'unavailable')
})

test('nothing undeclared or contradictory is rendered as ready', () => {
  assert.equal(readinessStatus(of()), 'unknown')
  assert.equal(readinessStatus(of({ status: 'unknown' })), 'unknown')
  assert.equal(readinessStatus(of({ status: 'ready', served: false })), 'unknown')
  assert.equal(readinessStatus(of({ status: 'unavailable', served: true })), 'unknown')
  for (const inherited of ['constructor', 'toString', '__proto__', 'hasOwnProperty']) {
    assert.equal(readinessStatus(of({ status: inherited } as unknown as Readiness)), 'unknown', inherited)
  }
})

test('the unavailable note does not claim every wire is unserved', () => {
  const note = readinessNote(of({ status: 'unavailable', served: false, reason: 'System One requests on /v1/systemone are served' }))
  assert.ok(note)
  assert.match(note, /does not route Chat, Responses or Messages requests/)
  assert.doesNotMatch(note, /cannot send requests to this provider/)
  assert.match(note, /System One requests on \/v1\/systemone are served\./)
})

test('a ready provider has no note and the others always do', () => {
  assert.equal(readinessNote(of({ status: 'ready', served: true })), undefined)
  assert.match(readinessNote(of({ status: 'experimental', served: true })) ?? '', /not yet qualified for production/)
  assert.match(readinessNote(of()) ?? '', /does not say whether it can serve/)
  assert.match(readinessNote(of({ status: 'ready', served: false })) ?? '', /does not say whether it can serve/)
})
