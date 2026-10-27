import assert from 'node:assert/strict'
import { test } from 'node:test'
import { connectionProxyText } from './connectionProxy.ts'

test('a connection proxy is named with its bypass list', () => {
  assert.equal(connectionProxyText({ connectionProxy: { url: 'http://proxy.example:3128' } }), 'Connection proxy http://proxy.example:3128')
  assert.equal(connectionProxyText({ connectionProxy: { url: 'http://proxy.example:3128', noProxy: 'localhost,.internal' } }), 'Connection proxy http://proxy.example:3128 · bypassed for localhost,.internal')
})

test('a bound pool takes precedence over the connection proxy', () => {
  assert.equal(connectionProxyText({ proxyPoolId: 'pool-1', connectionProxy: { url: 'http://proxy.example:3128' } }), 'Connection proxy http://proxy.example:3128 · used when the pool has no usable member')
})

test('no connection proxy renders nothing', () => {
  assert.equal(connectionProxyText({}), '')
  assert.equal(connectionProxyText({ proxyPoolId: 'pool-1' }), '')
})
