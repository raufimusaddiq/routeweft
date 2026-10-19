import { useCallback, useEffect, useRef, useState } from 'react'
import { BarChart, EditorialSkeleton, EmptyState, ErrorNotice, Sparkline, StackedBar } from './kit'
import { Caption, ErrCell, Fact, Main, Margin, Row, Statement, Status } from './editorial'
import { badRate, compact, num, outlier, pct, failureStatuses, largest, latestWeekTotal, observeSeries, observedAverage, periodDays, providerTone, rate, sameDayLastWeek, share, statusTone, utcDay, weekOverWeek, type Summary, type UsageEvent } from './format'

type Props = { onUnauthorized: () => void }

const periods = [['24h', 'Last 24 hours'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days'], ['all', 'All time']]
const words: Record<string, string> = { '24h': '24 hours', '7d': '7 days', '30d': '30 days', all: 'recorded history' }

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

export function Usage({ onUnauthorized }: Props) {
  const [period, setPeriod] = useState('7d')
  const [summary, setSummary] = useState<Summary | null>(null)
  const [events, setEvents] = useState<UsageEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)

  // Each load aborts the one before it, so the summary on screen always belongs to the period that is selected.
  const load = useCallback(async () => {
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    const { signal } = abort
    setLoading(true)
    setError('')
    try {
      const responses = await Promise.all([
        fetch(`/admin/v1/usage/summary?period=${period}`, { credentials: 'same-origin', signal }),
        fetch('/admin/v1/usage?page=1&pageSize=50', { credentials: 'same-origin', signal }),
      ])
      if (signal.aborted) return
      if (responses.some(response => response.status === 401)) { onUnauthorized(); return }
      const failed = responses.find(response => !response.ok)
      if (failed) throw new Error(await responseError(failed, 'Could not load usage.'))
      const [summaryData, eventData] = await Promise.all(responses.map(response => response.json())) as [Summary, { items: UsageEvent[] }]
      if (signal.aborted) return
      setSummary(summaryData)
      setEvents(eventData.items)
    } catch (cause) {
      if (signal.aborted || (cause instanceof DOMException && cause.name === 'AbortError')) return
      setError(cause instanceof Error ? cause.message : 'Could not load usage.')
    } finally { if (!signal.aborted) setLoading(false) }
  }, [period, onUnauthorized])

  useEffect(() => { void load(); return () => controller.current?.abort() }, [load])

  const first = loading && !summary
  const t = summary?.totals
  // The drawn window is zero-filled for a continuous axis; every comparison below sees only observed days (see observeSeries).
  const raw = summary?.series ?? []
  const observed = observeSeries(raw, period === '24h' ? 2 : periodDays(period, raw, utcDay()), utcDay())
  const series = observed.drawn
  const n = series.length
  const recordedDays = observed.days.length
  const days = Math.max(1, period === '24h' ? 1 : recordedDays || (period === '7d' ? 7 : 30))
  const dailyAverage = observedAverage(observed)
  const averageNote = recordedDays < n ? ` over the ${recordedDays} recorded days` : ''
  const points = series.map(day => ({ label: day.day, short: day.day.slice(5), value: day.requests }))
  const wow = weekOverWeek(observed)
  const ghost = sameDayLastWeek(observed)
  const thisWeek = latestWeekTotal(observed)
  // "The latest 7 days" is only a true label when the window has 7 days and is not the 24-hour period.
  const showWeek = period !== '24h' && n >= 7
  const tokens = t ? t.inputTokens + t.outputTokens : 0
  const hit = t ? (t.cacheReadTokens / Math.max(1, t.inputTokens + t.cacheReadTokens)) * 100 : 0
  const fleet = t ? rate(t.errors, t.requests) : 0
  const failures = summary ? failureStatuses(summary.statuses) : []
  const topFail = failures[0]
  const worst = summary ? outlier(summary) : null
  const topProvider = largest(summary?.providers.map(item => item.requests) ?? [])

  return <div className="usage-content" aria-busy={loading}>
    {first && !error && <div role="status" aria-label="Loading usage"><EditorialSkeleton /></div>}
    {error && <ErrorNotice message={error} busy={loading} onRetry={() => void load()} />}

    <Row gap="s" label="Period">
      <div className="ed-main">
        {summary && t
          ? <><p className="ed-headline"><strong>{compact(tokens)}</strong> tokens, <strong>{num(t.requests)}</strong> requests</p>
            <p className="ed-subline">in the {period === 'all' ? words.all : `last ${words[period]}`}. {pct(hit, 0)} of input came from cache{t.avgDurationMs !== undefined ? <>; the average call took <span className="ed-nowrap">{num(t.avgDurationMs)} ms.</span></> : '.'}</p></>
          : !first && <p className="ed-subline">No usage to show yet.</p>}
      </div>
      <Margin>
        <div className="ed-period usage-toolbar"><label htmlFor="usage-period" className="ed-label">Time period</label>
          <select id="usage-period" value={period} onChange={event => setPeriod(event.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
          <span className="count-label" role="status">{loading ? 'Loading…' : summary ? `${num(summary.totals.requests)} requests${summary.since ? ` since ${summary.since.slice(0, 10)}` : ''}` : ''}</span>
          <button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
      </Margin>
    </Row>

    {summary && t && <>
      <Row gap="s" label="Requests per day">
        <Main title="Requests per day">
          {points.length === 0
            ? <EmptyState reason="No attributed usage in this period.">{period !== 'all' && <button className="button-secondary" type="button" onClick={() => setPeriod('all')}>Show all time</button>}</EmptyState>
            : <figure className="ed-figure usage-chart-panel accent-surface ed-plate">
              <BarChart points={points} unit="requests" title="Requests per day" current={wow ? (index => index >= n - 7) : undefined} ghost={wow ? ghost : undefined} reference={n > 1 ? { value: dailyAverage } : undefined} />
              <figcaption><Caption kicker="Fig. 1">{wow ? `Cyan bars are the latest 7 days; each tick is the same day a week earlier. Dashed line: average ${num(dailyAverage)} successful requests a day${averageNote}.` : 'One bar per day.'}</Caption></figcaption>
            </figure>}
        </Main>
        <Margin>
          <dl className="ed-facts">
            <Fact label="Requests" value={<>{num(t.requests)} <small>req</small></>} note={wow ? `${num(wow.thisWeek)} successful in the latest 7 days, ${wow.change === null ? 'no prior week' : `${wow.change >= 0 ? '↑' : '↓'} ${Math.abs(wow.change).toFixed(1)}% vs prior 7 d`}` : showWeek ? `${num(thisWeek)} successful in the latest 7 days, ${num(t.requests / days)} requests a day on average` : `avg ${num(t.requests / days)} a day`} />
            <Fact label="Tokens" value={<>{compact(tokens)} <small>in + out</small></>} note={`input ${num(t.inputTokens)}, output ${num(t.outputTokens)}`} />
            <Fact label="Cache" value={<>{pct(hit, 0)} <small>of input</small></>} note={`read ${num(t.cacheReadTokens)}, write ${num(t.cacheWriteTokens)}`} />
            <Fact label="Latency" value={<>{t.avgDurationMs !== undefined ? num(t.avgDurationMs) : '—'} <small>ms avg</small></>} note={`TTFT ${t.avgTtftMs !== undefined ? `${num(t.avgTtftMs)} ms` : '—'}`} />
          </dl>
        </Margin>
      </Row>

      {topFail && t.errors > 0 && <Statement>
        <strong>{topFail.key}</strong> causes <strong>{Math.round((topFail.requests / Math.max(1, t.errors)) * 100)}%</strong> of failures.{worst && badRate(worst.errors, worst.requests, fleet) ? <> <span className="ed-quiet">{worst.key} is the weakest provider at {pct(worst.err)}.</span></> : null}
      </Statement>}

      <Row gap="s" label="Providers and models">
        <Main title="Providers">
          {summary.providers.length === 0
            ? <EmptyState reason="No provider activity in this period." />
            : <figure className="ed-figure">
              <table className="ed-table prov-table"><thead><tr><th scope="col">Provider</th><th scope="col" className="num">Requests</th><th scope="col"><span className="sr-only">Share</span></th><th scope="col" className="num">Errors</th></tr></thead>
                <tbody>{summary.providers.slice(0, 8).map(item => <tr key={item.key}>
                  <td><Status tone={providerTone(item.errors, item.requests, fleet)}><span className="ed-name">{item.key}</span></Status></td>
                  <td className="num ed-big">{num(item.requests)}</td>
                  <td className="share-cell"><span className="ed-bar"><span style={{ width: `${share(item.requests, topProvider)}%` }} /></span></td>
                  <ErrCell errors={item.errors} requests={item.requests} bad={badRate(item.errors, item.requests, fleet)} />
                </tr>)}</tbody></table>
              <figcaption><Caption kicker="Table 1">By requests; the error bar scales to 10%.</Caption></figcaption>
            </figure>}
        </Main>
        <Margin>
          <h3 className="ed-h3">Models</h3>
          {summary.models.length === 0
            ? <p className="ed-now-note">No model activity in this period.</p>
            : <ul className="ed-watch ed-models">{summary.models.slice(0, 6).map(model => <li key={model.key}><code>{model.key}</code><span className="num">{num(model.requests)} <small>req</small> · {compact(model.inputTokens + model.outputTokens)} <small>tok</small></span></li>)}</ul>}
        </Margin>
      </Row>

      <Row gap="l" label="Outcomes">
        <Main title="Outcomes">
          {summary.statuses.length === 0
            ? <EmptyState reason="No responses in this period." />
            : <figure className="ed-figure">
              <StackedBar title="Response status counts" segments={summary.statuses.map(item => ({ key: item.key, label: item.key, value: item.requests, tone: statusTone(item.key) }))} />
              <figcaption><Caption kicker="Fig. 2">Responses by status code. A failure is any response that is not a 2xx, redirects included.</Caption></figcaption>
            </figure>}
        </Main>
        <Margin>
          <dl className="ed-facts"><Fact label="Failed" value={pct(fleet)} note={`${num(t.errors)} of ${num(t.requests)} responses`} /></dl>
        </Margin>
      </Row>
    </>}

    {!first && (!error || summary) && <Row gap="m" label="Recent requests">
      <Main title="Recent requests">
        {events.length === 0
          ? <EmptyState reason="No requests recorded yet. Send a request through the gateway and it will appear here." />
          : <figure className="ed-figure">
            <table className="ed-table usage-table log-table"><thead><tr><th scope="col">Route</th><th scope="col" className="num">Status</th><th scope="col" className="num">In</th><th scope="col" className="num">Out</th><th scope="col" className="num">Cache</th><th scope="col" className="num">Latency</th><th scope="col" className="num">Time</th></tr></thead>
              <tbody>{events.slice(0, 9).map(event => <tr className="usage-event" key={event.id}>
                <td className="log-route">{event.providerId || '—'} / {event.modelId || '—'}</td>
                <td className="num log-status"><Status tone={statusTone(event.status)}>{event.status}</Status></td>
                <td className="num log-tokens">{num(event.inputTokens)} in</td>
                <td className="num log-out">{num(event.outputTokens)} out</td>
                <td className="num log-cache">{event.cacheReadTokens + event.cacheWriteTokens > 0 ? `${num(event.cacheReadTokens + event.cacheWriteTokens)} cache` : '—'}</td>
                <td className="num log-latency">{event.durationMs !== undefined ? `${event.durationMs} ms` : '—'}{event.ttftMs !== undefined ? ` · ${event.ttftMs}` : ''}</td>
                <td className="num log-time">{new Date(event.createdAt).toLocaleString()}</td>
              </tr>)}</tbody></table>
            <figcaption><Caption kicker="Log">Latest {Math.min(9, events.length)} of {events.length}, newest first, with provider, model and latency.</Caption></figcaption>
          </figure>}
      </Main>
      {summary && n > 1 && <Margin>
        <figure className="ed-mini"><figcaption className="ed-label">Tokens per day</figcaption><Sparkline values={series.map(day => day.inputTokens + day.outputTokens)} label="Tokens per day" caption={`${compact(tokens / days)} /day`} /></figure>
        <figure className="ed-mini"><figcaption className="ed-label">Cache tokens per day</figcaption><Sparkline values={series.map(day => day.cacheReadTokens + day.cacheWriteTokens)} label="Cache tokens per day" caption={`${pct(hit, 0)} hit`} /></figure>
      </Margin>}
    </Row>}
  </div>
}
