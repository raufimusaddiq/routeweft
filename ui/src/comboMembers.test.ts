import assert from 'node:assert/strict'
import { test } from 'node:test'
import { BLANK_TUNING, FUSION_DEFAULTS, MAX_COMBO_DEPTH, MAX_FUSION_TUNING_MS, aggregateCapabilities, isNestedMember, memberLabel, nestedCandidates, nestedProblem, nestedRefusal, tuningBody, tuningInput, type CapabilityCatalog, type ComboRefs } from './comboMembers.ts'

const model = (providerId: string, modelId: string) => ({ providerId, modelId })
const nested = (name: string) => ({ providerId: '', modelId: name })
const combo = (name: string, ...members: Array<{ providerId: string; modelId: string; selected?: boolean }>): ComboRefs => ({ name, members })
/** A chain c1 -> c2 -> ... -> cN: c1 has height N. */
const chain = (length: number): ComboRefs[] => Array.from({ length }, (_, i) => combo(`c${i + 1}`, i + 1 < length ? nested(`c${i + 2}`) : model('openai', 'gpt')))

test('a nested member has no provider and a model id (internal/runtime/combo.go ComboMember.IsCombo)', () => {
  assert.equal(isNestedMember(nested('team')), true)
  assert.equal(isNestedMember(model('openai', 'gpt')), false)
  assert.equal(isNestedMember({ providerId: '', modelId: '' }), false)
  assert.equal(memberLabel(nested('team')), 'team')
  assert.equal(memberLabel(model('openai', 'gpt')), 'openai / gpt')
})

test('nestedProblem is the server rule: no cycle, only known Combos, nothing deeper than 8', () => {
  assert.equal(nestedProblem([]), undefined)
  assert.equal(nestedProblem([combo('a', model('openai', 'gpt')), combo('b', nested('a'))]), undefined)
  assert.match(nestedProblem([combo('a', nested('a'))]) ?? '', /reach itself/)
  assert.match(nestedProblem([combo('a', nested('b')), combo('b', nested('a'))]) ?? '', /reach itself/)
  assert.match(nestedProblem([combo('a', nested('missing'))]) ?? '', /“missing” is not a known model or Combo/)
  assert.equal(MAX_COMBO_DEPTH, 8)
  assert.equal(nestedProblem(chain(8)), undefined, 'a chain of eight is allowed')
  assert.match(nestedProblem(chain(9)) ?? '', /deeper than 8/, 'nine is not')
  // A diamond is not a cycle.
  assert.equal(nestedProblem([combo('top', nested('l'), nested('r')), combo('l', nested('base')), combo('r', nested('base')), combo('base', model('openai', 'gpt'))]), undefined)
})

test('only Combos that will save are offered as members', () => {
  const saved = [combo('a', model('openai', 'gpt')), combo('b', nested('a')), combo('c', model('openai', 'gpt'))]
  // Editing b (which contains a): a is already in, b is itself, c is free.
  assert.deepEqual(nestedCandidates('b', [nested('a')], saved), ['c'])
  // Editing a: b contains a, so adding b would be a cycle; c is free.
  assert.deepEqual(nestedCandidates('a', [model('openai', 'gpt')], saved), ['c'])
  // A new, unsaved Combo can take any of them.
  assert.deepEqual(nestedCandidates('fresh', [], saved), ['a', 'b', 'c'])
  // The draft, not the saved copy, decides: dropping a from b's draft frees it again.
  assert.deepEqual(nestedCandidates('b', [], saved), ['a', 'c'])
  // Depth: c1 .. c8 is a full chain, so a new parent over c1 would be nine deep.
  assert.deepEqual(nestedCandidates('top', [], chain(8)).includes('c1'), false)
  assert.deepEqual(nestedCandidates('top', [], chain(7)).includes('c1'), true)
})

test('a refused nested Combo says why', () => {
  const saved = [combo('a', model('openai', 'gpt')), combo('b', nested('a'))]
  assert.match(nestedRefusal('zzz', 'b', [nested('a')], saved), /not a known model, alias or Combo/)
  assert.equal(nestedRefusal('b', 'b', [nested('a')], saved), 'A Combo cannot contain itself.')
  assert.equal(nestedRefusal('a', 'b', [nested('a')], saved), 'That member is already in this Combo.')
  assert.match(nestedRefusal('b', 'a', [model('openai', 'gpt')], saved), /reach itself/)
})

const catalog = (combos: ComboRefs[]): CapabilityCatalog => ({
  models: new Map([['openai/gpt-vision', ['vision', 'tools']], ['gemini/flash', ['audio-input', 'vision']], ['claude/opus', ['pdf']], ['x/plain', []]]),
  aliases: new Map([['fast', { providerId: 'gemini', modelId: 'flash' }]]),
  combos: new Map(combos.map(entry => [entry.name, entry])),
})

test('a Combo is described by the union of what its selected members support', () => {
  const inner = combo('inner', model('claude', 'opus'))
  const outer = combo('outer', model('openai', 'gpt-vision'), nested('inner'))
  const all = catalog([inner, outer])
  assert.deepEqual(aggregateCapabilities(outer, all), ['vision', 'pdf'], 'in the fixed order, nested Combo included')
  assert.deepEqual(aggregateCapabilities(combo('viaAlias', model('gemini', 'fast')), all), ['vision', 'audio-input'])
  assert.deepEqual(aggregateCapabilities(combo('aliasNoProvider', model('', 'fast')), all), [], 'a member with no provider is a nested Combo, not an alias')
  assert.deepEqual(aggregateCapabilities(combo('off', { ...model('openai', 'gpt-vision'), selected: false }, model('x', 'plain')), all), [], 'a deselected member contributes nothing')
  assert.deepEqual(aggregateCapabilities(combo('unknown', model('nobody', 'nothing'), nested('ghost')), all), [])
  assert.deepEqual(aggregateCapabilities(combo('empty'), all), [])
})

test('capability aggregation stops at a cycle and at the depth limit', () => {
  const loop = [combo('a', nested('b'), model('claude', 'opus')), combo('b', nested('a'), model('openai', 'gpt-vision'))]
  assert.deepEqual(aggregateCapabilities(loop[0], catalog(loop)), ['vision', 'pdf'])
  const deep = chain(12)
  assert.deepEqual(aggregateCapabilities(deep[0], catalog(deep)), [], 'the leaf is beyond the depth limit')
})

test('Fusion tuning: a blank field keeps the default, the rest follow the server bounds', () => {
  assert.deepEqual(tuningBody(BLANK_TUNING), {})
  assert.deepEqual(tuningBody({ minPanel: '3', stragglerGraceMs: '0', panelHardTimeoutMs: '600000' }), { tuning: { minPanel: 3, stragglerGraceMs: 0, panelHardTimeoutMs: 600000 } })
  assert.deepEqual(tuningBody({ minPanel: ' 1 ', stragglerGraceMs: '', panelHardTimeoutMs: '' }), { tuning: { minPanel: 1 } }, '0 and 1 are accepted; the server stores 2')
  for (const bad of [{ minPanel: '-1' }, { minPanel: '2.5' }, { minPanel: 'two' }, { stragglerGraceMs: '-5' }, { stragglerGraceMs: String(MAX_FUSION_TUNING_MS + 1) }, { panelHardTimeoutMs: '0' }, { panelHardTimeoutMs: String(MAX_FUSION_TUNING_MS + 1) }, { panelHardTimeoutMs: '1e3' }]) {
    assert.ok(tuningBody({ ...BLANK_TUNING, ...bad }).problem, JSON.stringify(bad))
  }
  assert.deepEqual(FUSION_DEFAULTS, { minPanel: 2, stragglerGraceMs: 8000, panelHardTimeoutMs: 90000 }, 'the oracle FUSION_DEFAULTS (combo.js:472-476)')
})

test('saved tuning round-trips through the form, so editing a Combo does not erase it', () => {
  const saved = { minPanel: 3, stragglerGraceMs: 1500, panelHardTimeoutMs: 20000 }
  assert.deepEqual(tuningBody(tuningInput(saved)), { tuning: saved })
  assert.deepEqual(tuningBody(tuningInput({ stragglerGraceMs: 0 })), { tuning: { stragglerGraceMs: 0 } }, 'a zero grace is kept, not read as blank')
  assert.deepEqual(tuningBody(tuningInput(undefined)), {})
})
