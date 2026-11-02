import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createElement, isValidElement, type ReactNode } from 'react'
import { ErrorBoundary, pageNameFromHash } from './ErrorBoundary.ts'

function textOf(node: ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(textOf).join('')
  if (isValidElement<{ children?: ReactNode }>(node)) return textOf(node.props.children)
  return ''
}

test('names the page from the hash route', () => {
  assert.equal(pageNameFromHash('#providers'), 'Providers')
  assert.equal(pageNameFromHash('#combo-capability-adapter'), 'Combo Capability Adapter')
  assert.equal(pageNameFromHash(''), 'Overview')
  assert.equal(pageNameFromHash('#'), 'Overview')
})

test('a render throw shows an alert naming the page instead of a blank app', () => {
  const boundary = new ErrorBoundary({ children: createElement('main', null, 'page') })
  assert.equal(ErrorBoundary.getDerivedStateFromError().failed, true)
  boundary.state = { failed: true, page: pageNameFromHash('#providers'), attempt: 0 }

  const tree = boundary.render()
  assert.ok(isValidElement<{ role?: string }>(tree))
  assert.equal(tree.props.role, 'alert')
  assert.match(textOf(tree), /The Providers page stopped rendering/)
  assert.match(textOf(tree), /Retry/)
  assert.doesNotMatch(textOf(tree), /boom/)
})
