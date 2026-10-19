import { useState } from 'react'
import { Delta, Status } from './kit2'
import { BarChart, ErrorNotice, MetricsSkeleton, Sparkline, StackedBar } from './kit'
import { statusTone } from './chartMath'
import { clock, compact, delta, num, pct, rate, statusClass, useData } from './proto'
import { HeroFacts, LiveStrip, Panel } from './Overview'

const periods = [['24h', 'Last 24 hours'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days']] as const

export function Usage({ onUnauthorized }: { onUnauthorized: () => void }) {
  const data = useData(onUnauthorized)
  const [period, setPeriod] = useState<'24h' | '7d' | '30d'>('30d')
  const s = period === '24h' ? data.day : period === '7d' ? data.week : data.month
  if (data.loading && !s) return <div className="skeleton-page" role="status" aria-label="Loading usage"><MetricsSkeleton /></div>
  if (data.error && !s) return <ErrorNotice message={data.error} onRetry={data.reload} />
  if (!s) return null
  const t = s.totals, days = period === '24h' ? 1 : period === '7d' ? 7 : 30
  const tokens = t.inputTokens + t.outputTokens
  const hit = (t.cacheReadTokens / Math.max(1, t.inputTokens + t.cacheReadTokens)) * 100
  const points = s.series.map(d => ({ label: d.day, short: d.day.slice(5), value: d.requests }))
  const topP = Math.max(1, ...s.providers.map(p => p.requests)), topM = Math.max(1, ...s.models.map(p => p.requests))
  const wk = period === '7d'
  return <div className="usage-content c-usage" aria-busy={data.loading}>
    <LiveStrip data={data} />
    <div className="toolbar toolbar-c"><label htmlFor="usage-period">Period</label><select id="usage-period" value={period} onChange={e => setPeriod(e.target.value as typeof period)}>{periods.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select><span className="count">since {s.since.slice(0, 10)}</span><button className="button-secondary" type="button" onClick={data.reload}>Refresh</button></div>
    <div className="bento">
      <Panel title="Requests per day" meta={`avg ${num(t.requests / days)} /day`} className="b-hero b-hero-1 b-full">
        <p className="pn-figure"><span className="tile-number">{num(t.requests)}</span><span className="tile-unit">req</span>{wk && <Delta value={delta(t.requests, data.prevWeek?.requests)} basis="vs prior 7 d" />}</p>
        <div className="well"><BarChart points={points} unit="requests" title="Requests per day" /></div>
        {s.series.length > 1 && <HeroFacts series={s.series} />}
      </Panel>
      <>
        <Panel title="Tokens" meta="in + out" className="b-third b-grow"><p className="pn-figure"><span className="tile-number">{compact(tokens)}</span><span className="tile-unit">tok</span></p><div className="well well-flush"><Sparkline values={s.series.map(d => d.inputTokens + d.outputTokens)} label="Tokens per day" caption={`${compact(tokens / days)} /day`} /></div></Panel>
        <Panel title="Cache" meta={`${pct(hit, 0)} hit`} className="b-third b-grow"><p className="pn-figure"><span className="tile-number">{compact(t.cacheReadTokens + t.cacheWriteTokens)}</span><span className="tile-unit">tok</span></p><div className="well well-flush"><Sparkline values={s.series.map(d => d.cacheReadTokens + d.cacheWriteTokens)} label="Cache tokens per day" caption={`${compact(t.cacheWriteTokens)} written`} /></div></Panel>
        <Panel title="Latency" meta={`TTFT ${num(t.avgTtftMs ?? 0)} ms`} className="b-third"><p className="pn-figure"><span className="tile-number">{num(t.avgDurationMs ?? 0)}</span><span className="tile-unit">ms avg</span></p><p className="tile-note">{pct(rate(t.errors, t.requests))} of responses failed</p><div className="well"><StackedBar title="Response status counts" segments={s.statuses.map(i => ({ key: i.key, label: i.key, value: i.requests, tone: statusTone(i.key) }))} /></div></Panel>
      </>
      <Panel title="Providers" meta={`${s.providers.length} active`} className="b-half">
        <div className="well"><table className="grid-table"><thead><tr><th scope="col">Provider</th><th scope="col" className="num">Req</th><th scope="col">Share</th><th scope="col" className="num">Err</th></tr></thead><tbody>{s.providers.slice(0, 8).map(i => { const e = rate(i.errors, i.requests); return <tr key={i.key}><td><Status tone={e > 8 ? 'bad' : e > 4 ? 'warn' : 'good'}><code>{i.key}</code></Status></td><td className="num">{num(i.requests)}</td><td><span className="bar"><span style={{ width: `${(i.requests / topP) * 100}%` }} /></span></td><td className="num">{pct(e)}</td></tr> })}</tbody></table></div>
      </Panel>
      <Panel title="Models" meta="by requests" className="b-half">
        <div className="well"><table className="grid-table"><thead><tr><th scope="col">Model</th><th scope="col" className="num">Req</th><th scope="col">Share</th><th scope="col" className="num">Tok</th></tr></thead><tbody>{s.models.slice(0, 8).map(i => <tr key={i.key}><td><code>{i.key}</code></td><td className="num">{num(i.requests)}</td><td><span className="bar"><span style={{ width: `${(i.requests / topM) * 100}%` }} /></span></td><td className="num">{compact(i.inputTokens + i.outputTokens)}</td></tr>)}</tbody></table></div>
      </Panel>
      <Panel title="Recent requests" meta="latest 8" className="b-full">
        <div className="well table-scroll"><table className="grid-table usage-table"><thead><tr><th scope="col">Time</th><th scope="col">Status</th><th scope="col">Route</th><th scope="col" className="num">In</th><th scope="col" className="num">Out</th><th scope="col" className="num">Latency</th></tr></thead><tbody>{data.events.slice(0, 8).map(e => <tr key={e.id} className="usage-event"><td>{clock(e.createdAt)}</td><td><Status tone={statusClass(e.status)}>{e.status}</Status></td><td>{e.providerId}/{e.modelId}</td><td className="num">{num(e.inputTokens)}</td><td className="num">{num(e.outputTokens)}</td><td className="num">{e.durationMs ?? 0} ms</td></tr>)}</tbody></table></div>
      </Panel>
    </div>
  </div>
}
