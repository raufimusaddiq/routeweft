import { useCallback, useEffect, useState } from 'react'

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

function Metric({ label, value, note, icon }: { label: string; value: string; note: string; icon: string }) {
  return (
    <section className="metric" aria-label={label}>
      <div className="metric-label"><span className="material-symbols" aria-hidden="true">{icon}</span>{label}</div>
      <p className="metric-value">{value}</p>
      <p className="metric-note">{note}</p>
    </section>
  )
}

export function Overview({ onUnauthorized }: Props) {
  const [summary, setSummary] = useState<Summary | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [updatedAt, setUpdatedAt] = useState('')

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
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return
      setError(cause instanceof Error ? cause.message : 'Overview is temporarily unavailable. Try again.')
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [onUnauthorized])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  if (loading && !summary) return <p className="page-status" role="status">Loading overview…</p>
  if (error && !summary) {
    return (
      <section className="page-error" role="alert">
        <span className="material-symbols" aria-hidden="true">error</span>
        <p>{error}</p>
        <button className="button-secondary" type="button" onClick={() => void load()}>Retry</button>
      </section>
    )
  }
  if (!summary) return null

  const ready = summary.runtime.health === 'ready'
  return (
    <div className="overview-content" aria-busy={loading}>
      <div className="overview-toolbar">
        <p className="updated-at" aria-live="polite">Updated {updatedAt}</p>
        <button className="button-secondary refresh-button" type="button" disabled={loading} onClick={() => void load()}>
          <span className="material-symbols" aria-hidden="true">refresh</span>
          Refresh
        </button>
      </div>
      {error && (
        <section className="page-error refresh-error" role="alert">
          <span className="material-symbols" aria-hidden="true">error</span>
          <p>{error}</p>
          <button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Retry</button>
        </section>
      )}

      <section className="metrics-grid" aria-label="Gateway summary">
        <Metric label="Gateway" value={ready ? 'Ready' : 'Not ready'} note={`Version ${summary.runtime.version || 'unknown'}`} icon="dns" />
        <Metric label="Enabled connections" value={count(summary.providers.enabledConnections)} note={`${count(summary.providers.nodes)} provider nodes · ${count(summary.providers.catalog)} in catalog`} icon="hub" />
        <Metric label="Active API keys" value={count(summary.apiKeys.active)} note="Enabled and unpaused" icon="key" />
        <Metric label="Requests recorded" value={count(summary.traffic.requests)} note={`${count(summary.traffic.active)} active now`} icon="monitoring" />
      </section>

      {(summary.traffic.errors > 0 || summary.telemetry.degraded) && (
        <section className="actionable-alert" aria-labelledby="actionable-title">
          <span className="material-symbols" aria-hidden="true">warning</span>
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
