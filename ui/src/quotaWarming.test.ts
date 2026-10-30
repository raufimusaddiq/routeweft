import assert from 'node:assert/strict'
import { test } from 'node:test'
import { WARMING_DESCRIPTIONS, WARMING_SETTINGS, parseWarming, warmingPatch, warmingProvider, warmingStatusText, withWarming } from './quotaView.ts'

test('only OAuth Claude and Codex accounts get a warming switch (pin index.js:1193)', () => {
  assert.equal(warmingProvider({ providerId: 'claude', authKind: 'oauth' }), 'claude')
  assert.equal(warmingProvider({ providerId: 'codex', authKind: 'oauth' }), 'codex')
  assert.equal(warmingProvider({ providerId: 'claude', authKind: 'api-key' }), null)
  assert.equal(warmingProvider({ providerId: 'codex', authKind: 'cookie' }), null)
  assert.equal(warmingProvider({ providerId: 'codex' }), null)
  assert.equal(warmingProvider({ providerId: 'gemini-cli', authKind: 'oauth' }), null)
})

test('every account is off until it is opted in; unreadable settings never read as on', () => {
  const empty = { claude: {}, codex: {} }
  assert.deepEqual(parseWarming(undefined), empty)
  assert.deepEqual(parseWarming({}), empty)
  assert.deepEqual(parseWarming({ claudeAutoPing: '{"connections":{}}', codexAutoPing: '{"connections":{}}' }), empty)
  for (const bad of ['', 'not json', 'null', '[]', '{"connections":[]}', '{"connections":"x"}', '{"connections":null}']) {
    assert.deepEqual(parseWarming({ claudeAutoPing: bad, codexAutoPing: bad }), empty, bad)
  }
  // Only a literal true is on; truthy lookalikes are not.
  assert.deepEqual(parseWarming({ codexAutoPing: '{"connections":{"a":true,"b":false,"c":1,"d":"true"}}' }).codex, { a: true })
})

test('the stored maps are read per provider', () => {
  const maps = parseWarming({ claudeAutoPing: '{"connections":{"c1":true}}', codexAutoPing: '{"connections":{"x1":true,"x2":true}}' })
  assert.deepEqual(maps.claude, { c1: true })
  assert.deepEqual(maps.codex, { x1: true, x2: true })
})

test('turning one account on or off changes only that account and keeps the map to opted-in accounts', () => {
  const before = { a: true, b: true }
  assert.deepEqual(withWarming(before, 'c', true), { a: true, b: true, c: true })
  assert.deepEqual(withWarming(before, 'a', false), { b: true })
  assert.deepEqual(withWarming({}, 'a', false), {})
  assert.deepEqual(before, { a: true, b: true }, 'the input is not mutated')
})

test('the save body is the settings PATCH the control API accepts, one provider at a time', () => {
  assert.deepEqual(warmingPatch('claude', { c1: true }), { set: { claudeAutoPing: '{"connections":{"c1":true}}' } })
  assert.deepEqual(warmingPatch('codex', {}), { set: { codexAutoPing: '{"connections":{}}' } })
  assert.equal(WARMING_SETTINGS.claude, 'claudeAutoPing')
  assert.equal(WARMING_SETTINGS.codex, 'codexAutoPing')
  // What a save writes is what a load reads back.
  assert.deepEqual(parseWarming({ codexAutoPing: warmingPatch('codex', { x: true }).set.codexAutoPing }).codex, { x: true })
})

test('state is stated in words and the explanation is the pin\'s, visible rather than a tooltip', () => {
  assert.equal(warmingStatusText(true, false), 'Warming on')
  assert.equal(warmingStatusText(false, false), 'Warming off')
  assert.equal(warmingStatusText(true, true), 'Saving…')
  assert.match(WARMING_DESCRIPTIONS.codex, /Consumes a small amount of quota/)
  assert.match(WARMING_DESCRIPTIONS.claude, /moment it resets/)
})
