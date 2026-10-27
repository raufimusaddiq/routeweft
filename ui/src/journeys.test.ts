import assert from 'node:assert/strict'
import { test } from 'node:test'
import { browserJourney, checkedNote, copilotNote, profileArnNote, validProfileArn, DEVICE_DEADLINE_GRACE_MS, DEVICE_REQUEST_TIMEOUT_MS, requestBound, deviceCopyItems, deviceJourney, devicePollDelay, devicePollingOver, identityPrompt, importJourney, loopbackHint, projectNote, validProjectId } from './journeys.ts'
import { connectMethods, type ProviderAuth } from './signIn.ts'

const origin = 'https://routeweft.example.test/callback'

test('codex keeps its loopback wording and paste field', () => {
  const journey = browserJourney('codex', { completion: 'redirect-url', loopbackRedirect: 'http://localhost:1455/auth/callback' })
  assert.equal(journey.hint, loopbackHint('http://localhost:1455/auth/callback'))
  assert.equal(journey.pasteLabel, 'Address from the browser')
  assert.equal(journey.masked, false)
  assert.deepEqual(journey.before, [])
})

test('xAI port 56121 is described as the browser’s own loopback, which nothing on the server listens on', () => {
  const journey = browserJourney('xai', { completion: 'redirect-url', loopbackRedirect: 'http://127.0.0.1:56121/callback' })
  const before = journey.before.join(' ')
  assert.match(before, /http:\/\/127\.0\.0\.1:56121\/callback/)
  assert.match(before, /not on the Routeweft server, and Routeweft does not listen on it/)
  assert.match(journey.hint, /If xAI shows a code instead of redirecting, paste that code/)
  assert.match(journey.hint, /listens on port 56121.*receives the address instead of your browser/)
  assert.equal(journey.pasteLabel, 'Address or code from xAI')
})

test('GitLab names the instance, the redirect to register, public and confidential applications, and the PAT alternative', () => {
  const journey = browserJourney('gitlab', { completion: 'code', redirectUri: origin })
  const before = journey.before.join(' ')
  assert.match(before, /Server base URL blank for gitlab\.com/)
  assert.match(before, new RegExp(`redirect URI ${origin.replace(/[.]/g, '\\.')} and the scopes api and read_user`))
  assert.match(before, /public application \(Confidential unchecked\) needs only its client id/)
  assert.match(before, /confidential application also needs its client secret/)
  assert.match(before, /Import personal access token/)
  assert.deepEqual(journey.requiredMeta, ['clientId'])
  assert.match(importJourney('gitlab').note ?? '', /personal access token with the api scope/)
  const gitlab: ProviderAuth = { id: 'gitlab', auth: 'oauth', authModes: [], signInMethods: [
    { mode: 'browser', authKind: 'oauth', resolvesIdentity: true, completion: 'code', metaKeys: ['clientId', 'clientSecret', 'baseUrl'], redirectUri: origin },
    { mode: 'import', authKind: 'oauth', resolvesIdentity: true, metaKeys: ['baseUrl'] },
  ] }
  assert.deepEqual(connectMethods(gitlab, 'GitLab Duo').map(method => method.label), ['Sign in with GitLab Duo', 'Import personal access token', 'Paste OAuth tokens'])
})

test('an unset public origin is said before a sign-in that returns to it', () => {
  const journey = browserJourney('gitlab', { completion: 'code', redirectUnconfigured: true })
  assert.match(journey.before[0], /Set the public origin first/)
  assert.match(journey.before.join(' '), /set the public origin first/)
})

test('Cline and ClinePass keep the WorkOS semantics', () => {
  const cline = browserJourney('cline', { completion: 'code', redirectUri: origin })
  assert.match(cline.before.join(' '), /WorkOS.*workos: prefix/)
  const pass = browserJourney('clinepass', { completion: 'code', redirectUri: origin })
  assert.match(pass.before.join(' '), /ClinePass uses the same Cline account sign-in; its connection lists only ClinePass \(cline-pass\/\) models/)
  assert.match(pass.hint, new RegExp(`${origin.replace(/[.]/g, '\\.')}\\?code=…`))
})

test('Google sign-ins explain the Code Assist project, and Antigravity its onboarding', () => {
  const gemini = browserJourney('gemini-cli', { completion: 'code', redirectUri: origin })
  assert.match(gemini.before.join(' '), /Code Assist project, which every request needs/)
  assert.doesNotMatch(gemini.before.join(' '), /onboarding/)
  assert.match(browserJourney('antigravity', { completion: 'code', redirectUri: origin }).before.join(' '), /finishes the account’s Code Assist onboarding in the background/)
  assert.match(projectNote({ projectMissing: true }) ?? '', /no Code Assist project.*Name the project below.*Reauthenticate/)
  assert.equal(projectNote({ project: 'p-123456', projectMissing: false }), null)
  assert.equal(projectNote({}), null)
  for (const good of ['my-project', 'gen-lang-client-0123456789', 'example.com:my-project']) assert.equal(validProjectId(good), true, good)
  for (const bad of ['', 'My-Project', '1project', 'short', 'ends-with-', 'has space here']) assert.equal(validProjectId(bad), false, bad)
})

test('encrypted native callbacks say the address finishes only this attempt', () => {
  const xiaomi = browserJourney('xiaomi-mimo', { completion: 'callback', redirectUri: 'https://routeweft.example.test/' })
  assert.match(xiaomi.before.join(' '), /encrypted to a key this server made for this sign-in alone/)
  assert.match(xiaomi.hint, /\?u=…/)
  const zed = browserJourney('zed', { completion: 'callback' }, { nativeAppPort: '61000' })
  assert.match(zed.before.join(' '), /http:\/\/127\.0\.0\.1:61000\//)
  assert.match(zed.before.join(' '), /encrypted to a key this server made/)
  assert.match(browserJourney('zed', { completion: 'callback' }).hint, /127\.0\.0\.1:58443/)
})

test('Claude and Kimchi say what is pasted back; Kimchi says it was validated', () => {
  const claude = browserJourney('claude', { completion: 'code' })
  assert.match(claude.hint, /ends in #state/)
  assert.equal(claude.masked, true)
  const kimchi = browserJourney('kimchi', { completion: 'token', redirectUri: origin })
  assert.match(kimchi.hint, /\?token=….*or only the token/)
  assert.equal(kimchi.masked, true)
  assert.match(checkedNote('kimchi'), /gateway accepted the token/)
  assert.match(checkedNote('claude'), /Check connection/)
})

test('a method that usually learns the account makes the identity optional', () => {
  const xai: ProviderAuth = { id: 'xai', auth: 'oauth', authModes: [], signInMethods: [{ mode: 'browser', authKind: 'oauth', resolvesIdentity: false, infersIdentity: true, completion: 'redirect-url' }] }
  const [browser] = connectMethods(xai, 'xAI')
  assert.equal(browser.askIdentity, false)
  assert.equal(browser.identityOptional, true)
  const claude: ProviderAuth = { id: 'claude', auth: 'oauth', authModes: [], signInMethods: [{ mode: 'browser', authKind: 'oauth', resolvesIdentity: false, completion: 'code' }] }
  assert.equal(connectMethods(claude, 'Claude')[0].askIdentity, true)
  assert.match(identityPrompt('xAI', 'optional'), /usually reports the account.*stops without saving and asks/)
})

test('every provider in #216 has its own journey', () => {
  for (const provider of ['claude', 'gemini-cli', 'antigravity', 'xai', 'cline', 'clinepass', 'gitlab', 'iflow', 'kimchi', 'xiaomi-mimo', 'zed']) {
    const journey = browserJourney(provider, { completion: 'code', redirectUri: origin })
    const plain = browserJourney('some-other-provider', { completion: 'code', redirectUri: origin })
    assert.notDeepEqual(journey, plain, `${provider} uses the generic journey`)
  }
})

// --- #217 device approval ----------------------------------------------------

const DEVICE_PROVIDERS = ['github', 'grok-cli', 'kimi', 'kilocode', 'codebuddy-cn', 'codebuddy-intl', 'kiro', 'qoder']

test('every device provider has its own approval notes and says a second sign-in of the same account renews it', () => {
  const seen = new Set<string>()
  for (const provider of DEVICE_PROVIDERS) {
    const journey = deviceJourney(provider)
    assert.ok(journey.before.length >= 2, provider)
    assert.match(journey.before.join(' '), /already connected renews that connection instead of adding a second one/)
    seen.add(journey.before[0])
  }
  // No provider falls back to another's wording.
  assert.equal(seen.size, DEVICE_PROVIDERS.length)
})

test('CodeBuddy regions name their own host and point at the other region', () => {
  const cn = deviceJourney('codebuddy-cn').before.join(' ')
  const intl = deviceJourney('codebuddy-intl').before.join(' ')
  assert.match(cn, /copilot\.tencent\.com/)
  assert.match(cn, /signs in with CodeBuddy \(Intl\) instead/)
  assert.match(intl, /www\.codebuddy\.ai/)
  assert.match(intl, /signs in with CodeBuddy \(CN\) instead/)
  assert.match(cn, /same account on both is two connections/)
  assert.match(deviceJourney('codebuddy-cn').approve, /shows no code/)
})

test('Kiro offers Builder ID or IAM Identity Center, and Identity Center needs its start URL', () => {
  const builder = deviceJourney('kiro')
  assert.match(builder.before.join(' '), /AWS Builder ID.*choose idc/s)
  assert.deepEqual(builder.requiredMeta, [])
  const idc = deviceJourney('kiro', { authMethod: 'idc' })
  assert.match(idc.before.join(' '), /start URL and the region/)
  assert.deepEqual(idc.requiredMeta, ['startUrl'])
  assert.match(builder.before.join(' '), /importing a token or an API key/)
})

test('Qoder says a stored connection does not make it served', () => {
  assert.match(deviceJourney('qoder').before.join(' '), /does not make Qoder served/)
})

test('GitHub explains the Copilot token, and the result flags an account without one', () => {
  assert.match(deviceJourney('github').before.join(' '), /mints the Copilot token/)
  assert.equal(copilotNote({ providerId: 'github', copilotMissing: false }), null)
  assert.equal(copilotNote({ providerId: 'kimi', copilotMissing: true }), null)
  assert.match(copilotNote({ providerId: 'github', copilotMissing: true }) ?? '', /no Copilot token/)
})

test('the code and link are offered for copying as given; a missing code or a non-web link is not', () => {
  assert.deepEqual(deviceCopyItems({ userCode: 'WXYZ-1234', verificationUrl: 'https://github.com/login/device' }).map(item => item.key), ['code', 'link'])
  assert.deepEqual(deviceCopyItems({ userCode: '', verificationUrl: 'https://example.invalid/codebuddy' }).map(item => item.key), ['link'])
  assert.deepEqual(deviceCopyItems({ userCode: 'C', verificationUrl: 'javascript:alert(1)' }).map(item => item.key), ['code'])
})

test('polling follows the server interval, never below 2 seconds, and stops on its own past the deadline', () => {
  assert.equal(devicePollDelay(5), 5000)
  assert.equal(devicePollDelay(10), 10000)
  assert.equal(devicePollDelay(1), 2000)
  // A provider interval above the slow_down cap is kept, not shortened.
  assert.equal(devicePollDelay(45), 45000)
  assert.equal(devicePollDelay(90), 90000)
  assert.equal(devicePollDelay(3600), 300000)
  assert.equal(devicePollDelay(undefined), 5000)
  const deadline = '2026-10-27T12:05:00Z'
  const at = Date.parse(deadline)
  assert.equal(devicePollingOver(deadline, at), false)
  assert.equal(devicePollingOver(deadline, at + DEVICE_DEADLINE_GRACE_MS), false)
  assert.equal(devicePollingOver(deadline, at + DEVICE_DEADLINE_GRACE_MS + 1), true)
  assert.equal(devicePollingOver(undefined, at), false)
  assert.equal(devicePollingOver('not a date', at), false)
})

// A scripted clock: set records the pending callback, fire runs it.
function fakeTimers() {
  const pending = new Map<number, () => void>()
  let next = 0
  return {
    set: (callback: () => void) => { next += 1; pending.set(next, callback); return next },
    clear: (handle: unknown) => { pending.delete(handle as number) },
    fire: () => { for (const [handle, callback] of [...pending]) { pending.delete(handle); callback() } },
    size: () => pending.size,
  }
}

test('a request the server never answers is aborted at the bound and reads as timed out', () => {
  assert.equal(DEVICE_REQUEST_TIMEOUT_MS, 30_000)
  const timers = fakeTimers()
  const bound = requestBound(undefined, DEVICE_REQUEST_TIMEOUT_MS, timers)
  assert.equal(bound.signal.aborted, false)
  timers.fire()
  assert.equal(bound.signal.aborted, true)
  assert.equal(bound.timedOut(), true)
})

test('the operator ending the sign-in aborts the request without reading as a timeout, and done clears the timer', () => {
  const timers = fakeTimers()
  const parent = new AbortController()
  const bound = requestBound(parent.signal, DEVICE_REQUEST_TIMEOUT_MS, timers)
  parent.abort()
  assert.equal(bound.signal.aborted, true)
  assert.equal(bound.timedOut(), false)
  bound.done()
  assert.equal(timers.size(), 0)
  const already = new AbortController(); already.abort()
  assert.equal(requestBound(already.signal, 1, fakeTimers()).signal.aborted, true)
})

test('an answered request clears its bound, so it never aborts later', () => {
  const timers = fakeTimers()
  const bound = requestBound(undefined, DEVICE_REQUEST_TIMEOUT_MS, timers)
  bound.done()
  timers.fire()
  assert.equal(bound.signal.aborted, false)
  assert.equal(bound.timedOut(), false)
})

// #219: Kiro's Google and GitHub sign-in are two real browser methods, each fixed
// to its auth method, beside the AWS SSO device code and the import.
const kiro: ProviderAuth = { id: 'kiro', auth: 'oauth', authModes: ['oauth', 'api-key'], signInMethods: [
  { mode: 'device', authKind: 'oauth', resolvesIdentity: false, metaKeys: ['region', 'startUrl', 'authMethod'] },
  { mode: 'browser', authKind: 'oauth', resolvesIdentity: false, variant: 'google', completion: 'redirect-url' },
  { mode: 'browser', authKind: 'oauth', resolvesIdentity: false, variant: 'github', completion: 'redirect-url' },
  { mode: 'import', authKind: 'api-key', resolvesIdentity: false },
] }

test('Kiro lists Sign in with Google and Sign in with GitHub, each fixed to its method', () => {
  const methods = connectMethods(kiro, 'Kiro')
  assert.deepEqual(methods.map(method => method.id), ['device', 'browser-google', 'browser-github', 'import', 'manual-oauth', 'manual-api-key'])
  const google = methods.find(method => method.id === 'browser-google')!
  const github = methods.find(method => method.id === 'browser-github')!
  assert.equal(google.label, 'Sign in with Google')
  assert.equal(github.label, 'Sign in with GitHub')
  assert.deepEqual(google.fixedMeta, { authMethod: 'google' })
  assert.deepEqual(github.fixedMeta, { authMethod: 'github' })
  // No field to type the method, and the account is asked for: Kiro's tokens do not name it.
  assert.deepEqual(google.metaKeys, [])
  assert.equal(google.askIdentity, true)
  assert.equal(methods.find(method => method.id === 'device')!.fixedMeta, undefined)
})

test('the Kiro social journey explains the kiro:// address it pastes back', () => {
  for (const [variant, who] of [['google', 'Google'], ['github', 'GitHub']] as const) {
    const journey = browserJourney('kiro', { completion: 'redirect-url', variant })
    assert.match(journey.hint, new RegExp(`Sign in with ${who}`))
    assert.match(journey.hint, /kiro:\/\/kiro\.kiroAgent\/authenticate-success\?code=…&state=…/)
    assert.match(journey.before.join(' '), /nothing on this server receives it/)
    assert.match(journey.before.join(' '), /renews that connection/)
    assert.equal(journey.pasteLabel, 'Address from the browser')
    assert.equal(journey.masked, false)
    assert.deepEqual(journey.requiredMeta, [])
  }
  assert.match(deviceJourney('kiro').before.join(' '), /Sign in with Google or Sign in with GitHub/)
})

test('a Kiro connection saved without a profile ARN says it cannot serve yet and how to recover', () => {
  assert.match(profileArnNote({ providerId: 'kiro', profileArnMissing: true }) ?? '', /requests are refused until the connection has one.*profile ARN.*Reauthenticate/s)
  assert.equal(profileArnNote({ providerId: 'kiro', profileArnMissing: false }), null)
  assert.equal(profileArnNote({ providerId: 'kiro' }), null)
  assert.equal(profileArnNote({ providerId: 'github', profileArnMissing: true }), null)
  assert.equal(validProfileArn('arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC_def-1'), true)
  assert.equal(validProfileArn(' arn:aws:codewhisperer:eu-central-1:123456789012:profile/X '), true)
  for (const bad of ['', 'arn:aws:codewhisperer:us-east-1:1234:profile/X', 'arn:aws:iam::123456789012:role/x', 'arn:aws:codewhisperer:us-east-1:123456789012:profile/', 'arn:aws:codewhisperer:us-east-1:123456789012:profile/a b']) {
    assert.equal(validProfileArn(bad), false, bad)
  }
})
