import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { BarChart, EditorialSkeleton, EmptyState, ErrorNotice, Sparkline, StackedBar } from './kit'
import { Caption, ErrCell, Fact, Main, Margin, Row, Statement, Status } from './editorial'
import { RequestDetails } from './RequestDetails'
import { UsageLogs } from './UsageLogs'
import { UsageTopology } from './UsageTopology'
import { badRate, compact, money, moneyFine, num, outlier, pct, failureStatuses, largest, latestWeekTotal, observeSeries, observedAverage, periodDays, providerTone, rate, sameDayLastWeek, share, statusTone, utcDay, weekOverWeek, type Summary } from './format'

type Props = { onUnauthorized: () => void }

const periods = [['today', 'Today (UTC)'], ['24h', 'Last 24 hours'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days'], ['60d', 'Last 60 days'], ['all', 'All time']]
// The window a headline sentence names. 'today' is the UTC calendar day, the one the daily series uses.
const windowPhrase: Record<string, string> = { today: 'so far today (UTC)', '24h': 'in the last 24 hours', '7d': 'in the last 7 days', '30d': 'in the last 30 days', '60d': 'in the last 60 days', all: 'in the recorded history' }
const tabs = [['overview', 'Overview'], ['logs', 'Requests'], ['details', 'Request details']] as const
type Tab = (typeof tabs)[number][0]

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

export function Usage({ onUnauthorized }: Props) {
  const [period, setPeriod] = useState('7d')
  const [tab, setTab] = useState<Tab>('overview')
  const [requestId, setRequestId] = useState('')
  const [summary, setSummary] = useState<Summary | null>(null)
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
      const response = await fetch(`/admin/v1/usage/summary?period=${period}`, { credentials: 'same-origin', signal })
      if (signal.aborted) return
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not load usage.'))
      const summaryData = await response.json() as Summary
      if (signal.aborted) return
      setSummary(summaryData)
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
  const days = Math.max(1, period === '24h' || period === 'today' ? 1 : recordedDays || periodDays(period, raw, utcDay()))
  const dailyAverage = observedAverage(observed)
  const averageNote = recordedDays < n ? ` over the ${recordedDays} recorded days` : ''
  const points = series.map(day => ({ label: day.day, short: day.day.slice(5), value: day.requests }))
  const wow = weekOverWeek(observed)
  const ghost = sameDayLastWeek(observed)
  const thisWeek = latestWeekTotal(observed)
  // "The latest 7 days" is only a true label when the window has 7 days and is not the 24-hour period.
  const showWeek = period !== '24h' && period !== 'today' && n >= 7
  const tokens = t ? t.inputTokens + t.outputTokens : 0
  const hit = t ? (t.cacheReadTokens / Math.max(1, t.inputTokens + t.cacheReadTokens)) * 100 : 0
  const fleet = t ? rate(t.errors, t.requests) : 0
  const failures = summary ? failureStatuses(summary.statuses) : []
  const topFail = failures[0]
  const worst = summary ? outlier(summary) : null
  const topProvider = largest(summary?.providers.map(item => item.requests) ?? [])

  const periodLabel = periods.find(([value]) => value === period)?.[1] ?? period
  const openRequest = (id: string) => { setRequestId(id); setTab('details') }
  // Roving tabindex: arrows, Home and End move between tabs, as the tabs pattern expects.
  const onTabKey = (event: KeyboardEvent) => {
    const index = tabs.findIndex(([value]) => value === tab)
    const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1
    if (next < 0) return
    event.preventDefault()
    setTab(tabs[next][0])
    document.getElementById(`usage-tab-${tabs[next][0]}`)?.focus()
  }

  return <div className="usage-content page-d" aria-busy={loading}>
    <div className="usage-tabs" role="tablist" aria-label="Usage views" onKeyDown={onTabKey}>
      {tabs.map(([value, label]) => <button key={value} id={`usage-tab-${value}`} className="usage-tab button-ghost" type="button" role="tab" aria-selected={tab === value} aria-controls="usage-panel" tabIndex={tab === value ? 0 : -1} onClick={() => setTab(value)}>{label}</button>)}
    </div>

    <div id="usage-panel" role="tabpanel" aria-labelledby={`usage-tab-${tab}`}>
    {tab !== 'details' && <Row gap="s" label="Period">
      <div className="ed-main">
        {tab === 'overview' && (summary && t
          ? <><p className="ed-headline"><strong>{compact(tokens)}</strong> tokens, <strong>{num(t.requests)}</strong> requests</p>
            <p className="ed-subline">{windowPhrase[period]}. {pct(hit, 0)} of input came from cache{t.avgDurationMs !== undefined ? <>; the average call took <span className="ed-nowrap">{num(t.avgDurationMs)} ms.</span></> : '.'}</p></>
          : !first && !error && <p className="ed-subline">No usage to show yet.</p>)}
        {tab === 'logs' && <p className="ed-subline">Every request in the window, newest first. Pick one to see what was recorded for it.</p>}
      </div>
      <Margin>
        <div className="ed-period usage-toolbar"><label htmlFor="usage-period" className="ed-label">Time period</label>
          <select id="usage-period" value={period} onChange={event => setPeriod(event.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
          {tab === 'overview' && <><span className="count-label" role="status">{loading ? 'Loading…' : summary ? `${num(summary.totals.requests)} requests${summary.since ? ` since ${summary.since.slice(0, 10)}` : ''}` : ''}</span>
          <button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></>}</div>
      </Margin>
    </Row>}

    {tab === 'overview' && first && !error && <div role="status" aria-label="Loading usage"><EditorialSkeleton /></div>}
    {tab === 'overview' && error && <ErrorNotice message={error} busy={loading} onRetry={() => void load()} />}

    {tab === 'logs' && <UsageLogs period={period} periodLabel={periodLabel} onUnauthorized={onUnauthorized} onOpen={openRequest} />}
    {tab === 'details' && <RequestDetails requestId={requestId} onSelect={setRequestId} onUnauthorized={onUnauthorized} />}

    {tab === 'overview' && summary && t && <>
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
            <Fact label="Cost" value={<>{t.cost === undefined ? '—' : `~${money(t.cost)}`} <small>est.</small></>} note="Priced from the model catalog, not a bill" />
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
                  <td className="num ed-big">{num(item.requests)}{item.cost !== undefined && <><br /><small className="ed-quiet">{money(item.cost)}</small></>}</td>
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
            : <ul className="ed-watch ed-models">{summary.models.slice(0, 6).map(model => <li key={model.key}><code>{model.key}</code><span className="num">{num(model.requests)} <small>req</small> · {compact(model.inputTokens + model.outputTokens)} <small>tok</small>{model.cost !== undefined ? <> · {money(model.cost)}</> : null}</span></li>)}</ul>}
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

    {tab === 'overview' && summary && <UsageTopology providers={summary.providers} onUnauthorized={onUnauthorized} />}

    {tab === 'overview' && summary && n > 1 && <Row gap="m" label="Daily trend">
      <Main title="Trend">
        <figure className="ed-figure"><p className="ed-subline">{compact(tokens / days)} tokens and {pct(hit, 0)} cache hit a day, on average, over the {summary.period === 'all' ? 'recorded history' : periodLabel.toLowerCase()}.</p>
          <figcaption><Caption kicker="Note">Individual requests are on the Requests tab.</Caption></figcaption></figure>
      </Main>
      <Margin>
        <figure className="ed-mini"><figcaption className="ed-label">Tokens per day</figcaption><Sparkline values={series.map(day => day.inputTokens + day.outputTokens)} label="Tokens per day" caption={`${compact(tokens / days)} /day`} /></figure>
        <figure className="ed-mini"><figcaption className="ed-label">Cache tokens per day</figcaption><Sparkline values={series.map(day => day.cacheReadTokens + day.cacheWriteTokens)} label="Cache tokens per day" caption={`${pct(hit, 0)} hit`} /></figure>
        <figure className="ed-mini"><figcaption className="ed-label">Cost per day</figcaption><Sparkline values={series.map(day => day.cost ?? 0)} label="Estimated cost per day" caption={`${moneyFine(summary.totals.cost)} est.`} /></figure>
      </Margin>
    </Row>}
    </div>
  </div>
}
