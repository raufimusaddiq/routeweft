import assert from 'node:assert/strict'
import { test } from 'node:test'
import { canRelink, lifecycleLine, relative, relinkPatch, testStatusOf, testStatusTone } from './connectionStatus.ts'

const now = Date.parse('2026-10-27T12:00:00Z')

test('test status keeps untested, accepted and failed apart', () => {
  assert.equal(testStatusOf({}), 'untested')
  assert.equal(testStatusOf({ testStatus: 'active' }), 'active')
  assert.equal(testStatusOf({ testStatus: 'failed' }), 'failed')
  assert.equal(testStatusTone('active'), ' state-good')
  assert.equal(testStatusTone('failed'), ' state-bad')
  assert.equal(testStatusTone('untested'), '')
})

test('relative times read in both directions', () => {
  assert.equal(relative('2026-10-27T11:55:00Z', now), '5 min ago')
  assert.equal(relative('2026-10-27T15:00:00Z', now), 'in 3 h')
  assert.equal(relative('not a time', now), '')
})

test('the lifecycle line names the check, models, expiry, origin and quota separately', () => {
  assert.equal(lifecycleLine({}, now), 'Never checked')
  assert.equal(lifecycleLine({ lastTestedAt: '2026-10-27T11:58:00Z', modelsAvailable: 1, expiresAt: '2026-10-27T14:00:00Z', authOrigin: 'oauth', plan: 'pro', quotaObservedAt: '2026-10-27T11:00:00Z' }, now),
    'Checked 2 min ago · 1 model listed · Token expires in 2 h · Credential browser sign-in · Plan pro · Quota seen 60 min ago')
  assert.equal(lifecycleLine({ expiresAt: '2026-10-26T12:00:00Z', authOrigin: 'import', modelsAvailable: 0 }, now), 'Never checked · 0 models listed · Token expired 24 h ago · Credential imported')
})

test('an edit sends only what changed, and a credential only when one was typed', () => {
  const connection = { name: 'Work', identity: 'work@example.invalid', authKind: 'api-key' }
  assert.deepEqual(relinkPatch(connection, { name: 'Work', identity: 'work@example.invalid', credential: '' }), {})
  assert.deepEqual(relinkPatch(connection, { name: ' Home ', identity: '', credential: '' }), { name: 'Home' })
  assert.deepEqual(relinkPatch(connection, { name: 'Work', identity: 'work@example.invalid', credential: ' sk-fixture ' }), { secret: { accessToken: 'sk-fixture' } })
  assert.deepEqual(relinkPatch({ ...connection, authKind: 'cookie' }, { name: '', identity: '', credential: 'session=fixture' }), { secret: { cookie: 'session=fixture' } })
})

test('only typed credentials are relinked by hand', () => {
  assert.equal(canRelink({ authKind: 'api-key' }), true)
  assert.equal(canRelink({ authKind: 'cookie' }), true)
  assert.equal(canRelink({ authKind: 'oauth' }), false)
  assert.equal(canRelink({ authKind: 'none' }), false)
})
