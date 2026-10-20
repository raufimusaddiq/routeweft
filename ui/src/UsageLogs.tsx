import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { EmptyState, ErrorNotice, SkeletonRows } from './kit'
import { Caption, Dot, Main, Row, Status } from './editorial'
import { num, numOrDash, periodStart, statusTone, type UsageEvent } from './format'
import { addLive, connectionLabel, freshLive, parseLiveRow, type Connection, type LiveRow } from './usageLive'

export const LOG_PAGE_SIZE = 25

const recorded = (row: UsageEvent | LiveRow): row is UsageEvent => 'id' in row

type Filters = { requestId: string; providerId: string; modelId: string; connectionId: string; apiKeyId: string; status: string }
const noFilters: Filters = { requestId: '', providerId: '', modelId: '', connectionId: '', apiKeyId: '', status: '' }
const fields: [keyof Filters, string][] = [['requestId', 'Request id'], ['providerId', 'Provider'], ['modelId', 'Model'], ['connectionId', 'Connection'], ['apiKeyId', 'Key id'], ['status', 'Status']]

type Props = { period: string; periodLabel: string; onUnauthorized: () => void; onOpen: (requestId: string) => void }

// The log is the server's own filtered list (GET /admin/v1/usage). Filters are applied on submit, not per keystroke,
// and the selected period becomes its startDate so Overview and Logs always describe the same window.
export function UsageLogs({ period, periodLabel, onUnauthorized, onOpen }: Props) {
  const [draft, setDraft] = useState<Filters>(noFilters)
  const [applied, setApplied] = useState<Filters>(noFilters)
  const [page, setPage] = useState(1)
  const [items, setItems] = useState<UsageEvent[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  const [live, setLive] = useState<LiveRow[]>([])
  const [paused, setPaused] = useState(false)
  const [connection, setConnection] = useState<Connection>('connecting')
  const [attempt, setAttempt] = useState(0)

  const load = useCallback(async () => {
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    const { signal } = abort
    setLoading(true)
    setError('')
    try {
      const query = new URLSearchParams({ page: String(page), pageSize: String(LOG_PAGE_SIZE) })
      for (const [key] of fields) if (applied[key].trim()) query.set(key, applied[key].trim())
      const start = periodStart(period)
      if (start) query.set('startDate', start)
      const response = await fetch(`/admin/v1/usage?${query}`, { credentials: 'same-origin', signal })
      if (signal.aborted) return
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) {
        let message = 'Could not load the request log.'
        try { message = ((await response.json()) as { error?: { message?: string } }).error?.message || message } catch { /* keep the fallback */ }
        throw new Error(message)
      }
      const body = await response.json() as { items: UsageEvent[]; total: number }
      if (signal.aborted) return
      setItems(body.items)
      setTotal(body.total)
    } catch (cause) {
      if (signal.aborted || (cause instanceof DOMException && cause.name === 'AbortError')) return
      setError(cause instanceof Error ? cause.message : 'Could not load the request log.')
    } finally { if (!signal.aborted) setLoading(false) }
  }, [applied, page, period, onUnauthorized])

  useEffect(() => { void load(); return () => controller.current?.abort() }, [load])
  // A new period starts again at the first page.
  useEffect(() => { setPage(1) }, [period])

  // The stream effect must not restart when load's identity changes, so it reads the latest through a ref.
  const reload = useRef(load)
  reload.current = load

  // Live view: completed requests as they finish, scoped by the same filters and period as the log. Only page 1 is live,
  // because the log is newest-first. Pausing closes the stream, which frees the server's subscriber slot.
  useEffect(() => {
    setLive([])
    if (paused) { setConnection('paused'); return }
    if (page !== 1) { setConnection('elsewhere'); return }
    if (typeof EventSource === 'undefined') { setConnection('unsupported'); return }
    const query = new URLSearchParams()
    for (const [key] of fields) if (applied[key].trim()) query.set(key, applied[key].trim())
    const start = periodStart(period)
    if (start) query.set('startDate', start)
    setConnection('connecting')
    const source = new EventSource(`/admin/v1/usage/stream?${query}`)
    // Every (re)connect reloads recorded history, so a gap while disconnected is filled and nothing is shown twice.
    source.onopen = () => { setConnection('live'); void reload.current() }
    source.onerror = () => {
      if (source.readyState === EventSource.CLOSED) { setConnection('closed'); void reload.current(); return }
      setConnection('reconnecting')
    }
    source.addEventListener('request.completed', event => {
      const row = parseLiveRow((event as MessageEvent<string>).data)
      if (row) setLive(previous => addLive(previous, row))
    })
    return () => source.close()
  }, [applied, page, period, paused, attempt])

  const submit = (event: FormEvent) => { event.preventDefault(); setPage(1); setApplied(draft) }
  const clear = () => { setDraft(noFilters); setPage(1); setApplied(noFilters) }
  const filtering = fields.some(([key]) => applied[key].trim())
  const pages = Math.max(1, Math.ceil(total / LOG_PAGE_SIZE))
  const fresh = freshLive(live, items)
  const rows = page === 1 ? [...fresh, ...items].slice(0, LOG_PAGE_SIZE) : items
  const shownTotal = total + (page === 1 ? fresh.length : 0)
  const first = loading && rows.length === 0 && !error
  const tone = connection === 'live' ? 'live' : connection === 'paused' || connection === 'elsewhere' || connection === 'unsupported' ? 'idle' : 'warn'

  return <>
    <Row gap="s" label="Filters">
      <div className="ed-main">
        <form className="log-filters" onSubmit={submit} aria-label="Filter the request log">
          {fields.map(([key, label]) => <label key={key} className="log-filter"><span className="ed-label">{label}</span>
            <input type={key === 'status' ? 'number' : 'text'} inputMode={key === 'status' ? 'numeric' : undefined} min={key === 'status' ? 100 : undefined} max={key === 'status' ? 599 : undefined} value={draft[key]} onChange={event => setDraft({ ...draft, [key]: event.target.value })} autoComplete="off" spellCheck={false} /></label>)}
          <div className="log-filter-actions"><button className="button-primary" type="submit">Apply filters</button><button className="button-secondary" type="button" disabled={!filtering && !fields.some(([key]) => draft[key])} onClick={clear}>Clear</button></div>
        </form>
      </div>
    </Row>

    <Row gap="s" label="Request log">
      <Main title="Requests">
        <div className="live-bar">
          <span className="status" role="status"><Dot tone={tone} />{connectionLabel[connection]}</span>
          {connection === 'closed' && <button className="button-secondary" type="button" onClick={() => setAttempt(value => value + 1)}>Reconnect</button>}
          {connection !== 'unsupported' && <button className="button-secondary" type="button" aria-pressed={paused} onClick={() => setPaused(value => !value)}>{paused ? 'Resume live view' : 'Pause live view'}</button>}
          <span className="count-label">New requests appear here as they finish.</span>
        </div>
        {error && <ErrorNotice message={error} busy={loading} onRetry={() => void load()} />}
        {first && <div role="status" aria-label="Loading requests"><SkeletonRows rows={8} columns={6} /></div>}
        {!first && !error && rows.length === 0 && <EmptyState reason={filtering ? 'No request matches these filters in this period.' : 'No requests recorded in this period.'}>{filtering && <button className="button-secondary" type="button" onClick={clear}>Clear filters</button>}</EmptyState>}
        {rows.length > 0 && <figure className="ed-figure" aria-busy={loading}>
          <table className="ed-table usage-table log-table"><thead><tr><th scope="col">Request</th><th scope="col">Route</th><th scope="col" className="num">Status</th><th scope="col" className="num">In / out</th><th scope="col" className="num">Latency</th><th scope="col" className="num">Time</th></tr></thead>
            <tbody>{rows.map(event => <tr key={recorded(event) ? event.id : `live-${event.requestId}`} className={recorded(event) ? undefined : 'log-row-live'}>
              <td><button className="link-button" type="button" onClick={() => onOpen(event.requestId)} aria-label={`Open details for request ${event.requestId}`}>{event.requestId}</button></td>
              <td className="log-route">{event.providerId || '—'} / {event.modelId || '—'}</td>
              <td className="num log-status"><Status tone={statusTone(event.status)}>{event.status}</Status></td>
              <td className="num log-tokens">{numOrDash(event.inputTokens)} / {numOrDash(event.outputTokens)}</td>
              <td className="num log-latency">{event.durationMs !== undefined ? `${num(event.durationMs)} ms` : '—'}{recorded(event) && event.ttftMs !== undefined ? ` · ${num(event.ttftMs)}` : ''}</td>
              <td className="num log-time">{new Date(event.createdAt).toLocaleString()}</td>
            </tr>)}</tbody></table>
          <figcaption><Caption kicker="Log">{num(shownTotal)} {shownTotal === 1 ? 'request' : 'requests'} {period === 'all' ? 'in recorded history' : `since the start of ${periodLabel.toLowerCase()}`}{filtering ? ', filtered' : ''}, newest first. Latency is duration, then time to first token.</Caption></figcaption>
        </figure>}
        {total > LOG_PAGE_SIZE && <nav className="log-pagination" aria-label="Request log pages">
          <button className="button-secondary" type="button" disabled={loading || page <= 1} onClick={() => setPage(page - 1)}>Previous</button>
          <span className="count-label" role="status">Page {page} of {pages}</span>
          <button className="button-secondary" type="button" disabled={loading || page >= pages} onClick={() => setPage(page + 1)}>Next</button>
        </nav>}
      </Main>
    </Row>
  </>
}
