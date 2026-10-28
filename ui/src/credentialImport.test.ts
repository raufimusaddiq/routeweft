import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { BULK_MAX_BYTES, IMPORT_MAX_BYTES, expiredNote, hostDiscoveryNote, importShapes, missingMeta, parseObject, readBulkFiles, readCredentialFile, savedReadinessNote, type ChosenFile } from './credentialImport.ts'
import { bulkSummary } from './onboarding.ts'
import { connectMethods } from './signIn.ts'

const SECRET = 'fixture-secret-marker'
const file = (name: string, content: string, size = new TextEncoder().encode(content).length): ChosenFile => ({ name, size, text: async () => content })
const shape = (provider: string, id: string) => {
  const found = importShapes(provider).find(item => item.id === id)
  assert.ok(found, `${provider} has no ${id} import`)
  return found
}

// The UI bounds are the server's, so a file refused here would be refused there.
test('the size bounds match the server', () => {
  const oauth = readFileSync(new URL('../../internal/control/api/oauth.go', import.meta.url), 'utf8')
  assert.match(oauth, /oauthMaxImportInput = 64 << 10/)
  assert.match(oauth, /decodeJSON\(w, r, 1<<20, &body\)/)
  assert.equal(IMPORT_MAX_BYTES, 64 << 10)
  assert.equal(BULK_MAX_BYTES, 1 << 20)
})

// Each specialized provider names its own shapes and where the credential lives.
test('every scoped provider offers its import shapes with instructions', () => {
  const expected: Record<string, string[]> = {
    cursor: ['token'], kiro: ['api-key', 'token', 'cli-proxy'], 'xiaomi-mimo': ['auth-json', 'key'],
    iflow: ['cookie'], gitlab: ['pat'], 'grok-cli': ['account'], codex: ['token'],
  }
  for (const [provider, ids] of Object.entries(expected)) {
    const shapes = importShapes(provider)
    assert.deepEqual(shapes.map(item => item.id), ids, provider)
    for (const item of shapes) assert.ok(item.steps.length > 0, `${provider}/${item.id} has no instructions`)
  }
  assert.match(shape('cursor', 'token').steps.join(' '), /state\.vscdb/)
  assert.match(shape('cursor', 'token').steps.join(' '), /storage\.serviceMachineId/)
  assert.match(shape('kiro', 'token').steps.join(' '), /kiro-auth-token\.json/)
  assert.match(shape('kiro', 'token').steps.join(' '), /clientIdHash/)
  assert.match(shape('xiaomi-mimo', 'auth-json').steps.join(' '), /mimocode\/auth\.json/)
  assert.match(shape('iflow', 'cookie').steps.join(' '), /BXAuth/)
  assert.match(shape('gitlab', 'pat').steps.join(' '), /api, read_user and ai_features/)
  // File-shaped imports take JSON; key- and cookie-shaped ones stay one masked line.
  assert.deepEqual(importShapes('kiro').map(item => item.json), [false, true, true])
  assert.equal(shape('iflow', 'cookie').json, false)
  // A provider without its own entry keeps the single masked line.
  assert.deepEqual(importShapes('unknown').map(item => item.id), ['token'])
})

// Cursor requires its machine id, as the pin's import route does.
test('cursor requires a well-formed machine id beside a full token', () => {
  const cursor = shape('cursor', 'token')
  const token = 'x'.repeat(60)
  assert.deepEqual(missingMeta(cursor, {}), ['machineId'])
  assert.deepEqual(missingMeta(cursor, { machineId: '0123abcd-0123-4abc-8def-0123456789ab' }), [])
  assert.match(cursor.check('short', { machineId: '0123abcd01234abc8def0123456789ab' }) ?? '', /at least 50/)
  assert.match(cursor.check(token, {}) ?? '', /Machine id/)
  assert.match(cursor.check(token, { machineId: 'not-a-uuid' }) ?? '', /UUID/)
  assert.equal(cursor.check(token, { machineId: '0123abcd-0123-4abc-8def-0123456789ab' }), null)
})

// A file of the wrong shape is named before it is sent, and no message quotes it.
test('json imports are schema-checked without quoting the input', () => {
  const cases: Array<[string, string, string, RegExp]> = [
    ['kiro', 'token', `{"accessToken":"${SECRET}"}`, /refreshToken/],
    ['kiro', 'token', `{"refreshToken":"${SECRET}","clientId":"c"}`, /both clientId and clientSecret/],
    ['kiro', 'token', `{"refreshToken":"${SECRET}","token_endpoint":"https://login.microsoftonline.com/t"}`, /CLIProxyAPI/],
    ['kiro', 'token', `{"refreshToken":${SECRET}`, /not valid JSON/],
    ['kiro', 'cli-proxy', `{"access_token":"${SECRET}"}`, /refresh_token, client_id, token_endpoint, profile_arn/],
    ['kiro', 'cli-proxy', `{"auth_method":"social","access_token":"${SECRET}"}`, /external_idp/],
    ['kiro', 'cli-proxy', `{"access_token":"a","refresh_token":"${SECRET}","client_id":"c","profile_arn":"p","scopes":["s"],"token_endpoint":"https://evil.example.invalid/token"}`, /Microsoft login/],
    ['kiro', 'cli-proxy', `{"access_token":"a","refresh_token":"${SECRET}","client_id":"c","profile_arn":"p","scopes":[],"token_endpoint":"https://login.microsoftonline.com/t"}`, /scopes/],
    ['kiro', 'api-key', `{"refreshToken":"${SECRET}"}`, /JSON file/],
    ['xiaomi-mimo', 'auth-json', `{"xiaomi":{"type":"api"}}`, /xiaomi\.key/],
    ['xiaomi-mimo', 'auth-json', `{"xiaomi":{"key":"${SECRET}"}}`, /sk-/],
    ['xiaomi-mimo', 'auth-json', `["${SECRET}"]`, /one JSON object/],
    ['xiaomi-mimo', 'key', SECRET, /sk-/],
    ['iflow', 'cookie', `session=${SECRET}`, /BXAuth/],
    ['grok-cli', 'account', `{"refresh_token":"${SECRET}"}`, /access_token/],
  ]
  for (const [provider, id, input, message] of cases) {
    const problem = shape(provider, id).check(input, {})
    assert.ok(problem, `${provider}/${id} accepted ${input}`)
    assert.match(problem, message, `${provider}/${id}`)
    assert.ok(!problem.includes(SECRET), `${provider}/${id} quotes the input`)
  }
  const good: Array<[string, string, string]> = [
    ['kiro', 'token', '{\n  "refreshToken": "aorAAAAAG-fixture",\n  "region": "us-east-1"\n}'],
    ['kiro', 'token', '{"refreshToken":"r","clientId":"c","clientSecret":"s","region":"eu-west-1"}'],
    ['kiro', 'cli-proxy', '{"auth_method":"external_idp","access_token":"a","refresh_token":"r","client_id":"c","profile_arn":"p","scopes":"s","token_endpoint":"https://login.microsoftonline.com/common/oauth2/v2.0/token"}'],
    ['kiro', 'api-key', 'fixture-kiro-key'],
    ['xiaomi-mimo', 'auth-json', '{"xiaomi":{"type":"api","key":"sk-fixture","metadata":{"uid":"7"}}}'],
    ['xiaomi-mimo', 'key', 'sk-fixture'],
    ['iflow', 'cookie', 'a=1; BXAuth=fixture; b=2'],
    ['grok-cli', 'account', '{"accessToken":"fixture"}'],
  ]
  for (const [provider, id, input] of good) assert.equal(shape(provider, id).check(input, {}), null, `${provider}/${id}`)
  assert.ok(!('error' in parseObject('{"a":1}')))
})

// A chosen file is bounded like a paste, must be text, and is named by its file
// name and size only.
test('a chosen file is read within the bound and never echoed', async () => {
  const content = `{"refreshToken":"${SECRET}"}`
  const read = await readCredentialFile(file('kiro-auth-token.json', content), IMPORT_MAX_BYTES)
  assert.ok(!('error' in read))
  assert.equal(read.text, content)
  assert.match(read.label, /^kiro-auth-token\.json \(\d+ B\)$/)
  assert.ok(!read.label.includes(SECRET))
  const tooLarge = await readCredentialFile(file('big.json', '{}', IMPORT_MAX_BYTES + 1), IMPORT_MAX_BYTES)
  assert.ok('error' in tooLarge && /at most 64\.0 KB/.test(tooLarge.error))
  const binary = await readCredentialFile(file('state.vscdb', 'SQLite format 3\u0000'), IMPORT_MAX_BYTES)
  assert.ok('error' in binary && /not a text file/.test(binary.error))
  const empty = await readCredentialFile(file('auth.json', '  \n'), IMPORT_MAX_BYTES)
  assert.ok('error' in empty && /empty/.test(empty.error))
  const unreadable = await readCredentialFile({ name: 'x.json', size: 2, text: async () => { throw new Error(SECRET) } }, IMPORT_MAX_BYTES)
  assert.ok('error' in unreadable && !unreadable.error.includes(SECRET))
})

// Bulk files are joined as the reference's dialog does: arrays, wrapped
// accounts and single objects alike.
test('bulk files join into one account list', async () => {
  const joined = await readBulkFiles([
    file('a.json', '[{"access_token":"1"},{"access_token":"2"}]'),
    file('b.json', '{"accounts":[{"access_token":"3"}]}'),
    file('c.json', '{"access_token":"4"}'),
  ], BULK_MAX_BYTES)
  assert.ok(!('error' in joined))
  assert.equal(joined.accounts.length, 4)
  assert.equal(joined.label, '3 files, 4 accounts')
  const bad = await readBulkFiles([file('a.json', `{"access_token":${SECRET}`)], BULK_MAX_BYTES)
  assert.ok('error' in bad && /a\.json is not valid JSON/.test(bad.error) && !bad.error.includes(SECRET))
  const large = await readBulkFiles([file('a.json', '[]', BULK_MAX_BYTES), file('b.json', '[]', 10)], BULK_MAX_BYTES)
  assert.ok('error' in large && /together/.test(large.error))
  assert.ok('error' in await readBulkFiles([], BULK_MAX_BYTES))
})

// Per-item results: added, updated, failed and stored-but-expired are each said.
test('bulk results report each item, including an expired credential', () => {
  const summary = bulkSummary({ total: 4, success: 3, failed: 1, results: [
    { index: 0, ok: true, outcome: 'added' },
    { index: 1, ok: true, outcome: 'updated' },
    { index: 2, ok: false, error: 'item is not an object' },
    { index: 3, ok: true, outcome: 'added', expired: true, refreshable: false },
  ] })
  assert.equal(summary.headline, '2 added, 1 already connected and updated, 1 failed, 1 saved with an expired credential.')
  assert.match(summary.rows[3].text, /expired and has no refresh token/)
  assert.equal(summary.rows[2].text, 'failed: item is not an object')
})

test('an expired import and an unserved provider are said after the save', () => {
  assert.equal(expiredNote({}), null)
  assert.match(expiredNote({ expired: true, refreshable: true }) ?? '', /refresh token/)
  assert.match(expiredNote({ expired: true, refreshable: false }) ?? '', /cannot serve requests/)
  assert.equal(savedReadinessNote('ready', 'Cursor'), null)
  assert.match(savedReadinessNote('unavailable', 'Cursor') ?? '', /does not route requests to Cursor/)
  assert.match(savedReadinessNote('unknown', 'Cursor') ?? '', /does not say/)
  assert.match(savedReadinessNote('experimental', 'Kiro') ?? '', /experimental/)
})

// Kimchi is dual-auth: its API key stays first, beside the browser token sign-in.
test('kimchi offers its API key and its browser token sign-in', () => {
  const methods = connectMethods({ id: 'kimchi', auth: 'api-key', authModes: ['api-key', 'oauth'], signInMethods: [{ mode: 'browser', authKind: 'oauth', resolvesIdentity: false, infersIdentity: true, completion: 'token' }] }, 'Kimchi')
  assert.deepEqual(methods.map(item => item.id), ['manual-api-key', 'browser', 'manual-oauth'])
})

// #296: discovery names where the key came from and whether the account session
// came with it, and has no credential to show.
test('a host discovery says what it found', () => {
  assert.equal(hostDiscoveryNote({ source: '/home/op/.local/share/mimocode/auth.json', accountSession: true }),
    'The key was read from /home/op/.local/share/mimocode/auth.json on the server. MiMo Desktop’s account session was attached, so the weekly quota can be read.')
  assert.match(hostDiscoveryNote({ source: '/x/auth.json', accountSession: false }), /^The key was read from \/x\/auth\.json on the server\. No MiMo Desktop account session could be read/)
  assert.match(hostDiscoveryNote(undefined), /^No MiMo Desktop account session could be read/)
})
