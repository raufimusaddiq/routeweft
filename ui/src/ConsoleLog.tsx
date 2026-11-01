import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { entryMatches, mergeLive, parseConsoleLines, type Entry } from './consoleLive'
import { channelLabel, type LiveEvent, type RefreshReason } from './liveEvents'
import { useLiveChannel } from './liveChannel'

type Props = { onUnauthorized: () => void }

const pageSize = 50
const levels = ['', 'DEBUG', 'INFO', 'WARN', 'ERROR']

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

function timestamp(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

// The live view (#277): new lines arrive on the shared channel and go on top of page 1, a clear empties every open
// page, and when the channel is down the page re-reads on a timer. Pausing, or being on another page, keeps what
// is shown still and counts what arrived instead.
export function ConsoleLog({ onUnauthorized }: Props) {
  const [items, setItems] = useState<Entry[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [level, setLevel] = useState('')
  const [query, setQuery] = useState('')
  const [levelDraft, setLevelDraft] = useState('')
  const [queryDraft, setQueryDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [paused, setPaused] = useState(false)
  const [waiting, setWaiting] = useState(0)
  const [clearing, setClearing] = useState(false)
  // What the event handlers must see right now, whatever has rendered since.
  const view = useRef({ items, page, level, query, paused })
  view.current = { items, page, level, query, paused }

  const load = useCallback(async (nextPage: number, selectedLevel: string, selectedQuery: string) => {
    setLoading(true)
    setError('')
    const params = new URLSearchParams({ page: String(nextPage), pageSize: String(pageSize) })
    if (selectedLevel) params.set('level', selectedLevel)
    if (selectedQuery.trim()) params.set('query', selectedQuery.trim())
    try {
      const response = await fetch(`/admin/v1/logs?${params}`, { credentials: 'same-origin' })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not load console logs.'))
      const data = await response.json() as { items: Entry[]; total: number; page: number }
      view.current.items = data.items
      setItems(data.items)
      setTotal(data.total)
      setPage(data.page)
      setWaiting(0)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load console logs.')
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load(1, '', '') }, [load])

  const onEvent = useCallback((event: LiveEvent) => {
    const current = view.current
    if (event.type === 'console.cleared') {
      view.current.items = []
      setItems([]); setTotal(0); setWaiting(0)
      setNotice('The logs were cleared.')
      return
    }
    const { lines, truncated } = parseConsoleLines(event.data)
    if (current.paused || current.page !== 1) {
      // Not touching what is on screen: say how much is waiting. A line the filter would hide does not count.
      const matching = lines.filter(line => entryMatches(line, current.level, current.query)).length
      if (matching > 0 || truncated) setWaiting(count => count + Math.max(matching, truncated ? 1 : 0))
      return
    }
    if (truncated) { void load(1, current.level, current.query); return }
    const merged = mergeLive(current.items, lines, current.level, current.query, pageSize)
    if (merged.added === 0) return
    view.current.items = merged.items
    setItems(merged.items)
    setTotal(count => count + merged.added)
  }, [load])

  const onRefresh = useCallback((_reason: RefreshReason) => {
    const current = view.current
    // A paused view stays as it is; everything else re-reads the page it is on.
    if (current.paused) { setWaiting(count => count || 1); return }
    void load(current.page, current.level, current.query)
  }, [load])

  const channel = useLiveChannel({ types: ['console.log', 'console.cleared'], onEvent, onRefresh })

  function applyFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setLevel(levelDraft)
    setQuery(queryDraft)
    setNotice('')
    void load(1, levelDraft, queryDraft)
  }

  function movePage(nextPage: number) {
    void load(nextPage, level, query)
  }

  function togglePause() {
    if (paused) {
      // Resuming shows the newest lines again.
      setPaused(false)
      view.current.paused = false
      void load(1, level, query)
    } else {
      setPaused(true)
      view.current.paused = true
    }
  }

  // The oracle clears without asking (console-logs/route.js:16-24); these are the only copy of the service's
  // recent diagnostics, so this asks first. The buffer is cleared whole, not just the lines the filter shows.
  async function clearLogs() {
    if (!window.confirm('Clear every log held in memory, not only the ones shown? This cannot be undone.')) return
    setClearing(true); setError(''); setNotice('')
    try {
      const response = await fetch('/admin/v1/logs', { method: 'DELETE', credentials: 'same-origin' })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not clear the logs.'))
      const result = await response.json().catch(() => ({})) as { cleared?: number }
      // The channel announces the clear to every page; when it is down this page still reflects it.
      view.current.items = []
      setItems([]); setTotal(0); setWaiting(0); setPage(1)
      setNotice(`Cleared ${(result.cleared ?? 0).toLocaleString()} log${result.cleared === 1 ? '' : 's'}.`)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not clear the logs.')
    } finally { setClearing(false) }
  }

  const pages = Math.max(1, Math.ceil(total / pageSize))
  const status = paused ? 'Live view paused' : channelLabel[channel]

  return <div className="console-log-content page-d" aria-busy={loading}>
    <form className="console-log-toolbar accent-surface" onSubmit={applyFilters}>
      <label htmlFor="console-log-level">Level</label>
      <select id="console-log-level" value={levelDraft} onChange={event => setLevelDraft(event.target.value)}>{levels.map(value => <option key={value} value={value}>{value || 'All levels'}</option>)}</select>
      <label htmlFor="console-log-query">Contains</label>
      <input id="console-log-query" type="search" value={queryDraft} onChange={event => setQueryDraft(event.target.value)} placeholder="Message or attribute" />
      <button className="button-primary" type="submit" disabled={loading}>Filter</button>
      <span className="count-label" role="status">{loading ? 'Loading…' : `${total.toLocaleString()} log${total === 1 ? '' : 's'}`}</span>
      <button className="button-secondary" type="button" disabled={loading} onClick={() => void load(page, level, query)}>Refresh</button>
      <button className="button-secondary" type="button" aria-pressed={paused} onClick={togglePause}>{paused ? 'Resume live view' : 'Pause live view'}</button>
      <button className="button-danger" type="button" disabled={clearing || loading} onClick={() => void clearLogs()}>{clearing ? 'Clearing…' : 'Clear logs'}</button>
    </form>
    <p className="count-label console-log-live" role="status" data-live-state={paused ? 'paused' : channel}>{status}{waiting > 0 ? ` · ${waiting.toLocaleString()} new line${waiting === 1 ? '' : 's'} waiting${paused ? '; resume to show' : '; go to page 1 or Refresh'}` : ''}</p>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}
    <section className="detail-panel" aria-labelledby="console-log-title">
      <div className="section-heading"><div><h2 id="console-log-title">Service logs</h2><p>Newest first · bounded in-memory log buffer.</p></div></div>
      {items.length === 0 && !loading ? <p className="empty-keys">No logs match these filters.</p> : <div className="console-log-list" aria-live="polite">
        {items.map(item => <article className="console-log-row" key={item.id}>
          <time className="console-log-time" dateTime={item.time}>{timestamp(item.time)}</time>
          <span className={`state-tag${item.level === 'ERROR' || item.level === 'WARN' ? ' state-warn' : ''}`}>{item.level}</span>
          <p>{item.message}</p>
          {item.attributes && Object.keys(item.attributes).length > 0 && <details><summary>Details</summary><pre>{JSON.stringify(item.attributes, null, 2)}</pre></details>}
        </article>)}
      </div>}
      <div className="console-log-pagination" aria-label="Log pages">
        <button className="button-secondary" type="button" disabled={loading || page <= 1} onClick={() => movePage(page - 1)}>Previous</button>
        <span className="count-label" role="status">Page {page} of {pages}</span>
        <button className="button-secondary" type="button" disabled={loading || page >= pages} onClick={() => movePage(page + 1)}>Next</button>
      </div>
    </section>
  </div>
}
