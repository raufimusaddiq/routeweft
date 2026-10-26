import assert from 'node:assert/strict'
import { test } from 'node:test'
import { attemptExpired, connectMethods, phaseForError, type ProviderAuth } from './signIn.ts'

test('codex offers browser sign-in and token import without asking for an identity', () => {
  const codex: ProviderAuth = { auth: 'oauth', authModes: [], signInMethods: [
    { mode: 'browser', authKind: 'oauth', resolvesIdentity: true, completion: 'redirect-url', loopbackRedirect: 'http://localhost:1455/auth/callback' },
    { mode: 'import', authKind: 'oauth', resolvesIdentity: true },
  ] }
  const methods = connectMethods(codex, 'Codex')
  assert.deepEqual(methods.map(method => method.id), ['browser', 'import', 'manual-oauth'])
  assert.equal(methods[0].label, 'Sign in with Codex')
  assert.equal(methods[0].askIdentity, false)
  assert.equal(methods[0].loopbackRedirect, 'http://localhost:1455/auth/callback')
  assert.equal(methods[1].askIdentity, false)
  // Raw tokens are still possible, but as their own choice.
  assert.equal(methods[2].askIdentity, true)
})

test('each method carries only its own fields', () => {
  const gitlab: ProviderAuth = { auth: 'oauth', authModes: [], signInMethods: [
    { mode: 'browser', authKind: 'oauth', resolvesIdentity: true, metaKeys: ['clientId', 'clientSecret', 'baseUrl'], completion: 'code' },
    { mode: 'import', authKind: 'oauth', resolvesIdentity: true, metaKeys: ['baseUrl'] },
  ] }
  const [browser, pat] = connectMethods(gitlab, 'GitLab Duo')
  assert.deepEqual(browser.metaKeys, ['clientId', 'clientSecret', 'baseUrl'])
  assert.deepEqual(pat.metaKeys, ['baseUrl'])
})

test('a dual-auth provider keeps its primary mode first and offers each mode', () => {
  const kimchi: ProviderAuth = { auth: 'api-key', authModes: ['api-key', 'oauth'], signInMethods: [{ mode: 'browser', authKind: 'oauth', resolvesIdentity: false, completion: 'token' }] }
  const methods = connectMethods(kimchi, 'Kimchi')
  assert.deepEqual(methods.map(method => method.id), ['manual-api-key', 'browser', 'manual-oauth'])
  assert.equal(methods[1].askIdentity, true)
  const iflow: ProviderAuth = { auth: 'oauth', authModes: ['oauth', 'cookie'], signInMethods: [
    { mode: 'browser', authKind: 'oauth', resolvesIdentity: true, completion: 'code' },
    { mode: 'import', authKind: 'cookie', resolvesIdentity: true },
  ] }
  assert.deepEqual(connectMethods(iflow, 'iFlow').map(method => method.label), ['Sign in with iFlow', 'Import cookie', 'Paste OAuth tokens', 'Cookie / session'])
})

test('providers without a flow keep the manual form, including no-auth', () => {
  assert.deepEqual(connectMethods({ auth: 'api-key', authModes: ['api-key'] }, 'openai').map(method => method.id), ['manual-api-key'])
  const none = connectMethods({ auth: 'none', authModes: [] }, 'mimo-free')
  assert.deepEqual(none.map(method => [method.id, method.label]), [['manual-none', 'No authentication']])
  assert.equal(connectMethods({ auth: 'oauth', authModes: [], signInMethods: [{ mode: 'device', authKind: 'oauth', resolvesIdentity: false }] }, 'GitHub Copilot')[0].label, 'Sign in with GitHub Copilot (device code)')
})

test('attempt outcomes are explicit', () => {
  const now = Date.parse('2026-10-27T12:00:00Z')
  assert.equal(attemptExpired('2026-10-27T11:59:59Z', now), true)
  assert.equal(attemptExpired('2026-10-27T12:10:00Z', now), false)
  assert.equal(attemptExpired(undefined, now), false)
  assert.equal(attemptExpired('not a time', now), false)
  assert.equal(phaseForError('oauth_expired', '2026-10-27T12:10:00Z', now), 'expired')
  assert.equal(phaseForError('oauth_state_invalid', '2026-10-27T11:00:00Z', now), 'expired')
  assert.equal(phaseForError('oauth_state_invalid', '2026-10-27T12:10:00Z', now), 'failed')
  assert.equal(phaseForError('oauth_denied', '2026-10-27T12:10:00Z', now), 'failed')
})
