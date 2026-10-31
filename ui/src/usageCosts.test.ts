import assert from 'node:assert/strict'
import { test } from 'node:test'
import { money, moneyFine } from './format.ts'

test('estimated cost keeps cents, and an absent cost is unknown rather than zero', () => {
  assert.equal(money(1.234), '$1.23')
  assert.equal(money(0), '$0.00')
  assert.equal(money(undefined), '—')
})

test('a sub-cent request keeps four decimals so it does not read as free', () => {
  assert.equal(moneyFine(0.0075), '$0.0075')
  assert.equal(moneyFine(0.01), '$0.01')
  assert.equal(moneyFine(2), '$2.00')
  assert.equal(moneyFine(undefined), '—')
})
