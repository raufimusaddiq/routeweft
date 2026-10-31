import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { PAGE_SIZE, Pager, fetchAll } from './ProviderPaging'

type Model = { providerId: string; id: string; name?: string; contextWindow?: number; capabilities: string[]; disabled: boolean }
type Node = { id: string; kind: string; providerId: string; name: string; prefix?: string; baseUrl?: string; transports: string[] }
type Connection = { id: string; nodeId: string; providerId: string; name: string; identity?: string; accountId?: string; enabled: boolean; priority: number }
type Props = { onUnauthorized: () => void }

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

/** The System One endpoint the oracle's "Add System One Provider" form starts from (AddCompatibleModal.js:33). */
export const DEFAULT_SYSTEMONE_URL = 'https://api.typesafe.ai/v1/systemone'
/** A custom System One node takes at most 64 model ids (provider-nodes/route.js:123-125). */
export const MAX_NODE_MODELS = 64
/** Cloudflare Workers AI serves System One under an account, so its connection needs the account ID (wire/cloudflare.go). */
const ACCOUNT_ID_PROVIDERS = new Set(['cloudflare-ai'])

/** One model id per line: trimmed, blanks dropped, duplicates removed in order. */
export function parseModelLines(text: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const line of text.split('\n')) {
    const id = line.trim()
    if (id && !seen.has(id)) { seen.add(id); out.push(id) }
  }
  return out
}

const EMPTY_NODE = { name: '', prefix: '', baseUrl: DEFAULT_SYSTEMONE_URL, models: '' }
const EMPTY_ACCOUNT = { name: '', identity: '', apiKey: '', accountId: '' }

// System One/Jev is a native typed transport (PRD-API-001, PRD §6 typesafe).
// This page configures the provider/model/account and exercises the typed request
// workflow through the existing session-gated model probe. Every list is read
// whole (all pages, so nothing past the first 100 is missing) and shown a page at a time.
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
  const [modelPage, setModelPage] = useState(1)
  const [accountPage, setAccountPage] = useState(1)
  const [nodeForm, setNodeForm] = useState(EMPTY_NODE)
  const [accountForm, setAccountForm] = useState(EMPTY_ACCOUNT)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [loadedModels, loadedNodes, loadedConnections] = await Promise.all([
        fetchAll<Model>('systemone', onUnauthorized),
        fetchAll<Node>('provider-nodes', onUnauthorized),
        fetchAll<Connection>('connections', onUnauthorized),
      ])
      if (!loadedModels || !loadedNodes || !loadedConnections) return
      setModels(loadedModels)
      setNodes(loadedNodes)
      setConnections(loadedConnections)
      setProviderID(current => current && (loadedModels.some(model => model.providerId === current) || loadedNodes.some(node => node.providerId === current && node.transports.includes('systemone'))) ? current : (loadedModels[0]?.providerId || loadedNodes.find(node => node.transports.includes('systemone'))?.providerId || ''))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load System One configuration.')
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  const providers = useMemo(() => Array.from(new Set([...models.map(model => model.providerId), ...nodes.filter(node => node.transports.includes('systemone')).map(node => node.providerId)])).sort(), [models, nodes])
  const providerModels = models.filter(model => model.providerId === providerID)
  const providerConnections = connections.filter(connection => connection.providerId === providerID)
  const providerNode = nodes.find(node => node.providerId === providerID && node.transports.includes('systemone'))
  const needsAccountID = ACCOUNT_ID_PROVIDERS.has(providerID)

  // A page past the end (after a delete, or a provider with fewer rows) falls back to the last one.
  const modelPages = Math.max(1, Math.ceil(providerModels.length / PAGE_SIZE))
  const accountPages = Math.max(1, Math.ceil(providerConnections.length / PAGE_SIZE))
  const shownModelPage = Math.min(modelPage, modelPages)
  const shownAccountPage = Math.min(accountPage, accountPages)
  const visibleModels = providerModels.slice((shownModelPage - 1) * PAGE_SIZE, shownModelPage * PAGE_SIZE)
  const visibleConnections = providerConnections.slice((shownAccountPage - 1) * PAGE_SIZE, shownAccountPage * PAGE_SIZE)

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

  // Custom typed-node onboarding (provider-nodes/route.js:93-127): a name, a prefix, the full
  // endpoint URL and up to 64 model ids. The server applies the same URL policy as every provider
  // (an allowed HTTP(S) origin) and rejects a prefix that is already in use.
  async function addProvider(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = nodeForm.name.trim()
    const prefix = nodeForm.prefix.trim()
    const baseUrl = nodeForm.baseUrl.trim()
    const ids = parseModelLines(nodeForm.models)
    if (!name || !prefix || !baseUrl) return
    if (ids.length > MAX_NODE_MODELS) { setError(`A System One provider takes at most ${MAX_NODE_MODELS} models here; ${ids.length} were given. Add the rest one at a time under Models.`); setNotice(''); return }
    await run('provider', '', async () => {
      const node = await send('/provider-nodes', 'POST', { kind: 'generic', name, prefix, baseUrl, transports: ['systemone'] }) as Node
      setProviderID(node.providerId)
      let added = 0
      try {
        for (const id of ids) { await send('/models', 'POST', { providerId: node.providerId, id, name: id }); added += 1 }
      } catch (cause) {
        // The provider exists; say how far the models got rather than hide it.
        setNodeForm(EMPTY_NODE)
        throw new Error(`System One provider ${node.providerId} was added, but model ${ids[added]} could not be added (${added} of ${ids.length} models saved): ${cause instanceof Error ? cause.message : 'System One action failed.'}`)
      }
      setNodeForm(EMPTY_NODE)
      setNotice(`System One provider ${node.providerId} added with ${ids.length} model${ids.length === 1 ? '' : 's'}.`)
    })
  }

  // Account onboarding: a name, the account's identity (its ID with the provider), the API key and,
  // for a provider that serves System One under an account (Cloudflare), the account ID the URL needs.
  async function addAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!providerNode) return
    const name = accountForm.name.trim()
    const identity = accountForm.identity.trim()
    const accountId = accountForm.accountId.trim()
    if (!name || !identity || !accountForm.apiKey || (needsAccountID && !accountId)) return
    await run('account', `Account ${name} added to ${providerID}.`, async () => {
      await send('/connections', 'POST', { providerId: providerID, nodeId: providerNode.id, name, identity, authKind: 'api-key', secret: { accessToken: accountForm.apiKey }, enabled: true, priority: providerConnections.length, ...(accountId ? { accountId } : {}) })
      setAccountForm(EMPTY_ACCOUNT)
    })
  }

  async function runTyped() {
    if (!providerID || !modelID) return
    const connection = providerConnections.find(item => item.enabled)
    if (!connection) { setError('Add an enabled connection for this System One provider first.'); return }
    await run('probe', '', async () => {
      const response = await send(`/connections/${encodeURIComponent(connection.id)}/test-models`, 'POST', { transport: 'systemone', modelIds: [modelID] }) as { results?: Array<{ modelId: string; ok: boolean; status?: number; error?: string }> } | null
      const result = response?.results?.[0]
      if (!result) { setError('The typed request returned no result.'); return }
      setNotice(result.ok ? `Typed request succeeded for ${result.modelId} (HTTP ${result.status}).` : `Typed request failed for ${result.modelId}: ${result.error || `HTTP ${result.status}`}.`)
    })
  }

  const enabledCount = providerConnections.filter(connection => connection.enabled).length

  return <div className="systemone-content page-d" aria-busy={loading}>
    <div className="systemone-toolbar"><label htmlFor="systemone-provider">System One provider</label><select id="systemone-provider" value={providerID} onChange={event => { setProviderID(event.target.value); setModelID(''); setModelPage(1); setAccountPage(1) }}>{providers.length === 0 && <option value="">No System One provider configured</option>}{providers.map(id => <option key={id} value={id}>{id}</option>)}</select><span className="count-label">{loading ? 'Loading…' : `${providers.length} provider${providers.length === 1 ? '' : 's'}`}</span><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    {providers.length === 0 && !loading && <section className="detail-panel"><h2>No System One provider yet</h2><p>Add a System One/Jev provider below, or under <a href="#providers">Providers</a>, then add an account to exercise the typed request workflow.</p></section>}

    {providerID && <div className="systemone-layout">
      <section className="detail-panel" aria-labelledby="systemone-models-title">
        <div className="section-heading"><div><h2 id="systemone-models-title">Models</h2><p>Native System One/Jev models for {providerID}.</p></div></div>
        <form className="provider-inline-form" onSubmit={event => void addModel(event)}><input required aria-label="System One model ID" placeholder="Model ID" value={newModel} onChange={event => setNewModel(event.target.value)} /><button className="button-secondary" type="submit" disabled={busy === 'model'}>Add model</button></form>
        {providerModels.length === 0 ? <p className="empty-keys">No models configured for this provider.</p> : visibleModels.map(model => <div className="model-row" key={model.id}><code>{model.id}</code><span>{model.name || '—'}</span><span className={`state-tag${model.disabled ? ' state-warn' : ''}`}>{model.disabled ? 'Disabled' : 'Enabled'}</span></div>)}
        <Pager label="System One models" page={shownModelPage} total={providerModels.length} onPage={setModelPage} />
      </section>

      <section className="detail-panel accent-surface" aria-labelledby="systemone-run-title">
        <div className="section-heading"><div><h2 id="systemone-run-title">Typed request workflow</h2><p>Runs a bounded System One/Jev request through the provider connection.</p></div></div>
        <div className="systemone-run">
          <label>Model<select aria-label="Typed request model" value={modelID} onChange={event => setModelID(event.target.value)}><option value="">Choose a model</option>{providerModels.filter(model => !model.disabled).map(model => <option key={model.id} value={model.id}>{model.id}</option>)}</select></label>
          <button className="button-primary" type="button" disabled={busy === 'probe' || !modelID || enabledCount === 0} onClick={() => void runTyped()}>{busy === 'probe' ? 'Running…' : 'Run typed request'}</button>
        </div>
        <p className="count-label">{enabledCount} enabled connection{enabledCount === 1 ? '' : 's'} · posts to the configured typed endpoint</p>
      </section>

      <section className="detail-panel" aria-labelledby="systemone-accounts-title">
        <div className="section-heading"><div><h2 id="systemone-accounts-title">Accounts</h2><p>Connections that serve {providerID}; a request uses the first enabled one.</p></div></div>
        {providerConnections.length === 0 ? <p className="empty-keys">No accounts yet. Add one below to run a typed request.</p> : <ul className="systemone-accounts" aria-label={`${providerID} accounts`}>{visibleConnections.map(connection => <li className="systemone-account" key={connection.id}><strong>{connection.name}</strong>{connection.identity && <code>{connection.identity}</code>}{connection.accountId && <span className="count-label">account ID {connection.accountId}</span>}<span className={`state-tag${connection.enabled ? '' : ' state-warn'}`}>{connection.enabled ? 'Enabled' : 'Disabled'}</span></li>)}</ul>}
        <Pager label="System One accounts" page={shownAccountPage} total={providerConnections.length} onPage={setAccountPage} />
        {providerNode ? <form className="provider-form" aria-label="Add a System One account" onSubmit={event => void addAccount(event)}>
          <div className="provider-form-grid">
            <label>Account name<input required value={accountForm.name} onChange={event => setAccountForm({ ...accountForm, name: event.target.value })} /></label>
            <label>Account identity<input required autoComplete="off" value={accountForm.identity} onChange={event => setAccountForm({ ...accountForm, identity: event.target.value })} /></label>
            {needsAccountID && <label>Cloudflare account ID<input required autoComplete="off" spellCheck={false} value={accountForm.accountId} onChange={event => setAccountForm({ ...accountForm, accountId: event.target.value })} /><small className="count-label">One path segment: letters, digits, hyphen or underscore.</small></label>}
            <label className="wide-field">API key<input required type="password" autoComplete="new-password" value={accountForm.apiKey} onChange={event => setAccountForm({ ...accountForm, apiKey: event.target.value })} /></label>
          </div>
          <div className="oauth-actions"><button className="button-secondary" type="submit" disabled={busy === 'account'}>{busy === 'account' ? 'Adding…' : 'Add account'}</button></div>
        </form> : <p className="empty-keys">{providerID} has no System One endpoint to attach an account to.</p>}
      </section>
    </div>}

    <section className="detail-panel" aria-labelledby="systemone-provider-title">
      <div className="section-heading"><div><h2 id="systemone-provider-title">Add a System One provider</h2><p>A custom typed endpoint. The native JSON body is forwarded unchanged; the endpoint URL follows the same outbound policy as every provider.</p></div></div>
      <form className="provider-form" aria-label="Add a System One provider" onSubmit={event => void addProvider(event)}>
        <div className="provider-form-grid">
          <label>Provider name<input required placeholder="System One (Prod)" value={nodeForm.name} onChange={event => setNodeForm({ ...nodeForm, name: event.target.value })} /></label>
          <label>Prefix<input required autoComplete="off" spellCheck={false} placeholder="s1-prod" value={nodeForm.prefix} onChange={event => setNodeForm({ ...nodeForm, prefix: event.target.value })} /></label>
          <label className="wide-field">Endpoint URL<input required type="url" spellCheck={false} value={nodeForm.baseUrl} onChange={event => setNodeForm({ ...nodeForm, baseUrl: event.target.value })} /></label>
          <label className="wide-field">Models, one per line (up to {MAX_NODE_MODELS})<textarea rows={3} spellCheck={false} placeholder="jev-latest" value={nodeForm.models} onChange={event => setNodeForm({ ...nodeForm, models: event.target.value })} /></label>
        </div>
        <div className="oauth-actions"><button className="button-secondary" type="submit" disabled={busy === 'provider'}>{busy === 'provider' ? 'Adding…' : 'Add provider'}</button></div>
      </form>
    </section>
  </div>
}
