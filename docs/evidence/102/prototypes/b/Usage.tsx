import { useState } from 'react'
import { Delta, Status } from './kit2'
import { BarChart, ErrorNotice, MetricsSkeleton, Sparkline, StackedBar } from './kit'
import { statusTone } from './chartMath'
import { clock, compact, delta, num, pct, rate, statusClass, useData } from './proto'
import { Caption, ErrCell, Fact, Main, Margin, Row, Statement, outlier } from './Overview'

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
  const series = s.series, n = series.length
  const points = series.map(d => ({ label: d.day, short: d.day.slice(5), value: d.requests }))
  const fleet = rate(t.errors, t.requests)
  const topFail = s.statuses.filter(x => !x.key.startsWith('2')).sort((a, b) => b.requests - a.requests)[0]
  const out = outlier(s)
  const wk = n >= 14
  const ghost = (i: number) => (wk && i >= n - 7 ? series[i - 7].requests : null)
  const topP = Math.max(1, ...s.providers.map(p => p.requests))

  return <div className="usage-content b-usage" aria-busy={data.loading}>
    <Row gap="s" label="Summary">
      <div className="ed-main">
        <p className="ed-headline"><strong>{compact(tokens)}</strong> tokens, <strong>{num(t.requests)}</strong> requests</p>
        <p className="ed-subline">in the last {periods.find(p => p[0] === period)![1]}. {pct(hit, 0)} of input came from cache; the average call took <span className="ed-nowrap">{num(t.avgDurationMs ?? 0)} ms.</span></p>
      </div>
      <Margin>
        <div className="ed-period"><label htmlFor="usage-period" className="ed-label">Period</label>
          <select id="usage-period" value={period} onChange={e => setPeriod(e.target.value as typeof period)}>{periods.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select>
          <button className="button-secondary" type="button" onClick={data.reload}>Refresh</button></div>
      </Margin>
    </Row>

    <Row gap="s" label="Requests per day">
      <Main title="Requests per day">
        <figure className="ed-figure accent-surface ed-plate">
          <BarChart points={points} unit="requests" title="Requests per day" current={wk ? (i => i >= n - 7) : undefined} ghost={wk ? ghost : undefined} reference={n > 1 ? { value: t.requests / days, label: `average ${num(t.requests / days)}` } : undefined} />
          <figcaption><Caption kicker="Fig. 1">{wk ? `Cyan bars are the last 7 days; each tick is the same day a week earlier. Dashed line: average ${num(t.requests / days)}.` : 'One bar per day.'}</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts">
          <Fact label="Requests" value={<>{num(t.requests)} <small>req</small></>} note={wk ? <Delta value={delta(series.slice(-7).reduce((a, d) => a + d.requests, 0), series.slice(-14, -7).reduce((a, d) => a + d.requests, 0))} basis="last 7 vs prior 7 d" /> : `avg ${num(t.requests / days)} /day`} />
          <Fact label="Tokens" value={<>{compact(tokens)} <small>in + out</small></>} note={`${compact(tokens / days)} /day`} />
          <Fact label="Cache" value={<>{pct(hit, 0)} <small>of input</small></>} note={`${compact(t.cacheReadTokens)} read, ${compact(t.cacheWriteTokens)} written`} />
        </dl>
      </Margin>
    </Row>

    {topFail && <Statement>
      <strong>{topFail.key}</strong> causes <strong>{Math.round((topFail.requests / Math.max(1, t.errors)) * 100)}%</strong> of failures.{out ? <> <span className="ed-quiet">{out.key} is the weakest provider at {pct(out.err)}.</span></> : null}
    </Statement>}

    <Row gap="s" label="Providers and models">
      <Main title="Providers">
        <figure className="ed-figure">
          <div className="table-scroll"><table className="ed-table prov-table"><thead><tr><th scope="col">Provider</th><th scope="col" className="num">Requests</th><th scope="col"><span className="sr-only">Share</span></th><th scope="col" className="num">Errors</th></tr></thead>
            <tbody>{s.providers.slice(0, 8).map(i => <tr key={i.key}><td><Status tone={rate(i.errors, i.requests) > 8 ? 'bad' : rate(i.errors, i.requests) > 4 ? 'warn' : 'good'}><span className="ed-name">{i.key}</span></Status></td><td className="num ed-big">{num(i.requests)}</td><td><span className="ed-bar"><span style={{ width: `${(i.requests / topP) * 100}%` }} /></span></td><ErrCell errors={i.errors} requests={i.requests} fleet={fleet} /></tr>)}</tbody></table></div>
          <figcaption><Caption kicker="Table 1">By requests; the error bar scales to 10%.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <h3 className="ed-h3">Models</h3>
        <ul className="ed-watch ed-models">{s.models.slice(0, 6).map(m => <li key={m.key}><code>{m.key}</code><span className="num">{num(m.requests)} <small>req</small></span></li>)}</ul>
      </Margin>
    </Row>

    <Row gap="l" label="Outcomes">
      <Main title="Outcomes">
        <figure className="ed-figure">
          <StackedBar title="Response status counts" segments={s.statuses.map(i => ({ key: i.key, label: i.key, value: i.requests, tone: statusTone(i.key) }))} />
          <figcaption><Caption kicker="Fig. 2">Responses by status code.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts"><Fact label="Failed" value={<>{pct(fleet)}</>} note={`${num(t.errors)} of ${num(t.requests)} responses`} /></dl>
      </Margin>
    </Row>

    <Row gap="m" label="Recent requests">
      <Main title="Recent requests">
        <figure className="ed-figure">
          <div className="table-scroll"><table className="ed-table usage-table"><thead><tr><th scope="col">Route</th><th scope="col" className="num">Status</th><th scope="col" className="num">In</th><th scope="col" className="num">Out</th><th scope="col" className="num">Latency</th><th scope="col" className="num">Time</th></tr></thead>
            <tbody>{data.events.slice(0, 9).map(e => <tr key={e.id} className="usage-event"><td>{e.providerId} / {e.modelId}</td><td className="num"><Status tone={statusClass(e.status)}>{e.status}</Status></td><td className="num">{num(e.inputTokens)}</td><td className="num">{num(e.outputTokens)}</td><td className="num">{e.durationMs ?? 0} ms</td><td className="num">{clock(e.createdAt)}</td></tr>)}</tbody></table></div>
          <figcaption><Caption kicker="Log">Latest 9, newest first.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <figure className="ed-mini"><figcaption className="ed-label">Tokens per day</figcaption><Sparkline values={series.map(d => d.inputTokens + d.outputTokens)} label="Tokens per day" caption={`${compact(tokens / days)} /day`} /></figure>
        <figure className="ed-mini"><figcaption className="ed-label">Cache tokens per day</figcaption><Sparkline values={series.map(d => d.cacheReadTokens + d.cacheWriteTokens)} label="Cache tokens per day" caption={`${pct(hit, 0)} hit`} /></figure>
      </Margin>
    </Row>
  </div>
}
