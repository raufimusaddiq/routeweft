import assert from 'node:assert/strict'
import { test } from 'node:test'
import { bulkSummary, canReauthenticate, closeSignInWindow, duplicateNote, openSignInWindow, outcomeMessage, parseBulkInput, sendSignInWindow, signInURL, type SignInWindow, type WindowOpener } from './onboarding.ts'
import { connectMethods, type ProviderAuth } from './signIn.ts'
import { quotaLine } from './quotaView.ts'

// A window the fake opener hands out, recording what the page did to it.
function fakeWindow(): SignInWindow & { visited: string[] } {
  const visited: string[] = []
  return {
    closed: false, opener: { dashboard: true }, visited,
    close() { this.closed = true },
    location: { replace(url: string) { visited.push(url) } },
  }
}

test('the sign-in window opens empty in the click, without an opener, and then goes to the provider', () => {
  const popup = fakeWindow()
  const calls: string[][] = []
  const opener: WindowOpener = (url, target, features) => { calls.push([url, target, features]); return popup }
  const opened = openSignInWindow(opener)
  assert.equal(opened, popup)
  assert.equal(calls.length, 1)
  assert.equal(calls[0][0], 'about:blank')
  // The provider's page must not be able to navigate the dashboard.
  assert.equal(popup.opener, null)
  assert.equal(sendSignInWindow(opened, 'https://auth.openai.com/oauth/authorize?state=s'), 'opened')
  assert.deepEqual(popup.visited, ['https://auth.openai.com/oauth/authorize?state=s'])
})

test('a blocked popup falls back to the link and paste-back', () => {
  // window.open answers null when a blocker refuses; some environments throw.
  assert.equal(openSignInWindow(() => null), null)
  assert.equal(openSignInWindow(() => { throw new Error('blocked') }), null)
  assert.equal(sendSignInWindow(null, 'https://auth.openai.com/oauth/authorize'), 'blocked')
  // A window the operator closed before /start answered is as good as blocked.
  const closed = fakeWindow()
  closed.closed = true
  assert.equal(sendSignInWindow(closed, 'https://auth.openai.com/oauth/authorize'), 'blocked')
  assert.deepEqual(closed.visited, [])
})

test('only a web address reaches the window or the link', () => {
  assert.equal(signInURL('https://auth.openai.com/oauth/authorize?a=1'), 'https://auth.openai.com/oauth/authorize?a=1')
  for (const bad of ['javascript:alert(1)', 'data:text/html,x', '', undefined, 'not a url']) assert.equal(signInURL(bad), null, String(bad))
  const popup = fakeWindow()
  assert.equal(sendSignInWindow(popup, 'javascript:alert(1)'), 'blocked')
  assert.deepEqual(popup.visited, [])
  assert.equal(popup.closed, true)
})

test('the window is closed when the attempt ends, and closing twice is harmless', () => {
  const popup = fakeWindow()
  closeSignInWindow(popup)
  assert.equal(popup.closed, true)
  closeSignInWindow(popup)
  closeSignInWindow(null)
})

test('the result says whether an account was added, updated in place or reauthenticated', () => {
  const base = { id: 'c1', name: 'Work', identity: 'a@example.invalid' }
  assert.match(outcomeMessage({ ...base, outcome: 'added' }), /Added as a new connection/)
  const updated = outcomeMessage({ ...base, outcome: 'updated' })
  assert.match(updated, /already connected as “Work”/)
  assert.match(updated, /no duplicate was added/)
  assert.match(outcomeMessage({ ...base, outcome: 'reauthenticated' }), /^Reauthenticated “Work” as a@example\.invalid/)
  // A connection without an identity is named by its label.
  assert.match(outcomeMessage({ id: 'c2', name: 'Account 2', identity: '', outcome: 'added' }), /Signed in as Account 2/)
})

test('only Codex warns that a sign-in can update a stored account', () => {
  assert.match(duplicateNote('codex') ?? '', /instead of adding a second one/)
  assert.equal(duplicateNote('claude'), null)
})

test('Reauthenticate is offered for OAuth connections of a provider with a browser sign-in', () => {
  const browser = [{ mode: 'browser' as const, authKind: 'oauth', resolvesIdentity: true }]
  assert.equal(canReauthenticate(browser, { authKind: 'oauth' }), true)
  assert.equal(canReauthenticate(browser, { authKind: 'api-key' }), false)
  assert.equal(canReauthenticate([{ mode: 'import', authKind: 'oauth', resolvesIdentity: true }], { authKind: 'oauth' }), false)
  assert.equal(canReauthenticate(undefined, { authKind: 'oauth' }), false)
})

test('bulk import is its own choice, only where the server offers it', () => {
  const codex: ProviderAuth = { auth: 'oauth', authModes: [], bulkImport: true, signInMethods: [
    { mode: 'browser', authKind: 'oauth', resolvesIdentity: true, completion: 'redirect-url' },
    { mode: 'import', authKind: 'oauth', resolvesIdentity: true },
  ] }
  assert.deepEqual(connectMethods(codex, 'Codex').map(method => method.id), ['browser', 'import', 'bulk', 'manual-oauth'])
  assert.equal(connectMethods({ ...codex, bulkImport: false }, 'Codex').some(method => method.kind === 'bulk'), false)
})

test('bulk input takes an array, an accounts object or one account, as the reference dialog does', () => {
  assert.deepEqual(parseBulkInput('[{"accessToken":"x"},{"accessToken":"y"}]'), { accounts: [{ accessToken: 'x' }, { accessToken: 'y' }] })
  assert.deepEqual(parseBulkInput('{"accounts":[{"accessToken":"x"}]}'), { accounts: [{ accessToken: 'x' }] })
  assert.deepEqual(parseBulkInput('{"accessToken":"x"}'), { accounts: [{ accessToken: 'x' }] })
  for (const empty of ['', '   ', '[]', '{"accounts":[]}', 'null', '7']) assert.ok('error' in parseBulkInput(empty), empty)
})

test('a bulk parse error never quotes the pasted text', () => {
  const secret = 'eyJhbGciOiJub25lIn0.sentinel-token'
  for (const text of [`[{"accessToken":"${secret}"`, `[{"accessToken":${secret}}]`, secret]) {
    const result = parseBulkInput(text)
    assert.ok('error' in result)
    assert.ok(!result.error.includes('sentinel'), result.error)
    assert.ok(!result.error.includes('eyJ'), result.error)
  }
})

test('bulk results report partial success item by item, numbered from one', () => {
  const summary = bulkSummary({ total: 4, success: 3, failed: 1, results: [
    { index: 0, ok: true, id: 'a', outcome: 'added' },
    { index: 1, ok: true, id: 'b', outcome: 'updated' },
    { index: 2, ok: false, error: 'missing accessToken' },
    { index: 3, ok: true, id: 'c', outcome: 'added' },
  ] })
  assert.equal(summary.headline, '2 added, 1 already connected and updated, 1 failed.')
  assert.deepEqual(summary.rows.map(row => [row.item, row.ok]), [[1, true], [2, true], [3, false], [4, true]])
  assert.match(summary.rows[1].text, /already connected/)
  assert.equal(summary.rows[2].text, 'failed: missing accessToken')
  assert.equal(bulkSummary({ success: 1, failed: 0, results: [{ index: 0, ok: true }] }).headline, '1 added.')
})

test('after login the account quota reads as unread until the background read reaches it', () => {
  assert.match(quotaLine(undefined), /not read yet/)
  assert.match(quotaLine({ connectionId: 'c', providerId: 'codex', name: 'a', enabled: true, status: 'not-reported' }), /not read yet/)
  assert.equal(quotaLine({ connectionId: 'c', providerId: 'codex', name: 'a', enabled: true, status: 'available', remaining: 0.42, plan: 'plus' }), 'available · 42% remaining · plan plus')
})
