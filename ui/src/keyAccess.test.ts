import assert from 'node:assert/strict'
import { test } from 'node:test'
import { accessBody, buildChoices, describeFailure, filterChoices, sameAccess, staleEntries, toggleAllow, type CatalogInput } from './keyAccess.ts'

const catalog: CatalogInput = {
  providers: [{ id: 'openai', staticModels: ['gpt-4o', 'gpt-4o-mini'] }, { id: 'idle', staticModels: ['never'] }],
  connections: [{ providerId: 'openai', enabled: true }, { providerId: 'idle', enabled: false }, { providerId: 'custom-x', enabled: true }],
  models: [{ providerId: 'custom-x', id: 'm1' }, { providerId: 'openai', id: 'gpt-4o-mini' }, { providerId: 'custom-x', id: 'off', disabled: true }],
  disabled: [{ providerId: 'openai', modelId: 'gpt-4o' }],
  aliases: [{ alias: 'fast', providerId: 'openai', modelId: 'gpt-4o-mini' }, { alias: 'ghost', providerId: 'idle', modelId: 'never' }],
  combos: [
    { name: 'team', members: [{ providerId: 'openai', modelId: 'gpt-4o-mini', selected: true }] },
    { name: 'via-alias', members: [{ providerId: '', modelId: 'fast', selected: true }] },
    { name: 'dead', members: [{ providerId: 'idle', modelId: 'never', selected: true }] },
    { name: 'deselected', members: [{ providerId: 'openai', modelId: 'gpt-4o-mini', selected: false }] },
  ],
}

test('choices only include what a client can reach', () => {
  const ids = buildChoices(catalog).map(choice => choice.id)
  assert.deepEqual(ids, ['team', 'via-alias', 'fast', 'custom-x/m1', 'openai/gpt-4o-mini'])
})

test('providers without an enabled connection, disabled models and dead aliases/combos are hidden', () => {
  const ids = new Set(buildChoices(catalog).map(choice => choice.id))
  for (const hidden of ['idle/never', 'openai/gpt-4o', 'custom-x/off', 'ghost', 'dead', 'deselected']) assert.equal(ids.has(hidden), false, hidden)
})

test('built-in seed models appear without a persisted row and are not duplicated', () => {
  const choices = buildChoices(catalog).filter(choice => choice.id === 'openai/gpt-4o-mini')
  assert.equal(choices.length, 1)
})

test('toggleAllow adds sorted, removes, and is idempotent', () => {
  let allow = toggleAllow([], 'b', true)
  allow = toggleAllow(allow, 'a', true)
  allow = toggleAllow(allow, 'a', true)
  assert.deepEqual(allow, ['a', 'b'])
  assert.deepEqual(toggleAllow(allow, 'a', false), ['b'])
})

test('stale entries are reported so they can be removed', () => {
  assert.deepEqual(staleEntries(['team', 'gone/model'], buildChoices(catalog)), ['gone/model'])
})

test('filterChoices matches id and detail case-insensitively', () => {
  const choices = buildChoices(catalog)
  assert.deepEqual(filterChoices(choices, ' MINI ').map(choice => choice.id), ['fast', 'openai/gpt-4o-mini']) // alias matches via its target
  assert.equal(filterChoices(choices, '').length, choices.length)
})

test('accessBody always states restricted explicitly and drops stale lists when unrestricted', () => {
  assert.deepEqual(accessBody({ restricted: false, allow: ['x'] }), { restricted: false, allow: [] })
  assert.deepEqual(accessBody({ restricted: true, allow: ['x'] }), { restricted: true, allow: ['x'] })
})

test('sameAccess ignores order and list contents when unrestricted', () => {
  assert.ok(sameAccess({ restricted: true, allow: ['a', 'b'] }, { restricted: true, allow: ['b', 'a'] }))
  assert.ok(sameAccess({ restricted: false, allow: ['a'] }, { restricted: false, allow: [] }))
  assert.ok(!sameAccess({ restricted: true, allow: [] }, { restricted: false, allow: [] }))
})

test('failures keep status and server wording', () => {
  assert.equal(describeFailure('Could not save access', 400, 'allowlist entries must not be empty'), 'Could not save access (400): allowlist entries must not be empty')
  assert.match(describeFailure('Could not save access', 503, 'API key management is not configured'), /unavailable \(503\).*not configured/)
  assert.equal(describeFailure('Could not save access', 409, undefined), 'Could not save access (409).')
})
