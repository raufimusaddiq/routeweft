import { useState } from 'react'
import { Delta, Status } from './kit2'
import { BarChart, ErrorNotice, MetricsSkeleton, Sparkline, StackedBar } from './kit'
import { statusTone } from './chartMath'
import { clock, compact, delta, num, pct, rate, statusClass, useData } from './proto'
import { Fig } from './Overview'

const periods = [['24h', '24 hours'], ['7d', '7 days'], ['30d', '30 days']] as const

export function Usage({ onUnauthorized }: { onUnauthorized: () => void }) {
  const data = useData(onUnauthorized)
  const [period, setPeriod] = useState<'24h' | '7d' | '30d'>('30d')
  const s = period === '24h' ? data.day : period === '7d' ? data.week : data.month
  if (data.loading && !s) return <div className="skeleton-page" role="status" aria-label="Loading usage"><MetricsSkeleton count={3} /></div>
  if (data.error && !s) return <ErrorNotice message={data.error} onRetry={data.reload} />
  if (!s) return null
  const t = s.totals
  const days = period === '24h' ? 1 : period === '7d' ? 7 : 30
  const tokens = t.inputTokens + t.outputTokens
  const hit = (t.cacheReadTokens / Math.max(1, t.inputTokens + t.cacheReadTokens)) * 100
  const points = s.series.map(d => ({ label: d.day, short: d.day.slice(5), value: d.requests }))
  const topP = Math.max(1, ...s.providers.map(p => p.requests))

  return <div className="usage-content b-usage" aria-busy={data.loading}>
    <div className="ed-lead ed-lead-usage">
      <p className="ed-headline"><strong>{compact(tokens)}</strong> tokens across <strong>{num(t.requests)}</strong> requests in {periods.find(p => p[0] === period)![1]}. <strong>{pct(hit, 0)}</strong> of input came from cache; the average call took <strong>{num(t.avgDurationMs ?? 0)} ms</strong>.</p>
      <div className="ed-period" role="group" aria-label="Time period"><label htmlFor="usage-period" className="tile-label">Period</label>
        <select id="usage-period" value={period} onChange={e => setPeriod(e.target.value as typeof period)}>{periods.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>
        <button className="button-secondary" type="button" onClick={data.reload}>Refresh</button></div>
    </div>

    <Fig kicker="Fig. 1" title="Requests per day" caption={<>{num(t.requests)} requests, <strong>{num(t.requests / days)}</strong> a day on average.{period === '7d' && delta(t.requests, data.prevWeek?.requests) !== null && <> <Delta value={delta(t.requests, data.prevWeek?.requests)} basis="vs prior 7 d" /></>}</>}>
      <section className="ed-figure accent-surface"><BarChart points={points} unit="requests" title="Requests per day" /></section>
    </Fig>

    <Fig kicker="Fig. 2" title="Tokens and cache" caption={<>{compact(t.inputTokens)} in, {compact(t.outputTokens)} out, {compact(t.cacheReadTokens)} read from cache and {compact(t.cacheWriteTokens)} written.</>}>
      <div className="ed-pair">
        <figure><figcaption className="tile-label">Tokens per day</figcaption><Sparkline values={s.series.map(d => d.inputTokens + d.outputTokens)} label="Tokens per day" caption={`${compact(tokens / days)} /day`} /></figure>
        <figure><figcaption className="tile-label">Cache tokens per day</figcaption><Sparkline values={s.series.map(d => d.cacheReadTokens + d.cacheWriteTokens)} label="Cache tokens per day" caption={`${pct(hit, 0)} hit`} /></figure>
      </div>
    </Fig>

    <Fig kicker="Fig. 3" title="Outcomes" caption={<>{pct(rate(t.errors, t.requests))} of responses failed ({num(t.errors)}).</>}>
      <StackedBar title="Response status counts" segments={s.statuses.map(i => ({ key: i.key, label: i.key, value: i.requests, tone: statusTone(i.key) }))} />
    </Fig>

    <Fig kicker="Table 1" title="Providers and models" caption="Share of requests; error rate beside each.">
      <div className="ed-pair ed-pair-top">
        <ol className="ed-list">{s.providers.slice(0, 7).map(i => { const e = rate(i.errors, i.requests); return <li key={i.key}><Status tone={e > 8 ? 'bad' : e > 4 ? 'warn' : 'good'}><span className="ed-name">{i.key}</span></Status><span className="ed-bar"><span style={{ width: `${(i.requests / topP) * 100}%` }} /></span><span className="ed-fig">{num(i.requests)}</span><span className="ed-sub">{pct(e)}</span></li> })}</ol>
        <ol className="ed-list">{s.models.slice(0, 7).map(i => <li key={i.key}><span className="ed-name ed-mono">{i.key}</span><span className="ed-bar"><span style={{ width: `${(i.requests / s.models[0].requests) * 100}%` }} /></span><span className="ed-fig">{num(i.requests)}</span><span className="ed-sub">{compact(i.inputTokens + i.outputTokens)} tok</span></li>)}</ol>
      </div>
    </Fig>

    <Fig kicker="Log" title="Recent requests" caption={`Latest ${Math.min(10, data.events.length)}, newest first.`}>
      <div className="table-scroll"><table className="ed-table usage-table"><thead><tr><th scope="col">Time</th><th scope="col">Status</th><th scope="col">Route</th><th scope="col" className="num">In</th><th scope="col" className="num">Out</th><th scope="col" className="num">Latency</th></tr></thead>
        <tbody>{data.events.slice(0, 10).map(e => <tr key={e.id} className="usage-event"><td>{clock(e.createdAt)}</td><td><Status tone={statusClass(e.status)}>{e.status}</Status></td><td>{e.providerId} / {e.modelId}</td><td className="num">{num(e.inputTokens)}</td><td className="num">{num(e.outputTokens)}</td><td className="num">{e.durationMs ?? 0} ms</td></tr>)}</tbody></table></div>
    </Fig>
  </div>
}
