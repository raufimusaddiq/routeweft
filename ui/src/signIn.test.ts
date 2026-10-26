import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { attemptExpired, attemptStillOpen, cancelResult, connectMethods, phaseForError, SIGN_IN_CODES, type ProviderAuth, type SignInStep } from './signIn.ts'

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
})

test('outcomes come from the server answer, never the clock', () => {
  assert.equal(phaseForError('oauth_expired'), 'expired')
  // Gone from the server says nothing about whether an earlier request saved it.
  assert.equal(phaseForError('oauth_state_invalid'), 'closed')
  assert.equal(phaseForError('oauth_denied'), 'failed')
  assert.equal(phaseForError('connection_save_failed'), 'failed')
  // No answer, a provider outage, or a refused paste leave the attempt open.
  for (const code of [undefined, 'oauth_poll_failed', 'oauth_code_missing', 'oauth_state_mismatch', 'not_a_known_code', '__proto__']) assert.equal(attemptStillOpen(code, 'callback'), true, String(code))
  for (const code of ['oauth_state_invalid', 'oauth_expired', 'oauth_denied', 'oauth_sign_in_rejected', 'connection_publish_failed']) assert.equal(attemptStillOpen(code, 'poll'), false, code)
})

test('a failed save ends a callback attempt but leaves a device attempt open', () => {
  for (const code of ['connection_save_failed', 'oauth_identity_unresolved', 'invalid_auth_kind', 'oauth_exchange_failed']) {
    assert.equal(attemptStillOpen(code, 'poll'), true, code)
    assert.equal(attemptStillOpen(code, 'callback'), false, code)
  }
})

// Reads the server's own source so a code added to the callback, poll or cancel
// path cannot reach the dashboard unclassified, and a code removed there cannot
// linger here.
test('every code the sign-in endpoints answer is classified', () => {
  const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
  const oauth = read('../../internal/control/api/oauth.go')
  const api = read('../../internal/control/api/api.go')
  const body = (source: string, name: string) => {
    const start = source.search(new RegExp(`\\nfunc (\\([^)]*\\) )?${name}\\(`))
    assert.ok(start >= 0, `${name} not found`)
    const end = source.indexOf('\nfunc ', start + 1)
    return source.slice(start, end < 0 ? undefined : end)
  }
  const codes = (source: string) => [...source.matchAll(/writeError\(w, http\.\w+, "([a-z_]+)"/g)].map(match => match[1])
  const shared = [...codes(body(api, 'requireSession')), ...codes(body(api, 'decodeJSON'))]
  const helpers = [...codes(body(oauth, 'oauthProviderFor')), ...codes(body(oauth, 'saveOnboardedConnection'))]
  const answered: Record<SignInStep, string[]> = {
    callback: [...shared, ...helpers, ...codes(body(oauth, 'flowRedirectURI')), ...codes(body(oauth, 'handleOAuthCallback'))],
    poll: [...shared, ...helpers, ...codes(body(oauth, 'handleOAuthDevicePoll'))],
    cancel: [...shared, ...codes(body(oauth, 'handleOAuthCancel'))],
  }
  for (const [step, list] of Object.entries(answered) as Array<[SignInStep, string[]]>) {
    assert.ok(list.length > 5, `${step}: extraction found too few codes`)
    for (const code of new Set(list)) assert.ok(SIGN_IN_CODES[code]?.[step], `${step} answers ${code} but signIn.ts does not classify it`)
  }
  for (const [code, steps] of Object.entries(SIGN_IN_CODES)) {
    for (const step of Object.keys(steps) as SignInStep[]) assert.ok(answered[step].includes(code), `${code} is classified for ${step}, which never answers it`)
  }
})

test('only a confirmed cancel counts as cancelled', () => {
  assert.equal(cancelResult(200, undefined), 'cancelled')
  // A save already in progress is neither cancelled nor known to be saved.
  assert.equal(cancelResult(409, 'oauth_completing'), 'saving')
  assert.equal(cancelResult(400, 'oauth_state_invalid'), 'closed')
  // No answer, a server error or anything unexpected leaves the attempt open.
  assert.equal(cancelResult(undefined, undefined), 'retry')
  assert.equal(cancelResult(500, undefined), 'retry')
  assert.equal(cancelResult(502, 'oauth_state_invalid'), 'retry')
  assert.equal(cancelResult(409, undefined), 'retry')
  assert.equal(cancelResult(400, 'invalid_request'), 'retry')
})
