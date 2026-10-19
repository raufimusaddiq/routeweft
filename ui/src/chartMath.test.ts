import assert from 'node:assert/strict'
import { test } from 'node:test'
import { niceStep, statusTone } from './chartMath.ts'

test('niceStep rounds up to 1, 2, 5 or 10 times a power of ten', () => {
  assert.equal(niceStep(0), 1)
  assert.equal(niceStep(1), 1)
  assert.equal(niceStep(1.2), 2)
  assert.equal(niceStep(2.5), 5)
  assert.equal(niceStep(7), 10)
  assert.equal(niceStep(23), 50)
  assert.equal(niceStep(120), 200)
  assert.equal(niceStep(Number.NaN), 1)
})

test('statusTone groups HTTP statuses by class', () => {
  assert.equal(statusTone('200'), 'good')
  assert.equal(statusTone('429'), 'warn')
  assert.equal(statusTone('503'), 'bad')
  assert.equal(statusTone('302'), 'neutral')
})
