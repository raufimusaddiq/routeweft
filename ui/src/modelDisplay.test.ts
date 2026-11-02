import { test } from 'node:test'
import assert from 'node:assert/strict'
import { modelFacts } from './modelDisplay.ts'

test('a model row with null capabilities and no context window renders its facts', () => {
  assert.equal(modelFacts({ capabilities: null }), '—')
})

test('an empty capability list reads as an em dash, not an empty string', () => {
  assert.equal(modelFacts({ capabilities: [] }), '—')
})

test('a known context window still wins, and capabilities join as before', () => {
  assert.equal(modelFacts({ contextWindow: 128000, capabilities: null }), '128,000 tokens')
  assert.equal(modelFacts({ capabilities: ['vision', 'pdf'] }), 'vision, pdf')
})
