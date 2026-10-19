import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { fetchAll, PAGE_SIZE, Pager, usePagedList } from './ProviderPaging'
import { EmptyState, ErrorNotice, SkeletonRows } from './kit'
import { Dot, Statement, Status, type Tone } from './editorial'
import { badRate, num, largest, outlier, pct, rate, share, topByRequests, type Summary } from './format'

type Provider = { id: string; transports: string[]; auth: string; authModes: string[]; defaultBaseURL?: string; modelCatalog: string; passthroughModels: boolean; staticModels?: string[]; reportsUsage: boolean; configuredNodes: number; enabledAccounts: number }
type Node = { id: string; kind: string; providerId: string; name: string; prefix?: string; baseUrl?: string; transports: string[] }
type Connection = { id: string; nodeId: string; providerId: string; name: string; authKind: string; identity: string; enabled: boolean; priority: number; proxyPoolId?: string; credentialConfigured: boolean }
type Model = { providerId: string; id: string; name?: string; contextWindow?: number; capabilities: string[]; disabled: boolean; custom?: boolean }
type Alias = { alias: string; providerId: string; modelId: string }
type Pricing = { providerId: string; modelId: string; inputPerMTok?: number; outputPerMTok?: number; cacheReadPerMTok?: number; cacheWritePerMTok?: number }
type ProxyPool = { id: string; name: string; enabled: boolean }
type OAuthFlow = { state: string; authorizeUrl: string; completion: 'code' | 'redirect-url' }
type Props = { onUnauthorized: () => void }

// Providers that offer browser sign-in; the rest keep the paste fields.
const OAUTH_LABELS: Record<string, string> = { claude: 'Claude', codex: 'Codex' }
const DEVICE_LABELS: Record<string, string> = { kilocode: 'Kilo Code' }
// Consecutive transient poll failures tolerated before the panel gives up.
const DEVICE_POLL_RETRIES = 5
type DeviceFlow = { state: string; userCode: string; verificationUrl: string; intervalSeconds: number }
const OAUTH_HINTS: Record<OAuthFlow['completion'], string> = {
  code: 'Sign in on the provider page, then copy the code it shows and paste it below.',
  'redirect-url': 'Sign in on the provider page. Your browser then goes to an address on localhost that will not load; copy that full address from the address bar and paste it below.',
}

class RequestError extends Error {
  constructor(message: string, readonly body: unknown) { super(message) }
}

export function Providers({ onUnauthorized }: Props) {
  const [providers, setProviders] = useState<Provider[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [reload, setReload] = useState(0)
  const [connectionPage, setConnectionPage] = useState(1)
  const [seedPage, setSeedPage] = useState(1)
  const [proxyPools, setProxyPools] = useState<ProxyPool[]>([])
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [loadFailed, setLoadFailed] = useState(false)
  const [usage, setUsage] = useState<Summary | null>(null)
  const [sort, setSort] = useState<'traffic' | 'name' | 'accounts'>('traffic')
  const [notice, setNotice] = useState('')
  const [testModelID, setTestModelID] = useState('')
  const [genericOpen, setGenericOpen] = useState(false)
  const [accountOpen, setAccountOpen] = useState(false)
  const [genericNodeID, setGenericNodeID] = useState('')
  const [generic, setGeneric] = useState({ name: '', prefix: '', baseUrl: '', transports: ['openai-chat'] })
  const [account, setAccount] = useState({ name: '', identity: '', authKind: '', accessToken: '', refreshToken: '', cookie: '' })
  const [oauthFlow, setOauthFlow] = useState<OAuthFlow | null>(null)
  const [oauthInput, setOauthInput] = useState('')
  const [deviceFlow, setDeviceFlow] = useState<DeviceFlow | null>(null)
  const [manual, setManual] = useState({ id: '', name: '', contextWindow: '', capabilities: '' })
  const [alias, setAlias] = useState('')
  const [aliasTarget, setAliasTarget] = useState('')
  const [price, setPrice] = useState({ modelId: '', input: '', output: '', cacheRead: '', cacheWrite: '' })
  const modelProbeController = useRef<AbortController | null>(null)
  const usageAbort = useRef<AbortController | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    setLoadFailed(false)
    try {
      const [loadedProviders, loadedNodes, loadedConnections, loadedPools] = await Promise.all([
        fetchAll<Provider>('providers', onUnauthorized), fetchAll<Node>('provider-nodes', onUnauthorized),
        fetchAll<Connection>('connections', onUnauthorized), fetchAll<ProxyPool>('proxy-pools', onUnauthorized),
      ])
      if (!loadedProviders || !loadedNodes || !loadedConnections || !loadedPools) return
      setNodes(loadedNodes)
      setConnections(loadedConnections)
      const genericProviders = loadedNodes.filter(node => node.kind === 'generic').map(node => ({ id: node.providerId, transports: node.transports, auth: 'api-key', authModes: ['api-key', 'none'], defaultBaseURL: node.baseUrl, modelCatalog: 'dynamic', passthroughModels: true, reportsUsage: false, configuredNodes: 1, enabledAccounts: loadedConnections.filter(connection => connection.providerId === node.providerId && connection.enabled).length }))
      setProviders([...loadedProviders, ...genericProviders])
      setProxyPools(loadedPools)
      // Traffic and error rates enrich the list; the catalog works without them.
      usageAbort.current?.abort()
      const usageController = new AbortController()
      usageAbort.current = usageController
      void fetch('/admin/v1/usage/summary?period=30d', { credentials: 'same-origin', signal: usageController.signal }).then(response => { if (response.status === 401) { onUnauthorized(); return null } return response.ok ? response.json() : null }).then(value => { if (!usageController.signal.aborted) setUsage(value as Summary | null) }).catch(() => { if (!usageController.signal.aborted) setUsage(null) })
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load Providers.')
      setLoadFailed(true)
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])
  // Device sign-in: the operator approves on the provider's page; poll until the
  // server reports the attempt ended. The server also throttles polling.
  useEffect(() => {
    if (!deviceFlow) return
    let cancelled = false
    let failures = 0
    const flow = deviceFlow
    const delay = Math.max(flow.intervalSeconds, 2) * 1000
    const timer = window.setTimeout(async function poll() {
      if (cancelled) return
      try {
        const result = await send(`/oauth/${encodeURIComponent(selected)}/poll`, 'POST', { state: flow.state }) as { status: string }
        if (cancelled) return
        failures = 0
        if (result.status === 'connected') {
          setDeviceFlow(null); setAccountOpen(false); setAccount({ name: '', identity: '', authKind: '', accessToken: '', refreshToken: '', cookie: '' })
          setNotice('Provider connection saved.'); await load(); setReload(value => value + 1)
          return
        }
        window.setTimeout(poll, delay)
      } catch (cause) {
        if (cancelled) return
        // The server keeps the attempt open on oauth_poll_failed (and a dropped
        // request never reached it), so retry those; anything else ended it.
        const code = cause instanceof RequestError ? (cause.body as { error?: { code?: string } } | null)?.error?.code : undefined
        const transient = code === 'oauth_poll_failed' || cause instanceof TypeError
        if (transient && ++failures < DEVICE_POLL_RETRIES) { window.setTimeout(poll, delay); return }
        setDeviceFlow(null); setError(cause instanceof Error ? cause.message : 'Sign-in failed.')
      }
    }, delay)
    return () => { cancelled = true; window.clearTimeout(timer) }
  }, [deviceFlow, selected, load])
  useEffect(() => () => { modelProbeController.current?.abort(); usageAbort.current?.abort() }, [])

  const selectedProvider = providers.find(provider => provider.id === selected)
  const selectedNode = nodes.find(node => node.kind === 'generic' && node.providerId === selected)
  const selectedConnections = connections.filter(connection => connection.providerId === selected).sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name))
  const modelList = usePagedList<Model>('models', selected, reload, onUnauthorized)
  const aliasList = usePagedList<Alias>('aliases', selected, reload, onUnauthorized)
  const priceList = usePagedList<Pricing>('pricing', selected, reload, onUnauthorized)
  const builtinModels = selectedProvider?.staticModels || []
  // Built-in models have no row in /models, so their disabled state comes from the
  // dedicated disabled-models list.
  const [disabledIDs, setDisabledIDs] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (!selected) { setDisabledIDs(new Set()); return }
    let live = true
    void fetchAll<{ modelId: string }>('models/disabled', onUnauthorized, `providerId=${encodeURIComponent(selected)}`)
      .then(rows => { if (live && rows) setDisabledIDs(new Set(rows.map(row => row.modelId))) })
      .catch(() => { if (live) setDisabledIDs(new Set()) })
    return () => { live = false }
  }, [selected, reload, onUnauthorized])
  const builtinPage = builtinModels.slice((seedPage - 1) * PAGE_SIZE, seedPage * PAGE_SIZE)
  const connectionPages = Math.max(1, Math.ceil(selectedConnections.length / PAGE_SIZE))
  useEffect(() => { if (connectionPage > connectionPages) setConnectionPage(connectionPages) }, [connectionPage, connectionPages])
  const connectionRows = selectedConnections.slice((connectionPage - 1) * PAGE_SIZE, connectionPage * PAGE_SIZE)
  // Model pickers offer the loaded page plus the built-in catalog; any other ID
  // can be typed and is validated by the server.
  const modelChoices = useMemo(() => [...new Set([...modelList.items.filter(model => !model.disabled).map(model => model.id), ...builtinModels])], [modelList.items, builtinModels])
  const visibleProviders = useMemo(() => {
    const term = filter.trim().toLowerCase()
    return providers.filter(provider => !term || provider.id.includes(term) || provider.transports.some(transport => transport.includes(term)))
  }, [providers, filter])

  const traffic = useMemo(() => new Map((usage?.providers ?? []).map(item => [item.key, item])), [usage])
  const fleet = usage ? rate(usage.totals.errors, usage.totals.requests) : 0
  // Page-level numbers (headline, concentration, attention) come from every provider; only the list below follows the filter.
  const allRows = useMemo(() => providers.map(provider => {
    const stats = traffic.get(provider.id)
    const total = connections.filter(connection => connection.providerId === provider.id).length
    return { provider, requests: stats?.requests ?? 0, errors: stats?.errors ?? 0, total }
  }), [providers, traffic, connections])
  const visibleIds = useMemo(() => new Set(visibleProviders.map(provider => provider.id)), [visibleProviders])
  const rows = useMemo(() => allRows.filter(row => visibleIds.has(row.provider.id)), [allRows, visibleIds])
  const order = (a: typeof rows[number], b: typeof rows[number]) => sort === 'name' ? a.provider.id.localeCompare(b.provider.id) : sort === 'accounts' ? b.total - a.total || a.provider.id.localeCompare(b.provider.id) : b.requests - a.requests || a.provider.id.localeCompare(b.provider.id)
  const connected = (row: typeof rows[number]) => row.provider.enabledAccounts > 0 || row.total > 0
  const inServiceAll = allRows.filter(connected)
  const inService = rows.filter(connected).sort(order)
  const catalogue = rows.filter(row => !connected(row)).sort((a, b) => a.provider.id.localeCompare(b.provider.id))
  const peak = largest(inServiceAll.map(row => row.requests))
  const monthRequests = Math.max(1, usage?.totals.requests ?? 1)
  const topTwo = topByRequests(inServiceAll.map(row => ({ id: row.provider.id, requests: row.requests })), 2)
  const topShare = (topTwo.reduce((total, row) => total + row.requests, 0) / monthRequests) * 100
  const tone = (row: typeof rows[number]): Tone => row.total === 0 && row.provider.enabledAccounts === 0 ? 'idle' : row.provider.enabledAccounts === 0 ? 'bad' : row.provider.enabledAccounts < row.total || badRate(row.errors, row.requests, fleet) ? 'warn' : 'good'
  const attention = inServiceAll.filter(row => tone(row) === 'warn' || tone(row) === 'bad')
  const worst = usage ? outlier(usage) : null

  async function send(path: string, method: string, body?: unknown, signal?: AbortSignal) {
    const response = await fetch(`/admin/v1${path}`, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal })
    if (response.status === 401) { onUnauthorized(); throw new Error('Admin session expired.') }
    const payload = response.status === 204 ? null : await response.json().catch(() => null) as { error?: { message?: string } } | null
    if (!response.ok) throw new RequestError(payload?.error?.message || 'Provider action failed.', payload)
    return payload
  }

  async function action(id: string, message: string, callback: () => Promise<void>) {
    setBusy(id); setError(''); setNotice('')
    try { await callback(); setNotice(message); await load(); setReload(value => value + 1) }
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

  async function startOAuth() {
    if (!selectedProvider || !account.name.trim() || !account.identity.trim()) return
    setOauthInput('')
    await action('oauth-start', '', async () => {
      const flow = await send(`/oauth/${encodeURIComponent(selected)}/start`, 'POST', { name: account.name, identity: account.identity }) as OAuthFlow
      setOauthFlow(flow)
    })
  }

  async function startDevice() {
    if (!selectedProvider || !account.name.trim() || !account.identity.trim()) return
    await action('device-start', '', async () => {
      setDeviceFlow(await send(`/oauth/${encodeURIComponent(selected)}/device`, 'POST', { name: account.name, identity: account.identity }) as DeviceFlow)
    })
  }

  async function finishOAuth() {
    if (!oauthFlow || !oauthInput.trim()) return
    // The attempt is single-use: it ends here whether or not the provider accepts the code.
    const flow = oauthFlow
    setOauthFlow(null); setOauthInput('')
    await action('oauth-finish', 'Provider connection saved.', async () => {
      await send(`/oauth/${encodeURIComponent(selected)}/callback`, 'POST', { state: flow.state, input: oauthInput })
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
    const modelIDs = selectedIDs || modelChoices.slice(0,20)
    if (!modelIDs.length) return
    const controller = new AbortController()
    modelProbeController.current = controller
    setBusy(`testmodels-${connection.id}`); setError(''); setNotice('')
    try {
      const transport = nodes.find(item => item.id === connection.nodeId)?.transports?.[0] || selectedProvider?.transports[0]
      const body = await send(`/connections/${encodeURIComponent(connection.id)}/test-models`, 'POST', { transport, modelIds: modelIDs }, controller.signal) as { transport?: string; results?: Array<{ modelId: string; ok: boolean; status?: number; error?: string }> }
      if (!Array.isArray(body.results)) throw new Error('Model test failed.')
      const passed = body.results.filter(result => result.ok).length
      setNotice(`Model test: ${passed}/${body.results.length} passed on ${body.transport || transport}. ${body.results.map(result => `${result.modelId}: ${result.ok ? 'OK' : result.error || `HTTP ${result.status}`}`).join('; ')}`)
    } catch (cause) {
      const partial = cause instanceof RequestError ? cause.body as { transport?: string; results?: Array<{ modelId: string; ok: boolean; status?: number; error?: string }> } : null
      if (!controller.signal.aborted && partial && Array.isArray(partial.results)) {
        const passed = partial.results.filter(result => result.ok).length
        setNotice(`Model test: ${passed}/${partial.results.length} passed on ${partial.transport || 'selected transport'}. ${partial.results.map(result => `${result.modelId}: ${result.ok ? 'OK' : result.error || `HTTP ${result.status}`}`).join('; ')}`)
      }
      setError(controller.signal.aborted ? 'Model test canceled.' : cause instanceof Error ? cause.message : 'Model test failed.')
    }
    finally { if (modelProbeController.current === controller) modelProbeController.current = null; setBusy('') }
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
    if (!aliasTarget.trim()) return
    await action('alias', 'Model alias saved.', async () => { await send('/aliases', 'POST', { alias, providerId: selected, modelId: aliasTarget.trim() }); setAlias(''); setAliasTarget('') })
  }

  async function addPricing(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const body: Record<string, unknown> = { providerId: selected, modelId: price.modelId }
    for (const [field, value] of [['inputPerMTok', price.input], ['outputPerMTok', price.output], ['cacheReadPerMTok', price.cacheRead], ['cacheWritePerMTok', price.cacheWrite]]) if (value !== '') body[field] = Number(value)
    await action('pricing', 'Pricing override saved.', async () => { await send('/pricing', 'POST', body); setPrice({ modelId: '', input: '', output: '', cacheRead: '', cacheWrite: '' }) })
  }

  const option = (row: typeof rows[number]) => {
    const bad = row.requests > 0 && badRate(row.errors, row.requests, fleet)
    return <button className={`provider-option${selected === row.provider.id ? ' provider-option-selected' : ''}`} type="button" key={row.provider.id} onClick={() => { setSelected(row.provider.id); setAccountOpen(false); setOauthFlow(null); setOauthInput(''); setDeviceFlow(null); setConnectionPage(1); setSeedPage(1) }}>
      <Status tone={tone(row)}><strong>{row.provider.id}</strong></Status>
      <span className="provider-option-meta">{row.provider.authModes.join(' / ') || row.provider.auth} · {row.provider.transports.length} transport{row.provider.transports.length === 1 ? '' : 's'}{row.total ? ` · ${row.provider.enabledAccounts}/${row.total} on` : ''}</span>
      <span className="ed-bar" aria-hidden="true"><span style={{ width: `${share(row.requests, peak)}%` }} /></span>
      <span className="provider-option-fig">{row.requests ? num(row.requests) : '—'}</span>
      <span className={`provider-option-err${bad ? ' err-text-bad' : ''}`}>{row.requests ? pct(rate(row.errors, row.requests)) : row.total ? '0.0%' : 'no account'}</span>
    </button>
  }

  return <div className="providers-content" aria-busy={loading}>
    <section className="ed-banner accent-surface" aria-label="Provider summary">
      <p className="ed-headline"><strong>{num(inServiceAll.length)}</strong> of {num(providers.length)} providers carry traffic</p>
      <p className="ed-subline">{attention.length > 0 ? <><Dot tone={attention.some(row => tone(row) === 'bad') ? 'bad' : 'warn'} /> <strong>{attention.map(row => row.provider.id).join(', ')}</strong> {attention.length === 1 ? 'needs' : 'need'} a look.</> : loading ? 'Loading providers…' : 'Every connected provider is enabled and within the fleet error rate.'}</p>
    </section>
    {usage && topTwo.length === 2 && topShare > 0 && <Statement><strong>{topTwo.map(row => row.id).join(' and ')}</strong> carry <strong>{Math.round(topShare)}%</strong> of the month's traffic.</Statement>}

    <div className="providers-toolbar gap-s"><label htmlFor="provider-filter">Find provider</label><input id="provider-filter" value={filter} onChange={event => setFilter(event.target.value)} placeholder="Name or transport" /><span className="count-label">{loading ? 'Loading…' : `${providers.length} providers`}</span>
      <div className="provider-sort" role="group" aria-label="Sort providers"><span className="ed-label">Sort by</span>{(['traffic', 'name', 'accounts'] as const).map(key => <button key={key} type="button" aria-pressed={sort === key} className={`ed-link${sort === key ? ' ed-link-on' : ''}`} onClick={() => setSort(key)}>{key === 'traffic' ? '30 d traffic' : key === 'name' ? 'Name' : 'Accounts'}</button>)}</div>
      <button className="button-primary" type="button" onClick={() => { setGenericNodeID(''); setGeneric({ name: '', prefix: '', baseUrl: '', transports: ['openai-chat'] }); setGenericOpen(value => !value) }}>Add Generic Provider</button><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && (loadFailed ? <ErrorNotice className="auth-error" message={error} busy={loading} onRetry={() => void load()} /> : <p className="auth-error" role="alert">{error}</p>)}{notice && <p className="inline-notice" role="status">{notice}</p>}

	    {selectedProvider && selectedConnections[0] && <form className="detail-panel model-test-form" onSubmit={event => { event.preventDefault(); if (testModelID) void testModels(selectedConnections[0], [testModelID]) }}><label htmlFor="test-model-id">Test model</label><input id="test-model-id" required list="provider-model-choices" placeholder="Model ID" value={testModelID} onChange={event => setTestModelID(event.target.value)} /><button className="button-secondary" type="submit" disabled={busy !== '' || !testModelID.trim()}>{busy.startsWith('testmodels-') ? 'Testing…' : 'Run model test'}</button>{busy.startsWith('testmodels-') && <button className="button-ghost" type="button" onClick={() => modelProbeController.current?.abort()}>Cancel model test</button>}<span className="count-label">First-token probe. Multi-model runs stop at the 10-minute batch limit.</span></form>}

    {genericOpen && <form className="detail-panel provider-form" onSubmit={event => void createGeneric(event)}>
      <div className="section-heading"><div><h2>New Generic Provider</h2><p>Endpoint and native LLM transports. URLs follow the configured outbound SSRF policy.</p></div><button className="button-ghost" type="button" onClick={() => setGenericOpen(false)}>Close</button></div>
      <div className="provider-form-grid"><label>Name<input required value={generic.name} onChange={event => setGeneric({ ...generic, name: event.target.value })} /></label><label>Unique model prefix<input required pattern="[a-zA-Z0-9_-]+" value={generic.prefix} onChange={event => setGeneric({ ...generic, prefix: event.target.value })} /></label><label className="wide-field">Base URL<input required type="url" placeholder="https://api.example.com/v1" value={generic.baseUrl} onChange={event => setGeneric({ ...generic, baseUrl: event.target.value })} /></label>
        <fieldset className="transport-options"><legend>Native transports</legend>{[['openai-chat','Chat Completions'],['openai-responses','Responses'],['anthropic-messages','Messages']].map(([value,label]) => <label key={value}><input type="checkbox" checked={generic.transports.includes(value)} onChange={event => setGeneric({ ...generic, transports: event.target.checked ? [...generic.transports,value] : generic.transports.filter(item => item !== value) })} />{label}</label>)}</fieldset>
      </div><button className="button-primary" type="submit" disabled={busy === 'generic' || generic.transports.length === 0}>{busy === 'generic' ? 'Saving…' : 'Save provider'}</button>
    </form>}

    <div className="providers-layout"><section className="provider-catalog" aria-label="Provider catalog">{loading && providers.length === 0 && <div role="status" aria-label="Loading providers"><SkeletonRows rows={8} columns={2} /></div>}
      {inService.length > 0 && <h2 className="ed-group">In service <span>30-day requests and errors</span></h2>}{inService.map(option)}
      {catalogue.length > 0 && <h2 className="ed-group ed-group-late">Catalogue <span>{catalogue.length} without an account</span></h2>}{catalogue.map(option)}
      {!loading && visibleProviders.length === 0 && <EmptyState reason="No provider matches this filter.">{filter && <button className="button-secondary" type="button" onClick={() => setFilter('')}>Clear filter</button>}</EmptyState>}</section>

    <section className="provider-details" aria-live="polite"><datalist id="provider-model-choices">{modelChoices.slice(0, 500).map(id => <option key={id} value={id} />)}</datalist>{!selectedProvider ? <div className="detail-panel provider-empty"><h2>Select a provider</h2><p>Browse built-in providers or add a Generic Provider.</p>{attention.length > 0 && <><h3 className="ed-h3">Needs a look</h3><ul className="ed-watch">{attention.map(row => <li key={row.provider.id}><Status tone={tone(row)}><button className="row-button" type="button" onClick={() => setSelected(row.provider.id)}><strong>{row.provider.id}</strong></button></Status><span>{row.provider.enabledAccounts < row.total ? `${row.provider.enabledAccounts} of ${row.total} accounts enabled.` : `${pct(rate(row.errors, row.requests))} failed against ${pct(fleet)} fleet-wide.`}</span></li>)}</ul></>}{worst && attention.length === 0 && <p className="ed-now-note">Highest error rate: {worst.key} at {pct(worst.err)}.</p>}</div> : <>
      <div className="detail-panel provider-summary"><div className="panel-heading"><div><p className="eyebrow">{selectedNode ? 'GENERIC PROVIDER' : 'BUILT-IN PROVIDER'}</p><h2>{selectedProvider.id}</h2></div><span className="state-tag">{selectedProvider.authModes.join(' / ') || selectedProvider.auth}</span></div><p>{selectedProvider.transports.join(' · ')} · models: {selectedProvider.modelCatalog}{selectedProvider.passthroughModels ? ' + passthrough' : ''}</p>{selectedNode?.baseUrl && <code className="technical-value">{selectedNode.baseUrl}</code>}
        <button className="button-primary" type="button" onClick={() => { const defaultAuth = selectedProvider.authModes[0] || selectedProvider.auth; setAccountOpen(value => !value); setAccount(current => ({ ...current, authKind: defaultAuth })) }}>Add connection</button>{selectedNode && <><button className="button-secondary" type="button" onClick={() => { setGenericNodeID(selectedNode.id); setGeneric({ name: selectedNode.name, prefix: selectedNode.prefix || selectedNode.providerId, baseUrl: selectedNode.baseUrl || '', transports: selectedNode.transports }); setGenericOpen(true) }}>Edit Generic Provider</button><button className="button-danger" type="button" onClick={() => { if (window.confirm(`Delete ${selectedNode.name} and all its connections?`)) void action('delete-generic', 'Generic Provider deleted.', () => send(`/provider-nodes/${encodeURIComponent(selectedNode.id)}`, 'DELETE').then(() => { setSelected('') })) }}>Delete Generic Provider</button></>}
      </div>

      {accountOpen && <form className="detail-panel provider-form" onSubmit={event => void createAccount(event)}><div className="section-heading"><div><h3>New connection</h3><p>Credential material stays server-side and is sealed at rest.</p></div><button className="button-ghost" type="button" onClick={() => { setAccountOpen(false); setOauthFlow(null); setOauthInput('') }}>Close</button></div><div className="provider-form-grid"><label>Account name<input required value={account.name} onChange={event => setAccount({ ...account, name: event.target.value })} /></label><label>Stable account identity<input required value={account.identity} onChange={event => setAccount({ ...account, identity: event.target.value })} /></label><label>Authentication<select value={account.authKind} onChange={event => { setOauthFlow(null); setOauthInput(''); setAccount({ ...account, authKind: event.target.value }) }}>{(selectedProvider.authModes.length ? selectedProvider.authModes : [selectedProvider.auth]).map(mode => <option key={mode} value={mode}>{mode}</option>)}</select></label>
        {account.authKind === 'oauth' && DEVICE_LABELS[selected] && <div className="wide-field oauth-panel" role="group" aria-label={`Sign in with ${DEVICE_LABELS[selected]}`}>
          {!deviceFlow ? <><p>Sign in with the provider instead of copying tokens by hand. Enter an account name and identity first.</p><div><button className="button-primary" type="button" disabled={busy !== '' || !account.name.trim() || !account.identity.trim()} onClick={() => void startDevice()}>{busy === 'device-start' ? 'Starting…' : `Sign in with ${DEVICE_LABELS[selected]}`}</button></div></>
            : <><p>Open the page below and confirm this code. This dashboard finishes the connection on its own once you approve.</p><p><code className="technical-value" aria-label="Device code">{deviceFlow.userCode}</code></p><div className="oauth-actions"><a className="button-secondary" href={deviceFlow.verificationUrl} target="_blank" rel="noopener noreferrer">Open {DEVICE_LABELS[selected]} sign-in page</a><button className="button-ghost" type="button" onClick={() => setDeviceFlow(null)}>Cancel</button></div><p role="status">Waiting for approval…</p></>}
          <p>Or paste tokens manually below.</p>
        </div>}
        {account.authKind === 'oauth' && OAUTH_LABELS[selected] && <div className="wide-field oauth-panel" role="group" aria-label={`Sign in with ${OAUTH_LABELS[selected]}`}>
          {!oauthFlow ? <><p>Sign in with the provider instead of copying tokens by hand. Enter an account name and identity first.</p><div><button className="button-primary" type="button" disabled={busy !== '' || !account.name.trim() || !account.identity.trim()} onClick={() => void startOAuth()}>{busy === 'oauth-start' ? 'Starting…' : `Sign in with ${OAUTH_LABELS[selected]}`}</button></div></>
            : <><p>{OAUTH_HINTS[oauthFlow.completion]}</p><div><a className="button-secondary" href={oauthFlow.authorizeUrl} target="_blank" rel="noopener noreferrer">Open {OAUTH_LABELS[selected]} sign-in page</a></div>
              <label>{oauthFlow.completion === 'code' ? 'Code from the provider' : 'Address from the browser'}<input type={oauthFlow.completion === 'code' ? 'password' : 'text'} autoComplete="off" spellCheck={false} value={oauthInput} onChange={event => setOauthInput(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); void finishOAuth() } }} /></label>
              <div className="oauth-actions"><button className="button-primary" type="button" disabled={busy !== '' || !oauthInput.trim()} onClick={() => void finishOAuth()}>{busy === 'oauth-finish' ? 'Finishing…' : 'Finish sign-in'}</button><button className="button-ghost" type="button" onClick={() => { setOauthFlow(null); setOauthInput('') }}>Cancel</button></div></>}
          <p>Or paste tokens manually below.</p>
        </div>}
        {account.authKind === 'cookie' ? <label className="wide-field">Cookie/session<input type="password" autoComplete="new-password" value={account.cookie} onChange={event => setAccount({ ...account, cookie: event.target.value })} /></label> : account.authKind !== 'none' && <><label className="wide-field">Access token / API key<input required type="password" autoComplete="new-password" value={account.accessToken} onChange={event => setAccount({ ...account, accessToken: event.target.value })} /></label>{account.authKind === 'oauth' && <label className="wide-field">Refresh token<input type="password" autoComplete="new-password" value={account.refreshToken} onChange={event => setAccount({ ...account, refreshToken: event.target.value })} /></label>}</>}
      </div><button className="button-primary" type="submit" disabled={busy === 'account'}>{busy === 'account' ? 'Saving…' : 'Save connection'}</button></form>}

      <section className="detail-panel" aria-labelledby="connection-list-title"><div className="section-heading"><div><h3 id="connection-list-title">Connections</h3><p>{selectedConnections.length} account{selectedConnections.length === 1 ? '' : 's'} · lower priority routes first</p></div></div>{selectedConnections.length === 0 ? <p className="empty-keys">No connections configured.</p> : <div className="connection-list">{connectionRows.map((connection,index) => <article className="connection-row" key={connection.id}><div className="connection-main"><strong>{connection.name}</strong><code>{connection.identity}</code></div><span className={`state-tag${connection.enabled ? ' state-good' : ' state-warn'}`}>{connection.enabled ? 'Enabled' : 'Disabled'}</span><label className="proxy-select">Proxy<select aria-label={`Proxy for ${connection.name}`} value={connection.proxyPoolId || ''} onChange={event => void action(`proxy-${connection.id}`, 'Proxy assignment saved.', () => send(`/connections/${encodeURIComponent(connection.id)}/proxy`, 'POST', { proxyPoolId: event.target.value }).then(() => undefined))}><option value="">No proxy pool</option>{proxyPools.map(pool => <option value={pool.id} key={pool.id}>{pool.name}{pool.enabled ? '' : ' (disabled)'}</option>)}</select></label><div className="connection-actions"><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{enabled:!connection.enabled})}>{connection.enabled ? 'Disable' : 'Enable'}</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{priority:connection.priority-1})} aria-label={`Move ${connection.name} up`} title="Move up">↑</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{priority:connection.priority+1})} aria-label={`Move ${connection.name} down`} title="Move down">↓</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void testConnection(connection)}>{busy === `test-${connection.id}` ? 'Checking…' : 'Check'}</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void discover(connection)}>{busy === `discover-${connection.id}` ? 'Discovering…' : 'Discover models'}</button><button className="button-danger" type="button" disabled={busy !== ''} onClick={() => { if (window.confirm(`Delete account “${connection.name}”?`)) void action(connection.id, 'Connection deleted.', () => send(`/connections/${encodeURIComponent(connection.id)}`, 'DELETE').then(() => undefined)) }}>Delete</button></div><small>Priority {connection.priority} · {connection.credentialConfigured ? 'credential stored' : 'no credential'} · position {(connectionPage - 1) * PAGE_SIZE + index + 1}</small></article>)}</div>}<Pager label="Connection" page={connectionPage} total={selectedConnections.length} onPage={setConnectionPage} /></section>

      <section className="detail-panel" aria-labelledby="models-title"><div className="section-heading"><div><h3 id="models-title">Models</h3><p>{modelList.total.toLocaleString()} configured model{modelList.total === 1 ? '' : 's'}{builtinModels.length > 0 && ` · ${builtinModels.length.toLocaleString()} built-in`}</p></div></div><form className="provider-inline-form" onSubmit={event => void addManualModel(event)}><input required aria-label="Model ID" placeholder="Model ID" value={manual.id} onChange={event => setManual({ ...manual,id:event.target.value })} /><input aria-label="Display name" placeholder="Display name" value={manual.name} onChange={event => setManual({ ...manual,name:event.target.value })} /><input aria-label="Context window" type="number" min="0" placeholder="Context" value={manual.contextWindow} onChange={event => setManual({ ...manual,contextWindow:event.target.value })} /><input aria-label="Capabilities" placeholder="Capabilities, comma-separated" value={manual.capabilities} onChange={event => setManual({ ...manual,capabilities:event.target.value })} /><button className="button-secondary" type="submit">Add manual model</button></form>{modelList.error && <p className="auth-error" role="alert">{modelList.error}</p>}{modelList.items.map(model => <div className="model-row" key={model.id}><code>{model.id}</code><span>{model.name || '—'}</span><small>{model.contextWindow ? `${model.contextWindow.toLocaleString()} tokens` : model.capabilities.join(', ')}</small><span className={`state-tag${model.disabled ? ' state-warn' : ''}`}>{model.disabled ? 'Disabled' : 'Available'}</span><button className="button-ghost" type="button" onClick={() => void action(`model-${model.id}`, 'Model status updated.', () => send('/models/disabled','PATCH',{providerId:selected,modelId:model.id,disabled:!model.disabled}).then(() => undefined))}>{model.disabled ? 'Enable' : 'Disable'}</button>{model.custom && <button className="button-danger" type="button" onClick={() => { if (window.confirm(`Delete custom model “${model.id}”? Its aliases and pricing override are removed. If a built-in model has this ID, it reverts to the built-in entry. Combos that use it must be edited first.`)) void action(`model-delete-${model.id}`, 'Custom model deleted.', () => send('/models','DELETE',{providerId:selected,modelId:model.id}).then(() => undefined)) }}>Delete</button>}</div>)}<Pager label="Model" page={modelList.page} total={modelList.total} loading={modelList.loading} onPage={modelList.setPage} />{builtinModels.length > 0 && <><h4 className="subsection-title">Built-in catalog</h4>{builtinPage.map(id => { const off = disabledIDs.has(id); return <div className="model-row" key={id}><code>{id}</code><span className="state-tag">Built-in</span><span className={`state-tag${off ? ' state-warn' : ''}`}>{off ? 'Disabled' : 'Available'}</span><button className="button-ghost" type="button" disabled={busy !== ''} onClick={() => void action(`model-${id}`, 'Model status updated.', () => send('/models/disabled','PATCH',{providerId:selected,modelId:id,disabled:!off}).then(() => undefined))}>{off ? 'Enable' : 'Disable'}</button></div> })}<Pager label="Built-in model" page={seedPage} total={builtinModels.length} onPage={setSeedPage} /></>}</section>

      <section className="detail-panel" aria-labelledby="alias-title"><div className="section-heading"><div><h3 id="alias-title">Aliases</h3><p>Stable names mapped to this provider’s models.</p></div></div><form className="provider-inline-form" onSubmit={event => void addAlias(event)}><input required aria-label="Alias" placeholder="Alias" value={alias} onChange={event => setAlias(event.target.value)} /><input required list="provider-model-choices" aria-label="Alias target model" placeholder="Target model ID" value={aliasTarget} onChange={event => setAliasTarget(event.target.value)} /><button className="button-secondary" type="submit" disabled={!alias.trim() || !aliasTarget.trim()}>Add alias</button></form>{aliasList.error && <p className="auth-error" role="alert">{aliasList.error}</p>}{aliasList.items.map(item => <div className="model-row" key={item.alias}><code>{item.alias}</code><span>→ {item.modelId}</span><button className="button-danger" type="button" onClick={() => void action(`alias-${item.alias}`,'Alias deleted.',() => send(`/aliases/${encodeURIComponent(item.alias)}`,'DELETE').then(() => undefined))}>Delete</button></div>)}<Pager label="Alias" page={aliasList.page} total={aliasList.total} loading={aliasList.loading} onPage={aliasList.setPage} /></section>

      <section className="detail-panel" aria-labelledby="pricing-title"><div className="section-heading"><div><h3 id="pricing-title">Pricing overrides</h3><p>Rates per million tokens. Empty fields stay unset.</p></div></div><form className="provider-inline-form pricing-form" onSubmit={event => void addPricing(event)}><input required list="provider-model-choices" aria-label="Pricing model" placeholder="Model ID" value={price.modelId} onChange={event => setPrice({...price,modelId:event.target.value})} /><input aria-label="Input price per million" type="number" min="0" step="any" placeholder="Input / MTok" value={price.input} onChange={event => setPrice({...price,input:event.target.value})} /><input aria-label="Output price per million" type="number" min="0" step="any" placeholder="Output / MTok" value={price.output} onChange={event => setPrice({...price,output:event.target.value})} /><input aria-label="Cache read price per million" type="number" min="0" step="any" placeholder="Cache read" value={price.cacheRead} onChange={event => setPrice({...price,cacheRead:event.target.value})} /><input aria-label="Cache write price per million" type="number" min="0" step="any" placeholder="Cache write" value={price.cacheWrite} onChange={event => setPrice({...price,cacheWrite:event.target.value})} /><button className="button-secondary" type="submit">Save rates</button></form>{priceList.error && <p className="auth-error" role="alert">{priceList.error}</p>}{priceList.items.map(item => <div className="model-row" key={item.modelId}><code>{item.modelId}</code><span>in {item.inputPerMTok ?? '—'} · out {item.outputPerMTok ?? '—'}</span><span>cache {item.cacheReadPerMTok ?? '—'} / {item.cacheWritePerMTok ?? '—'}</span><button className="button-danger" type="button" onClick={() => void action(`price-${item.modelId}`,'Pricing override deleted.',() => send('/pricing','DELETE',{providerId:selected,modelId:item.modelId}).then(() => undefined))}>Delete</button></div>)}<Pager label="Pricing" page={priceList.page} total={priceList.total} loading={priceList.loading} onPage={priceList.setPage} /></section>
    </>}</section></div>
  </div>
}
