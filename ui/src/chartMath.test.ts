import assert from 'node:assert/strict'
import { test } from 'node:test'
import { niceStep } from './chartMath.ts'

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
