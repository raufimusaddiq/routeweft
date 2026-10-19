import { useEffect, useState, type ReactNode } from 'react'
import { Dot, Delta, Status } from './kit2'
import { BarChart, ErrorNotice, MetricsSkeleton, Sparkline, StackedBar } from './kit'
import { statusTone } from './chartMath'
import { ago, clock, compact, delta, num, pct, rate, statusClass, useData, type Data } from './proto'

// Persistent live strip: the one accent surface. Ticks once a second.
export function LiveStrip({ data }: { data: Data }) {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => { const id = window.setInterval(() => setNow(new Date()), 1000); return () => window.clearInterval(id) }, [])
  const o = data.overview, d = data.day
  if (!o || !d) return null
  const ready = o.runtime.health === 'ready'
  return <div className="live accent-surface" role="status" aria-label="Live gateway status">
    <span className="live-cell live-main"><Dot tone={ready ? 'good' : 'bad'} /><strong>{ready ? 'LIVE' : 'NOT READY'}</strong><span className="live-clock">{clock(now.toISOString())}</span></span>
    <span className="live-cell"><small>Last request</small><b>{ago(o.traffic.lastRequestAt, now.getTime())}</b></span>
    <span className="live-cell"><small>24 h</small><b>{num(d.totals.requests)} req</b></span>
    <span className="live-cell"><small>Errors 24 h</small><b>{pct(rate(d.totals.errors, d.totals.requests))}</b></span>
    <span className="live-cell"><small>Latency</small><b>{num(d.totals.avgDurationMs ?? 0)} ms</b></span>
    <span className="live-cell"><small>Connections</small><b>{o.providers.enabledConnections} / {data.connections.length}</b></span>
  </div>
}

export function Panel({ title, meta, className = '', children }: { title: string; meta?: ReactNode; className?: string; children: ReactNode }) {
  return <section className={`pn ${className}`.trim()} aria-label={title}><header className="pn-head"><h2>{title}</h2>{meta && <span className="count">{meta}</span>}</header>{children}</section>
}

export function HeroFacts({ series }: { series: { day: string; requests: number }[] }) {
  const weekend = (d: string) => [0, 6].includes(new Date(d + 'T12:00:00').getDay())
  const avg = (rows: typeof series) => rows.reduce((total, d) => total + d.requests, 0) / Math.max(1, rows.length)
  const wd = avg(series.filter(d => !weekend(d.day))), we = avg(series.filter(d => weekend(d.day)))
  const low = series.reduce((b, d) => d.requests < b.requests ? d : b, series[0])
  const high = series.reduce((b, d) => d.requests > b.requests ? d : b, series[0])
  return <dl className="hero-facts">
    <div><dt>Weekday average</dt><dd>{num(wd)} <small>req/day</small></dd></div>
    <div><dt>Weekend average</dt><dd>{num(we)} <small>req/day</small></dd><Delta value={delta(we, wd)} basis="vs weekday" good="none" /></div>
    <div><dt>Busiest day</dt><dd>{num(high.requests)} <small>on {high.day.slice(5)}</small></dd></div>
    <div><dt>Quietest day</dt><dd>{num(low.requests)} <small>on {low.day.slice(5)}</small></dd></div>
  </dl>
}

export function Overview({ onUnauthorized }: { onUnauthorized: () => void }) {
  const data = useData(onUnauthorized)
  const { overview, week, month, prevWeek, events } = data
  if (data.loading && !overview) return <div className="skeleton-page" role="status" aria-label="Loading overview"><MetricsSkeleton count={4} /></div>
  if (data.error && !overview) return <ErrorNotice message={data.error} onRetry={data.reload} />
  if (!overview || !week || !month) return null
  const t = week.totals, m = month.totals
  const tokens = t.inputTokens + t.outputTokens
  const peak = month.series.reduce((b, d) => d.requests > b.requests ? d : b, month.series[0])
  const points = month.series.map(d => ({ label: d.day, short: d.day.slice(5), value: d.requests }))
  const top = Math.max(1, ...week.providers.map(p => p.requests))
  return <div className="overview-content c-overview" aria-busy={data.loading}>
    <LiveStrip data={data} />
    <div className="bento">
      <Panel title="Requests · 30 days" meta={`avg ${num(m.requests / 30)} /day`} className="b-hero b-hero-1">
        <p className="pn-figure"><span className="tile-number">{num(m.requests)}</span><span className="tile-unit">req</span><span className="pn-peak">peak {num(peak.requests)} on {peak.day.slice(5)}</span></p>
        <div className="well"><BarChart points={points} unit="requests" title="Requests per day, 30 days" /></div>
        <HeroFacts series={month.series} />
      </Panel>
      <Panel title="Tokens · 7 days" meta="in + out" className="b-side b-grow">
        <p className="pn-figure"><span className="tile-number">{compact(tokens)}</span><span className="tile-unit">tok</span></p>
        <Delta value={delta(tokens, prevWeek ? prevWeek.inputTokens + prevWeek.outputTokens : undefined)} basis="vs prior 7 d" />
        <div className="well well-flush"><Sparkline values={week.series.map(d => d.inputTokens + d.outputTokens)} label="Tokens per day" caption={`${compact(tokens / 7)} /day`} /></div>
      </Panel>
      <Panel title="Failures · 7 days" meta={`${num(t.errors)} of ${num(t.requests)}`} className="b-third">
        <p className="pn-figure"><span className="tile-number">{pct(rate(t.errors, t.requests))}</span><span className="tile-unit">error rate</span></p>
        <Delta value={rate(t.errors, t.requests) - rate(m.errors, m.requests)} basis="vs 30 d" good="down" suffix=" pt" digits={2} />
        <div className="well"><StackedBar title="Response status counts" segments={week.statuses.map(i => ({ key: i.key, label: i.key, value: i.requests, tone: statusTone(i.key) }))} /></div>
      </Panel>
      <Panel title="Providers · 7 days" meta={`${week.providers.length} active`} className="b-twothird">
        <div className="well"><table className="grid-table"><thead><tr><th scope="col">Provider</th><th scope="col" className="num">Req</th><th scope="col">Share</th><th scope="col" className="num">Err</th></tr></thead>
          <tbody>{week.providers.slice(0, 10).map(i => { const e = rate(i.errors, i.requests); return <tr key={i.key}><td><Status tone={e > 8 ? 'bad' : e > 4 ? 'warn' : 'good'}><code>{i.key}</code></Status></td><td className="num">{num(i.requests)}</td><td><span className="bar"><span style={{ width: `${(i.requests / top) * 100}%` }} /></span></td><td className="num">{pct(e)}</td></tr> })}</tbody></table></div>
      </Panel>
      <Panel title="Recent requests" meta={`${num(overview.traffic.requests)} total`} className="b-full">
        <div className="well table-scroll"><table className="grid-table"><thead><tr><th scope="col">Time</th><th scope="col">Status</th><th scope="col">Route</th><th scope="col" className="num">Tokens in / out</th><th scope="col" className="num">Latency</th></tr></thead>
          <tbody>{events.slice(0, 7).map(e => <tr key={e.id}><td>{clock(e.createdAt)}</td><td><Status tone={statusClass(e.status)}>{e.status}</Status></td><td>{e.providerId}/{e.modelId}</td><td className="num">{num(e.inputTokens)} / {num(e.outputTokens)}</td><td className="num">{e.durationMs ?? 0} ms</td></tr>)}</tbody></table></div>
      </Panel>
    </div>
  </div>
}
