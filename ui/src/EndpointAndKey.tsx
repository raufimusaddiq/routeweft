import { FormEvent, useCallback, useEffect, useState } from 'react'

type APIKey = { id: string; name: string; prefix: string; enabled: boolean; paused: boolean; createdAt: string; lastUsedAt?: string }
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

export function EndpointAndKey({ onUnauthorized }: Props) {
  const [keys, setKeys] = useState<APIKey[]>([])
  const [total, setTotal] = useState(0)
  const [keyPage, setKeyPage] = useState(1)
  const [requireApiKey, setRequireApiKey] = useState(true)
  const [loading, setLoading] = useState(true)
  const [settingBusy, setSettingBusy] = useState(false)
  const [busyID, setBusyID] = useState('')
  const [newName, setNewName] = useState('')
  const [secret, setSecret] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [copyState, setCopyState] = useState('')
  const baseURL = `${location.origin}/v1`

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
    setBusyID('create')
    setError('')
    setNotice('')
    try {
      const response = await fetch('/admin/v1/keys', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      })
      if (response.status === 401) return onUnauthorized()
      if (!response.ok) throw new Error(await responseError(response, 'Could not create API key.'))
      const result = await response.json() as { secret: string }
      setSecret(result.secret)
      setNewName('')
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
      if (!response.ok) throw new Error(await responseError(response, 'Could not update API key.'))
      setNotice(`“${key.name}” ${paused ? 'paused' : 'resumed'}.`)
      setKeys(current => current.map(item => item.id === key.id ? { ...item, paused } : item))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not update API key.')
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
      if (!response.ok) throw new Error(await responseError(response, 'Could not revoke API key.'))
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
        </form>

        {loading && keys.length === 0 ? <p className="page-status" role="status">Loading API keys…</p> : keys.length === 0 ? <p className="empty-keys">No API keys yet. Create a named key to connect a client.</p> : (
          <div className="key-list" aria-label="API keys">
            {keys.map(key => <article className="key-row" key={key.id}>
              <div className="key-main"><h3>{key.name}</h3><code>{key.prefix}••••••••</code></div>
              <div className="key-status"><span className={`state-tag${key.enabled && !key.paused ? ' state-good' : ' state-warn'}`}>{!key.enabled ? 'Disabled' : key.paused ? 'Paused' : 'Active'}</span><span>Last used: {key.lastUsedAt ? new Date(key.lastUsedAt).toLocaleString() : 'Never'}</span></div>
              <div className="key-actions"><button className="button-secondary" type="button" disabled={busyID !== ''} onClick={() => void setPaused(key, !key.paused)}>{key.paused ? 'Resume' : 'Pause'}</button><button className="button-danger" type="button" disabled={busyID !== ''} onClick={() => void revoke(key)}>Revoke</button></div>
            </article>)}
          </div>
        )}

        {total > pageSize && <nav className="pagination" aria-label="API key pages"><button className="button-secondary" type="button" disabled={keyPage <= 1 || loading} onClick={() => void load(keyPage - 1)}>Previous</button><span>Page {keyPage} of {Math.ceil(total / pageSize)}</span><button className="button-secondary" type="button" disabled={keyPage >= Math.ceil(total / pageSize) || loading} onClick={() => void load(keyPage + 1)}>Next</button></nav>}
      </section>
    </div>
  )
}
