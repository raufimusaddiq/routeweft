import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'

type Model = { providerId: string; id: string; name?: string; contextWindow?: number; capabilities: string[]; disabled: boolean }
type Node = { id: string; kind: string; providerId: string; name: string; baseUrl?: string; transports: string[] }
type Connection = { id: string; nodeId: string; providerId: string; name: string; enabled: boolean; priority: number }
type Props = { onUnauthorized: () => void }

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

// System One/Jev is a native typed transport (PRD-API-001, PRD §6 typesafe).
// This page configures the provider/model and exercises the typed request
// workflow through the existing session-gated model probe.
export function SystemOne({ onUnauthorized }: Props) {
  const [models, setModels] = useState<Model[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [providerID, setProviderID] = useState('')
  const [modelID, setModelID] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [newModel, setNewModel] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const paths = ['/systemone?page=1&pageSize=100', '/provider-nodes?page=1&pageSize=100', '/connections?page=1&pageSize=100']
      const responses = await Promise.all(paths.map(path => fetch(`/admin/v1${path}`, { credentials: 'same-origin' })))
      if (responses.some(response => response.status === 401)) { onUnauthorized(); return }
      const failed = responses.find(response => !response.ok)
      if (failed) throw new Error(await responseError(failed, 'Could not load System One configuration.'))
      const [modelData, nodeData, connectionData] = await Promise.all(responses.map(response => response.json())) as Array<{ items: unknown[] }>
      const loadedModels = modelData.items as Model[]
      setModels(loadedModels)
      setNodes(nodeData.items as Node[])
      setConnections(connectionData.items as Connection[])
      setProviderID(current => current && loadedModels.some(model => model.providerId === current) ? current : (loadedModels[0]?.providerId || ''))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load System One configuration.')
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  const providers = useMemo(() => Array.from(new Set([...models.map(model => model.providerId), ...nodes.filter(node => node.transports.includes('systemone')).map(node => node.providerId)])).sort(), [models, nodes])
  const providerModels = models.filter(model => model.providerId === providerID)
  const providerConnections = connections.filter(connection => connection.providerId === providerID)

  async function send(path: string, method: string, body?: unknown) {
    const response = await fetch(`/admin/v1${path}`, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
    if (response.status === 401) { onUnauthorized(); throw new Error('Admin session expired.') }
    if (!response.ok) throw new Error(await responseError(response, 'System One action failed.'))
    return response.status === 204 ? null : response.json()
  }

  async function run(id: string, message: string, callback: () => Promise<void>) {
    setBusy(id); setError(''); setNotice('')
    try { await callback(); if (message) setNotice(message); await load() }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'System One action failed.') }
    finally { setBusy('') }
  }

  async function addModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!providerID || !newModel.trim()) return
    await run('model', 'System One model added.', async () => { await send('/models', 'POST', { providerId: providerID, id: newModel.trim(), name: newModel.trim() }); setNewModel('') })
  }

  async function runTyped() {
    if (!providerID || !modelID) return
    const connection = providerConnections.find(item => item.enabled)
    if (!connection) { setError('Add an enabled connection for this System One provider first.'); return }
    await run('probe', '', async () => {
      const response = await send(`/connections/${encodeURIComponent(connection.id)}/test-models`, 'POST', { transport: 'systemone', modelIds: [modelID] }) as { results: Array<{ modelId: string; ok: boolean; status?: number; error?: string }> }
      const result = response.results[0]
      setNotice(result.ok ? `Typed request succeeded for ${result.modelId} (HTTP ${result.status}).` : `Typed request failed for ${result.modelId}: ${result.error || `HTTP ${result.status}`}.`)
    })
  }

  return <div className="systemone-content" aria-busy={loading}>
    <div className="systemone-toolbar"><label htmlFor="systemone-provider">System One provider</label><select id="systemone-provider" value={providerID} onChange={event => { setProviderID(event.target.value); setModelID('') }}>{providers.length === 0 && <option value="">No System One provider configured</option>}{providers.map(id => <option key={id} value={id}>{id}</option>)}</select><span className="count-label">{loading ? 'Loading…' : `${providers.length} provider${providers.length === 1 ? '' : 's'}`}</span><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    {providers.length === 0 && !loading && <section className="detail-panel"><h2>No System One provider yet</h2><p>Add a System One/Jev provider and connection under <a href="#providers">Providers</a>, then return here to exercise the typed request workflow.</p></section>}

    {providerID && <div className="systemone-layout">
      <section className="detail-panel" aria-labelledby="systemone-models-title">
        <div className="section-heading"><div><h2 id="systemone-models-title">Models</h2><p>Native System One/Jev models for {providerID}.</p></div></div>
        <form className="provider-inline-form" onSubmit={event => void addModel(event)}><input required aria-label="System One model ID" placeholder="Model ID" value={newModel} onChange={event => setNewModel(event.target.value)} /><button className="button-secondary" type="submit" disabled={busy === 'model'}>Add model</button></form>
        {providerModels.length === 0 ? <p className="empty-keys">No models configured for this provider.</p> : providerModels.map(model => <div className="model-row" key={model.id}><code>{model.id}</code><span>{model.name || '—'}</span><span className={`state-tag${model.disabled ? ' state-warn' : ''}`}>{model.disabled ? 'Disabled' : 'Available'}</span></div>)}
      </section>

      <section className="detail-panel" aria-labelledby="systemone-run-title">
        <div className="section-heading"><div><h2 id="systemone-run-title">Typed request workflow</h2><p>Runs a bounded System One/Jev request through the provider connection.</p></div></div>
        <div className="systemone-run">
          <label>Model<select aria-label="Typed request model" value={modelID} onChange={event => setModelID(event.target.value)}><option value="">Choose a model</option>{providerModels.filter(model => !model.disabled).map(model => <option key={model.id} value={model.id}>{model.id}</option>)}</select></label>
          <button className="button-primary" type="button" disabled={busy === 'probe' || !modelID || providerConnections.every(connection => !connection.enabled)} onClick={() => void runTyped()}>{busy === 'probe' ? 'Running…' : 'Run typed request'}</button>
        </div>
        <p className="count-label">{providerConnections.filter(connection => connection.enabled).length} enabled connection{providerConnections.filter(connection => connection.enabled).length === 1 ? '' : 's'} · posts to the configured typed endpoint</p>
      </section>
    </div>}
  </div>
}
