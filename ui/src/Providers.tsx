import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { fetchAll, PAGE_SIZE, Pager, usePagedList } from './ProviderPaging'
import { EmptyState, ErrorNotice, SkeletonRows } from './kit'
import { Dot, Statement, Status, type Tone } from './editorial'
import { badRate, num, largest, outlier, pct, rate, share, topByRequests, type Summary } from './format'
import { READINESS_LABEL, readinessNote, readinessStatus, type Readiness } from './readiness'
import { attemptExpired, attemptStillOpen, cancelResult, connectMethods, phaseForError, type AttemptPhase, type Completion, type ConnectMethod, type SignInMethod, type SignInMode } from './signIn'

function ReadinessTag({ provider }: { provider: { readiness?: Readiness } }) {
  const status = readinessStatus(provider)
  return <span className={`state-tag readiness-tag readiness-${status}`} title={provider.readiness?.reason || 'Set by this build, not by account settings.'}>{READINESS_LABEL[status]}</span>
}
type Provider = { id: string; transports: string[]; auth: string; authModes: string[]; defaultBaseURL?: string; modelCatalog: string; passthroughModels: boolean; staticModels?: string[]; reportsUsage: boolean; flowType?: string; signInModes?: SignInMode[]; metaKeys?: string[]; signInMethods?: SignInMethod[]; configuredNodes: number; enabledAccounts: number; readiness?: Readiness }
type Node = { id: string; kind: string; providerId: string; name: string; prefix?: string; baseUrl?: string; transports: string[] }
type Connection = { id: string; nodeId: string; providerId: string; name: string; authKind: string; identity: string; enabled: boolean; priority: number; proxyPoolId?: string; credentialConfigured: boolean }
type Model = { providerId: string; id: string; name?: string; contextWindow?: number; capabilities: string[]; disabled: boolean; custom?: boolean }
type Alias = { alias: string; providerId: string; modelId: string }
type Pricing = { providerId: string; modelId: string; inputPerMTok?: number; outputPerMTok?: number; cacheReadPerMTok?: number; cacheWritePerMTok?: number }
type ProxyPool = { id: string; name: string; enabled: boolean }
// Each attempt carries the provider it was started for, so polling, finishing and
// cancelling stay on that provider even if another one is selected meanwhile.
type OAuthFlow = { provider: string; state: string; authorizeUrl: string; completion: Completion; expiresAt?: string }
// How the last sign-in attempt ended. A success carries the saved connection so
// the operator can see the identity the provider reported and name it.
type Outcome = { phase: AttemptPhase; message: string; connection?: Connection }
type Props = { onUnauthorized: () => void }

// The backend reports which sign-in affordances a provider actually has, derived
// from the same flow tables the endpoints use. Offering both panels for every
// oauth provider is what produced buttons that 404; the UI renders only what the
// server says exists, read straight from the field it is named after.
// Operator fields, by the meta key the flow reads. A flow that declares none gets
// no parameter fields: an operator should not be asked for input nothing consumes.
const META_FIELDS: Array<{ key: string; label: string; placeholder?: string; options?: string[]; secret?: boolean }> = [
  { key: 'clientId', label: 'Application client id' },
  // A confidential OAuth application sends this at exchange; a public one leaves it
  // blank. Masked like every other credential.
  { key: 'clientSecret', label: 'Application client secret', secret: true },
  { key: 'baseUrl', label: 'Server base URL', placeholder: 'https://gitlab.com' },
  { key: 'machineId', label: 'Machine id' },
  { key: 'region', label: 'AWS region', placeholder: 'us-east-1' },
  { key: 'startUrl', label: 'AWS SSO start URL' },
  { key: 'nativeAppPort', label: 'Native app port', placeholder: '58443' },
  { key: 'keyName', label: 'Key name' },
  { key: 'uid', label: 'Account uid' },
  // Kiro's AWS SSO method: a closed set, so it is a select rather than free text.
  { key: 'authMethod', label: 'AWS SSO method', options: ['builder-id', 'idc'] },
]
// Display labels for the providers whose product name differs from their id.
// Presentation only: nothing here decides behavior, and an id without an entry
// falls back to the id itself, which is still honest.
const PROVIDER_LABELS: Record<string, string> = {
  claude: 'Claude', codex: 'Codex', xai: 'xAI', github: 'GitHub Copilot',
  gitlab: 'GitLab Duo', kimi: 'Kimi', kiro: 'Kiro', qoder: 'Qoder',
  cursor: 'Cursor', kilocode: 'Kilo Code', iflow: 'iFlow', zed: 'Zed',
  cline: 'Cline', clinepass: 'ClinePass', kimchi: 'Kimchi', trae: 'Trae',
  windsurf: 'Windsurf', 'gemini-cli': 'Gemini CLI', antigravity: 'Antigravity',
  'codebuddy-cn': 'CodeBuddy (CN)', 'codebuddy-intl': 'CodeBuddy (Intl)',
  'xiaomi-mimo': 'Xiaomi MiMo', 'grok-cli': 'Grok CLI',
}
function providerLabel(id: string): string {
  return PROVIDER_LABELS[id] ?? id
}
// Consecutive transient poll failures tolerated before the panel gives up.
const DEVICE_POLL_RETRIES = 5
type DeviceFlow = { provider: string; state: string; userCode: string; verificationUrl: string; intervalSeconds: number; expiresAt?: string }
const OAUTH_HINTS: Record<Completion, string> = {
  code: 'Sign in on the provider page, then copy the code it shows and paste it below.',
  'redirect-url': 'Sign in on the provider page. Your browser then goes to an address on localhost that will not load; copy that full address from the address bar and paste it below.',
  token: 'Paste the token, cookie or key the provider issued. It is validated before it is stored.',
  callback: 'Sign in on the provider page. Your browser then goes to a local address that will not load; copy that full address and paste it below.',
}
// A pinned loopback redirect is a port on the operator's own computer. Routeweft
// never listens there (a server cannot receive a request for the browser's own
// localhost), so the operator is told the page will not load and why.
function loopbackHint(redirect: string): string {
  return `Sign in on the provider page. Your browser is then sent to ${redirect}. That address is on the computer running this browser, not on the Routeweft server, so the page will not load; that is expected. Copy the full address from the address bar and paste it below.`
}
const ATTEMPT_LABEL: Record<AttemptPhase, string> = { success: 'Connected', expired: 'Expired', cancelled: 'Cancelled', failed: 'Failed', closed: 'Closed' }
const EMPTY_ACCOUNT = { name: '', identity: '', accessToken: '', refreshToken: '', cookie: '' }
// Shown for an attempt the server no longer holds. Gone does not say whether an
// earlier request saved it (a lost response, a save that finished as time ran
// out), so the operator is pointed at the connection list instead of told.
const CLOSED_MESSAGE = 'This sign-in is no longer open on the server: it expired or was already used. Check the connections below for anything it saved.'
const errorCodeOf = (cause: unknown) => cause instanceof RequestError ? (cause.body as { error?: { code?: string } } | null)?.error?.code : undefined

class RequestError extends Error {
  constructor(message: string, readonly body: unknown, readonly status?: number) { super(message) }
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
  const [projectId, setProjectId] = useState('')
  const [account, setAccount] = useState(EMPTY_ACCOUNT)
  const [methodId, setMethodId] = useState('')
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const [nickname, setNickname] = useState('')
  // Set when a flow that normally reads the account from the provider came back
  // without one, so the next attempt asks the operator instead.
  const [needIdentity, setNeedIdentity] = useState(false)
  // What a request that did not end the attempt found out (a save in progress, no
  // answer, a cancel the server did not confirm). Shown on the pending attempt,
  // which stays tracked.
  const [attemptNote, setAttemptNote] = useState('')
  // The attempt's time limit has passed. Informational only: the server decides
  // what that means, and a save may already be under way.
  const [pastExpiry, setPastExpiry] = useState(false)
  // Polling stopped after repeated failures to reach the server or the provider;
  // the attempt is still tracked and the operator resumes it.
  const [pollStalled, setPollStalled] = useState(false)
  // The state of the attempt on screen, read after an await to tell whether the
  // attempt the request was about is still the current one.
  const liveState = useRef<string | null>(null)
  const [oauthFlow, setOauthFlow] = useState<OAuthFlow | null>(null)
  // Two independent secrets: the import panel's credential and the browser flow's
  // pasted code. They rendered together for providers with both modes (codex,
  // gitlab, …), so one state made the second field overwrite the first.
  const [oauthInput, setOauthInput] = useState('')
  const [importInput, setImportInput] = useState('')
  const [deviceFlow, setDeviceFlow] = useState<DeviceFlow | null>(null)
  // Operator input a flow needs beyond name/identity: GitLab's self-hosted
  // application client id and base URL, Kiro's region, Cursor's machine id.
  const [metaInput, setMetaInput] = useState<Record<string, string>>({})
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
    if (!deviceFlow || pollStalled) return
    let cancelled = false
    let failures = 0
    // Cancel and unmount end the in-flight poll request, not just the next tick.
    const controller = new AbortController()
    const flow = deviceFlow
    const delay = Math.max(flow.intervalSeconds, 2) * 1000
    const timer = window.setTimeout(async function poll() {
      if (cancelled) return
      try {
        const result = await send(`/oauth/${encodeURIComponent(flow.provider)}/poll`, 'POST', { state: flow.state }, controller.signal) as { status: string; connection?: Connection }
        if (cancelled) return
        failures = 0
        if (result.status === 'connected') {
          signedIn(result.connection); await load(); setReload(value => value + 1)
          return
        }
        window.setTimeout(poll, delay)
      } catch (cause) {
        // An abort is the operator ending the sign-in, not a failure to report.
        if (cancelled || (cause instanceof DOMException && cause.name === 'AbortError')) return
        // Without an answer that ends it, the attempt is still open on the server
        // (and may be saving), so it stays tracked: retry, then pause for the
        // operator rather than drop it.
        if (attemptStillOpen(errorCodeOf(cause))) {
          if (++failures < DEVICE_POLL_RETRIES) { window.setTimeout(poll, delay); return }
          setPollStalled(true)
          setAttemptNote('The server or the provider could not be reached. The sign-in may still be open or saving; check again.')
          return
        }
        setDeviceFlow(null); attemptFailed(cause)
      }
    }, delay)
    return () => { cancelled = true; window.clearTimeout(timer); controller.abort() }
  }, [deviceFlow, pollStalled, load])
  useEffect(() => () => { modelProbeController.current?.abort(); usageAbort.current?.abort() }, [])
  // Reaching the server-issued expiry only changes what the panel says. The attempt
  // stays tracked, and a poll keeps running, until the server answers: an approval
  // just before the limit may still be saving, so the clock cannot say whether
  // anything was saved.
  const pendingExpiry = oauthFlow?.expiresAt ?? deviceFlow?.expiresAt
  liveState.current = oauthFlow?.state ?? deviceFlow?.state ?? null
  useEffect(() => {
    setPastExpiry(attemptExpired(pendingExpiry, Date.now()))
    if (!pendingExpiry) return
    const timer = window.setTimeout(() => setPastExpiry(true), Math.max(0, Date.parse(pendingExpiry) - Date.now()))
    return () => window.clearTimeout(timer)
  }, [pendingExpiry])

  const selectedProvider = providers.find(provider => provider.id === selected)
  const methods = useMemo(() => selectedProvider ? connectMethods(selectedProvider, providerLabel(selected)) : [], [selectedProvider, selected])
  const method: ConnectMethod | undefined = methods.find(item => item.id === methodId) ?? methods[0]
  const pending = oauthFlow !== null || deviceFlow !== null
  // A pending attempt belongs to the provider it was started for. Selecting another
  // provider must not re-point or drop it, so the form points back to it instead.
  const attemptProvider = (oauthFlow ?? deviceFlow)?.provider
  const attemptElsewhere = attemptProvider !== undefined && attemptProvider !== selected
  const askIdentity = method?.kind === 'manual' || Boolean(method?.askIdentity) || needIdentity
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
    if (!response.ok) throw new RequestError(payload?.error?.message || 'Provider action failed.', payload, response.status)
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
    // Only the manual method submits the form; sign-in methods have their own buttons.
    if (!selectedProvider || method?.kind !== 'manual' || !account.name.trim() || !account.identity.trim()) return
    const authKind = method.authKind
    await action('account', 'Provider connection saved.', async () => {
      const secret: Record<string, string | Record<string, string>> = {}
      if (authKind === 'cookie') secret.cookie = account.cookie
      else if (authKind !== 'none') { secret.accessToken = account.accessToken; if (account.refreshToken) secret.refreshToken = account.refreshToken }
      if (selected === 'vertex-partner' && projectId.trim()) secret.providerData = { projectId: projectId.trim() }
      await send('/connections', 'POST', { providerId: selected, nodeId: selectedNode?.id, name: account.name, identity: account.identity, authKind, secret, enabled: true, priority: selectedConnections.length })
      setAccountOpen(false); resetSecrets(); setProjectId(''); setAccount(EMPTY_ACCOUNT)
    })
  }

  // The identity is sent only when the method asks for it; the name is never asked
  // before a sign-in, and is offered as a nickname once the provider has answered.
  function signInAccount() {
    return askIdentity ? { identity: account.identity } : {}
  }

  function signedIn(connection?: Connection) {
    resetSecrets(); setAccount(EMPTY_ACCOUNT); setNickname('')
    setOutcome({ phase: 'success', message: connection ? `Signed in as ${connection.identity}; the connection is saved.` : 'Provider connection saved.', connection })
  }

  // Reports how the server ended an attempt. Called only with a server answer that
  // ended it (attemptStillOpen is false), never on the clock or on silence.
  function attemptFailed(cause: unknown) {
    if (errorCodeOf(cause) === 'oauth_identity_unresolved') setNeedIdentity(true)
    const phase = phaseForError(errorCodeOf(cause))
    if (phase === 'closed') { void load().then(() => setReload(value => value + 1)) }
    setOutcome(phase === 'expired'
      ? { phase, message: 'The provider reports the sign-in code expired. Nothing was saved; start again.' }
      : phase === 'closed' ? { phase, message: CLOSED_MESSAGE }
      : { phase, message: cause instanceof Error ? cause.message : 'Sign-in failed.' })
  }

  async function startOAuth() {
    if (!selectedProvider || (askIdentity && !account.identity.trim())) return
    setOauthInput(''); setOutcome(null)
    await action('oauth-start', '', async () => {
      const flow = await send(`/oauth/${encodeURIComponent(selected)}/start`, 'POST', { ...signInAccount(), meta: flowMeta() }) as Omit<OAuthFlow, 'provider'>
      setOauthFlow({ ...flow, provider: selected })
    })
  }

  // Ends the pending attempt on the server. The attempt stays tracked (and a
  // device poll keeps running) until the server confirms it is gone, so a failed
  // cancel can be retried and a save already under way still reports its result.
  // Reports whether the attempt is no longer live.
  async function cancelAttempt(): Promise<boolean> {
    const flow = oauthFlow ?? deviceFlow
    if (!flow) return true
    const state = flow.state
    setBusy('oauth-cancel'); setError(''); setAttemptNote('')
    let status: number | undefined, code: string | undefined, message = ''
    try {
      await send(`/oauth/${encodeURIComponent(flow.provider)}/cancel`, 'POST', { state })
      status = 200
    } catch (cause) {
      status = cause instanceof RequestError ? cause.status : undefined
      code = errorCodeOf(cause)
      message = cause instanceof Error ? cause.message : ''
    } finally { setBusy('') }
    // A poll may have finished the attempt while the cancel was in flight; its
    // result is the truth, not this cancel's.
    if (liveState.current !== state) return true
    switch (cancelResult(status, code)) {
      case 'cancelled':
        setOauthFlow(null); setOauthInput(''); setDeviceFlow(null)
        setOutcome({ phase: 'cancelled', message: 'Sign-in cancelled. Nothing was saved.' })
        return true
      case 'saving':
        setAttemptNote('The provider already approved this sign-in and it is being saved, so it can no longer be cancelled. Waiting for the result.')
        return false
      case 'closed':
        setOauthFlow(null); setOauthInput(''); setDeviceFlow(null)
        setOutcome({ phase: 'closed', message: CLOSED_MESSAGE })
        await load(); setReload(value => value + 1)
        return true
      default:
        setAttemptNote(`The sign-in could not be cancelled${message ? ` (${message})` : ''}. It may still be open; try again.`)
        return false
    }
  }

  // Closing the panel abandons a pending attempt on the server as well; if the
  // server did not confirm, the panel stays open on that attempt.
  async function closeAccount() {
    if ((oauthFlow || deviceFlow) && !await cancelAttempt()) return
    setAccountOpen(false); resetSecrets(); setAccount(EMPTY_ACCOUNT); setNickname('')
  }

  async function saveNickname(connection: Connection) {
    const name = nickname.trim()
    if (!name) { setOutcome(null); setAccountOpen(false); return }
    await action(`nickname-${connection.id}`, 'Connection name saved.', async () => {
      await send(`/connections/${encodeURIComponent(connection.id)}`, 'PATCH', { name })
      setOutcome(null); setNickname(''); setAccountOpen(false)
    })
  }

  // The operator input a flow needs beyond name/identity. Empty values are dropped
  // so a flow sees "not supplied" rather than an empty string.
  function flowMeta(): Record<string, string> {
    const meta: Record<string, string> = {}
    for (const [key, value] of Object.entries(metaInput)) {
      if (value.trim()) meta[key] = value.trim()
    }
    return meta
  }

  // Clears every credential-bearing field. Called on close and on successful save:
  // a typed client secret or pasted token must not outlive the panel, and must not
  // be re-sent by the next sign-in.
  function resetSecrets() {
    setOauthFlow(null)
    setOauthInput('')
    setImportInput('')
    setMetaInput({})
    setDeviceFlow(null)
    setOutcome(null)
    setNeedIdentity(false)
    setAttemptNote('')
    setPollStalled(false)
  }

  async function startDevice() {
    if (!selectedProvider || (askIdentity && !account.identity.trim())) return
    setOutcome(null)
    await action('device-start', '', async () => {
      const flow = await send(`/oauth/${encodeURIComponent(selected)}/device`, 'POST', { ...signInAccount(), meta: flowMeta() }) as Omit<DeviceFlow, 'provider'>
      setDeviceFlow({ ...flow, provider: selected })
    })
  }

  // An import has no attempt to track: the credential is validated and stored in
  // one request.
  async function importOnly() {
    if (!selectedProvider || !importInput.trim() || (askIdentity && !account.identity.trim())) return
    setOutcome(null)
    await action('oauth-import', '', async () => {
      try {
        signedIn(await send(`/oauth/${encodeURIComponent(selected)}/import`, 'POST', { ...signInAccount(), input: importInput, meta: flowMeta() }) as Connection)
      } catch (cause) {
        const code = errorCodeOf(cause)
        if (code === 'oauth_identity_unresolved') setNeedIdentity(true)
        // No answer: the import may have landed, so do not invite a blind retry.
        if (code === undefined) {
          await load(); setReload(value => value + 1)
          throw new Error('No answer from the server; the import may have been saved. Check the connections below before importing again.')
        }
        throw cause
      }
    })
  }

  async function finishOAuth() {
    if (!oauthFlow || !oauthInput.trim()) return
    const flow = oauthFlow
    const input = oauthInput
    setBusy('oauth-finish'); setError(''); setNotice(''); setAttemptNote('')
    try {
      // finishOAuth only ever handles a flow that came from /start, so it is always
      // a browser flow and always completes on /callback — even when the operator
      // pastes a token (kimchi). Import-only providers use importOnly() instead, so
      // routing on the completion hint here sent kimchi to a 404.
      signedIn(await send(`/oauth/${encodeURIComponent(flow.provider)}/callback`, 'POST', { state: flow.state, input }) as Connection)
      await load(); setReload(value => value + 1)
    } catch (cause) {
      const code = errorCodeOf(cause)
      if (code === undefined) {
        // No answer: the exchange may have saved the connection. Keep the attempt;
        // finishing again gets the server's answer (a used attempt reads as closed).
        setAttemptNote('No answer from the server. The sign-in may have completed; check the connections below, then finish again or cancel.')
        await load(); setReload(value => value + 1)
      } else if (attemptStillOpen(code)) {
        // A paste the server refused before touching the attempt: paste again.
        setError(cause instanceof Error ? cause.message : 'Sign-in failed.')
      } else {
        resetSecrets(); attemptFailed(cause)
      }
    } finally { setBusy('') }
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

  // Selecting a provider resets the connection panel, except while an attempt is
  // pending: dropping it would abandon a sign-in the server still holds.
  const option = (row: typeof rows[number]) => {
    const bad = row.requests > 0 && badRate(row.errors, row.requests, fleet)
    return <button className={`provider-option${selected === row.provider.id ? ' provider-option-selected' : ''}`} type="button" key={row.provider.id} onClick={() => { setSelected(row.provider.id); setConnectionPage(1); setSeedPage(1); if (!pending) { setAccountOpen(false); resetSecrets() } }}>
      <Status tone={tone(row)}><strong>{row.provider.id}</strong></Status>
      <span className="provider-option-meta">{row.provider.authModes.join(' / ') || row.provider.auth} · {row.provider.transports.length} transport{row.provider.transports.length === 1 ? '' : 's'}{row.total ? ` · ${row.provider.enabledAccounts}/${row.total} on` : ''} · <span className={`readiness-text readiness-${readinessStatus(row.provider)}`}>{READINESS_LABEL[readinessStatus(row.provider)].toLowerCase()}</span></span>
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
      <div className="detail-panel provider-summary"><div className="panel-heading"><div><p className="eyebrow">{selectedNode ? 'GENERIC PROVIDER' : 'BUILT-IN PROVIDER'}</p><h2>{selectedProvider.id}</h2></div><span className="provider-tags"><span className="state-tag">{selectedProvider.authModes.join(' / ') || selectedProvider.auth}</span>{!selectedNode && <ReadinessTag provider={selectedProvider} />}</span></div><p>{selectedProvider.transports.join(' · ')} · models: {selectedProvider.modelCatalog}{selectedProvider.passthroughModels ? ' + passthrough' : ''}</p>{!selectedNode && readinessNote(selectedProvider) && <p className="readiness-note" role="note">{readinessNote(selectedProvider)}</p>}{selectedNode?.baseUrl && <code className="technical-value">{selectedNode.baseUrl}</code>}
        <button className="button-primary" type="button" onClick={() => { if (accountOpen) void closeAccount(); else { setMethodId(''); setAccountOpen(true) } }}>Add connection</button>{selectedNode && <><button className="button-secondary" type="button" onClick={() => { setGenericNodeID(selectedNode.id); setGeneric({ name: selectedNode.name, prefix: selectedNode.prefix || selectedNode.providerId, baseUrl: selectedNode.baseUrl || '', transports: selectedNode.transports }); setGenericOpen(true) }}>Edit Generic Provider</button><button className="button-danger" type="button" onClick={() => { if (window.confirm(`Delete ${selectedNode.name} and all its connections?`)) void action('delete-generic', 'Generic Provider deleted.', () => send(`/provider-nodes/${encodeURIComponent(selectedNode.id)}`, 'DELETE').then(() => { setSelected('') })) }}>Delete Generic Provider</button></>}
      </div>

      {accountOpen && method && <form className="detail-panel provider-form" onSubmit={event => void createAccount(event)}><div className="section-heading"><div><h3>New connection</h3><p>Credential material stays server-side and is sealed at rest.</p></div><button className="button-ghost" type="button" onClick={() => void closeAccount()}>Close</button></div><div className="provider-form-grid">
        {attemptElsewhere ? <div className="wide-field oauth-panel" role="status"><p>A {providerLabel(attemptProvider)} sign-in is still pending. Finish or cancel it before starting another.</p><div><button className="button-secondary" type="button" onClick={() => setSelected(attemptProvider)}>Back to {providerLabel(attemptProvider)}</button></div></div> : <>
        {methods.length > 1 && <fieldset className="wide-field access-picker" disabled={pending}><legend>Connect with</legend><div className="access-modes" role="radiogroup" aria-label="Connection method">
          {methods.map(item => <label key={item.id}><input type="radio" name="connect-method" checked={item.id === method.id} onChange={() => { resetSecrets(); setMethodId(item.id) }} /><span><strong>{item.label}</strong></span></label>)}
        </div></fieldset>}
        {method.kind === 'manual' && <><label>Account name<input required value={account.name} onChange={event => setAccount({ ...account, name: event.target.value })} /></label><label>Stable account identity<input required value={account.identity} onChange={event => setAccount({ ...account, identity: event.target.value })} /></label></>}
        {method.kind !== 'manual' && askIdentity && !pending && !outcome?.connection && <label className="wide-field">Stable account identity<input required value={account.identity} onChange={event => setAccount({ ...account, identity: event.target.value })} /><small>{providerLabel(selected)} does not report which account signed in, so name it here, for example its email.</small></label>}
        {method.kind !== 'manual' && method.metaKeys.length > 0 && !pending && !outcome?.connection && <div className="wide-field oauth-panel" role="group" aria-label="Sign-in parameters">
          <p>Only fill what this provider requires. Leave the rest blank.</p>
          {method.metaKeys.map(key => {
            const field = META_FIELDS.find(item => item.key === key)
            // An unknown key still renders: better a raw label than a field the
            // flow needs and the operator cannot see.
            if (field?.options) {
              return <label key={key}>{field.label}<select value={metaInput[key] ?? ''} onChange={event => setMetaInput({ ...metaInput, [key]: event.target.value })}><option value="">Default</option>{field.options.map(option => <option key={option} value={option}>{option}</option>)}</select></label>
            }
            return <label key={key}>{field?.label ?? key}<input type={field?.secret ? 'password' : 'text'} autoComplete="off" spellCheck={false} placeholder={field?.placeholder} value={metaInput[key] ?? ''} onChange={event => setMetaInput({ ...metaInput, [key]: event.target.value })} /></label>
          })}
        </div>}
        {outcome && <div className="wide-field oauth-panel" role={outcome.phase === 'success' ? 'status' : 'alert'} aria-label="Sign-in result">
          <p><span className={`state-tag ${outcome.phase === 'success' ? 'state-good' : 'state-warn'}`}>{ATTEMPT_LABEL[outcome.phase]}</span> {outcome.message}</p>
          {outcome.connection ? <><label>Nickname (optional)<input value={nickname} placeholder={outcome.connection.name} maxLength={200} onChange={event => setNickname(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); void saveNickname(outcome.connection!) } }} /></label>
            <div className="oauth-actions"><button className="button-primary" type="button" disabled={busy !== ''} onClick={() => void saveNickname(outcome.connection!)}>{nickname.trim() ? 'Save nickname' : 'Done'}</button></div></>
            : outcome.phase === 'success' && <div className="oauth-actions"><button className="button-primary" type="button" onClick={() => void closeAccount()}>Done</button></div>}
        </div>}
        {method.kind === 'device' && !outcome?.connection && <div className="wide-field oauth-panel" role="group" aria-label={method.label}>
          {!deviceFlow ? <><p>Approve this dashboard on the provider's page with a one-time code. The account is read from the provider.</p><div><button className="button-primary" type="button" disabled={busy !== '' || (askIdentity && !account.identity.trim())} onClick={() => void startDevice()}>{busy === 'device-start' ? 'Starting…' : method.label}</button></div></>
            : <><p>Open the page below and confirm this code. This dashboard finishes the connection on its own once you approve.</p><p><code className="technical-value" aria-label="Device code">{deviceFlow.userCode}</code></p><div className="oauth-actions"><a className="button-secondary" href={deviceFlow.verificationUrl} target="_blank" rel="noopener noreferrer">Open {providerLabel(deviceFlow.provider)} sign-in page</a><button className="button-ghost" type="button" disabled={busy === 'oauth-cancel'} onClick={() => void cancelAttempt()}>{busy === 'oauth-cancel' ? 'Cancelling…' : 'Cancel sign-in'}</button></div><p role="status"><span className="state-tag">Pending</span> Waiting for approval{deviceFlow.expiresAt ? `; the code expires at ${new Date(deviceFlow.expiresAt).toLocaleTimeString()}` : ''}.</p>{pastExpiry && <p>The time limit has passed. A sign-in approved before it may still be saving, so this waits for the server's answer.</p>}{attemptNote && <p role="alert">{attemptNote}</p>}{pollStalled && <div><button className="button-secondary" type="button" onClick={() => { setAttemptNote(''); setPollStalled(false) }}>Check again</button></div>}</>}
        </div>}
        {method.kind === 'import' && !outcome?.connection && <div className="wide-field oauth-panel" role="group" aria-label={method.label}>
          <p>Paste the credential {providerLabel(selected)} issued. It is validated with the provider before it is stored{askIdentity ? '' : ', and the account is read from it'}.</p>
          <label>{method.authKind === 'cookie' ? 'Cookie' : method.authKind === 'api-key' ? 'API key or token' : 'Token'}<input type="password" autoComplete="off" spellCheck={false} value={importInput} onChange={event => setImportInput(event.target.value)} /></label>
          <div className="oauth-actions"><button className="button-primary" type="button" disabled={busy !== '' || !importInput.trim() || (askIdentity && !account.identity.trim())} onClick={() => void importOnly()}>{busy === 'oauth-import' ? 'Saving…' : 'Import credential'}</button></div>
        </div>}
        {method.kind === 'browser' && !outcome?.connection && <div className="wide-field oauth-panel" role="group" aria-label={method.label}>
          {!oauthFlow ? <><p>Sign in on the provider's page instead of copying tokens by hand.{askIdentity ? '' : ' The account is read from the provider, and you can name it afterwards.'}</p><div><button className="button-primary" type="button" disabled={busy !== '' || (askIdentity && !account.identity.trim())} onClick={() => void startOAuth()}>{busy === 'oauth-start' ? 'Starting…' : method.label}</button></div></>
            : <><p>{method.loopbackRedirect && oauthFlow.completion === 'redirect-url' ? loopbackHint(method.loopbackRedirect) : OAUTH_HINTS[oauthFlow.completion]}</p><div><a className="button-secondary" href={oauthFlow.authorizeUrl} target="_blank" rel="noopener noreferrer">Open {providerLabel(oauthFlow.provider)} sign-in page</a></div>
              <label>{oauthFlow.completion === 'code' ? 'Code from the provider' : oauthFlow.completion === 'token' ? 'Token, cookie or key' : 'Address from the browser'}<input type={oauthFlow.completion === 'code' || oauthFlow.completion === 'token' ? 'password' : 'text'} autoComplete="off" spellCheck={false} value={oauthInput} onChange={event => setOauthInput(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); void finishOAuth() } }} /></label>
              <div className="oauth-actions"><button className="button-primary" type="button" disabled={busy !== '' || !oauthInput.trim()} onClick={() => void finishOAuth()}>{busy === 'oauth-finish' ? 'Finishing…' : 'Finish sign-in'}</button><button className="button-ghost" type="button" disabled={busy === 'oauth-cancel'} onClick={() => void cancelAttempt()}>{busy === 'oauth-cancel' ? 'Cancelling…' : 'Cancel sign-in'}</button></div>
              <p role="status"><span className="state-tag">Pending</span> Waiting for the provider's answer{oauthFlow.expiresAt ? `; this sign-in expires at ${new Date(oauthFlow.expiresAt).toLocaleTimeString()}` : ''}.</p>{pastExpiry && <p>The time limit has passed, so the server will most likely refuse this sign-in. Finish or cancel it to get the server's answer.</p>}{attemptNote && <p role="alert">{attemptNote}</p>}</>}
        </div>}
        {method.kind === 'manual' && <>
          {selected === 'vertex-partner' && <label className="wide-field">Project ID<input required value={projectId} onChange={event => setProjectId(event.target.value)} placeholder="my-gcp-project" /></label>}
          {method.authKind === 'cookie' ? <label className="wide-field">Cookie/session<input type="password" autoComplete="new-password" value={account.cookie} onChange={event => setAccount({ ...account, cookie: event.target.value })} /></label> : method.authKind !== 'none' && <><label className="wide-field">Access token / API key<input required type="password" autoComplete="new-password" value={account.accessToken} onChange={event => setAccount({ ...account, accessToken: event.target.value })} /></label>{method.authKind === 'oauth' && <label className="wide-field">Refresh token<input type="password" autoComplete="new-password" value={account.refreshToken} onChange={event => setAccount({ ...account, refreshToken: event.target.value })} /></label>}</>}
        </>}
        </>}
      </div>{method.kind === 'manual' && !attemptElsewhere && <button className="button-primary" type="submit" disabled={busy === 'account'}>{busy === 'account' ? 'Saving…' : 'Save connection'}</button>}</form>}

      <section className="detail-panel" aria-labelledby="connection-list-title"><div className="section-heading"><div><h3 id="connection-list-title">Connections</h3><p>{selectedConnections.length} account{selectedConnections.length === 1 ? '' : 's'} · lower priority routes first</p></div></div>{selectedConnections.length === 0 ? <p className="empty-keys">No connections configured.</p> : <div className="connection-list">{connectionRows.map((connection,index) => <article className="connection-row" key={connection.id}><div className="connection-main"><strong>{connection.name}</strong><code>{connection.identity}</code></div><span className={`state-tag${connection.enabled ? ' state-good' : ' state-warn'}`}>{connection.enabled ? 'Enabled' : 'Disabled'}</span><label className="proxy-select">Proxy<select aria-label={`Proxy for ${connection.name}`} value={connection.proxyPoolId || ''} onChange={event => void action(`proxy-${connection.id}`, 'Proxy assignment saved.', () => send(`/connections/${encodeURIComponent(connection.id)}/proxy`, 'POST', { proxyPoolId: event.target.value }).then(() => undefined))}><option value="">No proxy pool</option>{proxyPools.map(pool => <option value={pool.id} key={pool.id}>{pool.name}{pool.enabled ? '' : ' (disabled)'}</option>)}</select></label><div className="connection-actions"><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{enabled:!connection.enabled})}>{connection.enabled ? 'Disable' : 'Enable'}</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{priority:connection.priority-1})} aria-label={`Move ${connection.name} up`} title="Move up">↑</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void changeConnection(connection,{priority:connection.priority+1})} aria-label={`Move ${connection.name} down`} title="Move down">↓</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void testConnection(connection)}>{busy === `test-${connection.id}` ? 'Checking…' : 'Check'}</button><button className="button-secondary" type="button" disabled={busy !== ''} onClick={() => void discover(connection)}>{busy === `discover-${connection.id}` ? 'Discovering…' : 'Discover models'}</button><button className="button-danger" type="button" disabled={busy !== ''} onClick={() => { if (window.confirm(`Delete account “${connection.name}”?`)) void action(connection.id, 'Connection deleted.', () => send(`/connections/${encodeURIComponent(connection.id)}`, 'DELETE').then(() => undefined)) }}>Delete</button></div><small>Priority {connection.priority} · {connection.credentialConfigured ? 'credential stored' : 'no credential'} · position {(connectionPage - 1) * PAGE_SIZE + index + 1}</small></article>)}</div>}<Pager label="Connection" page={connectionPage} total={selectedConnections.length} onPage={setConnectionPage} /></section>

      <section className="detail-panel" aria-labelledby="models-title"><div className="section-heading"><div><h3 id="models-title">Models</h3><p>{modelList.total.toLocaleString()} configured model{modelList.total === 1 ? '' : 's'}{builtinModels.length > 0 && ` · ${builtinModels.length.toLocaleString()} built-in`}</p></div></div><form className="provider-inline-form" onSubmit={event => void addManualModel(event)}><input required aria-label="Model ID" placeholder="Model ID" value={manual.id} onChange={event => setManual({ ...manual,id:event.target.value })} /><input aria-label="Display name" placeholder="Display name" value={manual.name} onChange={event => setManual({ ...manual,name:event.target.value })} /><input aria-label="Context window" type="number" min="0" placeholder="Context" value={manual.contextWindow} onChange={event => setManual({ ...manual,contextWindow:event.target.value })} /><input aria-label="Capabilities" placeholder="Capabilities, comma-separated" value={manual.capabilities} onChange={event => setManual({ ...manual,capabilities:event.target.value })} /><button className="button-secondary" type="submit">Add manual model</button></form>{modelList.error && <p className="auth-error" role="alert">{modelList.error}</p>}{modelList.items.map(model => <div className="model-row" key={model.id}><code>{model.id}</code><span>{model.name || '—'}</span><small>{model.contextWindow ? `${model.contextWindow.toLocaleString()} tokens` : model.capabilities.join(', ')}</small><span className={`state-tag${model.disabled ? ' state-warn' : ''}`}>{model.disabled ? 'Disabled' : 'Available'}</span><button className="button-ghost" type="button" onClick={() => void action(`model-${model.id}`, 'Model status updated.', () => send('/models/disabled','PATCH',{providerId:selected,modelId:model.id,disabled:!model.disabled}).then(() => undefined))}>{model.disabled ? 'Enable' : 'Disable'}</button>{model.custom && <button className="button-danger" type="button" onClick={() => { if (window.confirm(`Delete custom model “${model.id}”? Its aliases and pricing override are removed. If a built-in model has this ID, it reverts to the built-in entry. Combos that use it must be edited first.`)) void action(`model-delete-${model.id}`, 'Custom model deleted.', () => send('/models','DELETE',{providerId:selected,modelId:model.id}).then(() => undefined)) }}>Delete</button>}</div>)}<Pager label="Model" page={modelList.page} total={modelList.total} loading={modelList.loading} onPage={modelList.setPage} />{builtinModels.length > 0 && <><h4 className="subsection-title">Built-in catalog</h4>{builtinPage.map(id => { const off = disabledIDs.has(id); return <div className="model-row" key={id}><code>{id}</code><span className="state-tag">Built-in</span><span className={`state-tag${off ? ' state-warn' : ''}`}>{off ? 'Disabled' : 'Available'}</span><button className="button-ghost" type="button" disabled={busy !== ''} onClick={() => void action(`model-${id}`, 'Model status updated.', () => send('/models/disabled','PATCH',{providerId:selected,modelId:id,disabled:!off}).then(() => undefined))}>{off ? 'Enable' : 'Disable'}</button></div> })}<Pager label="Built-in model" page={seedPage} total={builtinModels.length} onPage={setSeedPage} /></>}</section>

      <section className="detail-panel" aria-labelledby="alias-title"><div className="section-heading"><div><h3 id="alias-title">Aliases</h3><p>Stable names mapped to this provider’s models.</p></div></div><form className="provider-inline-form" onSubmit={event => void addAlias(event)}><input required aria-label="Alias" placeholder="Alias" value={alias} onChange={event => setAlias(event.target.value)} /><input required list="provider-model-choices" aria-label="Alias target model" placeholder="Target model ID" value={aliasTarget} onChange={event => setAliasTarget(event.target.value)} /><button className="button-secondary" type="submit" disabled={!alias.trim() || !aliasTarget.trim()}>Add alias</button></form>{aliasList.error && <p className="auth-error" role="alert">{aliasList.error}</p>}{aliasList.items.map(item => <div className="model-row" key={item.alias}><code>{item.alias}</code><span>→ {item.modelId}</span><button className="button-danger" type="button" onClick={() => void action(`alias-${item.alias}`,'Alias deleted.',() => send(`/aliases/${encodeURIComponent(item.alias)}`,'DELETE').then(() => undefined))}>Delete</button></div>)}<Pager label="Alias" page={aliasList.page} total={aliasList.total} loading={aliasList.loading} onPage={aliasList.setPage} /></section>

      <section className="detail-panel" aria-labelledby="pricing-title"><div className="section-heading"><div><h3 id="pricing-title">Pricing overrides</h3><p>Rates per million tokens. Empty fields stay unset.</p></div></div><form className="provider-inline-form pricing-form" onSubmit={event => void addPricing(event)}><input required list="provider-model-choices" aria-label="Pricing model" placeholder="Model ID" value={price.modelId} onChange={event => setPrice({...price,modelId:event.target.value})} /><input aria-label="Input price per million" type="number" min="0" step="any" placeholder="Input / MTok" value={price.input} onChange={event => setPrice({...price,input:event.target.value})} /><input aria-label="Output price per million" type="number" min="0" step="any" placeholder="Output / MTok" value={price.output} onChange={event => setPrice({...price,output:event.target.value})} /><input aria-label="Cache read price per million" type="number" min="0" step="any" placeholder="Cache read" value={price.cacheRead} onChange={event => setPrice({...price,cacheRead:event.target.value})} /><input aria-label="Cache write price per million" type="number" min="0" step="any" placeholder="Cache write" value={price.cacheWrite} onChange={event => setPrice({...price,cacheWrite:event.target.value})} /><button className="button-secondary" type="submit">Save rates</button></form>{priceList.error && <p className="auth-error" role="alert">{priceList.error}</p>}{priceList.items.map(item => <div className="model-row" key={item.modelId}><code>{item.modelId}</code><span>in {item.inputPerMTok ?? '—'} · out {item.outputPerMTok ?? '—'}</span><span>cache {item.cacheReadPerMTok ?? '—'} / {item.cacheWritePerMTok ?? '—'}</span><button className="button-danger" type="button" onClick={() => void action(`price-${item.modelId}`,'Pricing override deleted.',() => send('/pricing','DELETE',{providerId:selected,modelId:item.modelId}).then(() => undefined))}>Delete</button></div>)}<Pager label="Pricing" page={priceList.page} total={priceList.total} loading={priceList.loading} onPage={priceList.setPage} /></section>
    </>}</section></div>
  </div>
}
