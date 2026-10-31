// Settings a connection holds besides its credential, for the providers whose requests or model
// listing go to a host or a name the operator chooses (#228). They are saved with the connection,
// shown again when it is edited, and read by the server on every request; none is a credential.
// Cloudflare's account ID is the one that is a connection field of its own, not provider data.

export type SettingField = { key: string; label: string; placeholder?: string; required?: boolean; defaultValue?: string; hint?: string }

export const SETTING_FIELDS: Record<string, SettingField[]> = {
  azure: [
    { key: 'azureEndpoint', label: 'Azure endpoint', placeholder: 'https://my-resource.openai.azure.com', required: true },
    { key: 'deployment', label: 'Deployment name', placeholder: 'defaults to the model name', hint: 'Left blank, the model name is the deployment.' },
    { key: 'apiVersion', label: 'API version', placeholder: '2024-10-01-preview', defaultValue: '2024-10-01-preview' },
    { key: 'organization', label: 'Organization (optional)' },
  ],
  kimchi: [
    { key: 'kimchiEndpoint', label: 'Catalog host (optional)', placeholder: 'https://llm.kimchi.dev', hint: 'Only the model list is read from here.' },
  ],
  'ollama-local': [
    { key: 'baseUrl', label: 'Ollama host URL', placeholder: 'http://localhost:11434', hint: 'A private address is only reached when the operator has turned on trusted-local upstreams.' },
  ],
}

/** Providers whose connection needs the account ID of the URL they are served under. */
export const ACCOUNT_ID_PROVIDERS: readonly string[] = ['cloudflare-ai']

export function settingFields(providerId: string): SettingField[] {
  return SETTING_FIELDS[providerId] ?? []
}

export function emptySettings(providerId: string): Record<string, string> {
  return Object.fromEntries(settingFields(providerId).map(field => [field.key, field.defaultValue ?? '']))
}

/** The values to prefill an edit with: what is saved, else the field's default for display only. */
export function savedSettings(providerId: string, saved: Record<string, string> | undefined): Record<string, string> {
  return Object.fromEntries(settingFields(providerId).map(field => [field.key, saved?.[field.key] ?? '']))
}

/** The provider data a new connection is saved with: every field that has a value, trimmed. */
export function providerDataFor(providerId: string, values: Record<string, string>): Record<string, string> {
  const data: Record<string, string> = {}
  for (const field of settingFields(providerId)) {
    const value = (values[field.key] ?? '').trim()
    if (value) data[field.key] = value
  }
  return data
}

/**
 * The provider data an edit sends: only the fields that changed, so the others stay as saved.
 * A field emptied by the operator is sent empty, which the server stores as cleared.
 */
export function settingsPatch(providerId: string, values: Record<string, string>, saved: Record<string, string> | undefined): Record<string, string> {
  const patch: Record<string, string> = {}
  for (const field of settingFields(providerId)) {
    const next = (values[field.key] ?? '').trim()
    if (next !== (saved?.[field.key] ?? '').trim()) patch[field.key] = next
  }
  return patch
}

export function missingSetting(providerId: string, values: Record<string, string>): string {
  const field = settingFields(providerId).find(item => item.required && !(values[item.key] ?? '').trim())
  return field ? field.label : ''
}
