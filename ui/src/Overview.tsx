import { BarChart, EditorialSkeleton, ErrorNotice, StackedBar } from './kit'
import { useOverviewData } from './data'
import { Caption, Dot, ErrCell, Fact, Main, Margin, Row, Statement, Status } from './editorial'
import { ago, badRate, clock, delta, isWeekend, num, outlier, pct, priorWeek, failureStatuses, providerTone, rate, share, statusTone, sum, weekendLighter } from './format'

type Props = { onUnauthorized: () => void }

function when(value: string) {
  if (!value) return 'No requests recorded'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

export function Overview({ onUnauthorized }: Props) {
  const data = useOverviewData(onUnauthorized)
  const { overview, week, month, events, connections } = data

  if (data.loading && !overview) return <div className="overview-content" role="status" aria-label="Loading overview"><EditorialSkeleton /></div>
  if (data.error && !overview) return <ErrorNotice message={data.error} onRetry={data.reload} />
  if (!overview) return null

  const ready = overview.runtime.health === 'ready'
  const off = connections.filter(connection => !connection.enabled)
  const attention: { key: string; tone: 'bad' | 'warn'; title: string; text: string }[] = []
  const t = week?.totals
  const err = t ? rate(t.errors, t.requests) : 0
  const fleet = err
  const worst = week ? outlier(week) : null
  const flagged = worst !== null && badRate(worst.errors, worst.requests, fleet)
  if (worst && flagged) attention.push({ key: `err-${worst.key}`, tone: 'bad', title: worst.key, text: `${pct(worst.err)} failed against ${pct(fleet)} fleet-wide.` })
  for (const connection of off) attention.push({ key: `off-${connection.id}`, tone: 'warn', title: connection.providerId, text: `Account “${connection.name}” is switched off.` })
  if (overview.telemetry.degraded) attention.push({ key: 'telemetry', tone: 'warn', title: 'Usage telemetry', text: 'Degraded: some accounting may be delayed or incomplete.' })
  if (overview.traffic.errors > 0 && !flagged) attention.push({ key: 'failed', tone: 'warn', title: 'Failed requests', text: `${num(overview.traffic.errors)} recorded.` })

  const series = month?.series ?? []
  const n = series.length
  const points = series.map(day => ({ label: day.day, short: day.day.slice(5), value: day.requests }))
  const prior = priorWeek(series)
  const thisWeek = sum(series.slice(-7), 'requests')
  const lastWeek = prior ? sum(prior, 'requests') : undefined
  const up = t ? delta(t.requests, lastWeek) : null
  const m = month?.totals
  const err30 = m ? rate(m.errors, m.requests) : 0
  const average = m && n ? m.requests / n : 0
  const peak = n ? series.reduce((best, day) => (day.requests > best.requests ? day : best), series[0]) : null
  const lighter = weekendLighter(series)
  const ghost = (index: number) => (prior && index >= n - 7 ? series[index - 7].requests : null)
  const failures = week ? failureStatuses(week.statuses) : []
  const shown = events.slice(0, 7)
  const slow = [...shown].sort((a, b) => (b.durationMs ?? 0) - (a.durationMs ?? 0))[0]
  const fast = [...shown].sort((a, b) => (a.durationMs ?? 0) - (b.durationMs ?? 0))[0]

  return <div className="overview-content" aria-busy={data.loading}>
    <Row gap="s" label="This week">
      <div className="ed-main">
        {t
          ? <><p className="ed-headline"><strong>{num(t.requests)}</strong> requests this week{up !== null && <>, <strong>{up >= 0 ? '↑' : '↓'} {Math.abs(up).toFixed(1)}%</strong></>}</p>
            <p className="ed-subline">{up !== null ? 'on the week before. ' : ''}{pct(err)} failed{m ? <>, {err <= err30 ? 'better' : 'worse'} than the <span className="ed-nowrap">30-day {pct(err30)}.</span></> : '.'}</p></>
          : <><p className="ed-headline"><strong>{num(overview.traffic.requests)}</strong> requests recorded</p>
            <p className="ed-subline">{num(overview.traffic.errors)} failed. Usage history is not available right now.</p></>}
      </div>
      <Margin>
        <section className="ed-now accent-surface" aria-label="Live now">
          <h3 className="ed-label"><Dot tone={ready ? 'good' : 'bad'} /> {ready ? 'Live' : 'Not ready'}</h3>
          <p className="ed-now-value">{ago(overview.traffic.lastRequestAt)}</p>
          <p className="ed-now-note">since the last request. {num(overview.providers.enabledConnections)} connections enabled, {num(overview.apiKeys.active)} API keys, {num(overview.traffic.active)} active now.</p>
          <p className="ed-now-foot"><span className="updated-at" aria-live="polite">Updated {data.updatedAt}</span><button className="button-secondary refresh-button" type="button" disabled={data.loading} onClick={data.reload}>Refresh</button></p>
        </section>
      </Margin>
    </Row>
    {data.error && <ErrorNotice className="refresh-error" message={data.error} busy={data.loading} onRetry={data.reload} />}

    {n > 0 && t && m && peak && <Row gap="s" label="Requests per day">
      <Main title="Requests per day">
        <figure className="ed-figure">
          <BarChart points={points} unit="requests" title="Requests per day, 30 days" shade={isWeekend} current={index => index >= n - 7} ghost={ghost} reference={{ value: average }} />
          <figcaption><Caption kicker="Fig. 1">Last 30 days. Cyan bars are this week; each white tick is the same day a week earlier. The dashed line is the 30-day average ({num(average)}), weekends shaded.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts">
          <Fact label="This week" value={<>{num(t.requests)} <small>req</small></>} note={up !== null ? `${up >= 0 ? '↑' : '↓'} ${Math.abs(up).toFixed(1)}% vs prior 7 d (${num(lastWeek ?? 0)})` : `${num(thisWeek)} in the latest 7 days`} />
          <Fact label="Busiest day" value={<>{num(peak.requests)} <small>req</small></>} note={`${peak.day.slice(5)}, ${Math.round((peak.requests / Math.max(1, average) - 1) * 100)}% above the 30-day average`} />
          <Fact label="Weekends" value={lighter === null || Math.abs(lighter) < 1 ? '—' : <>{Math.abs(Math.round(lighter))}% <small>{lighter >= 0 ? 'lighter' : 'heavier'}</small></>} note={lighter === null ? 'needs weekday and weekend days' : Math.abs(lighter) < 1 ? 'about the same as weekdays' : `than weekdays, ${n} days`} />
        </dl>
      </Margin>
    </Row>}

    {worst && flagged && <Statement action="Review providers" href="#providers">
      <strong>{worst.key}</strong> fails <strong>{pct(worst.err)}</strong> of its requests, <strong>{worst.times.toFixed(1)}×</strong> the fleet rate.
    </Statement>}

    {week && t && <Row gap="s" label="Providers">
      <Main title="Where the traffic goes">
        <figure className="ed-figure">
          <table className="ed-table prov-table"><thead><tr><th scope="col">Provider</th><th scope="col" className="num">Requests</th><th scope="col"><span className="sr-only">Share</span></th><th scope="col" className="num">Errors</th></tr></thead>
            <tbody>{week.providers.slice(0, 8).map(item => <tr key={item.key}>
              <td><Status tone={providerTone(item.errors, item.requests, fleet)}><span className="ed-name">{item.key}</span></Status></td>
              <td className="num ed-big">{num(item.requests)}</td>
              <td className="share-cell"><span className="ed-bar"><span style={{ width: `${share(item.requests, week.providers[0].requests)}%` }} /></span></td>
              <ErrCell errors={item.errors} requests={item.requests} bad={badRate(item.errors, item.requests, fleet)} />
            </tr>)}</tbody></table>
          <figcaption><Caption kicker="Table 1">Last 7 days by requests. The error bar scales to 10%; red where a provider is more than 1.5× the fleet rate.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <h3 className="ed-h3">Needs a look</h3>
        {attention.length === 0
          ? <p className="ed-now-note">Nothing needs attention: every account is enabled and no provider is far above the fleet error rate.</p>
          : <ul className="ed-watch" aria-label="Needs a look">{attention.map(item => <li key={item.key}><Status tone={item.tone}><strong>{item.title}</strong></Status><span>{item.text}</span></li>)}</ul>}
        <a className="ed-link-out" href="#providers">Open providers</a>
      </Margin>
    </Row>}

    {week && t && <Row gap="l" label="Outcomes">
      <Main title="Outcomes">
        <figure className="ed-figure">
          <StackedBar title="Response status counts" segments={week.statuses.map(item => ({ key: item.key, label: item.key, value: item.requests, tone: statusTone(item.key) }))} />
          <figcaption><Caption kicker="Fig. 2">Responses in the last 7 days by status code. A failure is any response that is not a 2xx, redirects included.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts">
          <Fact label="Succeeded" value={pct(100 - err)} note={`${num(t.requests - t.errors)} of ${num(t.requests)} responses`} />
          {failures[0] && <Fact label="Largest failure" value={failures[0].key} note={`${num(failures[0].requests)} of ${num(t.errors)} failures (${Math.round((failures[0].requests / Math.max(1, t.errors)) * 100)}%)`} />}
        </dl>
      </Margin>
    </Row>}

    {!week && attention.length > 0 && <Row gap="s" label="Needs a look">
      <Main title="Needs a look"><ul className="ed-watch" aria-label="Needs a look">{attention.map(item => <li key={item.key}><Status tone={item.tone}><strong>{item.title}</strong></Status><span>{item.text}</span></li>)}</ul></Main>
      <Margin><a className="ed-link-out" href="#usage">Open usage</a></Margin>
    </Row>}

    <Row gap="m" label="Latest requests">
      <Main title="Latest requests">
        <figure className="ed-figure">
          {shown.length === 0
            ? <p className="ed-now-note">No requests recorded yet. Send a request through the gateway and it will appear here.</p>
            : <table className="ed-table log-table"><thead><tr><th scope="col">Route</th><th scope="col" className="num">Status</th><th scope="col" className="num">Tokens in / out</th><th scope="col" className="num">Latency</th><th scope="col" className="num">Time</th></tr></thead>
              <tbody>{shown.map(event => <tr key={event.id}><td className="log-route">{event.providerId} / {event.modelId}</td><td className="num log-status"><Status tone={statusTone(event.status)}>{event.status}</Status></td><td className="num log-tokens">{num(event.inputTokens)} / {num(event.outputTokens)}</td><td className="num log-latency">{event.durationMs ?? 0} ms</td><td className="num log-time">{clock(event.createdAt)}</td></tr>)}</tbody></table>}
          <figcaption><Caption kicker="Log">Newest first, {num(overview.traffic.requests)} requests recorded in all.</Caption></figcaption>
        </figure>
      </Main>
      <Margin>
        <dl className="ed-facts">
          <Fact label="Runtime" value={<Status tone={ready ? 'good' : 'bad'}>{ready ? 'Ready' : 'Not ready'}</Status>} note={`Build ${overview.runtime.version || 'unknown'} (${overview.runtime.commit || 'unknown'})`} />
          <Fact label="Config" value={<>r{num(overview.runtime.configRevision)}</>} note={`Snapshot ${num(overview.runtime.snapshotVersion)}`} />
          <Fact label="Telemetry" value={<Status tone={overview.telemetry.degraded ? 'warn' : overview.telemetry.enabled ? 'good' : 'idle'}>{overview.telemetry.enabled ? (overview.telemetry.degraded ? 'Degraded' : 'Enabled') : 'Disabled'}</Status>} note={`${num(overview.telemetry.written)} written, ${num(overview.telemetry.lost)} lost, ${num(overview.telemetry.lostDiagnostics)} diagnostics lost`} />
          <Fact label="Last request" value={when(overview.traffic.lastRequestAt)} />
          {slow && <Fact label="Slowest of these" value={<>{num(slow.durationMs ?? 0)} <small>ms</small></>} note={`${slow.providerId} / ${slow.modelId}`} />}
          {fast && <Fact label="Fastest of these" value={<>{num(fast.durationMs ?? 0)} <small>ms</small></>} note={`${fast.providerId} / ${fast.modelId}`} />}
        </dl>
      </Margin>
    </Row>
  </div>
}
