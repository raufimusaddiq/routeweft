import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { QUOTA_PAGE_SIZE, QUOTA_SORTS, REFRESH_POLL_LIMIT, REFRESH_POLL_MS, WARMING_DESCRIPTIONS, cachedAt, canSpendResetCredit, creditsByExpiry, notUpdatedBy, parseWarming, quotaQuery, refreshTerminal, refreshText, remainingText, freshCreditCheck, spendConfirmation, spendOutcome, statusLabel, statusTone, until, warmingPatch, warmingProvider, warmingStatusText, windowLabel, windowValue, withWarming, zoned, type QuotaItem, type RefreshJob, type ResetCredits, type SpendOutcome, type WarmingMaps } from './quotaView'

type Props = { onUnauthorized: () => void }
type QuotaPage = { items: QuotaItem[]; page: number; total: number; providers?: string[] }
type CreditView = { loading: boolean; error?: string; data?: ResetCredits }

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

function Reset({ value, label = 'resets' }: { value?: string; label?: string }) {
  const local = zoned(value)
  if (!value || !local) return <span className="quota-reset">no reset time</span>
  return <span className="quota-reset" title={zoned(value, 'UTC') ?? undefined}>{label} <time dateTime={value}>{local}</time> ({until(value)})</span>
}

function CreditList({ name, view }: { name: string; view: CreditView }) {
  if (view.loading) return <p className="quota-credits count-label" role="status">Loading reset credits…</p>
  if (view.error) return <p className="quota-credits auth-error" role="alert">{view.error}</p>
  const list = view.data?.credits ?? []
  if (list.length === 0) return <p className="quota-credits count-label">No reset credit details returned for this account.</p>
  return <ul className="quota-windows quota-credits" aria-label={`${name} reset credits`}>
    <li className="quota-window-note">{list.length} reset credit{list.length === 1 ? '' : 's'}, {view.data?.availableCount ?? 0} available</li>
    {list.map((credit, index) => <li key={`${credit.status}-${credit.expiresAt ?? index}`}>
      <span className="quota-window-name">{credit.status}</span>
      <span className="quota-reset">{credit.grantedAt ? <>granted <time dateTime={credit.grantedAt}>{zoned(credit.grantedAt)}</time></> : 'grant time not reported'}</span>
      {credit.expiresAt ? <Reset value={credit.expiresAt} label="expires" /> : <span className="quota-reset">no expiry reported</span>}
    </li>)}
  </ul>
}

export function Quota({ onUnauthorized }: Props) {
  const [items, setItems] = useState<QuotaItem[]>([])
  const [total, setTotal] = useState(0)
  const [providers, setProviders] = useState<string[]>([])
  const [page, setPage] = useState(1)
  const [sort, setSort] = useState('default')
  const [provider, setProvider] = useState('')
  const [search, setSearch] = useState('')
  const [searchDraft, setSearchDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [reload, setReload] = useState(0)
  const [job, setJob] = useState<RefreshJob | null>(null)
  const [jobGaveUp, setJobGaveUp] = useState(false)
  const following = useRef<AbortController | null>(null)
  const [credits, setCredits] = useState<Record<string, CreditView>>({})
  const [spending, setSpending] = useState('')
  // 'check' while a spend reads the credit list, 'spend' once it is confirmed.
  const [spendPhase, setSpendPhase] = useState<'check' | 'spend'>('check')

  // Per-account warming opt-ins (#297). null until the settings are read: a
  // switch is never drawn from a guess, so an unreadable setting shows no switch
  // rather than a false "off".
  const [warming, setWarming] = useState<WarmingMaps | null>(null)
  const [warmingError, setWarmingError] = useState('')
  const [warmingBusy, setWarmingBusy] = useState('')

  useEffect(() => () => following.current?.abort(), [])

  const loadWarming = useCallback(async (signal: AbortSignal) => {
    try {
      const response = await fetch('/admin/v1/settings', { credentials: 'same-origin', cache: 'no-store', signal })
      if (response.status === 401) { if (!signal.aborted) onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not read the quota warming settings.'))
      const data = await response.json() as { settings?: Record<string, string> }
      if (signal.aborted) return
      setWarming(parseWarming(data.settings)); setWarmingError('')
    } catch (cause) {
      if (!signal.aborted) { setWarming(null); setWarmingError(cause instanceof Error ? cause.message : 'Could not read the quota warming settings.') }
    }
  }, [onUnauthorized])

  const load = useCallback(async (signal: AbortSignal) => {
    setLoading(true)
    setError('')
    try {
      const response = await fetch(`/admin/v1/quota?${quotaQuery(page, sort, provider, search)}`, { credentials: 'same-origin', signal })
      if (response.status === 401) { if (!signal.aborted) onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not load quota.'))
      const data = await response.json() as QuotaPage
      if (signal.aborted) return
      const last = Math.max(1, Math.ceil(data.total / QUOTA_PAGE_SIZE))
      if (page > last) { setPage(last); return }
      setItems(data.items); setTotal(data.total); setProviders(data.providers ?? [])
    } catch (cause) {
      if (!signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not load quota.')
    } finally { if (!signal.aborted) setLoading(false) }
  }, [onUnauthorized, page, sort, provider, search])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    void loadWarming(controller.signal)
    return () => controller.abort()
  }, [load, loadWarming, reload])

  // Warming is usage-increasing, so a change is one deliberate click and is saved
  // at once. The latest stored map is read first and only this account's entry is
  // changed in it, as the pin does (index.js:557-577), so an opt-in made elsewhere
  // is not undone. A failed save leaves the switch as it was and says so; the pin
  // reverts on a network error only and ignores an HTTP error, which this does not.
  async function toggleWarming(item: QuotaItem, on: boolean) {
    const provider = warmingProvider(item)
    if (!provider || warmingBusy) return
    setWarmingBusy(item.connectionId); setError(''); setNotice('')
    try {
      const current = await fetch('/admin/v1/settings', { credentials: 'same-origin', cache: 'no-store' })
      if (current.status === 401) { onUnauthorized(); return }
      if (!current.ok) throw new Error(await responseError(current, 'Could not read the quota warming settings.'))
      const stored = parseWarming(((await current.json()) as { settings?: Record<string, string> }).settings)
      const next = withWarming(stored[provider], item.connectionId, on)
      const response = await fetch('/admin/v1/settings', { method: 'PATCH', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(warmingPatch(provider, next)) })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not save the quota warming setting.'))
      setWarming({ ...stored, [provider]: next })
      setNotice(on ? `Quota warming is on for ${item.name}. It sends a small request after each reset.` : `Quota warming is off for ${item.name}.`)
    } catch (cause) {
      setError(`${cause instanceof Error ? cause.message : 'Could not save the quota warming setting.'} Warming for ${item.name} was not changed.`)
    } finally { setWarmingBusy('') }
  }

  // A 202 only accepts the refresh. The page follows the job and reloads the
  // rows when it ends; until then they keep their previous observations (#226).
  async function refresh() {
    following.current?.abort()
    const controller = new AbortController()
    following.current = controller
    setRefreshing(true); setError(''); setNotice(''); setJobGaveUp(false)
    try {
      const response = await fetch('/admin/v1/quota/refresh', { method: 'POST', credentials: 'same-origin', signal: controller.signal })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not request a quota refresh.'))
      let current = ((await response.json()) as { job?: RefreshJob }).job
      if (!current) { setNotice('Quota refresh requested. Its progress is not reported; use Reload later.'); return }
      setJob(current)
      for (let polls = 0; !refreshTerminal(current) && polls < REFRESH_POLL_LIMIT; polls++) {
        await new Promise(resolve => setTimeout(resolve, REFRESH_POLL_MS))
        if (controller.signal.aborted) return
        const status = await fetch('/admin/v1/quota/refresh', { credentials: 'same-origin', signal: controller.signal })
        if (status.status === 401) { onUnauthorized(); return }
        if (!status.ok) throw new Error(await responseError(status, 'Could not read the quota refresh.'))
        const latest = ((await status.json()) as { job?: RefreshJob }).job
        if (!latest || latest.id !== current.id) break
        current = latest
        setJob(current)
      }
      if (controller.signal.aborted) return
      if (refreshTerminal(current)) setReload(value => value + 1)
      else setJobGaveUp(true)
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not request a quota refresh.')
    } finally { if (!controller.signal.aborted) setRefreshing(false) }
  }

  async function showCredits(item: QuotaItem) {
    if (credits[item.connectionId] && !credits[item.connectionId].loading) {
      setCredits(({ [item.connectionId]: _, ...rest }) => rest)
      return
    }
    await loadCredits(item)
  }

  // A live read of Codex's credit list; it replaces whatever list was shown.
  // It returns the list, or null when it could not be read.
  async function loadCredits(item: QuotaItem): Promise<ResetCredits | null> {
    setCredits(current => ({ ...current, [item.connectionId]: { loading: true } }))
    let view: CreditView
    try {
      const response = await fetch(`/admin/v1/quota/${encodeURIComponent(item.connectionId)}/codex-reset-credits`, { credentials: 'same-origin', cache: 'no-store' })
      if (response.status === 401) { onUnauthorized(); return null }
      if (!response.ok) throw new Error(await responseError(response, 'Could not load Codex reset credits.'))
      const data = await response.json() as ResetCredits
      view = { loading: false, data: { availableCount: data.availableCount, credits: creditsByExpiry(data.credits ?? []) } }
    } catch (cause) {
      view = { loading: false, error: cause instanceof Error ? cause.message : 'Could not load Codex reset credits.' }
    }
    setCredits(current => ({ ...current, [item.connectionId]: view }))
    return view.data ?? null
  }

  // A freshly read credit list is newer than the row's cached count.
  function withFreshCount(item: QuotaItem): QuotaItem {
    const fresh = credits[item.connectionId]?.data
    return fresh ? { ...item, availableResetCredits: fresh.availableCount } : item
  }

  // One credit per confirmed click, and every spend starts with a live read of
  // Codex's credit list: the confirmation and the go-ahead come from that read,
  // never from a count read earlier, on this page or before it was opened
  // again, so an earlier spend with an unknown outcome cannot be followed by
  // another without a fresh read. A failed read spends nothing. The request is
  // never repeated; an unknown outcome is reported and the list read again.
  async function spendCredit(item: QuotaItem) {
    if (spending || credits[item.connectionId]?.loading || !canSpendResetCredit(withFreshCount(item))) return
    setSpending(item.connectionId); setSpendPhase('check'); setError(''); setNotice('')
    const fresh = await loadCredits(item)
    const blocked = freshCreditCheck(fresh)
    if (blocked || !fresh) { setError(blocked); setSpending(''); return }
    if (!window.confirm(spendConfirmation({ ...item, availableResetCredits: fresh.availableCount }))) { setSpending(''); return }
    setSpendPhase('spend')
    let outcome: SpendOutcome
    try {
      const response = await fetch(`/admin/v1/quota/${encodeURIComponent(item.connectionId)}/codex-reset-credits`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm: true }) })
      if (response.status === 401) { onUnauthorized(); setSpending(''); return }
      outcome = spendOutcome(response.status, await response.json().catch(() => null))
    } catch {
      outcome = spendOutcome(0, null)
    }
    if (outcome.tone === 'notice') setNotice(outcome.text)
    else setError(outcome.text)
    if (outcome.reload) {
      setCredits(({ [item.connectionId]: _, ...rest }) => rest)
      setReload(value => value + 1)
    }
    // An unknown outcome may have spent the credit: the list shown before the
    // spend is stale, so it is read again.
    if (outcome.unknown) await loadCredits(item)
    setSpending('')
  }

  function applySearch(event: FormEvent) {
    event.preventDefault()
    setPage(1); setSearch(searchDraft)
  }

  const pages = Math.max(1, Math.ceil(total / QUOTA_PAGE_SIZE))
  const filtered = provider !== '' || search !== ''

  return <div className="quota-content page-d" aria-busy={loading}>
    <div className="quota-toolbar accent-surface"><span className="count-label" role="status">{loading ? 'Loading…' : `${total.toLocaleString()} account${total === 1 ? '' : 's'}${filtered ? ' match' : ''}`}</span><button className="button-primary" type="button" disabled={refreshing} onClick={() => void refresh()}>{refreshing ? 'Refreshing…' : 'Refresh quota'}</button><button className="button-secondary" type="button" disabled={loading} onClick={() => setReload(value => value + 1)}>Reload</button></div>
    <form className="quota-filters" onSubmit={applySearch} role="search" aria-label="Quota accounts">
      <label htmlFor="quota-provider">Provider</label>
      <select id="quota-provider" value={provider} onChange={event => { setPage(1); setProvider(event.target.value) }}><option value="">All providers</option>{providers.map(id => <option key={id} value={id}>{id}</option>)}</select>
      <label htmlFor="quota-search">Account</label>
      <input id="quota-search" type="search" autoComplete="off" value={searchDraft} onChange={event => setSearchDraft(event.target.value)} placeholder="Name or provider" />
      <button className="button-secondary" type="submit" disabled={loading}>Search</button>
      <label htmlFor="quota-sort">Sort</label>
      <select id="quota-sort" value={sort} onChange={event => { setPage(1); setSort(event.target.value) }}>{QUOTA_SORTS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
    </form>
    {warmingError && <p className="auth-error" role="alert">{warmingError} Warming switches are hidden until the settings can be read.</p>}
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}
    {job && <p className={job.status === 'failed' ? 'auth-error quota-refresh-state' : 'inline-notice quota-refresh-state'} role="status" data-refresh-status={job.status}>{refreshText(job, jobGaveUp)}</p>}

    <section className="detail-panel" aria-labelledby="quota-title">
      <div className="section-heading"><div><h2 id="quota-title">Provider accounts</h2><p>Remaining share of each limit window and its reset, where the provider reports one. Times show your zone; hover for UTC. Refresh is asynchronous.</p></div></div>
      {items.length === 0 && !loading ? <p className="empty-keys">{filtered ? 'No accounts match these filters.' : 'No provider connections configured.'}</p> : <div className="quota-list" aria-live="polite">{items.map(item => <article className="quota-row" key={item.connectionId} aria-label={`${item.name} (${item.providerId})`}>
        <div className="quota-main"><strong>{item.name}</strong><code>{item.providerId}</code>{item.plan && <small className="quota-plan">plan {item.plan}</small>}</div>
        <span className={`state-tag ${statusTone(item.status)}`}>{statusLabel(item.status)}</span>
        <span className="quota-remaining">{remainingText(item)}</span>
        {item.cooldownUntil ? <Reset value={item.cooldownUntil} label="cooldown until" /> : <Reset value={item.nextResetAt ?? item.resetAt} label="next reset" />}
        <span className="count-label">{item.observedAt ? <>observed <time dateTime={item.observedAt}>{zoned(item.observedAt)}</time></> : 'not yet observed'}</span>
        {item.stale && <span className="state-tag state-warn">stale</span>}
        {notUpdatedBy(item, job) && <span className="state-tag state-warn">not updated by the last refresh</span>}
        {!item.enabled && <span className="state-tag state-warn">disabled</span>}
        {item.creditAvailable === false && <span className="state-tag state-warn">provider reports no usable balance</span>}
        {typeof item.availableResetCredits === 'number' && <span className="count-label">{item.availableResetCredits} reset credit{item.availableResetCredits === 1 ? '' : 's'}</span>}
        {warming && warmingProvider(item) && (() => {
          const provider = warmingProvider(item) as 'claude' | 'codex'
          const on = warming[provider][item.connectionId] === true
          const saving = warmingBusy === item.connectionId
          const helpId = `warming-help-${item.connectionId.replace(/\s/g, '_')}`
          return <div className="quota-warming">
            <label className="switch-control quota-warming-switch"><span className="switch-label" role="status">{warmingStatusText(on, saving)}</span><input type="checkbox" role="switch" checked={on} disabled={warmingBusy !== ''} aria-label={`Quota warming for ${item.name}`} aria-describedby={helpId} onChange={event => void toggleWarming(item, event.target.checked)} /></label>
            <small id={helpId} className="count-label">{WARMING_DESCRIPTIONS[provider]} Off unless you turn it on.</small>
          </div>
        })()}
        {item.providerId === 'codex' && <span className="quota-actions">
          <button className="button-secondary" type="button" aria-expanded={Boolean(credits[item.connectionId])} disabled={credits[item.connectionId]?.loading} onClick={() => void showCredits(item)}>Credit expiry</button>
          <button className="button-danger" type="button" disabled={spending !== '' || Boolean(credits[item.connectionId]?.loading) || !canSpendResetCredit(withFreshCount(item))} title={canSpendResetCredit(withFreshCount(item)) ? 'Reads the credit list from Codex first, then asks before spending one credit.' : 'No reset credit reported for this account'} onClick={() => void spendCredit(item)}>{spending === item.connectionId ? (spendPhase === 'check' ? 'Reading credits…' : 'Using 1 credit…') : 'Use 1 reset credit'}</button>
        </span>}
        {item.windows && item.windows.length > 0 && <ul className="quota-windows" aria-label={`${item.name} limit windows`}>{item.windows.map(window => <li key={window.name}>
          <span className="quota-window-name">{windowLabel(window)}</span>
          <span className="quota-remaining">{windowValue(window)}</span>
          {window.resetAt && <Reset value={window.resetAt} />}
          {cachedAt(window) && <span className="quota-window-note">cached, read <time dateTime={window.observedAt}>{zoned(window.observedAt)}</time></span>}
        </li>)}{item.windowsObservedAt && item.observedAt && Date.parse(item.windowsObservedAt) < Date.parse(item.observedAt) && <li className="quota-window-note">windows from <time dateTime={item.windowsObservedAt}>{zoned(item.windowsObservedAt)}</time>, the last successful read</li>}</ul>}
        {credits[item.connectionId] && <CreditList name={item.name} view={credits[item.connectionId]} />}
      </article>)}</div>}
      {total > QUOTA_PAGE_SIZE && <nav className="pagination" aria-label="Quota account pages"><button className="button-secondary" type="button" disabled={page <= 1 || loading} onClick={() => setPage(page - 1)}>Previous</button><span>Page {page} of {pages} · {total.toLocaleString()} accounts</span><button className="button-secondary" type="button" disabled={page >= pages || loading} onClick={() => setPage(page + 1)}>Next</button></nav>}
    </section>
  </div>
}
