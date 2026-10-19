import { useCallback, useEffect, useMemo, useState } from 'react'

type Totals = { requests: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number; errors: number; avgDurationMs?: number; avgTtftMs?: number }
type Day = { day: string; requests: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number }
type Breakdown = { key: string; requests: number; inputTokens: number; outputTokens: number; errors: number }
type Summary = { period: string; since: string; totals: Totals; series: Day[]; providers: Breakdown[]; models: Breakdown[]; statuses: Breakdown[] }
type UsageEvent = { id: number; requestId: string; status: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number; createdAt: string; providerId?: string; modelId?: string; connectionId?: string; apiKeyId?: string; durationMs?: number; ttftMs?: number }
type Props = { onUnauthorized: () => void }

const periods = [['24h', 'Last 24 hours'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days'], ['all', 'All time']]
const numberFormat = new Intl.NumberFormat()

// Total tokens (input + output). Authoritative cost lives in the pricing overrides.
function totalTokens(totals: Totals) {
  const tokens = totals.inputTokens + totals.outputTokens
  return numberFormat.format(tokens)
}

function bar(value: number, max: number) {
  return max <= 0 ? 0 : Math.max(2, Math.round((value / max) * 100))
}

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

export function Usage({ onUnauthorized }: Props) {
  const [period, setPeriod] = useState('7d')
  const [summary, setSummary] = useState<Summary | null>(null)
  const [events, setEvents] = useState<UsageEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const responses = await Promise.all([
        fetch(`/admin/v1/usage/summary?period=${period}`, { credentials: 'same-origin' }),
        fetch('/admin/v1/usage?page=1&pageSize=50', { credentials: 'same-origin' }),
      ])
      if (responses.some(response => response.status === 401)) { onUnauthorized(); return }
      const failed = responses.find(response => !response.ok)
      if (failed) throw new Error(await responseError(failed, 'Could not load usage.'))
      const [summaryData, eventData] = await Promise.all(responses.map(response => response.json())) as [Summary, { items: UsageEvent[] }]
      setSummary(summaryData)
      setEvents(eventData.items)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load usage.')
    } finally { setLoading(false) }
  }, [period, onUnauthorized])

  useEffect(() => { void load() }, [load])

  const maxRequests = useMemo(() => Math.max(1, ...(summary?.series.map(day => day.requests) || [1])), [summary])
  const maxProvider = useMemo(() => Math.max(1, ...(summary?.providers.map(item => item.requests) || [1])), [summary])

  return <div className="usage-content" aria-busy={loading}>
    <div className="usage-toolbar"><label htmlFor="usage-period">Time period</label><select id="usage-period" value={period} onChange={event => setPeriod(event.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select><span className="count-label" role="status">{loading ? 'Loading…' : summary ? `${numberFormat.format(summary.totals.requests)} requests${summary.since ? ` since ${summary.since.slice(0, 10)}` : ''}` : ''}</span><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}

    {summary && <>
      <div className="metrics-grid usage-metrics">
        <section className="metric" aria-label="Requests"><div className="metric-label"><span className="material-symbols" aria-hidden="true">monitoring</span>Requests</div><p className="metric-value">{numberFormat.format(summary.totals.requests)}</p><p className="metric-note">{numberFormat.format(summary.totals.errors)} error{summary.totals.errors === 1 ? '' : 's'}</p></section>
        <section className="metric" aria-label="Tokens"><div className="metric-label"><span className="material-symbols" aria-hidden="true">arrow_downward</span>Tokens</div><p className="metric-value">{totalTokens(summary.totals)}</p><p className="metric-note">input {numberFormat.format(summary.totals.inputTokens)} · output {numberFormat.format(summary.totals.outputTokens)}</p></section>
        <section className="metric" aria-label="Cache tokens"><div className="metric-label"><span className="material-symbols" aria-hidden="true">bolt</span>Cache tokens</div><p className="metric-value">{numberFormat.format(summary.totals.cacheReadTokens + summary.totals.cacheWriteTokens)}</p><p className="metric-note">read {numberFormat.format(summary.totals.cacheReadTokens)} · write {numberFormat.format(summary.totals.cacheWriteTokens)}</p></section>
        <section className="metric" aria-label="Latency"><div className="metric-label"><span className="material-symbols" aria-hidden="true">speed</span>Latency</div><p className="metric-value">{summary.totals.avgDurationMs !== undefined ? `${summary.totals.avgDurationMs} ms` : '—'}</p><p className="metric-note">TTFT {summary.totals.avgTtftMs !== undefined ? `${summary.totals.avgTtftMs} ms` : '—'}</p></section>
      </div>

      <div className="usage-layout">
        <section className="detail-panel" aria-labelledby="usage-history-title">
          <div className="section-heading"><div><h2 id="usage-history-title">Daily history</h2><p>Requests per day across the selected period.</p></div></div>
          {summary.series.length === 0 ? <p className="empty-keys">No attributed usage in this period.</p> : <div className="usage-chart" role="img" aria-label="Requests per day">{summary.series.map(day => <div className="usage-bar-column" key={day.day}><div className="usage-bar" style={{ height: `${bar(day.requests, maxRequests)}%` }} title={`${day.day}: ${day.requests} requests`} /><span className="usage-bar-label">{day.day.slice(5)}</span></div>)}</div>}
        </section>

        <section className="detail-panel" aria-labelledby="usage-providers-title">
          <div className="section-heading"><div><h2 id="usage-providers-title">Providers</h2><p>Requests and errors by provider.</p></div></div>
          {summary.providers.length === 0 ? <p className="empty-keys">No provider activity in this period.</p> : <div className="breakdown-list">{summary.providers.map(item => <div className="breakdown-row" key={item.key}><code>{item.key}</code><span className="breakdown-track"><span className="breakdown-fill" style={{ width: `${bar(item.requests, maxProvider)}%` }} /></span><span className="breakdown-count">{item.requests}</span>{item.errors > 0 && <span className="state-tag state-warn">{item.errors} err</span>}</div>)}</div>}
        </section>

        <section className="detail-panel" aria-labelledby="usage-models-title">
          <div className="section-heading"><div><h2 id="usage-models-title">Models</h2><p>Top models by request count.</p></div></div>
          {summary.models.length === 0 ? <p className="empty-keys">No model activity in this period.</p> : <div className="breakdown-list">{summary.models.map(item => <div className="breakdown-row" key={item.key}><code>{item.key}</code><span className="breakdown-count">{item.requests}</span><span className="count-label">{numberFormat.format(item.inputTokens + item.outputTokens)} tok</span></div>)}</div>}
        </section>

        <section className="detail-panel" aria-labelledby="usage-status-title">
          <div className="section-heading"><div><h2 id="usage-status-title">Status</h2><p>Response status distribution.</p></div></div>
          {summary.statuses.length === 0 ? <p className="empty-keys">No responses in this period.</p> : <div className="breakdown-list">{summary.statuses.map(item => <div className="breakdown-row" key={item.key}><code>{item.key}</code><span className={`state-tag${item.key.startsWith('2') ? ' state-good' : ' state-warn'}`}>{item.requests}</span></div>)}</div>}
        </section>
      </div>
    </>}

    <section className="detail-panel usage-log" aria-labelledby="usage-log-title">
      <div className="section-heading"><div><h2 id="usage-log-title">Recent requests</h2><p>Latest {events.length} usage events with provider/model/account attribution.</p></div></div>
      {events.length === 0 ? <p className="empty-keys">No requests recorded.</p> : <div className="usage-event-list">{events.map(event => <article className="usage-event" key={event.id}>
        <span className={`state-tag${event.status >= 200 && event.status < 300 ? ' state-good' : ' state-warn'}`}>{event.status}</span>
        <code>{event.providerId || '—'} / {event.modelId || '—'}</code>
        <span className="usage-event-tokens">{numberFormat.format(event.inputTokens)} in · {numberFormat.format(event.outputTokens)} out{event.cacheReadTokens + event.cacheWriteTokens > 0 ? ` · ${numberFormat.format(event.cacheReadTokens + event.cacheWriteTokens)} cache` : ''}</span>
        <span className="count-label">{event.durationMs !== undefined ? `${event.durationMs} ms` : ''}{event.ttftMs !== undefined ? ` · TTFT ${event.ttftMs} ms` : ''}</span>
        <code className="usage-event-id">{event.requestId}</code>
        <span className="count-label">{new Date(event.createdAt).toLocaleString()}</span>
      </article>)}</div>}
    </section>
  </div>
}
