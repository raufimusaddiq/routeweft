import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { QUOTA_PAGE_SIZE, QUOTA_SORTS, cachedAt, quotaQuery, remainingText, statusLabel, statusTone, until, windowLabel, windowValue, zoned, type QuotaItem } from './quotaView'

type Props = { onUnauthorized: () => void }
type QuotaPage = { items: QuotaItem[]; page: number; total: number; providers?: string[] }

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

function Reset({ value, label = 'resets' }: { value?: string; label?: string }) {
  const local = zoned(value)
  if (!value || !local) return <span className="quota-reset">no reset time</span>
  return <span className="quota-reset" title={zoned(value, 'UTC') ?? undefined}>{label} <time dateTime={value}>{local}</time> ({until(value)})</span>
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
    return () => controller.abort()
  }, [load, reload])

  async function refresh() {
    setRefreshing(true); setError(''); setNotice('')
    try {
      const response = await fetch('/admin/v1/quota/refresh', { method: 'POST', credentials: 'same-origin' })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not request a quota refresh.'))
      setNotice('Quota refresh requested. It runs in the background and never blocks inference.')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not request a quota refresh.')
    } finally { setRefreshing(false) }
  }

  function applySearch(event: FormEvent) {
    event.preventDefault()
    setPage(1); setSearch(searchDraft)
  }

  const pages = Math.max(1, Math.ceil(total / QUOTA_PAGE_SIZE))
  const filtered = provider !== '' || search !== ''

  return <div className="quota-content" aria-busy={loading}>
    <div className="quota-toolbar accent-surface"><span className="count-label" role="status">{loading ? 'Loading…' : `${total.toLocaleString()} account${total === 1 ? '' : 's'}${filtered ? ' match' : ''}`}</span><button className="button-primary" type="button" disabled={refreshing} onClick={() => void refresh()}>{refreshing ? 'Requesting…' : 'Refresh quota'}</button><button className="button-secondary" type="button" disabled={loading} onClick={() => setReload(value => value + 1)}>Reload</button></div>
    <form className="quota-filters" onSubmit={applySearch} role="search" aria-label="Quota accounts">
      <label htmlFor="quota-provider">Provider</label>
      <select id="quota-provider" value={provider} onChange={event => { setPage(1); setProvider(event.target.value) }}><option value="">All providers</option>{providers.map(id => <option key={id} value={id}>{id}</option>)}</select>
      <label htmlFor="quota-search">Account</label>
      <input id="quota-search" type="search" autoComplete="off" value={searchDraft} onChange={event => setSearchDraft(event.target.value)} placeholder="Name or provider" />
      <button className="button-secondary" type="submit" disabled={loading}>Search</button>
      <label htmlFor="quota-sort">Sort</label>
      <select id="quota-sort" value={sort} onChange={event => { setPage(1); setSort(event.target.value) }}>{QUOTA_SORTS.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
    </form>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    <section className="detail-panel" aria-labelledby="quota-title">
      <div className="section-heading"><div><h2 id="quota-title">Provider accounts</h2><p>Remaining share of each limit window and its reset, where the provider reports one. Times show your zone; hover for UTC. Refresh is asynchronous.</p></div></div>
      {items.length === 0 && !loading ? <p className="empty-keys">{filtered ? 'No accounts match these filters.' : 'No provider connections configured.'}</p> : <div className="quota-list" aria-live="polite">{items.map(item => <article className="quota-row" key={item.connectionId} aria-label={`${item.name} (${item.providerId})`}>
        <div className="quota-main"><strong>{item.name}</strong><code>{item.providerId}</code>{item.plan && <small className="quota-plan">plan {item.plan}</small>}</div>
        <span className={`state-tag ${statusTone(item.status)}`}>{statusLabel(item.status)}</span>
        <span className="quota-remaining">{remainingText(item)}</span>
        {item.cooldownUntil ? <Reset value={item.cooldownUntil} label="cooldown until" /> : <Reset value={item.nextResetAt ?? item.resetAt} label="next reset" />}
        <span className="count-label">{item.observedAt ? <>observed <time dateTime={item.observedAt}>{zoned(item.observedAt)}</time></> : 'not yet observed'}</span>
        {item.stale && <span className="state-tag state-warn">stale</span>}
        {!item.enabled && <span className="state-tag state-warn">disabled</span>}
        {item.creditAvailable === false && <span className="state-tag state-warn">provider reports no usable balance</span>}
        {typeof item.availableResetCredits === 'number' && <span className="count-label">{item.availableResetCredits} reset credit{item.availableResetCredits === 1 ? '' : 's'}</span>}
        {item.windows && item.windows.length > 0 && <ul className="quota-windows" aria-label={`${item.name} limit windows`}>{item.windows.map(window => <li key={window.name}>
          <span className="quota-window-name">{windowLabel(window)}</span>
          <span className="quota-remaining">{windowValue(window)}</span>
          {window.resetAt && <Reset value={window.resetAt} />}
          {cachedAt(window) && <span className="quota-window-note">cached, read <time dateTime={window.observedAt}>{zoned(window.observedAt)}</time></span>}
        </li>)}{item.windowsObservedAt && item.observedAt && Date.parse(item.windowsObservedAt) < Date.parse(item.observedAt) && <li className="quota-window-note">windows from <time dateTime={item.windowsObservedAt}>{zoned(item.windowsObservedAt)}</time>, the last successful read</li>}</ul>}
      </article>)}</div>}
      {total > QUOTA_PAGE_SIZE && <nav className="pagination" aria-label="Quota account pages"><button className="button-secondary" type="button" disabled={page <= 1 || loading} onClick={() => setPage(page - 1)}>Previous</button><span>Page {page} of {pages} · {total.toLocaleString()} accounts</span><button className="button-secondary" type="button" disabled={page >= pages || loading} onClick={() => setPage(page + 1)}>Next</button></nav>}
    </section>
  </div>
}
