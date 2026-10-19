import { useCallback, useEffect, useMemo, useState } from 'react'
import { BarChart, EmptyState, ErrorNotice, MetricsSkeleton, Skeleton, SkeletonRows, Sparkline, StackedBar, type Segment } from './kit'

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

function statusTone(key: string): Segment['tone'] {
  if (key.startsWith('2')) return 'good'
  if (key.startsWith('4')) return 'warn'
  if (key.startsWith('5')) return 'bad'
  return 'neutral'
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

  const first = loading && !summary
  const series = summary?.series || []
  const points = series.map(day => ({ label: day.day, short: day.day.slice(5), value: day.requests }))
  const trend = {
    requests: series.map(day => day.requests),
    tokens: series.map(day => day.inputTokens + day.outputTokens),
    cache: series.map(day => day.cacheReadTokens + day.cacheWriteTokens),
  }
  const totals = summary?.totals

  return <div className="usage-content" aria-busy={loading}>
    <div className="usage-toolbar"><label htmlFor="usage-period">Time period</label><select id="usage-period" value={period} onChange={event => setPeriod(event.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select><span className="count-label" role="status">{loading ? 'Loading…' : summary ? `${numberFormat.format(summary.totals.requests)} requests${summary.since ? ` since ${summary.since.slice(0, 10)}` : ''}` : ''}</span><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && <ErrorNotice className="auth-error" message={error} busy={loading} onRetry={() => void load()} />}

    {first && !error && <div className="skeleton-page" role="status" aria-label="Loading usage">
      <MetricsSkeleton />
      <div className="usage-layout"><section className="detail-panel usage-chart-panel"><Skeleton width="30%" height={10} /><Skeleton width="100%" height={168} /></section><section className="detail-panel"><Skeleton width="30%" height={10} /><SkeletonRows rows={4} columns={3} /></section></div>
      <section className="detail-panel"><Skeleton width="20%" height={10} /><SkeletonRows rows={6} columns={6} /></section>
    </div>}

    {summary && totals && <>
      <div className="metrics-grid usage-metrics">
        <section className="metric" aria-label="Requests"><div className="metric-label">Requests</div><p className="metric-value">{numberFormat.format(totals.requests)}</p><p className="metric-note">{numberFormat.format(totals.errors)} error{totals.errors === 1 ? '' : 's'}</p><Sparkline values={trend.requests} label="Requests per day" /></section>
        <section className="metric" aria-label="Tokens"><div className="metric-label">Tokens</div><p className="metric-value">{totalTokens(totals)}</p><p className="metric-note">input {numberFormat.format(totals.inputTokens)} · output {numberFormat.format(totals.outputTokens)}</p><Sparkline values={trend.tokens} label="Tokens per day" /></section>
        <section className="metric" aria-label="Cache tokens"><div className="metric-label">Cache tokens</div><p className="metric-value">{numberFormat.format(totals.cacheReadTokens + totals.cacheWriteTokens)}</p><p className="metric-note">read {numberFormat.format(totals.cacheReadTokens)} · write {numberFormat.format(totals.cacheWriteTokens)}</p><Sparkline values={trend.cache} label="Cache tokens per day" /></section>
        <section className="metric" aria-label="Latency"><div className="metric-label">Latency</div><p className="metric-value">{totals.avgDurationMs !== undefined ? `${totals.avgDurationMs} ms` : '—'}</p><p className="metric-note">TTFT {totals.avgTtftMs !== undefined ? `${totals.avgTtftMs} ms` : '—'}</p></section>
      </div>

      <div className="usage-layout">
        <section className="detail-panel usage-chart-panel accent-surface" aria-labelledby="usage-history-title">
          <div className="section-heading"><div><h2 id="usage-history-title">Daily history</h2><p>Requests per day across the selected period.</p></div></div>
          {points.length === 0
            ? <EmptyState reason="No attributed usage in this period.">{period !== 'all' && <button className="button-secondary" type="button" onClick={() => setPeriod('all')}>Show all time</button>}</EmptyState>
            : <BarChart points={points} unit="requests" title="Requests per day" />}
        </section>

        <section className="detail-panel" aria-labelledby="usage-status-title">
          <div className="section-heading"><div><h2 id="usage-status-title">Status</h2><p>Response status distribution.</p></div></div>
          {summary.statuses.length === 0 ? <EmptyState reason="No responses in this period." /> : <StackedBar title="Response status counts" segments={summary.statuses.map(item => ({ key: item.key, label: item.key, value: item.requests, tone: statusTone(item.key) }))} />}
        </section>

        <section className="detail-panel" aria-labelledby="usage-providers-title">
          <div className="section-heading"><div><h2 id="usage-providers-title">Providers</h2><p>Requests and errors by provider.</p></div></div>
          {summary.providers.length === 0 ? <EmptyState reason="No provider activity in this period." /> : <div className="breakdown-list">{summary.providers.map(item => <div className="breakdown-row" key={item.key}><code>{item.key}</code><span className="breakdown-track"><span className="breakdown-fill" style={{ width: `${bar(item.requests, maxProvider)}%` }} /></span><span className="breakdown-count">{item.requests}</span>{item.errors > 0 && <span className="state-tag state-warn">{item.errors} err</span>}</div>)}</div>}
        </section>

        <section className="detail-panel" aria-labelledby="usage-models-title">
          <div className="section-heading"><div><h2 id="usage-models-title">Models</h2><p>Top models by request count.</p></div></div>
          {summary.models.length === 0 ? <EmptyState reason="No model activity in this period." /> : <div className="breakdown-list">{summary.models.map(item => <div className="breakdown-row" key={item.key}><code>{item.key}</code><span className="breakdown-count">{item.requests}</span><span className="count-label">{numberFormat.format(item.inputTokens + item.outputTokens)} tok</span></div>)}</div>}
        </section>
      </div>
    </>}

    {!first && <section className="detail-panel usage-log" aria-labelledby="usage-log-title">
      <div className="section-heading"><div><h2 id="usage-log-title">Recent requests</h2><p>Latest {events.length} usage events with provider/model/account attribution.</p></div></div>
      {events.length === 0 ? <EmptyState reason="No requests recorded yet. Send a request through the gateway and it will appear here." /> : <div className="table-scroll"><table className="usage-table">
        <thead><tr><th scope="col">Status</th><th scope="col">Provider / model</th><th scope="col" className="num">In</th><th scope="col" className="num">Out</th><th scope="col" className="num">Cache</th><th scope="col" className="num">Latency</th><th scope="col">Request</th><th scope="col">Time</th></tr></thead>
        <tbody>{events.map(event => <tr className="usage-event" key={event.id}>
          <td><span className={`state-tag${event.status >= 200 && event.status < 300 ? ' state-good' : ' state-warn'}`}>{event.status}</span></td>
          <td className="technical-value">{event.providerId || '—'} / {event.modelId || '—'}</td>
          <td className="num">{numberFormat.format(event.inputTokens)}</td>
          <td className="num">{numberFormat.format(event.outputTokens)}</td>
          <td className="num">{event.cacheReadTokens + event.cacheWriteTokens > 0 ? numberFormat.format(event.cacheReadTokens + event.cacheWriteTokens) : '—'}</td>
          <td className="num">{event.durationMs !== undefined ? `${event.durationMs} ms` : '—'}{event.ttftMs !== undefined ? ` · ${event.ttftMs}` : ''}</td>
          <td className="technical-value usage-event-id">{event.requestId}</td>
          <td className="technical-value">{new Date(event.createdAt).toLocaleString()}</td>
        </tr>)}</tbody>
      </table></div>}
    </section>}
  </div>
}
