import assert from 'node:assert/strict'
import { test } from 'node:test'
import { inFlight, topologyNodes, type ActiveRequest, type RecentRequest } from './usageTopology.ts'

const active = (provider: string, model: string, account: string, count: number): ActiveRequest => ({ provider, model, account, count })
const recent = (provider: string, model: string, at = '2026-11-01T00:00:00Z'): RecentRequest => ({ timestamp: at, model, provider, promptTokens: 100, completionTokens: 20, status: 'success' })

test('topology nodes union live, recent and period activity, with live counts', () => {
  const nodes = topologyNodes(
    [active('openai', 'gpt-5', 'work', 2), active('openai', 'gpt-4.1', 'work', 1), active('anthropic', 'claude', 'home', 0)],
    [recent('google', 'gemini-3'), recent('openai', 'gpt-5')],
    [{ key: 'xai', requests: 3, inputTokens: 0, outputTokens: 0, errors: 0 }],
    'google',
  )
  assert.deepEqual(nodes.map(node => [node.provider, node.active, node.error]), [
    ['openai', 3, false],
    ['anthropic', 0, false],
    ['google', 0, true],
    ['xai', 0, false],
  ])
  assert.deepEqual(nodes[0].models, ['gpt-5', 'gpt-4.1'])
  assert.deepEqual(nodes[0].accounts, ['work'])
})

test('the node list is bounded and ignores rows with no provider', () => {
  const many = Array.from({ length: 12 }, (_, index) => active(`p${index}`, 'm', 'a', index))
  assert.equal(topologyNodes(many, [], [], '', 8).length, 8)
  assert.deepEqual(topologyNodes([active('', 'm', 'a', 1)], [recent('', 'm')], [], ''), [])
})

test('in-flight counts every active row', () => {
  assert.equal(inFlight([active('a', 'm', 'x', 2), active('b', 'm', 'y', 3)]), 5)
  assert.equal(inFlight([]), 0)
})
