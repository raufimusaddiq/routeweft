import { Dot, Delta, Status, Tile } from './kit2'
import { Sparkline, StackedBar, ErrorNotice, MetricsSkeleton } from './kit'
import { statusTone } from './chartMath'
import { ago, clock, compact, delta, num, pct, rate, statusClass, useData } from './proto'

type Props = { onUnauthorized: () => void }

export function Overview({ onUnauthorized }: Props) {
  const data = useData(onUnauthorized)
  const { overview, week, prevWeek, events } = data
  if (data.loading && !overview) return <div className="skeleton-page" role="status" aria-label="Loading overview"><MetricsSkeleton count={6} /></div>
  if (data.error && !overview) return <ErrorNotice message={data.error} onRetry={data.reload} />
  if (!overview || !week) return null

  const t = week.totals
  const days = Math.max(1, week.series.length)
  const err = rate(t.errors, t.requests)
  const m = data.month?.totals
  const prevErr = m ? rate(m.errors, m.requests) : null
  const hit = (t.cacheReadTokens / Math.max(1, t.inputTokens + t.cacheReadTokens)) * 100
  const prevHit = m ? (m.cacheReadTokens / Math.max(1, m.inputTokens + m.cacheReadTokens)) * 100 : null
  const tokens = t.inputTokens + t.outputTokens
  const ready = overview.runtime.health === 'ready'
  const off = data.connections.filter(c => !c.enabled)
  const traffic = week.providers
  const top = Math.max(1, ...traffic.map(item => item.requests))

  return <div className="overview-content a-overview" aria-busy={data.loading}>
    <div className="strip">
      <Tile accent label="Requests · 7 days" value={num(t.requests)} unit="req">
        <Delta value={delta(t.requests, prevWeek?.requests)} basis="vs prior 7 d" />
        <Sparkline values={week.series.map(day => day.requests)} label="Requests" />
        <p className="tile-live"><Dot tone={ready ? 'good' : 'bad'} />{ready ? 'Live' : 'Not ready'} · last request {ago(overview.traffic.lastRequestAt)}</p>
      </Tile>
      <Tile label="Error rate" value={pct(err)} unit={`${num(t.errors)} of ${compact(t.requests)}`}>
        <Delta value={prevErr === null ? null : err - prevErr} basis="vs 30 d" good="down" suffix=" pt" digits={2} />
      </Tile>
      <Tile label="Latency" value={num(t.avgDurationMs ?? 0)} unit="ms avg">
        <Delta value={delta(t.avgDurationMs ?? 0, m?.avgDurationMs)} basis="vs 30 d" good="down" />
        <p className="tile-note">TTFT {num(t.avgTtftMs ?? 0)} ms</p>
      </Tile>
      <Tile label="Tokens" value={compact(tokens)} unit="in + out">
        <Delta value={delta(tokens, prevWeek ? prevWeek.inputTokens + prevWeek.outputTokens : undefined)} basis="vs prior 7 d" />
        <Sparkline values={week.series.map(day => day.inputTokens + day.outputTokens)} label="Tokens" />
      </Tile>
      <Tile label="Cache hit" value={pct(hit, 0)} unit="of input">
        <Delta value={prevHit === null ? null : hit - prevHit} basis="vs 30 d" suffix=" pt" digits={1} />
      </Tile>
      <Tile label="Connections" className={off.length ? 'tile-risk' : ''} value={String(overview.providers.enabledConnections)} unit={`of ${data.connections.length} enabled`}>
        <p className="tile-note">{off.length ? `${off.length} disabled: ${off.map(c => c.providerId).join(', ')}` : `${overview.providers.nodes} provider nodes`} · {overview.apiKeys.active} API keys</p>
      </Tile>
    </div>

    <div className="split">
      <section className="panel" aria-labelledby="traffic-title">
        <header className="panel-head"><h2 id="traffic-title">Traffic by provider · 7 days</h2><span className="count">{traffic.length} active · {num(avgPerDay(t.requests, days))} req/day</span></header>
        <table className="grid-table">
          <thead><tr><th scope="col">Provider</th><th scope="col" className="num">Requests</th><th scope="col">Share</th><th scope="col" className="num">Errors</th><th scope="col" className="num">Tokens</th></tr></thead>
          <tbody>{traffic.slice(0, 10).map(item => {
            const e = rate(item.errors, item.requests)
            return <tr key={item.key}>
              <td><Status tone={e > 8 ? 'bad' : e > 4 ? 'warn' : 'good'}><code>{item.key}</code></Status></td>
              <td className="num">{num(item.requests)}</td>
              <td><span className="bar"><span style={{ width: `${(item.requests / top) * 100}%` }} /></span></td>
              <td className="num">{pct(e)}</td>
              <td className="num">{compact(item.inputTokens + item.outputTokens)}</td>
            </tr>
          })}</tbody>
        </table>
      </section>
      <div className="stack">
        <section className="panel" aria-labelledby="status-title">
          <header className="panel-head"><h2 id="status-title">Response status · 7 days</h2></header>
          <StackedBar title="Response status counts" segments={week.statuses.map(item => ({ key: item.key, label: item.key, value: item.requests, tone: statusTone(item.key) }))} />
        </section>
        <section className="panel" aria-labelledby="runtime-title">
          <header className="panel-head"><h2 id="runtime-title">Runtime</h2></header>
          <dl className="kv">
            <div><dt>Gateway</dt><dd><Status tone={ready ? 'good' : 'bad'}>{ready ? 'Ready' : 'Not ready'}</Status></dd></div>
            <div><dt>Build</dt><dd>{overview.runtime.version}</dd></div>
            <div><dt>Config revision</dt><dd>r{num(overview.runtime.configRevision)}</dd></div>
            <div><dt>Telemetry</dt><dd><Status tone={overview.telemetry.degraded ? 'warn' : overview.telemetry.enabled ? 'good' : 'idle'}>{overview.telemetry.degraded ? 'Degraded' : overview.telemetry.enabled ? 'Enabled' : 'Disabled'}</Status></dd></div>
          </dl>
        </section>
      </div>
    </div>

    <section className="panel" aria-labelledby="recent-title">
      <header className="panel-head"><h2 id="recent-title">Recent requests</h2><span className="count">latest {Math.min(10, events.length)} of {num(overview.traffic.requests)}</span></header>
      <div className="table-scroll"><table className="grid-table">
        <thead><tr><th scope="col">Time</th><th scope="col">Status</th><th scope="col">Provider / model</th><th scope="col" className="num">In</th><th scope="col" className="num">Out</th><th scope="col" className="num">Latency</th></tr></thead>
        <tbody>{events.slice(0, 10).map(event => <tr key={event.id}>
          <td>{clock(event.createdAt)}</td>
          <td><Status tone={statusClass(event.status)}>{event.status}</Status></td>
          <td>{event.providerId} / {event.modelId}</td>
          <td className="num">{num(event.inputTokens)} tok</td>
          <td className="num">{num(event.outputTokens)} tok</td>
          <td className="num">{event.durationMs ?? 0} ms</td>
        </tr>)}</tbody>
      </table></div>
    </section>
  </div>
}

function avgPerDay(total: number, days: number) { return total / days }
