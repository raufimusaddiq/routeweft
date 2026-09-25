import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'

type Provider = { id: string; transports: string[]; auth: string; authModes: string[]; defaultBaseURL?: string; modelCatalog: string; passthroughModels: boolean; staticModels?: string[]; reportsUsage: boolean; configuredNodes: number; enabledAccounts: number }
type Node = { id: string; kind: string; providerId: string; name: string; prefix?: string; baseUrl?: string; transports: string[] }
type Connection = { id: string; nodeId: string; providerId: string; name: string; authKind: string; identity: string; enabled: boolean; priority: number; proxyPoolId?: string; credentialConfigured: boolean }
type Model = { providerId: string; id: string; name?: string; contextWindow?: number; capabilities: string[]; disabled: boolean }
type Alias = { alias: string; providerId: string; modelId: string }
type Pricing = { providerId: string; modelId: string; inputPerMTok?: number; outputPerMTok?: number; cacheReadPerMTok?: number; cacheWritePerMTok?: number }
type ProxyPool = { id: string; name: string; enabled: boolean }
type Props = { onUnauthorized: () => void }

async function errorMessage(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

export function Providers({ onUnauthorized }: Props) {
  const [providers, setProviders] = useState<Provider[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [aliases, setAliases] = useState<Alias[]>([])
  const [pricing, setPricing] = useState<Pricing[]>([])
  const [proxyPools, setProxyPools] = useState<ProxyPool[]>([])
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [testModelID, setTestModelID] = useState('')
  const [genericOpen, setGenericOpen] = useState(false)
  const [accountOpen, setAccountOpen] = useState(false)
  const [genericNodeID, setGenericNodeID] = useState('')
  const [generic, setGeneric] = useState({ name: '', prefix: '', baseUrl: '', transports: ['openai-chat'] })
  const [account, setAccount] = useState({ name: '', identity: '', authKind: '', accessToken: '', refreshToken: '', cookie: '' })
  const [manual, setManual] = useState({ id: '', name: '', contextWindow: '', capabilities: '' })
  const [alias, setAlias] = useState('')
  const [price, setPrice] = useState({ modelId: '', input: '', output: '', cacheRead: '', cacheWrite: '' })

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const paths = ['/providers?page=1&pageSize=100', '/provider-nodes?page=1&pageSize=100', '/connections?page=1&pageSize=100', '/models?page=1&pageSize=100', '/aliases?page=1&pageSize=100', '/pricing?page=1&pageSize=100', '/proxy-pools?page=1&pageSize=100']
      const responses = await Promise.all(paths.map(path => fetch(`/admin/v1${path}`, { credentials: 'same-origin' })))
      if (responses.some(response => response.status === 401)) { onUnauthorized(); return }
      const failed = responses.find(response => !response.ok)
      if (failed) throw new Error(await errorMessage(failed, 'Could not load provider configuration.'))
      const data = await Promise.all(responses.map(response => response.json())) as Array<{ items: unknown[] }>
      const loadedNodes = data[1].items as Node[]
      const loadedConnections = data[2].items as Connection[]
      setNodes(loadedNodes)
      setConnections(loadedConnections)
      const genericProviders = loadedNodes.filter(node => node.kind === 'generic').map(node => ({ id: node.providerId, transports: node.transports, auth: 'api-key', authModes: ['api-key', 'none'], defaultBaseURL: node.baseUrl, modelCatalog: 'dynamic', passthroughModels: true, reportsUsage: false, configuredNodes: 1, enabledAccounts: loadedConnections.filter(connection => connection.providerId === node.providerId && connection.enabled).length }))
      setProviders([...(data[0].items as Provider[]), ...genericProviders])
      setModels(data[3].items as Model[])
      setAliases(data[4].items as Alias[])
      setPricing(data[5].items as Pricing[])
      setProxyPools(data[6].items as ProxyPool[])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load Providers.')
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  const selectedProvider = providers.find(provider => provider.id === selected)
  const selectedNode = nodes.find(node => node.kind === 'generic' && node.providerId === selected)
  const selectedConnections = connections.filter(connection => connection.providerId === selected).sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name))
  const selectedModels = useMemo(() => {
    const configured = models.filter(model => model.providerId === selected)
    const configuredIDs = new Set(configured.map(model => model.id))
    const seeds: Model[] = (selectedProvider?.staticModels || []).filter(id => !configuredIDs.has(id)).map(id => ({ providerId: selected, id, capabilities: [], disabled: false }))
    return [...configured, ...seeds]
  }, [models, selected, selectedProvider])
  const visibleProviders = useMemo(() => {
    const term = filter.trim().toLowerCase()
    return providers.filter(provider => !term || provider.id.includes(term) || provider.transports.some(transport => transport.includes(term)))
  }, [providers, filter])

  async function send(path: string, method: string, body?: unknown) {
    const response = await fetch(`/admin/v1${path}`, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
    if (response.status === 401) { onUnauthorized(); throw new Error('Admin session expired.') }
    if (!response.ok) throw new Error(await errorMessage(response, 'Provider action failed.'))
    return response.status === 204 ? null : response.json()
  }

  async function action(id: string, message: string, callback: () => Promise<void>) {
    setBusy(id); setError(''); setNotice('')
    try { await callback(); setNotice(message); await load() }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Provider action failed.') }
    finally { setBusy('') }
  }

  async function createGeneric(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    await action('generic', 'Generic Provider added.', async () => {
      const response = await send(genericNodeID ? `/provider-nodes/${encodeURIComponent(genericNodeID)}` : '/provider-nodes', genericNodeID ? 'PATCH' : 'POST', { kind: 'generic', ...generic }) as Node
      setSelected(response.providerId)
      setGenericOpen(false)
      setGenericNodeID('')
      setGeneric({ name: '', prefix: '', baseUrl: '', transports: ['openai-chat'] })
    })
  }

  async function createAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!selectedProvider || !account.name.trim() || !account.identity.trim()) return
    await action('account', 'Provider connection saved.', async () => {
      const secret: Record<string, string> = {}
      if (account.authKind === 'cookie') secret.cookie = account.cookie
      else if (account.authKind !== 'none') { secret.accessToken = account.accessToken; if (account.refreshToken) secret.refreshToken = account.refreshToken }
      await send('/connections', 'POST', { providerId: selected, nodeId: selectedNode?.id, name: account.name, identity: account.identity, authKind: account.authKind || selectedProvider.authModes[0] || selectedProvider.auth, secret, enabled: true, priority: selectedConnections.length })
      setAccountOpen(false); setAccount({ name: '', identity: '', authKind: '', accessToken: '', refreshToken: '', cookie: '' })
    })
  }

  async function changeConnection(connection: Connection, patch: Partial<Connection>) {
    if (patch.priority !== undefined) {
      const direction = patch.priority < connection.priority ? 'up' : 'down'
      await action(connection.id, 'Connection order updated.', () => send(`/connections/${encodeURIComponent(connection.id)}/order`, 'POST', { direction }).then(() => undefined))
      return
    }
    await action(connection.id, 'Connection updated.', () => send(`/connections/${encodeURIComponent(connection.id)}`, 'PATCH', patch).then(() => undefined))
  }

  async function testConnection(connection: Connection) {
    await action(`test-${connection.id}`, 'Connection check succeeded.', () => send(`/connections/${encodeURIComponent(connection.id)}/test`, 'POST').then(() => undefined))
  }

  async function discover(connection: Connection) {
    await action(`discover-${connection.id}`, 'Model catalog refreshed.', () => send(`/connections/${encodeURIComponent(connection.id)}/discover`, 'POST').then(() => undefined))
  }

  async function testModels(connection: Connection, selectedIDs?: string[]) {
    const modelIDs = selectedIDs || selectedModels.filter(model => !model.disabled).slice(0,20).map(model => model.id)
    if (!modelIDs.length) return
    setBusy(`testmodels-${connection.id}`); setError(''); setNotice('')
    try {
      const transport = nodes.find(item => item.id === connection.nodeId)?.transports?.[0] || selectedProvider?.transports[0]
      const response = await send(`/connections/${encodeURIComponent(connection.id)}/test-models`, 'POST', { transport, modelIds: modelIDs }) as { transport: string; results: Array<{ modelId: string; ok: boolean; status?: number; error?: string }> }
      const passed = response.results.filter(result => result.ok).length
      setNotice(`Model test: ${passed}/${response.results.length} passed on ${response.transport}. ${response.results.map(result => `${result.modelId}: ${result.ok ? 'OK' : result.error || `HTTP ${result.status}`}`).join('; ')}`)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Model test failed.') }
    finally { setBusy('') }
  }

  async function addManualModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    await action('model', 'Manual model added.', async () => {
      await send('/models', 'POST', { providerId: selected, id: manual.id, name: manual.name, contextWindow: Number(manual.contextWindow) || 0, capabilities: manual.capabilities.split(',').map(value => value.trim()).filter(Boolean) })
      setManual({ id: '', name: '', contextWindow: '', capabilities: '' })
    })
  }

  async function addAlias(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const model = selectedModels.find(item => item.id === price.modelId) || selectedModels[0]
    if (!model) return
    await action('alias', 'Model alias saved.', async () => { await send('/aliases', 'POST', { alias, providerId: selected, modelId: model.id }); setAlias('') })
  }

  async function addPricing(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const body: Record<string, unknown> = { providerId: selected, modelId: price.modelId }
    for (const [field, value] of [['inputPerMTok', price.input], ['outputPerMTok', price.output], ['cacheReadPerMTok', price.cacheRead], ['cacheWritePerMTok', price.cacheWrite]]) if (value !== '') body[field] = Number(value)
    await action('pricing', 'Pricing override saved.', async () => { await send('/pricing', 'POST', body); setPrice({ modelId: '', input: '', output: '', cacheRead: '', cacheWrite: '' }) })
  }

  return <div className="providers-content" aria-busy={loading}>
    <div className="providers-toolbar"><label htmlFor="provider-filter">Find provider</label><input id="provider-filter" value={filter} onChange={event => setFilter(event.target.value)} placeholder="Name or transport" /><span className="count-label">{loading ? 'Loading…' : `${providers.length} providers`}</span><button className="button-primary" type="button" onClick={() => { setGenericNodeID(''); setGeneric({ name: '', prefix: '', baseUrl: '', transports: ['openai-chat'] }); setGenericOpen(value => !value) }}>Add Generic Provider</button><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    {selectedProvider && selectedConnections[0] && <form className="detail-panel model-test-form" onSubmit={event => { event.preventDefault(); if (testModelID) void testModels(selectedConnections[0], [testModelID]) }}><label htmlFor="test-model-id">Test model</label><select id="test-model-id" required value={testModelID} onChange={event => setTestModelID(event.target.value)}><option value="">Choose a model</option>{selectedModels.filter(model => !model.disabled).map(model => <option key={model.id} value={model.id}>{model.id}</option>)}</select><button className="button-secondary" type="submit" disabled={busy !== '' || selectedModels.filter(model => !model.disabled).length === 0}>{busy.startsWith('testmodels-') ? 'Testing…' : 'Run model test'}</button><span className="count-label">Native transport probe; small upstream request.</span></form>}

    {genericOpen && <form className="detail-panel provider-form" onSubmit={event => void createGeneric(event)}>
      <div className="section-heading"><div><h2>New Generic Provider</h2><p>Endpoint and native LLM transports. URLs follow the configured outbound SSRF policy.</p></div><button className="button-ghost" type="button" onClick={() => setGenericOpen(false)}>Close</button></div>
      <div className="provider-form-grid"><label>Name<input required value={generic.name} onChange={event => setGeneric({ ...generic, name: event.target.value })} /></label><label>Unique model prefix<input required pattern="[a-zA-Z0-9_-]+" value={generic.prefix} onChange={event => setGeneric({ ...generic, prefix: event.target.value })} /></label><label className="wide-field">Base URL<input required type="url" placeholder="https://api.example.com/v1" value={generic.baseUrl} onChange={event => setGeneric({ ...generic, baseUrl: event.target.value })} /></label>
        <fieldset className="transport-options"><legend>Native transports</legend>{[['openai-chat','Chat Completions'],['openai-responses','Responses'],['anthropic-messages','Messages']].map(([value,label]) => <label key={value}><input type="checkbox" checked={generic.transports.includes(value)} onChange={event => setGeneric({ ...generic, transports: event.target.checked ? [...generic.transports,value] : generic.transports.filter(item => item !== value) })} />{label}</label>)}</fieldset>
      </div><button className="button-primary" type="submit" disabled={busy === 'generic' || generic.transports.length === 0}>{busy === 'generic' ? 'Saving…' : 'Save provider'}</button>
    </form>}

    <div className="providers-layout"><section className="provider-catalog" aria-label="Provider catalog">{visibleProviders.map(provider => <button className={`provider-card${selected === provider.id ? ' provider-card-selected' : ''}`} type="button" key={provider.id} onClick={() => { setSelected(provider.id); setAccountOpen(false) }}>
      <span className="provider-card-top"><strong>{provider.id}</strong><span className="state-tag">{provider.modelCatalog}</span></span><span className="provider-card-meta">{provider.configuredNodes} node{provider.configuredNodes === 1 ? '' : 's'} · {provider.enabledAccounts} enabled</span><span className="provider-transport">{provider.transports.join(' · ')}</span>
    </button>)}{visibleProviders.length === 0 && <p className="empty-keys">No provider matches this filter.</p>}</section>

    <section className="provider-details" aria-live="polite">{!selectedProvider ? <div className="detail-panel"><h2>Select a provider</h2><p>Browse built-in providers or add a Generic Provider.</p></div> : <>
      <div className="detail-panel provider-summary"><div className="panel-heading"><div><p className="eyebrow">{selectedNode ? 'GENERIC PROVIDER' : 'BUILT-IN PROVIDER'}</p><h2>{selectedProvider.id}</h2></div><span className="state-tag">{selectedProvider.authModes.join(' / ') || selectedProvider.auth}</span></div><p>{selectedProvider.transports.join(' · ')} · models: {selectedProvider.modelCatalog}{selectedProvider.passthroughModels ? ' + passthrough' : ''}</p>{selectedNode?.baseUrl && <code className="technical-value">{selectedNode.baseUrl}</code>}
        <button className="button-primary" type="button" onClick={() => { const defaultAuth = selectedProvider.authModes[0] || selectedProvider.auth; setAccountOpen(value => !value); setAccount(current => ({ ...current, authKind: defaultAuth })) }}>Add connection</button>{selectedNode && <><button className="button-secondary" type="button" onClick={() => { setGenericNodeID(selectedNode.id); setGeneric({ name: selectedNode.name, prefix: selectedNode.prefix || selectedNode.providerId, baseUrl: selectedNode.baseUrl || '', transports: selectedNode.transports }); setGenericOpen(true) }}>Edit Generic Provider</button><button className="button-danger" type="button" onClick={() => { if (window.confirm(`Delete ${selectedNode.name} and all its connections?`)) void action('delete-generic', 'Generic Provider deleted.', () => send(`/provider-nodes/${encodeURIComponent(selectedNode.id)}`, 'DELETE').then(() => { setSelected('') })) }}>Delete Generic Provider</button></>}
      </div>

      {accountOpen && <form className="detail-panel provider-form" onSubmit={event => void createAccount(event)}><div className="section-heading"><div><h3>New connection</h3><p>Credential material stays server-side and is sealed at rest.</p></div><button className="button-ghost" type="button" onClick={() => setAccountOpen(false)}>Close</button></div><div className="provider-form-grid"><label>Account name<input required value={account.name} onChange={event => setAccount({ ...account, name: event.target.value })} /></label><label>Stable account identity<input required value={account.identity} onChange={event => setAccount({ ...account, identity: event.target.value })} /></label><label>Authentication<select value={account.authKind} onChange={event => setAccount({ ...account, authKind: event.target.value })}>{(selectedProvider.authModes.length ? selectedProvider.authModes : [selectedProvider.auth]).map(mode => <option key={mode} value={mode}>{mode}</option>)}</select></label>
        {account.authKind === 'cookie' ? <label className="wide-field">Cookie/session<input type="password" autoComplete="new-password" value={account.cookie} onChange={event => setAccount({ ...account, cookie: event.target.value })} /></label> : account.authKind !== 'none' && <><label className="wide-field">Access token / API key<input required type="password" autoComplete="new-password" value={account.accessToken} onChange={event => setAccount({ ...account, accessToken: event.target.value })} /></label>{account.authKind === 'oauth' && <label className="wide-field">Refresh token<input type="password" autoComplete="new-password" value={account.refreshToken} onChange={event => setAccount({ ...account, refreshToken: event.target.value })} /></label>}</>}
      </div><button className="button-primary" type="submit" disabled={busy === 'account'}>{busy === 'account' ? 'Saving…' : 'Save connection'}</button></form>}

      <section className="detail-panel" aria-labelledby="connection-list-title"><div className="section-heading"><div><h3 id="connection-list-title">Connections</h3><p>{selectedConnections.length} account{selectedConnections.length === 1 ? '' : 's'} · lower priority routes first</p></div></div>{selectedConnections.length === 0 ? <p className="empty-keys">No connections configured.</p> : <div className="connection-list">{selectedConnections.map((connection,index) => <article className="connection-row" key={connection.id}><div className="connection-main"><strong>{connection.name}</strong><code>{connection.identity}</code></div><span className={`state-tag${connection.enabled ? ' state-good' : ' state-warn'}`}>{connection.enabled ? 'Enabled' : 'Disabled'}</span><label className="proxy-select">Proxy<select aria-label={`Proxy for ${connection.name}`} value={connection.proxyPoolId || ''} onChange={event => void action(`proxy-${connection.id}`, 'Proxy assignment saved.', () => send(`/connections/${encodeURIComponent(connection.id)}/proxy`, 'POST', { proxyPoolId: event.target.value }).then(() => undefined))}><option value="">No proxy pool</option>{proxyPools.map(pool => <option value={pool.id} key={pool.id}>{pool.name}{pool.enabled ? '' : ' (disabled)'}</option>)}</select></label><div className="connection-actions"><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{enabled:!connection.enabled})}>{connection.enabled ? 'Disable' : 'Enable'}</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{priority:connection.priority-1})} aria-label={`Move ${connection.name} up`} title="Move up">↑</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{priority:connection.priority+1})} aria-label={`Move ${connection.name} down`} title="Move down">↓</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void testConnection(connection)}>{busy === `test-${connection.id}` ? 'Checking…' : 'Check'}</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void discover(connection)}>{busy === `discover-${connection.id}` ? 'Discovering…' : 'Discover models'}</button><button className="button-danger" type="button" disabled={busy !== ''} onClick={() => { if (window.confirm(`Delete account “${connection.name}”?`)) void action(connection.id, 'Connection deleted.', () => send(`/connections/${encodeURIComponent(connection.id)}`, 'DELETE').then(() => undefined)) }}>Delete</button></div><small>Priority {connection.priority} · {connection.credentialConfigured ? 'credential stored' : 'no credential'} · position {index + 1}</small></article>)}</div>}</section>

      <section className="detail-panel" aria-labelledby="models-title"><div className="section-heading"><div><h3 id="models-title">Models</h3><p>{selectedModels.length} configured model{selectedModels.length === 1 ? '' : 's'}</p></div></div><form className="provider-inline-form" onSubmit={event => void addManualModel(event)}><input required aria-label="Model ID" placeholder="Model ID" value={manual.id} onChange={event => setManual({ ...manual,id:event.target.value })} /><input aria-label="Display name" placeholder="Display name" value={manual.name} onChange={event => setManual({ ...manual,name:event.target.value })} /><input aria-label="Context window" type="number" min="0" placeholder="Context" value={manual.contextWindow} onChange={event => setManual({ ...manual,contextWindow:event.target.value })} /><input aria-label="Capabilities" placeholder="Capabilities, comma-separated" value={manual.capabilities} onChange={event => setManual({ ...manual,capabilities:event.target.value })} /><button className="button-secondary" type="submit">Add manual model</button></form>{selectedModels.map(model => <div className="model-row" key={model.id}><code>{model.id}</code><span>{model.name || '—'}</span><small>{model.contextWindow ? `${model.contextWindow.toLocaleString()} tokens` : model.capabilities.join(', ')}</small><span className={`state-tag${model.disabled ? ' state-warn' : ''}`}>{model.disabled ? 'Disabled' : 'Available'}</span><button className="button-ghost" type="button" onClick={() => void action(`model-${model.id}`, 'Model status updated.', () => send('/models/disabled','PATCH',{providerId:selected,modelId:model.id,disabled:!model.disabled}).then(() => undefined))}>{model.disabled ? 'Enable' : 'Disable'}</button></div>)}</section>

      <section className="detail-panel" aria-labelledby="alias-title"><div className="section-heading"><div><h3 id="alias-title">Aliases</h3><p>Stable names mapped to this provider’s models.</p></div></div><form className="provider-inline-form" onSubmit={event => void addAlias(event)}><input required aria-label="Alias" placeholder="Alias" value={alias} onChange={event => setAlias(event.target.value)} /><select aria-label="Alias target model" required value={price.modelId} onChange={event => setPrice({...price,modelId:event.target.value})}><option value="">Choose model</option>{selectedModels.map(model => <option key={model.id} value={model.id}>{model.id}</option>)}</select><button className="button-secondary" type="submit" disabled={!selectedModels.length}>Add alias</button></form>{aliases.filter(item => item.providerId === selected).map(item => <div className="model-row" key={item.alias}><code>{item.alias}</code><span>→ {item.modelId}</span><button className="button-danger" type="button" onClick={() => void action(`alias-${item.alias}`,'Alias deleted.',() => send(`/aliases/${encodeURIComponent(item.alias)}`,'DELETE').then(() => undefined))}>Delete</button></div>)}</section>

      <section className="detail-panel" aria-labelledby="pricing-title"><div className="section-heading"><div><h3 id="pricing-title">Pricing overrides</h3><p>Rates per million tokens. Empty fields stay unset.</p></div></div><form className="provider-inline-form pricing-form" onSubmit={event => void addPricing(event)}><select aria-label="Pricing model" required value={price.modelId} onChange={event => setPrice({...price,modelId:event.target.value})}><option value="">Choose model</option>{selectedModels.map(model => <option key={model.id} value={model.id}>{model.id}</option>)}</select><input aria-label="Input price per million" type="number" min="0" step="any" placeholder="Input / MTok" value={price.input} onChange={event => setPrice({...price,input:event.target.value})} /><input aria-label="Output price per million" type="number" min="0" step="any" placeholder="Output / MTok" value={price.output} onChange={event => setPrice({...price,output:event.target.value})} /><input aria-label="Cache read price per million" type="number" min="0" step="any" placeholder="Cache read" value={price.cacheRead} onChange={event => setPrice({...price,cacheRead:event.target.value})} /><input aria-label="Cache write price per million" type="number" min="0" step="any" placeholder="Cache write" value={price.cacheWrite} onChange={event => setPrice({...price,cacheWrite:event.target.value})} /><button className="button-secondary" type="submit">Save rates</button></form>{pricing.filter(item => item.providerId === selected).map(item => <div className="model-row" key={item.modelId}><code>{item.modelId}</code><span>in {item.inputPerMTok ?? '—'} · out {item.outputPerMTok ?? '—'}</span><span>cache {item.cacheReadPerMTok ?? '—'} / {item.cacheWritePerMTok ?? '—'}</span><button className="button-danger" type="button" onClick={() => void action(`price-${item.modelId}`,'Pricing override deleted.',() => send('/pricing','DELETE',{providerId:selected,modelId:item.modelId}).then(() => undefined))}>Delete</button></div>)}</section>
    </>}</section></div>
  </div>
}
