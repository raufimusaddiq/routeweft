import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { EmptyState, ErrorNotice, SkeletonRows } from './kit'
import { Caption, Main, Row, Status } from './editorial'
import { num, periodStart, statusTone, type UsageEvent } from './format'

export const LOG_PAGE_SIZE = 25

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

  const submit = (event: FormEvent) => { event.preventDefault(); setPage(1); setApplied(draft) }
  const clear = () => { setDraft(noFilters); setPage(1); setApplied(noFilters) }
  const filtering = fields.some(([key]) => applied[key].trim())
  const pages = Math.max(1, Math.ceil(total / LOG_PAGE_SIZE))
  const first = loading && items.length === 0 && !error

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
        {error && <ErrorNotice message={error} busy={loading} onRetry={() => void load()} />}
        {first && <div role="status" aria-label="Loading requests"><SkeletonRows rows={8} columns={6} /></div>}
        {!first && !error && items.length === 0 && <EmptyState reason={filtering ? 'No request matches these filters in this period.' : 'No requests recorded in this period.'}>{filtering && <button className="button-secondary" type="button" onClick={clear}>Clear filters</button>}</EmptyState>}
        {items.length > 0 && <figure className="ed-figure" aria-busy={loading}>
          <table className="ed-table usage-table log-table"><thead><tr><th scope="col">Request</th><th scope="col">Route</th><th scope="col" className="num">Status</th><th scope="col" className="num">In / out</th><th scope="col" className="num">Latency</th><th scope="col" className="num">Time</th></tr></thead>
            <tbody>{items.map(event => <tr key={event.id}>
              <td><button className="link-button" type="button" onClick={() => onOpen(event.requestId)} aria-label={`Open details for request ${event.requestId}`}>{event.requestId}</button></td>
              <td className="log-route">{event.providerId || '—'} / {event.modelId || '—'}</td>
              <td className="num log-status"><Status tone={statusTone(event.status)}>{event.status}</Status></td>
              <td className="num log-tokens">{num(event.inputTokens)} / {num(event.outputTokens)}</td>
              <td className="num log-latency">{event.durationMs !== undefined ? `${num(event.durationMs)} ms` : '—'}{event.ttftMs !== undefined ? ` · ${num(event.ttftMs)}` : ''}</td>
              <td className="num log-time">{new Date(event.createdAt).toLocaleString()}</td>
            </tr>)}</tbody></table>
          <figcaption><Caption kicker="Log">{num(total)} {total === 1 ? 'request' : 'requests'} {period === 'all' ? 'in recorded history' : `since the start of ${periodLabel.toLowerCase()}`}{filtering ? ', filtered' : ''}, newest first. Latency is duration, then time to first token.</Caption></figcaption>
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
