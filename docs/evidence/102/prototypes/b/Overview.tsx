import type { ReactNode } from 'react'
import { Dot, Status } from './kit2'
import { BarChart, ErrorNotice, MetricsSkeleton, StackedBar } from './kit'
import { statusTone } from './chartMath'
import { ago, clock, compact, delta, num, pct, rate, statusClass, useData, type Summary } from './proto'

// ---- editorial building blocks (shared by B's three pages) ----
export function Row({ gap = 'm', label, children }: { gap?: 's' | 'm' | 'l'; label: string; children: ReactNode }) {
  return <section className={`ed-row gap-${gap}`} aria-label={label}>{children}</section>
}
export function Main({ title, children }: { title: string; children: ReactNode }) {
  return <div className="ed-main"><h2 className="ed-h2">{title}</h2>{children}</div>
}
export function Caption({ kicker, children }: { kicker: string; children: ReactNode }) {
  return <p className="ed-caption"><span className="ed-kicker">{kicker}</span> {children}</p>
}
export function Margin({ children }: { children: ReactNode }) { return <aside className="ed-margin">{children}</aside> }
export function Fact({ label, value, note }: { label: string; value: ReactNode; note?: ReactNode }) {
  return <div className="ed-fact"><dt>{label}</dt><dd>{value}</dd>{note && <dd className="ed-fact-note">{note}</dd>}</div>
}
export function Statement({ children, action, href }: { children: ReactNode; action?: string; href?: string }) {
  return <section className="ed-statement-row gap-m" aria-label="Finding"><p className="ed-statement">{children}</p>{action && <a className="ed-link-out" href={href}>{action}</a>}</section>
}

// worst provider that has enough traffic to mean something
export function outlier(summary: Summary) {
  const fleet = rate(summary.totals.errors, summary.totals.requests)
  const worst = summary.providers.filter(p => p.requests >= 50).map(p => ({ ...p, err: rate(p.errors, p.requests) })).sort((a, b) => b.err - a.err)[0]
  return worst ? { ...worst, fleet, times: worst.err / Math.max(0.01, fleet) } : null
}

export function ErrCell({ errors, requests, fleet }: { errors: number; requests: number; fleet: number }) {
  const e = rate(errors, requests)
  const bad = e > fleet * 1.5 && e > 3
  return <td className="num err-cell"><span className={`err-bar${bad ? ' err-bad' : ''}`} aria-hidden="true"><span style={{ width: `${Math.min(100, (e / 10) * 100)}%` }} /></span><span className={bad ? 'err-text-bad' : ''}>{pct(e)}</span></td>
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
  const days = month.series, n = days.length
  const weekend = (d: string) => [0, 6].includes(new Date(d + 'T12:00:00').getDay())
  const avg = (rows: typeof days) => rows.reduce((s, d) => s + d.requests, 0) / Math.max(1, rows.length)
  const lighter = 100 - (avg(days.filter(d => weekend(d.day))) / Math.max(1, avg(days.filter(d => !weekend(d.day))))) * 100
  const peak = days.reduce((b, d) => d.requests > b.requests ? d : b, days[0])
  const points = days.map(d => ({ label: d.day, short: d.day.slice(5), value: d.requests }))
  const out = outlier(week)
  const off = data.connections.filter(c => !c.enabled)
  const ready = overview.runtime.health === 'ready'
  const failures = week.statuses.filter(x => !x.key.startsWith('2')).sort((a, b) => b.requests - a.requests)
  const topFail = failures[0]
  const ghost = (i: number) => (i >= n - 7 && i - 7 >= 0 ? days[i - 7].requests : null)
  const shownEvents = events.slice(0, 7)
  const slow = [...shownEvents].sort((a, b) => (b.durationMs ?? 0) - (a.durationMs ?? 0))[0]
  const fast = [...shownEvents].sort((a, b) => (a.durationMs ?? 0) - (b.durationMs ?? 0))[0]
  const fleet = rate(t.errors, t.requests)

  return <div className="overview-content b-overview" aria-busy={data.loading}>
    <Row gap="s" label="This week">
      <div className="ed-main">
        <p className="ed-headline"><strong>{num(t.requests)}</strong> requests this week, <strong>{up >= 0 ? '↑' : '↓'} {Math.abs(up).toFixed(1)}%</strong></p>
        <p className="ed-subline">on the week before. {pct(err)} failed, {err <= err30 ? 'better' : 'worse'} than the <span className="ed-nowrap">30-day {pct(err30)}.</span></p>
      </div>
      <Margin>
        <section className="ed-now accent-surface" aria-label="Live now">
          <h3 className="ed-label"><Dot tone={ready ? 'good' : 'bad'} /> {ready ? 'Live' : 'Not ready'}</h3>
          <p className="ed-now-value">{ago(overview.traffic.lastRequestAt)}</p>
          <p className="ed-now-note">since the last request. {overview.providers.enabledConnections} of {data.connections.length} connections enabled, {overview.apiKeys.active} API keys.</p>
        </section>
      </Margin>
    </Row>

    <Row gap="s" label="Requests per day">
      <Main title="Requests per day">
        <figure className="ed-figure">
          <BarChart points={points} unit="requests" title="Requests per day, 30 days" shade={weekend} current={i => i >= n - 7} ghost={ghost} reference={{ value: m.requests / 30, label: `30-day average ${num(m.requests / 30)}` }} />
          <figcaption><Caption kicker="Fig. 1">Last 30 days. Cyan bars are this week; each white tick is the same day a week earlier. The dashed line is the 30-day average ({num(m.requests / 30)}), weekends shaded.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts">
          <Fact label="This week" value={<>{num(t.requests)} <small>req</small></>} note={`${up >= 0 ? '↑' : '↓'} ${Math.abs(up).toFixed(1)}% vs prior 7 d (${num(prevWeek?.requests ?? 0)})`} />
          <Fact label="Busiest day" value={<>{num(peak.requests)} <small>req</small></>} note={`${peak.day.slice(5)}, ${Math.round((peak.requests / (m.requests / 30) - 1) * 100)}% above the 30-day average`} />
          <Fact label="Weekends" value={<>{Math.round(lighter)}% <small>lighter</small></>} note="than weekdays, 30 days" />
        </dl>
      </Margin>
    </Row>

    {out && <Statement action="Review providers" href="#providers">
      <strong>{out.key}</strong> fails <strong>{pct(out.err)}</strong> of its requests, <strong>{out.times.toFixed(1)}×</strong> the fleet rate.
    </Statement>}

    <Row gap="s" label="Providers">
      <Main title="Where the traffic goes">
        <figure className="ed-figure">
          <div className="table-scroll"><table className="ed-table prov-table"><thead><tr><th scope="col">Provider</th><th scope="col" className="num">Requests</th><th scope="col"><span className="sr-only">Share</span></th><th scope="col" className="num">Errors</th></tr></thead>
            <tbody>{week.providers.slice(0, 8).map(item => <tr key={item.key}>
              <td><Status tone={rate(item.errors, item.requests) > 8 ? 'bad' : rate(item.errors, item.requests) > 4 ? 'warn' : 'good'}><span className="ed-name">{item.key}</span></Status></td>
              <td className="num ed-big">{num(item.requests)}</td>
              <td><span className="ed-bar"><span style={{ width: `${(item.requests / week.providers[0].requests) * 100}%` }} /></span></td>
              <ErrCell errors={item.errors} requests={item.requests} fleet={fleet} />
            </tr>)}</tbody></table></div>
          <figcaption><Caption kicker="Table 1">Last 7 days by requests. The error bar scales to 10%; red where a provider is more than 1.5× the fleet rate.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <h3 className="ed-h3">Needs a look</h3>
        <ul className="ed-watch">
          {out && out.err > fleet * 1.5 && <li><Status tone="bad"><strong>{out.key}</strong></Status><span>{pct(out.err)} failed against {pct(fleet)} fleet-wide.</span></li>}
          {off.map(c => <li key={c.id}><Status tone="warn"><strong>{c.providerId}</strong></Status><span>Account “{c.name}” is switched off.</span></li>)}
          {week.providers.filter(p => p.key !== out?.key && rate(p.errors, p.requests) > 4 && p.requests >= 50).map(p => <li key={p.key}><Status tone="warn"><strong>{p.key}</strong></Status><span>{pct(rate(p.errors, p.requests))} failed on {num(p.requests)} requests.</span></li>)}
        </ul>
        <a className="ed-link-out" href="#providers">Open providers</a>
      </Margin>
    </Row>

    <Row gap="l" label="Outcomes">
      <Main title="Outcomes">
        <figure className="ed-figure">
          <StackedBar title="Response status counts" segments={week.statuses.map(item => ({ key: item.key, label: item.key, value: item.requests, tone: statusTone(item.key) }))} />
          <figcaption><Caption kicker="Fig. 2">Responses in the last 7 days by status code.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts">
          <Fact label="Succeeded" value={<>{pct(100 - err)}</>} note={`${num(t.requests - t.errors)} of ${num(t.requests)} responses`} />
          {topFail && <Fact label="Largest failure" value={<>{topFail.key}</>} note={`${num(topFail.requests)} of ${num(t.errors)} failures (${Math.round((topFail.requests / Math.max(1, t.errors)) * 100)}%)`} />}
        </dl>
      </Margin>
    </Row>

    <Row gap="m" label="Latest requests">
      <Main title="Latest requests">
        <figure className="ed-figure">
          <div className="table-scroll"><table className="ed-table"><thead><tr><th scope="col">Route</th><th scope="col" className="num">Status</th><th scope="col" className="num">Tokens in / out</th><th scope="col" className="num">Latency</th><th scope="col" className="num">Time</th></tr></thead>
            <tbody>{shownEvents.map(e => <tr key={e.id}><td>{e.providerId} / {e.modelId}</td><td className="num"><Status tone={statusClass(e.status)}>{e.status}</Status></td><td className="num">{num(e.inputTokens)} / {num(e.outputTokens)}</td><td className="num">{e.durationMs ?? 0} ms</td><td className="num">{clock(e.createdAt)}</td></tr>)}</tbody></table></div>
          <figcaption><Caption kicker="Log">Newest first, {num(overview.traffic.requests)} requests recorded in all.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts">
          {slow && <Fact label="Slowest of these" value={<>{num(slow.durationMs ?? 0)} <small>ms</small></>} note={`${slow.providerId} / ${slow.modelId}`} />}
          {fast && <Fact label="Fastest of these" value={<>{num(fast.durationMs ?? 0)} <small>ms</small></>} note={`${fast.providerId} / ${fast.modelId}`} />}
        </dl>
      </Margin>
    </Row>
  </div>
}
