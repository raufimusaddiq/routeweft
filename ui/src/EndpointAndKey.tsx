import { FormEvent, useCallback, useEffect, useState } from 'react'
import { AccessPicker, useAccessCatalog } from './KeyAccess'
import { accessBody, describeFailure, sameAccess, type AccessState } from './keyAccess'

type APIKey = { id: string; name: string; prefix: string; enabled: boolean; paused: boolean; createdAt: string; lastUsedAt?: string; access: AccessState }
type Props = { onUnauthorized: () => void }

const pageSize = 50

async function responseError(response: Response, fallback: string) {
  try {
    const body = await response.json() as { error?: { message?: string } }
    return body.error?.message || fallback
  } catch {
    return fallback
  }
}

async function failure(action: string, response: Response) {
  return describeFailure(action, response.status, await responseError(response, ''))
}

const unrestricted: AccessState = { restricted: false, allow: [] }

export function EndpointAndKey({ onUnauthorized }: Props) {
  const [keys, setKeys] = useState<APIKey[]>([])
  const [total, setTotal] = useState(0)
  const [keyPage, setKeyPage] = useState(1)
  const [requireApiKey, setRequireApiKey] = useState(true)
  const [loading, setLoading] = useState(true)
  const [settingBusy, setSettingBusy] = useState(false)
  const [busyID, setBusyID] = useState('')
  const [newName, setNewName] = useState('')
  const [newAccess, setNewAccess] = useState<AccessState>(unrestricted)
  const [editingID, setEditingID] = useState('')
  const [draft, setDraft] = useState<AccessState>(unrestricted)
  const [editError, setEditError] = useState('')
  const [secret, setSecret] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [copyState, setCopyState] = useState('')
  const baseURL = `${location.origin}/v1`
  const accessCatalog = useAccessCatalog(newAccess.restricted || editingID !== '', onUnauthorized)

  const load = useCallback(async (requestedPage = 1) => {
    setLoading(true)
    setError('')
    try {
      const [keyResponse, settingsResponse] = await Promise.all([
        fetch(`/admin/v1/keys?page=${requestedPage}&pageSize=${pageSize}`, { credentials: 'same-origin' }),
        fetch('/admin/v1/settings', { credentials: 'same-origin' }),
      ])
      if (keyResponse.status === 401 || settingsResponse.status === 401) {
        onUnauthorized()
        return
      }
      if (!keyResponse.ok) throw new Error(await responseError(keyResponse, 'Could not load API keys.'))
      if (!settingsResponse.ok) throw new Error(await responseError(settingsResponse, 'Could not load API settings.'))
      const [keyData, settingsData] = await Promise.all([
        keyResponse.json() as Promise<{ items: APIKey[]; total: number }>,
        settingsResponse.json() as Promise<{ settings: Record<string, string>; writable: string[] }>,
      ])
      setKeys(keyData.items)
      setTotal(keyData.total)
      setKeyPage(requestedPage)
      setRequireApiKey(settingsData.settings.requireApiKey !== 'false')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load Endpoint & Key.')
    } finally {
      setLoading(false)
    }
  }, [onUnauthorized])

  useEffect(() => { void load(1) }, [load])

  async function copyText(value: string, target: string) {
    try {
      await navigator.clipboard.writeText(value)
      setCopyState(target)
      setNotice('Copied to clipboard.')
    } catch {
      setCopyState('')
      setNotice('Clipboard unavailable. Select and copy the value directly.')
    }
  }

  async function toggleRequirement(enabled: boolean) {
    if (!enabled && !window.confirm('Disabling API-key enforcement allows inference requests without a Routeweft key. Continue?')) return
    setSettingBusy(true)
    setError('')
    setNotice('')
    try {
      const response = await fetch('/admin/v1/settings', {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ set: { requireApiKey: String(enabled) } }),
      })
      if (response.status === 401) return onUnauthorized()
      if (!response.ok) throw new Error(await responseError(response, 'Could not update API-key enforcement.'))
      setRequireApiKey(enabled)
      setNotice(`API-key enforcement ${enabled ? 'enabled' : 'disabled'}.`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not update API-key enforcement.')
    } finally {
      setSettingBusy(false)
    }
  }

  async function createKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = newName.trim()
    if (!name) return
    if (newAccess.restricted && newAccess.allow.length === 0 && !window.confirm('No models are selected, so this key will not be able to call any model. Create it anyway?')) return
    setBusyID('create')
    setError('')
    setNotice('')
    try {
      const response = await fetch('/admin/v1/keys', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, access: accessBody(newAccess) }),
      })
      if (response.status === 401) return onUnauthorized()
      if (!response.ok) throw new Error(await failure('Could not create API key', response))
      const result = await response.json() as { secret: string }
      setSecret(result.secret)
      setNewName('')
      setNewAccess(unrestricted)
      setNotice('Key created. Copy it now; it will not be shown again.')
      await load(1)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not create API key.')
    } finally {
      setBusyID('')
    }
  }

  async function setPaused(key: APIKey, paused: boolean) {
    setBusyID(key.id)
    setError('')
    setNotice('')
    try {
      const response = await fetch(`/admin/v1/keys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ paused }),
      })
      if (response.status === 401) return onUnauthorized()
      if (!response.ok) throw new Error(await failure('Could not update API key', response))
      const stored = (await response.json() as { key: APIKey }).key
      setNotice(`“${key.name}” ${paused ? 'paused' : 'resumed'}.`)
      setKeys(current => current.map(item => item.id === key.id ? { ...item, ...stored } : item))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not update API key.')
    } finally {
      setBusyID('')
    }
  }

  function openEditor(key: APIKey) {
    setEditingID(key.id)
    setDraft({ restricted: key.access.restricted, allow: [...key.access.allow] })
    setEditError('')
  }

  async function saveAccess(key: APIKey) {
    if (key.access.restricted && !draft.restricted && !window.confirm(`“${key.name}” will be able to use every model, alias and Combo. Make it unrestricted?`)) return
    if (draft.restricted && draft.allow.length === 0 && !window.confirm('No models are selected, so this key will not be able to call any model. Save anyway?')) return
    setBusyID(key.id)
    setEditError('')
    setNotice('')
    try {
      const response = await fetch(`/admin/v1/keys/${encodeURIComponent(key.id)}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ access: accessBody(draft) }),
      })
      if (response.status === 401) return onUnauthorized()
      if (!response.ok) throw new Error(await failure('Could not save access', response))
      // Show what the server stored, not what was sent.
      const stored = (await response.json() as { key: APIKey }).key
      setKeys(current => current.map(item => item.id === key.id ? { ...item, ...stored } : item))
      setNotice(`Access for “${key.name}” saved: ${stored.access.restricted ? `restricted to ${stored.access.allow.length} ${stored.access.allow.length === 1 ? 'entry' : 'entries'}` : 'unrestricted'}.`)
      setEditingID('')
    } catch (cause) {
      setEditError(cause instanceof Error ? cause.message : 'Could not save access.')
    } finally {
      setBusyID('')
    }
  }

  async function revoke(key: APIKey) {
    if (!window.confirm(`Revoke “${key.name}”? Requests using it will stop authenticating.`)) return
    setBusyID(key.id)
    setError('')
    setNotice('')
    try {
      const response = await fetch(`/admin/v1/keys/${encodeURIComponent(key.id)}`, { method: 'DELETE', credentials: 'same-origin' })
      if (response.status === 401) return onUnauthorized()
      if (!response.ok) throw new Error(await failure('Could not revoke API key', response))
      setNotice(`“${key.name}” revoked.`)
      const nextPage = keys.length === 1 && keyPage > 1 ? keyPage - 1 : keyPage
      setTotal(current => Math.max(0, current - 1))
      if (nextPage !== keyPage) {
        await load(nextPage)
      } else {
        setKeys(current => current.filter(item => item.id !== key.id))
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not revoke API key.')
    } finally {
      setBusyID('')
    }
  }

  return (
    <div className="endpoint-content" aria-busy={loading}>
      {error && <p className="auth-error" role="alert">{error}</p>}
      {notice && <p className="inline-notice" role="status">{notice}</p>}

      <section className="detail-panel endpoint-panel" aria-labelledby="endpoint-title">
        <div className="panel-heading"><h2 id="endpoint-title">Base endpoint</h2><span className="state-tag">OpenAI-compatible</span></div>
        <p className="endpoint-intro">Use this URL with OpenAI-compatible clients. Provider and protocol-specific paths are listed below.</p>
        <div className="copy-field"><code>{baseURL}</code><button className="button-secondary" type="button" onClick={() => void copyText(baseURL, 'endpoint')}>{copyState === 'endpoint' ? 'Copied' : 'Copy URL'}</button></div>
        <h3 className="section-label">Transport examples</h3>
        <div className="transport-grid">
          <div><span>Chat Completions</span><code>POST /v1/chat/completions</code></div>
          <div><span>Responses</span><code>POST /v1/responses</code></div>
          <div><span>Messages</span><code>POST /v1/messages</code></div>
          <div><span>Gemini compatibility</span><code>POST /v1beta/models/{'{model}'}:generateContent</code></div>
          <div><span>Ollama compatibility</span><code>POST /v1/api/chat</code></div>
          <div><span>System One</span><code>POST /v1/systemone</code></div>
        </div>
      </section>

      <section className="detail-panel enforcement-panel" aria-labelledby="enforcement-title">
        <div><h2 id="enforcement-title">Require an API key</h2><p>Reject inference requests unless a valid Routeweft key is provided.</p></div>
        <label className="switch-control"><span className="switch-label">{requireApiKey ? 'Required' : 'Not required'}</span><input type="checkbox" role="switch" checked={requireApiKey} disabled={settingBusy || loading} onChange={event => void toggleRequirement(event.target.checked)} aria-label="Require an API key for inference" /></label>
      </section>

      <section className="keys-section" aria-labelledby="keys-title">
        <div className="section-heading"><div><h2 id="keys-title">Client API keys</h2><p>Create named keys. The full value is shown once.</p></div><span className="count-label">{loading ? 'Loading…' : `${new Intl.NumberFormat().format(total)} total`}</span></div>

        {secret && <section className="secret-panel" aria-labelledby="secret-title" role="status">
          <div className="panel-heading"><div><h3 id="secret-title">Copy your new key</h3><p>Routeweft will not show this secret again.</p></div><button className="button-ghost" type="button" onClick={() => { setSecret(''); setCopyState('') }}>Done</button></div>
          <div className="copy-field"><input aria-label="New API key secret" readOnly value={secret} onFocus={event => event.currentTarget.select()} /><button className="button-primary" type="button" onClick={() => void copyText(secret, 'secret')}>{copyState === 'secret' ? 'Copied' : 'Copy key'}</button></div>
        </section>}

        <form className="create-key-form" onSubmit={event => void createKey(event)}>
          <label htmlFor="new-key-name">New key name</label>
          <div><input id="new-key-name" autoComplete="off" maxLength={120} placeholder="e.g. production service" value={newName} onChange={event => setNewName(event.target.value)} /><button className="button-primary" type="submit" disabled={busyID === 'create' || !newName.trim()}>{busyID === 'create' ? 'Creating…' : 'Create key'}</button></div>
          <AccessPicker idPrefix="new-key" value={newAccess} catalog={accessCatalog} disabled={busyID === 'create'} onChange={setNewAccess} />
        </form>

        {loading && keys.length === 0 ? <p className="page-status" role="status">Loading API keys…</p> : keys.length === 0 ? <p className="empty-keys">No API keys yet. Create a named key to connect a client.</p> : (
          <div className="key-list" aria-label="API keys">
            {keys.map(key => <article className="key-row" key={key.id}>
              <div className="key-main"><h3>{key.name}</h3><code>{key.prefix}••••••••</code></div>
              <div className="key-status"><span className={`state-tag${key.enabled && !key.paused ? ' state-good' : ' state-warn'}`}>{!key.enabled ? 'Disabled' : key.paused ? 'Paused' : 'Active'}</span><span className="state-tag">{key.access.restricted ? `Restricted · ${key.access.allow.length}` : 'Unrestricted'}</span><span>Last used: {key.lastUsedAt ? new Date(key.lastUsedAt).toLocaleString() : 'Never'}</span></div>
              <div className="key-actions"><button className="button-secondary" type="button" aria-expanded={editingID === key.id} disabled={busyID !== ''} onClick={() => editingID === key.id ? setEditingID('') : openEditor(key)}>Access</button><button className="button-secondary" type="button" disabled={busyID !== ''} onClick={() => void setPaused(key, !key.paused)}>{key.paused ? 'Resume' : 'Pause'}</button><button className="button-danger" type="button" disabled={busyID !== ''} onClick={() => void revoke(key)}>Revoke</button></div>
              {editingID === key.id && <form className="key-access-editor" aria-label={`Access for ${key.name}`} onSubmit={event => { event.preventDefault(); void saveAccess(key) }}>
                <AccessPicker idPrefix={`key-${key.id}`} value={draft} catalog={accessCatalog} disabled={busyID === key.id} onChange={setDraft} />
                {editError && <p className="auth-error" role="alert">{editError}</p>}
                <div className="key-access-actions"><button className="button-primary" type="submit" disabled={busyID === key.id || sameAccess(draft, key.access)}>{busyID === key.id ? 'Saving…' : 'Save access'}</button><button className="button-ghost" type="button" disabled={busyID === key.id} onClick={() => setEditingID('')}>Cancel</button></div>
              </form>}
            </article>)}
          </div>
        )}

        {total > pageSize && <nav className="pagination" aria-label="API key pages"><button className="button-secondary" type="button" disabled={keyPage <= 1 || loading} onClick={() => void load(keyPage - 1)}>Previous</button><span>Page {keyPage} of {Math.ceil(total / pageSize)}</span><button className="button-secondary" type="button" disabled={keyPage >= Math.ceil(total / pageSize) || loading} onClick={() => void load(keyPage + 1)}>Next</button></nav>}
      </section>
    </div>
  )
}
