import assert from 'node:assert/strict'
import test from 'node:test'
import { ACCOUNT_ID_PROVIDERS, emptySettings, missingSetting, providerDataFor, savedSettings, settingFields, settingsPatch } from './connectionSettings.ts'

test('only the providers that read a setting offer one', () => {
  assert.deepEqual(settingFields('azure').map(f => f.key), ['azureEndpoint', 'deployment', 'apiVersion', 'organization'])
  assert.deepEqual(settingFields('kimchi').map(f => f.key), ['kimchiEndpoint'])
  assert.deepEqual(settingFields('ollama-local').map(f => f.key), ['baseUrl'])
  assert.deepEqual(settingFields('openai'), [])
  assert.deepEqual([...ACCOUNT_ID_PROVIDERS], ['cloudflare-ai'])
})

test('a new Azure connection starts with the pin\'s API version and needs an endpoint', () => {
  const values = emptySettings('azure')
  assert.equal(values.apiVersion, '2024-10-01-preview')
  assert.equal(missingSetting('azure', values), 'Azure endpoint')
  assert.equal(missingSetting('azure', { ...values, azureEndpoint: ' https://r.openai.azure.com ' }), '')
  assert.equal(missingSetting('kimchi', emptySettings('kimchi')), '')
})

test('saved data holds trimmed non-empty values only', () => {
  assert.deepEqual(providerDataFor('azure', { azureEndpoint: ' https://r.openai.azure.com ', deployment: '', apiVersion: '2025-01-01', organization: '  ' }), { azureEndpoint: 'https://r.openai.azure.com', apiVersion: '2025-01-01' })
  assert.deepEqual(providerDataFor('openai', { baseUrl: 'https://x' }), {})
})

test('an edit sends only what changed, and an emptied field as cleared', () => {
  const saved = { azureEndpoint: 'https://r.openai.azure.com', deployment: 'a', apiVersion: '2024-10-01-preview' }
  const shown = savedSettings('azure', saved)
  assert.deepEqual(shown, { azureEndpoint: 'https://r.openai.azure.com', deployment: 'a', apiVersion: '2024-10-01-preview', organization: '' })
  assert.deepEqual(settingsPatch('azure', shown, saved), {})
  assert.deepEqual(settingsPatch('azure', { ...shown, deployment: 'b', apiVersion: '' }, saved), { deployment: 'b', apiVersion: '' })
})
