import { useCallback, useEffect, useState } from 'react'
import { ErrorNotice, MetricsSkeleton, Skeleton, SkeletonRows, Sparkline } from './kit'

type Summary = {
  runtime: { health: string; version: string; commit: string; configRevision: number; snapshotVersion: number }
  providers: { catalog: number; nodes: number; enabledConnections: number }
  apiKeys: { active: number }
  traffic: { active: number; requests: number; errors: number; lastRequestAt: string }
  telemetry: { enabled: boolean; written: number; lost: number; lostDiagnostics: number; degraded: boolean }
}

type Props = { onUnauthorized: () => void }

const numberFormat = new Intl.NumberFormat()

function count(value: number) {
  return numberFormat.format(value)
}

function when(value: string) {
  if (!value) return 'No requests recorded'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function Metric({ label, value, note, trend, accent }: { label: string; value: string; note: string; trend?: number[]; accent?: boolean }) {
  return (
    <section className={`metric${accent ? ' accent-surface' : ''}`} aria-label={label}>
      <div className="metric-label">{label}</div>
      <p className="metric-value">{value}</p>
      <p className="metric-note">{note}</p>
      {trend && <Sparkline values={trend} label={`${label} per day`} />}
    </section>
  )
}

export function Overview({ onUnauthorized }: Props) {
  const [summary, setSummary] = useState<Summary | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [updatedAt, setUpdatedAt] = useState('')
  const [trend, setTrend] = useState<number[]>([])

  // The trend is decoration for the request count; a failure here must not fail the page.
  const loadTrend = useCallback(async (signal?: AbortSignal) => {
    try {
      const usage = await fetch('/admin/v1/usage/summary?period=7d', { credentials: 'same-origin', signal })
      if (usage.ok) setTrend(((await usage.json()) as { series?: { requests: number }[] }).series?.map(day => day.requests) ?? [])
    } catch { /* keep the previous trend */ }
  }, [])

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    setError('')
    try {
      const response = await fetch('/admin/v1/overview', { credentials: 'same-origin', signal })
      if (response.status === 401) {
        onUnauthorized()
        return
      }
      if (!response.ok) throw new Error('Overview is temporarily unavailable. Try again.')
      const value = await response.json() as Summary
      setSummary(value)
      setUpdatedAt(new Date().toLocaleTimeString())
      void loadTrend(signal)
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return
      setError(cause instanceof Error ? cause.message : 'Overview is temporarily unavailable. Try again.')
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [onUnauthorized, loadTrend])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  if (loading && !summary) {
    return (
      <div className="overview-content skeleton-page" role="status" aria-label="Loading overview">
        <MetricsSkeleton />
        <div className="detail-grid">{[0, 1, 2].map(index => <section className="detail-panel" key={index}><Skeleton width="40%" height={10} /><SkeletonRows rows={3} columns={2} /></section>)}</div>
      </div>
    )
  }
  if (error && !summary) return <ErrorNotice message={error} onRetry={() => void load()} />
  if (!summary) return null

  const ready = summary.runtime.health === 'ready'
  return (
    <div className="overview-content" aria-busy={loading}>
      <div className="overview-toolbar">
        <p className="updated-at" aria-live="polite">Updated {updatedAt}</p>
        <button className="button-secondary refresh-button" type="button" disabled={loading} onClick={() => void load()}>
          Refresh
        </button>
      </div>
      {error && (
        <ErrorNotice className="refresh-error" message={error} busy={loading} onRetry={() => void load()} />
      )}

      <section className="metrics-grid" aria-label="Gateway summary">
        <Metric accent label="Gateway" value={ready ? 'Ready' : 'Not ready'} note={`Version ${summary.runtime.version || 'unknown'}`} />
        <Metric label="Enabled connections" value={count(summary.providers.enabledConnections)} note={`${count(summary.providers.nodes)} provider nodes · ${count(summary.providers.catalog)} in catalog`} />
        <Metric label="Active API keys" value={count(summary.apiKeys.active)} note="Enabled and unpaused" />
        <Metric label="Requests recorded" value={count(summary.traffic.requests)} note={`${count(summary.traffic.active)} active now`} trend={trend} />
      </section>

      {(summary.traffic.errors > 0 || summary.telemetry.degraded) && (
        <section className="actionable-alert" aria-labelledby="actionable-title">
                    <div>
            <h2 id="actionable-title">Needs attention</h2>
            {summary.traffic.errors > 0 && (
              <p>{count(summary.traffic.errors)} failed requests recorded. <a href="#usage">Review Usage</a>.</p>
            )}
            {summary.telemetry.degraded && <p>Usage telemetry is degraded; some accounting may be delayed or incomplete.</p>}
          </div>
        </section>
      )}

      <div className="detail-grid">
        <section className="detail-panel" aria-labelledby="runtime-title">
          <div className="panel-heading"><h2 id="runtime-title">Runtime</h2><span className={`state-tag${ready ? ' state-good' : ' state-warn'}`}>{ready ? 'Healthy' : 'Check readiness'}</span></div>
          <dl className="detail-list">
            <div><dt>Health</dt><dd>{ready ? 'Ready' : 'Not ready'}</dd></div>
            <div><dt>Build</dt><dd className="technical-value">{summary.runtime.version || 'unknown'} <span>({summary.runtime.commit || 'unknown'})</span></dd></div>
            <div><dt>Config revision</dt><dd className="technical-value">{count(summary.runtime.configRevision)}</dd></div>
            <div><dt>Snapshot</dt><dd className="technical-value">{count(summary.runtime.snapshotVersion)}</dd></div>
          </dl>
        </section>
        <section className="detail-panel" aria-labelledby="activity-title">
          <div className="panel-heading"><h2 id="activity-title">Recent activity</h2><span className="state-tag">{count(summary.traffic.active)} active</span></div>
          <dl className="detail-list">
            <div><dt>Total requests</dt><dd className="technical-value">{count(summary.traffic.requests)}</dd></div>
            <div><dt>Failed requests</dt><dd className="technical-value">{count(summary.traffic.errors)}</dd></div>
            <div><dt>Last request</dt><dd>{when(summary.traffic.lastRequestAt)}</dd></div>
          </dl>
        </section>
        <section className="detail-panel" aria-labelledby="telemetry-title">
          <div className="panel-heading"><h2 id="telemetry-title">Usage telemetry</h2><span className={`state-tag${summary.telemetry.degraded ? ' state-warn' : ''}`}>{summary.telemetry.enabled ? (summary.telemetry.degraded ? 'Degraded' : 'Enabled') : 'Disabled'}</span></div>
          <dl className="detail-list">
            <div><dt>Events written</dt><dd className="technical-value">{count(summary.telemetry.written)}</dd></div>
            <div><dt>Events lost</dt><dd className="technical-value">{count(summary.telemetry.lost)}</dd></div>
            <div><dt>Diagnostics lost</dt><dd className="technical-value">{count(summary.telemetry.lostDiagnostics)}</dd></div>
          </dl>
        </section>
      </div>
    </div>
  )
}
