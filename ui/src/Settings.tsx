import { useCallback, useEffect, useState, type FormEvent } from 'react'

type Values = Record<string, string>
type Props = { onUnauthorized: () => void }
type BackupCheck = { valid: boolean; schemaVersion: number; configRevision: number }

const providerStrategies = ['fill-first', 'round-robin', 'sticky-round-robin']
const comboStrategies = ['fallback', 'round-robin', 'sticky-round-robin']

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

function truthy(value: string | undefined) {
  return value === 'true'
}

function formatJSON(value: string | undefined, fallback: string) {
  const raw = value || fallback
  try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return raw }
}

export function Settings({ onUnauthorized }: Props) {
  const [settings, setSettings] = useState<Values>({})
  const [writable, setWritable] = useState<string[]>([])
  const [draft, setDraft] = useState<Values>({})
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [restoreFile, setRestoreFile] = useState<File | null>(null)
  const [restoreCheck, setRestoreCheck] = useState<BackupCheck | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const response = await fetch('/admin/v1/settings', { credentials: 'same-origin' })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not load settings.'))
      const data = await response.json() as { settings: Values; writable: string[] }
      setSettings(data.settings)
      setWritable(data.writable)
      setDraft({})
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load settings.')
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  async function save(values: Values, message: string) {
    setBusy('settings'); setError(''); setNotice('')
    try {
      const response = await fetch('/admin/v1/settings', { method: 'PATCH', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ set: values }) })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not save settings.'))
      setNotice(message)
      await load()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not save settings.')
    } finally { setBusy('') }
  }

  function value(key: string, fallback = '') {
    const raw = draft[key] ?? settings[key] ?? fallback
    return key === 'outboundProxyUrl' && raw === '[redacted]' && draft[key] === undefined ? '' : raw
  }

  function field(key: string, fallback = '') {
    return { value: value(key, fallback), onChange: (event: { target: { value: string } }) => setDraft(current => ({ ...current, [key]: event.target.value })) }
  }

  function dirty(key: string) {
    return draft[key] !== undefined && draft[key] !== settings[key]
  }

  function SaveButton({ setting, message }: { setting: string; message: string }) {
    async function saveValue() {
      const next = draft[setting]
      const numeric = ['stickyRoundRobinLimit', 'comboStickyRoundRobinLimit', 'observabilityMaxRecords', 'observabilityBatchSize', 'observabilityFlushIntervalMs', 'observabilityMaxJsonSize']
      const minimum = setting === 'stickyRoundRobinLimit' || setting === 'comboStickyRoundRobinLimit' ? 0 : 1
      if (numeric.includes(setting) && (!/^\d+$/.test(next) || !Number.isSafeInteger(Number(next)) || Number(next) < minimum)) {
        setError(`${setting} must be a whole number of at least ${minimum}.`)
        return
      }
      if (setting === 'outboundProxyUrl' && next) {
        try {
          const url = new URL(next)
          if (url.protocol !== 'http:' && url.protocol !== 'https:') throw new Error()
        } catch {
          setError('Proxy URL must be a valid HTTP or HTTPS URL.')
          return
        }
      }
      await save({ [setting]: next }, message)
    }
    return <button className="button-secondary" type="button" disabled={loading || busy !== '' || !writable.includes(setting) || !dirty(setting)} onClick={() => void saveValue()}>Save</button>
  }

  function Toggle({ setting, label, description }: { setting: string; label: string; description: string }) {
    const enabled = truthy(settings[setting])
    return <div className="setting-toggle"><div><strong>{label}</strong><small>{description}</small></div><span className={`state-tag${enabled ? ' state-good' : ''}`}>{enabled ? 'Enabled' : 'Disabled'}</span><button className="button-secondary" type="button" disabled={loading || busy !== '' || !writable.includes(setting)} onClick={() => void save({ [setting]: String(!enabled) }, `${label} updated.`)}>{enabled ? 'Disable' : 'Enable'}</button></div>
  }

  function NumberField({ setting, label, fallback, min = 1 }: { setting: string; label: string; fallback: string; min?: number }) {
    return <div className="settings-field"><label htmlFor={`setting-${setting}`}>{label}<input id={`setting-${setting}`} type="number" min={min} step="1" disabled={loading || busy !== '' || !writable.includes(setting)} {...field(setting, fallback)} /></label><SaveButton setting={setting} message={`${label} saved.`} /></div>
  }

  function JSONEditor({ setting, label, description, kind = 'object' }: { setting: string; label: string; description: string; kind?: 'object' | 'string-array' }) {
    const raw = value(setting, kind === 'object' ? '{}' : '[]')
    const display = draft[setting] ?? formatJSON(raw, kind === 'object' ? '{}' : '[]')
    function saveJSON() {
      try {
        const parsed: unknown = JSON.parse(display)
        if (kind === 'string-array' && (!Array.isArray(parsed) || parsed.some(item => typeof item !== 'string'))) throw new Error('Enter a JSON array of strings.')
        if (kind === 'object' && (!parsed || Array.isArray(parsed) || typeof parsed !== 'object')) throw new Error('Enter a JSON object.')
        if (setting === 'providerStrategies' && Object.values(parsed as Record<string, unknown>).some(item => typeof item !== 'string' || !providerStrategies.includes(item))) throw new Error(`Provider strategies must be one of: ${providerStrategies.join(', ')}.`)
        if (setting === 'comboStrategies' && Object.values(parsed as Record<string, unknown>).some(item => typeof item !== 'string' || !comboStrategies.includes(item))) throw new Error(`Combo strategies must be one of: ${comboStrategies.join(', ')}.`)
        void save({ [setting]: JSON.stringify(parsed) }, `${label} saved.`)
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : 'Enter valid JSON.')
      }
    }
    return <div className="settings-json-field"><label htmlFor={`setting-${setting}`}>{label}</label><p>{description}</p><textarea id={`setting-${setting}`} spellCheck={false} disabled={loading || busy !== '' || !writable.includes(setting)} value={display} onChange={event => setDraft(current => ({ ...current, [setting]: event.target.value }))} /><button className="button-secondary" type="button" disabled={loading || busy !== '' || !writable.includes(setting) || !dirty(setting)} onClick={saveJSON}>Save</button></div>
  }

  async function changePassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(''); setNotice('')
    if (!currentPassword || !newPassword) { setError('Enter the current and new password.'); return }
    if (newPassword !== confirmPassword) { setError('New passwords do not match.'); return }
    setBusy('password')
    try {
      const response = await fetch('/admin/v1/auth/password', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ currentPassword, newPassword }) })
      if (response.status === 401) {
        const data = await response.json().catch(() => ({})) as { error?: { code?: string; message?: string } }
        if (data.error?.code === 'invalid_credentials') setError(data.error.message || 'Current password is incorrect.')
        else onUnauthorized()
        return
      }
      if (!response.ok) throw new Error(await responseError(response, 'Could not change the admin password.'))
      setCurrentPassword(''); setNewPassword(''); setConfirmPassword('')
      setNotice('Password changed. All admin sessions were signed out; sign in again with the new password.')
      onUnauthorized()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not change the admin password.')
    } finally { setBusy('') }
  }

  async function checkRestore() {
    if (!restoreFile) { setError('Choose a Routeweft backup archive first.'); return }
    setBusy('restore-check'); setError(''); setNotice(''); setRestoreCheck(null)
    try {
      const response = await fetch('/admin/v1/backup/restore/check', { method: 'POST', credentials: 'same-origin', body: restoreFile })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Backup validation failed.'))
      const checked = await response.json() as BackupCheck
      if (!checked.valid) throw new Error('Backup validation did not return a valid archive.')
      setRestoreCheck(checked)
      setNotice('Backup passed integrity, schema, metadata, and configuration validation. Live data is unchanged.')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Backup validation failed.')
    } finally { setBusy('') }
  }

  async function activateRestore() {
    if (!restoreFile || !restoreCheck?.valid) return
    if (!window.confirm('Restore this backup? The current database will be replaced after Routeweft saves a rollback copy. Active requests and admin sessions will be interrupted.')) return
    setBusy('restore'); setError(''); setNotice('')
    try {
      const response = await fetch('/admin/v1/backup/restore', { method: 'POST', credentials: 'same-origin', body: restoreFile })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Restore activation was rejected.'))
      onUnauthorized()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Restore activation failed.')
    } finally { setBusy('') }
  }

  return <div className="settings-content" aria-busy={loading}>
    <div className="settings-toolbar"><span className="count-label" role="status">{loading ? 'Loading…' : 'Settings are saved through the active configuration transaction.'}</span><button className="button-secondary" type="button" disabled={loading || busy !== ''} onClick={() => void load()}>Reload</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    <div className="settings-grid">
      <section className="detail-panel" aria-labelledby="settings-auth-title">
        <div className="section-heading"><div><h2 id="settings-auth-title">Authentication</h2><p>Dashboard login is mandatory; inference API-key enforcement is configurable.</p></div></div>
        <div className="setting-toggle"><div><strong>Dashboard login</strong><small>Admin control-plane routes always require a valid session.</small></div><span className="state-tag state-good">Required</span></div>
        <Toggle setting="requireApiKey" label="Require client API key" description="Reject inference requests without an active Routeweft key." />
        <form className="settings-password" onSubmit={event => void changePassword(event)}>
          <h3>Change dashboard password</h3>
          <label htmlFor="change-password-current">Current password<input id="change-password-current" type="password" autoComplete="current-password" disabled={loading || busy !== ''} value={currentPassword} onChange={event => setCurrentPassword(event.target.value)} /></label>
          <label htmlFor="change-password-new">New password<input id="change-password-new" type="password" autoComplete="new-password" disabled={loading || busy !== ''} value={newPassword} onChange={event => setNewPassword(event.target.value)} /></label>
          <label htmlFor="change-password-confirm">Confirm new password<input id="change-password-confirm" type="password" autoComplete="new-password" disabled={loading || busy !== ''} value={confirmPassword} onChange={event => setConfirmPassword(event.target.value)} /></label>
          <button className="button-secondary" type="submit" disabled={loading || busy !== ''}>Change password</button>
          <p className="count-label">Rotation verifies the current password and signs out every admin session.</p>
        </form>
      </section>

      <section className="detail-panel" aria-labelledby="settings-routing-title">
        <div className="section-heading"><div><h2 id="settings-routing-title">Provider routing</h2><p>Default strategy and per-provider overrides.</p></div></div>
        <div className="settings-field"><label htmlFor="setting-providerStrategy">Default provider strategy<select id="setting-providerStrategy" disabled={loading || busy !== '' || !writable.includes('providerStrategy')} {...field('providerStrategy', 'fill-first')}>{providerStrategies.map(strategy => <option key={strategy} value={strategy}>{strategy}</option>)}</select></label><SaveButton setting="providerStrategy" message="Provider strategy saved." /></div>
        <NumberField setting="stickyRoundRobinLimit" label="Sticky round-robin limit" fallback="3" min={0} />
        <JSONEditor setting="providerStrategies" label="Provider strategy overrides" description="JSON object mapping provider IDs to fill-first, round-robin, or sticky-round-robin." />
      </section>

      <section className="detail-panel" aria-labelledby="settings-combo-title">
        <div className="section-heading"><div><h2 id="settings-combo-title">Combo routing</h2><p>Default behavior for Combos without their own strategy.</p></div></div>
        <div className="settings-field"><label htmlFor="setting-comboStrategy">Default Combo strategy<select id="setting-comboStrategy" disabled={loading || busy !== '' || !writable.includes('comboStrategy')} {...field('comboStrategy', 'fallback')}>{comboStrategies.map(strategy => <option key={strategy} value={strategy}>{strategy}</option>)}</select></label><SaveButton setting="comboStrategy" message="Combo strategy saved." /></div>
        <NumberField setting="comboStickyRoundRobinLimit" label="Combo sticky round-robin limit" fallback="1" min={0} />
        <JSONEditor setting="comboStrategies" label="Combo strategy overrides" description="JSON object mapping Combo names to fallback, round-robin, or sticky-round-robin." />
      </section>

      <section className="detail-panel" aria-labelledby="settings-quota-title">
        <div className="section-heading"><div><h2 id="settings-quota-title">Quota visibility</h2><p>Advanced visibility map, persisted without imposing an undocumented key schema.</p></div></div>
        <JSONEditor setting="quotaVisibility" label="Quota visibility map" description="JSON object. The product contract defines an empty default; map semantics are not yet specified." />
      </section>

      <section className="detail-panel" aria-labelledby="settings-observability-title">
        <div className="section-heading"><div><h2 id="settings-observability-title">Observability</h2><p>Bounded request accounting and detail retention limits.</p></div></div>
        <Toggle setting="enableObservability" label="Enable observability" description="Store request Usage and bounded request details." />
        <NumberField setting="observabilityMaxRecords" label="Maximum retained records" fallback="1000" />
        <NumberField setting="observabilityBatchSize" label="Write batch size" fallback="20" />
        <NumberField setting="observabilityFlushIntervalMs" label="Flush interval (ms)" fallback="5000" />
        <NumberField setting="observabilityMaxJsonSize" label="Maximum detail JSON bytes" fallback="5242880" />
        <p className="count-label settings-note">The telemetry worker and detail-store limits are initialized at startup; restart Routeweft after changing these values.</p>
      </section>

      <section className="detail-panel" aria-labelledby="settings-proxy-title">
        <div className="section-heading"><div><h2 id="settings-proxy-title">Outbound proxy</h2><p>Global proxy policy; connection proxy pools remain managed under Providers.</p></div></div>
        <Toggle setting="outboundProxyEnabled" label="Enable global outbound proxy" description="Use this proxy when no connection-level proxy overrides it." />
        <div className="settings-field"><label htmlFor="setting-outboundProxyUrl">Proxy URL<input id="setting-outboundProxyUrl" type="url" disabled={loading || busy !== '' || !writable.includes('outboundProxyUrl')} placeholder="http://proxy.example:3128" {...field('outboundProxyUrl')} /></label><SaveButton setting="outboundProxyUrl" message="Proxy URL saved." /></div>
        <p className="count-label settings-note">The shared SSRF policy rejects embedded credentials, query strings, and fragments in proxy URLs.</p>
        <JSONEditor setting="noProxy" label="No-proxy entries" description="JSON array of hosts, suffixes, or CIDRs excluded from the global proxy." kind="string-array" />
      </section>

      <section className="detail-panel" aria-labelledby="settings-compatibility-title">
        <div className="section-heading"><div><h2 id="settings-compatibility-title">Compatibility and tools</h2><p>Contract-defined flags; unsupported semantics remain explicit rather than inferred.</p></div></div>
        <Toggle setting="dnsToolEnabled" label="DNS tool flag" description="Persist the configured flag; no DNS-tool runtime consumer is currently defined." />
        <JSONEditor setting="providerCompatibility" label="Provider compatibility map" description="JSON object stored as configured. Its per-provider keys and behavior are not defined in the current product contract." />
      </section>

      <section className="detail-panel" aria-labelledby="settings-backup-title">
        <div className="section-heading"><div><h2 id="settings-backup-title">Database backup and restore</h2><p>Validated restore is staged before live activation.</p></div></div>
        <p className="settings-warning">Backup archives contain the Routeweft database and sensitive operator/provider state. Store them securely.</p>
        <a className="button-secondary settings-download" href="/admin/v1/backup">Download backup archive</a>
        <div className="settings-restore">
          <label htmlFor="restore-file">Restore archive<input id="restore-file" type="file" accept=".zip,application/zip" disabled={loading || busy !== ''} onChange={event => { setRestoreFile(event.target.files?.[0] || null); setRestoreCheck(null); setError(''); setNotice('') }} /></label>
          <button className="button-secondary" type="button" disabled={loading || !restoreFile || busy !== ''} onClick={() => void checkRestore()}>{busy === 'restore-check' ? 'Checking…' : 'Validate archive'}</button>
          {restoreCheck?.valid && <div className="restore-check-result" role="status"><span>Valid · schema {restoreCheck.schemaVersion} · config revision {restoreCheck.configRevision}</span><button className="button-danger" type="button" disabled={loading || busy !== ''} onClick={() => void activateRestore()}>Restore database</button></div>}
        </div>
      </section>

      <section className="detail-panel" aria-labelledby="settings-preferences-title">
        <div className="section-heading"><div><h2 id="settings-preferences-title">Preferences</h2><p>Appearance remains available in the sidebar on every page.</p></div></div>
        <p className="count-label">Light, dark, and system theme choices are stored locally in this browser.</p>
      </section>
    </div>
  </div>
}
