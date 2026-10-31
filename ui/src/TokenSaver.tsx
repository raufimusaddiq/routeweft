import { useCallback, useEffect, useState } from 'react'
import { Status } from './editorial'
import { num } from './format'
import { helperReason, helperState, logState, newestLogs, statsRows, type HeadroomStatus, type PXPIPELog, type PXPIPEStats, type PXPIPEStatus } from './tokenSaverPanels'

type Settings = Record<string, string>
type Props = { onUnauthorized: () => void }

// Token-saver settings owned by the writable allowlist (PRD-XFORM-001/004/005).
const levels = [['lite', 'Lite'], ['full', 'Full'], ['ultra', 'Ultra']]

function truthy(value: string | undefined) {
  return value === 'true'
}

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

async function readJSON<T>(path: string, onUnauthorized: () => void, fallback: string): Promise<T> {
  const response = await fetch(path, { credentials: 'same-origin' })
  if (response.status === 401) { onUnauthorized(); throw new Error(fallback) }
  if (!response.ok) throw new Error(await responseError(response, fallback))
  return await response.json() as T
}

const messageOf = (cause: unknown, fallback: string) => cause instanceof Error ? cause.message : fallback

export function TokenSaver({ onUnauthorized }: Props) {
  const [settings, setSettings] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [draft, setDraft] = useState<Settings>({})
  // Read-only R09 diagnostics: the helpers' probes, PXPIPE's counters and its tail log.
  const [headroom, setHeadroom] = useState<HeadroomStatus | null>(null)
  const [pxpipe, setPxpipe] = useState<PXPIPEStatus | null>(null)
  const [stats, setStats] = useState<PXPIPEStats | null>(null)
  const [logs, setLogs] = useState<PXPIPELog[] | null>(null)
  const [helperError, setHelperError] = useState('')
  const [statsError, setStatsError] = useState('')
  const [logsError, setLogsError] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const response = await fetch('/admin/v1/settings', { credentials: 'same-origin' })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not load token-saver settings.'))
      const data = await response.json() as { settings: Settings }
      setSettings(data.settings)
      setDraft({})
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load token-saver settings.')
    } finally { setLoading(false) }
    // Diagnostics are optional data: a failed probe never blocks the settings page.
    const [headroomResult, pxpipeResult, statsResult, logsResult] = await Promise.allSettled([
      readJSON<HeadroomStatus>('/admin/v1/headroom/status', onUnauthorized, 'Could not load Headroom health.'),
      readJSON<PXPIPEStatus>('/admin/v1/pxpipe/status', onUnauthorized, 'Could not load PXPIPE health.'),
      readJSON<{ stats: PXPIPEStats }>('/admin/v1/pxpipe/stats?limit=1', onUnauthorized, 'Could not load PXPIPE stats.'),
      readJSON<{ logs: PXPIPELog[] }>('/admin/v1/pxpipe/logs?limit=50', onUnauthorized, 'Could not load PXPIPE logs.'),
    ])
    setHeadroom(headroomResult.status === 'fulfilled' ? headroomResult.value : null)
    setPxpipe(pxpipeResult.status === 'fulfilled' ? pxpipeResult.value : null)
    setStats(statsResult.status === 'fulfilled' ? statsResult.value.stats : null)
    setLogs(logsResult.status === 'fulfilled' ? logsResult.value.logs : null)
    setHelperError([headroomResult, pxpipeResult].filter(result => result.status === 'rejected').map(result => messageOf(result.reason, 'Helper health is unavailable.')).join(' '))
    setStatsError(statsResult.status === 'rejected' ? messageOf(statsResult.reason, 'PXPIPE stats are unavailable.') : '')
    setLogsError(logsResult.status === 'rejected' ? messageOf(logsResult.reason, 'PXPIPE logs are unavailable.') : '')
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  async function save(id: string, message: string, values: Settings) {
    setBusy(id); setError(''); setNotice('')
    try {
      const response = await fetch('/admin/v1/settings', { method: 'PATCH', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ set: values }) })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not save token-saver settings.'))
      setNotice(message)
      await load()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not save token-saver settings.')
    } finally { setBusy('') }
  }

  function toggle(key: string, message: string) {
    return () => void save(key, message, { [key]: String(!truthy(settings[key])) })
  }

  function value(key: string, fallback = '') {
    return draft[key] ?? settings[key] ?? fallback
  }

  function field(key: string, fallback = '') {
    return { value: value(key, fallback), onChange: (event: { target: { value: string } }) => setDraft(current => ({ ...current, [key]: event.target.value })) }
  }

  function dirty(key: string) {
    return draft[key] !== undefined && draft[key] !== settings[key]
  }

  function SaveButton({ id, setting, message }: { id: string; setting: string; message: string }) {
    return <button className="button-secondary" type="button" disabled={busy === id || !dirty(setting)} onClick={() => void save(id, message, { [setting]: draft[setting] })}>{busy === id ? 'Saving…' : 'Save'}</button>
  }

  function Toggle({ id, setting, label, description }: { id: string; setting: string; label: string; description: string }) {
    const on = truthy(settings[setting])
    return <div className="feature-row">
      <div className="feature-main"><strong>{label}</strong><small>{description}</small></div>
      <span className={`state-tag${on ? ' state-good' : ''}`}>{on ? 'Enabled' : 'Disabled'}</span>
      <button className="button-secondary" type="button" disabled={busy === id} onClick={toggle(setting, `${label} ${on ? 'disabled' : 'enabled'}.`)}>{busy === id ? 'Saving…' : on ? 'Disable' : 'Enable'}</button>
    </div>
  }

  const shownLogs = logs ? newestLogs(logs, 50) : null

  return <div className="token-saver-content" aria-busy={loading}>
    <div className="token-saver-toolbar accent-surface"><span className="count-label" role="status">{loading ? 'Loading…' : 'Prompt-efficiency pipeline in order: RTK → Headroom → Caveman → Ponytail → PXPIPE'}</span><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Reload</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    <section className="detail-panel token-saver-panel" aria-labelledby="features-title">
      <div className="section-heading"><div><h2 id="features-title">Features</h2><p>Each transform is fail-open: a failure never turns a valid request into an error.</p></div></div>
      <Toggle id="rtk" setting="rtkEnabled" label="RTK" description="Redundancy/token killer applied first." />
      <Toggle id="headroom" setting="headroomEnabled" label="Headroom" description="External context compression before Caveman/Ponytail." />
      <Toggle id="caveman" setting="cavemanEnabled" label="Caveman" description="Injects a terse-response policy at the configured level." />
      <Toggle id="ponytail" setting="ponytailEnabled" label="Ponytail" description="Injects an answer-shape policy at the configured level." />
      <Toggle id="pxpipe" setting="pxpipeEnabled" label="PXPIPE" description="Large/image-heavy context transform above the min-char threshold." />
    </section>

    <div className="token-saver-grid">
      <section className="detail-panel" aria-labelledby="caveman-title"><div className="section-heading"><div><h3 id="caveman-title">Caveman level</h3><p>Response terseness.</p></div></div><div className="setting-row"><select aria-label="Caveman level" {...field('cavemanLevel', 'full')}>{levels.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select><SaveButton id="cavemanLevel" setting="cavemanLevel" message="Caveman level saved." /></div></section>
      <section className="detail-panel" aria-labelledby="ponytail-title"><div className="section-heading"><div><h3 id="ponytail-title">Ponytail level</h3><p>Answer-shape strength.</p></div></div><div className="setting-row"><select aria-label="Ponytail level" {...field('ponytailLevel', 'full')}>{levels.map(([v, l]) => <option key={v} value={v}>{l}</option>)}</select><SaveButton id="ponytailLevel" setting="ponytailLevel" message="Ponytail level saved." /></div></section>

      <section className="detail-panel" aria-labelledby="headroom-title"><div className="section-heading"><div><h3 id="headroom-title">Headroom</h3><p>Endpoint, timeout and user-message compression.</p></div></div>
        <div className="setting-row"><label>URL<input type="url" aria-label="Headroom URL" placeholder="http://localhost:8787" {...field('headroomUrl')} /></label><SaveButton id="headroomUrl" setting="headroomUrl" message="Headroom URL saved." /></div>
        <div className="setting-row"><label>Timeout (ms)<input type="number" min="0" aria-label="Headroom timeout" {...field('headroomTimeoutMs', '3000')} /></label><SaveButton id="headroomTimeoutMs" setting="headroomTimeoutMs" message="Headroom timeout saved." /></div>
        <div className="feature-row"><div className="feature-main"><strong>Compress user messages</strong><small>When enabled, user-role messages are also compressed.</small></div><span className={`state-tag${truthy(settings.headroomCompressUserMessages) ? ' state-good' : ''}`}>{truthy(settings.headroomCompressUserMessages) ? 'On' : 'Off'}</span><button className="button-secondary" type="button" disabled={busy === 'headroomCompressUserMessages'} onClick={toggle('headroomCompressUserMessages', 'Headroom user-message compression updated.')}>{truthy(settings.headroomCompressUserMessages) ? 'Disable' : 'Enable'}</button></div>
      </section>

      <section className="detail-panel" aria-labelledby="pxpipe-title"><div className="section-heading"><div><h3 id="pxpipe-title">PXPIPE</h3><p>Threshold, timeout and auto-install policy.</p></div></div>
        <div className="setting-row"><label>Min characters<input type="number" min="0" aria-label="PXPIPE min chars" {...field('pxpipeMinChars', '25000')} /></label><SaveButton id="pxpipeMinChars" setting="pxpipeMinChars" message="PXPIPE threshold saved." /></div>
        <div className="setting-row"><label>Timeout (ms)<input type="number" min="0" aria-label="PXPIPE timeout" {...field('pxpipeTimeoutMs', '15000')} /></label><SaveButton id="pxpipeTimeoutMs" setting="pxpipeTimeoutMs" message="PXPIPE timeout saved." /></div>
        <div className="feature-row"><div className="feature-main"><strong>Auto-install</strong><small>Allow installing the PXPIPE helper when missing.</small></div><span className={`state-tag${truthy(settings.pxpipeAutoInstall) ? ' state-good' : ''}`}>{truthy(settings.pxpipeAutoInstall) ? 'On' : 'Off'}</span><button className="button-secondary" type="button" disabled={busy === 'pxpipeAutoInstall'} onClick={toggle('pxpipeAutoInstall', 'PXPIPE auto-install updated.')}>{truthy(settings.pxpipeAutoInstall) ? 'Disable' : 'Enable'}</button></div>
      </section>
    </div>

    <section className="detail-panel token-saver-panel" aria-labelledby="helper-health-title">
      <div className="section-heading"><div><h2 id="helper-health-title">Helper health</h2><p>Read-only probes of the configured helpers. Routeweft does not start, stop or install them.</p></div></div>
      {helperError && <p className="count-label" role="status">{helperError}</p>}
      {[{ name: 'Headroom', status: headroom }, { name: 'PXPIPE', status: pxpipe }].map(({ name, status }) => {
        if (!status) return <div className="helper-row" key={name}><div className="feature-main"><strong>{name}</strong><small>{helperError ? 'Status unavailable' : 'Probing the helper…'}</small></div></div>
        const state = helperState(status.health)
        return <div className="helper-row" key={name}>
          <div className="feature-main"><strong>{name}</strong><small>{status.enabled ? 'Enabled' : 'Disabled'} · {status.url || 'no URL configured'}</small></div>
          <Status tone={state.tone}>{state.label}</Status>
          <span className="helper-reason">{helperReason(status.health)}</span>
        </div>
      })}
    </section>

    <section className="detail-panel token-saver-panel" aria-labelledby="pxpipe-stats-title">
      <div className="section-heading"><div><h2 id="pxpipe-stats-title">PXPIPE stats</h2><p>Process-local counters since the helper pipeline started; a restart resets them.</p></div></div>
      {stats ? <dl className="helper-stats">{statsRows(stats).map(row => <div className="helper-stat" key={row.label}><dt>{row.label}</dt><dd>{num(row.value)}</dd></div>)}</dl> : <p className="count-label" role="status">{statsError || 'Loading…'}</p>}
    </section>

    <section className="detail-panel token-saver-panel" aria-labelledby="pxpipe-logs-title">
      <div className="section-heading"><div><h2 id="pxpipe-logs-title">PXPIPE logs</h2><p>The latest 50 attempts, newest first. The log is process-local and resets on restart.</p></div></div>
      {shownLogs ? shownLogs.length > 0 ? <ul className="helper-log">{shownLogs.map((entry, index) => {
        const state = logState(entry)
        return <li className="helper-log-row" key={`${entry.status}-${index}`}>
          <Status tone={state.tone}>{state.label}</Status>
          <span className="technical-value helper-log-chars" aria-label={`Characters ${entry.inputChars} in, ${entry.outputChars} out`}>{num(entry.inputChars)} → {num(entry.outputChars)}</span>
          <span className="count-label">{num(entry.durationMs)} ms</span>
          {entry.error && <p className="helper-log-error">{entry.error}</p>}
        </li>
      })}</ul> : <p className="count-label">No PXPIPE attempts since the process started.</p> : <p className="count-label" role="status">{logsError || 'Loading…'}</p>}
    </section>

    <p className="count-label token-saver-footnote">Managed status/start/stop/restart for Headroom and PXPIPE applies when Routeweft owns the service runtime; this deployment configures endpoints only.</p>
  </div>
}
