import { useState } from 'react'
import { Dot, Delta, Status, Tile } from './kit2'
import { BarChart, ErrorNotice, MetricsSkeleton, Sparkline, StackedBar } from './kit'
import { statusTone } from './chartMath'
import { clock, compact, delta, num, pct, rate, statusClass, useData } from './proto'

type Props = { onUnauthorized: () => void }
const periods = [['24h', 'Last 24 hours'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days']] as const

export function Usage({ onUnauthorized }: Props) {
  const data = useData(onUnauthorized)
  const [period, setPeriod] = useState<'24h' | '7d' | '30d'>('30d')
  const summary = period === '24h' ? data.day : period === '7d' ? data.week : data.month
  if (data.loading && !summary) return <div className="skeleton-page" role="status" aria-label="Loading usage"><MetricsSkeleton /></div>
  if (data.error && !summary) return <ErrorNotice message={data.error} onRetry={data.reload} />
  if (!summary) return null
  const t = summary.totals
  const days = Math.max(1, period === '24h' ? 1 : period === '7d' ? 7 : 30)
  const tokens = t.inputTokens + t.outputTokens
  const hit = (t.cacheReadTokens / Math.max(1, t.inputTokens + t.cacheReadTokens)) * 100
  const points = summary.series.map(day => ({ label: day.day, short: day.day.slice(5), value: day.requests }))
  const topProv = Math.max(1, ...summary.providers.map(item => item.requests))
  const topModel = Math.max(1, ...summary.models.map(item => item.requests))

  return <div className="usage-content a-usage" aria-busy={data.loading}>
    <div className="toolbar">
      <label htmlFor="usage-period">Period</label>
      <select id="usage-period" value={period} onChange={event => setPeriod(event.target.value as typeof period)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
      <span className="count">{num(t.requests)} requests since {summary.since.slice(0, 10)}</span>
      <button className="button-secondary" type="button" onClick={data.reload}>Refresh</button>
    </div>
    <div className="strip strip-4">
      <Tile accent label="Requests" value={num(t.requests)} unit="req">
        <Delta value={period === '7d' ? delta(t.requests, data.prevWeek?.requests) : null} basis={`avg ${num(t.requests / days)} /day`} />
        <Sparkline values={summary.series.map(day => day.requests)} label="Requests per day" />
      </Tile>
      <Tile label="Tokens" value={compact(tokens)} unit="in + out">
        <Delta value={period === '7d' ? delta(tokens, data.prevWeek ? data.prevWeek.inputTokens + data.prevWeek.outputTokens : undefined) : null} basis={`avg ${compact(tokens / days)} /day`} />
        <Sparkline values={summary.series.map(day => day.inputTokens + day.outputTokens)} label="Tokens per day" />
      </Tile>
      <Tile label="Cache" value={compact(t.cacheReadTokens + t.cacheWriteTokens)} unit="tokens">
        <Delta value={null} basis={`${pct(hit, 0)} hit · ${compact(t.cacheWriteTokens)} written`} />
        <Sparkline values={summary.series.map(day => day.cacheReadTokens + day.cacheWriteTokens)} label="Cache tokens per day" />
      </Tile>
      <Tile label="Latency" value={num(t.avgDurationMs ?? 0)} unit="ms avg">
        <Delta value={null} basis={`TTFT ${num(t.avgTtftMs ?? 0)} ms`} />
        <p className="tile-note">{pct(rate(t.errors, t.requests))} errors · {num(t.errors)} failed</p>
      </Tile>
    </div>

    <div className="split">
      <section className="panel" aria-labelledby="usage-history-title">
        <header className="panel-head"><h2 id="usage-history-title">Requests per day</h2><span className="count">{summary.series.length} days · avg {num(t.requests / days)} /day</span></header>
        <BarChart points={points} unit="requests" title="Requests per day" />
      </section>
      <section className="panel" aria-labelledby="usage-status-title">
        <header className="panel-head"><h2 id="usage-status-title">Response status</h2><span className="count">{num(t.requests)} responses</span></header>
        <StackedBar title="Response status counts" segments={summary.statuses.map(item => ({ key: item.key, label: item.key, value: item.requests, tone: statusTone(item.key) }))} />
      </section>
    </div>

    <div className="split split-even">
      <section className="panel" aria-labelledby="usage-providers-title">
        <header className="panel-head"><h2 id="usage-providers-title">Providers</h2><span className="count">{summary.providers.length} active</span></header>
        <table className="grid-table"><thead><tr><th scope="col">Provider</th><th scope="col" className="num">Requests</th><th scope="col">Share</th><th scope="col" className="num">Errors</th></tr></thead>
          <tbody>{summary.providers.slice(0, 9).map(item => { const e = rate(item.errors, item.requests); return <tr key={item.key}><td><Status tone={e > 8 ? 'bad' : e > 4 ? 'warn' : 'good'}><code>{item.key}</code></Status></td><td className="num">{num(item.requests)}</td><td><span className="bar"><span style={{ width: `${(item.requests / topProv) * 100}%` }} /></span></td><td className="num">{pct(e)}</td></tr> })}</tbody></table>
      </section>
      <section className="panel" aria-labelledby="usage-models-title">
        <header className="panel-head"><h2 id="usage-models-title">Models</h2><span className="count">top {Math.min(9, summary.models.length)} by requests</span></header>
        <table className="grid-table"><thead><tr><th scope="col">Model</th><th scope="col" className="num">Requests</th><th scope="col">Share</th><th scope="col" className="num">Tokens</th></tr></thead>
          <tbody>{summary.models.slice(0, 9).map(item => <tr key={item.key}><td><code>{item.key}</code></td><td className="num">{num(item.requests)}</td><td><span className="bar"><span style={{ width: `${(item.requests / topModel) * 100}%` }} /></span></td><td className="num">{compact(item.inputTokens + item.outputTokens)}</td></tr>)}</tbody></table>
      </section>
    </div>

    <section className="panel" aria-labelledby="usage-log-title">
      <header className="panel-head"><h2 id="usage-log-title">Recent requests</h2><span className="count">latest {Math.min(12, data.events.length)}</span></header>
      <div className="table-scroll"><table className="grid-table usage-table">
        <thead><tr><th scope="col">Time</th><th scope="col">Status</th><th scope="col">Provider / model</th><th scope="col" className="num">In</th><th scope="col" className="num">Out</th><th scope="col" className="num">Cache</th><th scope="col" className="num">Latency</th><th scope="col">Request</th></tr></thead>
        <tbody>{data.events.slice(0, 12).map(event => <tr key={event.id} className="usage-event">
          <td>{clock(event.createdAt)}</td><td><Status tone={statusClass(event.status)}>{event.status}</Status></td><td>{event.providerId} / {event.modelId}</td>
          <td className="num">{num(event.inputTokens)}</td><td className="num">{num(event.outputTokens)}</td><td className="num">{event.cacheReadTokens ? num(event.cacheReadTokens) : '—'}</td><td className="num">{event.durationMs ?? 0} ms</td><td>{event.requestId}</td>
        </tr>)}</tbody>
      </table></div>
    </section>
  </div>
}
