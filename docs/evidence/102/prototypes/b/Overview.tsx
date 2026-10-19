import { Dot, Delta, Status } from './kit2'
import { BarChart, ErrorNotice, MetricsSkeleton, Sparkline, StackedBar } from './kit'
import { statusTone } from './chartMath'
import { ago, clock, compact, delta, num, pct, rate, statusClass, useData } from './proto'
import type { ReactNode } from 'react'

export function Fig({ kicker, title, caption, children }: { kicker: string; title: string; caption: ReactNode; children: ReactNode }) {
  return <section className="ed-section" aria-label={title}>
    <aside className="ed-aside"><p className="ed-kicker">{kicker}</p><h2>{title}</h2><p className="ed-caption">{caption}</p></aside>
    <div className="ed-body">{children}</div>
  </section>
}

export function Overview({ onUnauthorized }: { onUnauthorized: () => void }) {
  const data = useData(onUnauthorized)
  const { overview, week, month, prevWeek, events } = data
  if (data.loading && !overview) return <div className="skeleton-page" role="status" aria-label="Loading overview"><MetricsSkeleton count={3} /></div>
  if (data.error && !overview) return <ErrorNotice message={data.error} onRetry={data.reload} />
  if (!overview || !week || !month) return null
  const t = week.totals, m = month.totals
  const up = delta(t.requests, prevWeek?.requests) ?? 0
  const err = rate(t.errors, t.requests), err30 = rate(m.errors, m.requests)
  const days = month.series
  const weekend = (d: string) => [0, 6].includes(new Date(d + 'T12:00:00').getDay())
  const avg = (rows: typeof days) => rows.reduce((s, d) => s + d.requests, 0) / Math.max(1, rows.length)
  const lighter = 100 - (avg(days.filter(d => weekend(d.day))) / Math.max(1, avg(days.filter(d => !weekend(d.day))))) * 100
  const peak = days.reduce((b, d) => d.requests > b.requests ? d : b, days[0])
  const points = days.map(d => ({ label: d.day, short: d.day.slice(5), value: d.requests }))
  const top = week.providers[0]
  const ready = overview.runtime.health === 'ready'
  const topShare = (top.requests / Math.max(1, t.requests)) * 100

  return <div className="overview-content b-overview" aria-busy={data.loading}>
    <div className="ed-lead">
      <p className="ed-headline">
        <strong>{num(t.requests)}</strong> requests in the last 7 days, <strong>{up >= 0 ? '▲' : '▼'} {Math.abs(up).toFixed(1)}%</strong> on the week before.
        {' '}<strong>{pct(err)}</strong> failed, {err <= err30 ? 'better' : 'worse'} than the 30-day <span className="ed-quiet">{pct(err30)}</span>.
      </p>
      <section className="ed-now accent-surface" aria-label="Live now">
        <h3 className="tile-label"><Dot tone={ready ? 'good' : 'bad'} /> {ready ? 'Live' : 'Not ready'}</h3>
        <p className="ed-now-value">{ago(overview.traffic.lastRequestAt)}</p>
        <p className="ed-now-note">since the last request · {overview.providers.enabledConnections} of {data.connections.length} connections enabled · {overview.apiKeys.active} API keys</p>
        <Sparkline values={week.series.map(d => d.requests)} label="Requests per day" caption="7 days" />
      </section>
    </div>

    <Fig kicker="Fig. 1" title="Requests per day" caption={<>Last 30 days. Weekends run {Math.round(lighter)}% lighter than weekdays. Peak: <strong>{num(peak.requests)}</strong> on {peak.day.slice(5)}.</>}>
      <BarChart points={points} unit="requests" title="Requests per day, 30 days" />
    </Fig>

    <Fig kicker="Table 1" title="Where the traffic goes" caption={<>{top.key} carries {Math.round(topShare)}% of the week. {week.providers.filter(p => rate(p.errors, p.requests) > 5).map(p => p.key).join(', ') || 'No provider'} above 5% errors.</>}>
      <ol className="ed-list">{week.providers.slice(0, 8).map(item => { const e = rate(item.errors, item.requests); return <li key={item.key}>
        <Status tone={e > 8 ? 'bad' : e > 4 ? 'warn' : 'good'}><span className="ed-name">{item.key}</span></Status>
        <span className="ed-bar"><span style={{ width: `${(item.requests / week.providers[0].requests) * 100}%` }} /></span>
        <span className="ed-fig">{num(item.requests)} <small>req</small></span>
        <span className="ed-sub">{pct(e)} err</span>
      </li> })}</ol>
    </Fig>

    <Fig kicker="Fig. 2" title="Outcomes" caption={<>{num(t.errors)} of {num(t.requests)} responses were not 2xx. {(() => { const f = week.statuses.filter(x => !x.key.startsWith('2')).sort((a, b) => b.requests - a.requests)[0]; return f ? `Largest failure class: ${f.key} (${num(f.requests)}).` : '' })()}</>}>
      <StackedBar title="Response status counts" segments={week.statuses.map(item => ({ key: item.key, label: item.key, value: item.requests, tone: statusTone(item.key) }))} />
    </Fig>

    <Fig kicker="Log" title="Latest requests" caption={<>Newest first, of {num(overview.traffic.requests)} recorded.</>}>
      <table className="ed-table"><thead><tr><th scope="col">Time</th><th scope="col">Status</th><th scope="col">Route</th><th scope="col" className="num">Tokens in / out</th><th scope="col" className="num">Latency</th></tr></thead>
        <tbody>{events.slice(0, 7).map(e => <tr key={e.id}><td>{clock(e.createdAt)}</td><td><Status tone={statusClass(e.status)}>{e.status}</Status></td><td>{e.providerId} / {e.modelId}</td><td className="num">{num(e.inputTokens)} / {num(e.outputTokens)}</td><td className="num">{e.durationMs ?? 0} ms</td></tr>)}</tbody></table>
    </Fig>
  </div>
}
