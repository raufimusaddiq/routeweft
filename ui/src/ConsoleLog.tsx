import { useCallback, useEffect, useState, type FormEvent } from 'react'

type Entry = { id: number; time: string; level: string; message: string; attributes?: Record<string, unknown> }
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
      setItems(data.items)
      setTotal(data.total)
      setPage(data.page)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load console logs.')
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load(1, '', '') }, [load])

  function applyFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setLevel(levelDraft)
    setQuery(queryDraft)
    void load(1, levelDraft, queryDraft)
  }

  function movePage(nextPage: number) {
    void load(nextPage, level, query)
  }

  const pages = Math.max(1, Math.ceil(total / pageSize))

  return <div className="console-log-content" aria-busy={loading}>
    <form className="console-log-toolbar" onSubmit={applyFilters}>
      <label htmlFor="console-log-level">Level</label>
      <select id="console-log-level" value={levelDraft} onChange={event => setLevelDraft(event.target.value)}>{levels.map(value => <option key={value} value={value}>{value || 'All levels'}</option>)}</select>
      <label htmlFor="console-log-query">Contains</label>
      <input id="console-log-query" type="search" value={queryDraft} onChange={event => setQueryDraft(event.target.value)} placeholder="Message or attribute" />
      <button className="button-primary" type="submit" disabled={loading}>Filter</button>
      <span className="count-label" role="status">{loading ? 'Loading…' : `${total.toLocaleString()} log${total === 1 ? '' : 's'}`}</span>
      <button className="button-secondary" type="button" disabled={loading} onClick={() => void load(page, level, query)}>Refresh</button>
    </form>
    {error && <p className="auth-error" role="alert">{error}</p>}
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
