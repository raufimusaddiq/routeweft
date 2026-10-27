import assert from 'node:assert/strict'
import { test } from 'node:test'
import { browserJourney, checkedNote, identityPrompt, importJourney, loopbackHint, projectNote, validProjectId } from './journeys.ts'
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
