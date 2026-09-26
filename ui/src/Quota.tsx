import { useCallback, useEffect, useState } from 'react'

type QuotaItem = { connectionId: string; providerId: string; name: string; enabled: boolean; status: string; remaining?: number; resetAt?: string; observedAt?: string; cooldownUntil?: string }
type Props = { onUnauthorized: () => void }

const numberFormat = new Intl.NumberFormat()

function when(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function relative(value?: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const minutes = Math.round((date.getTime() - Date.now()) / 60000)
  if (minutes <= 0) return 'now'
  if (minutes < 60) return `in ${minutes}m`
  const hours = Math.round(minutes / 60)
  return hours < 48 ? `in ${hours}h` : `in ${Math.round(hours / 24)}d`
}

function statusTone(status: string) {
  if (status === 'available') return 'state-good'
  if (status === 'unknown') return ''
  return 'state-warn'
}

async function responseError(response: Response, fallback: string) {
  try { return ((await response.json()) as { error?: { message?: string } }).error?.message || fallback } catch { return fallback }
}

export function Quota({ onUnauthorized }: Props) {
  const [items, setItems] = useState<QuotaItem[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const response = await fetch('/admin/v1/quota?page=1&pageSize=100', { credentials: 'same-origin' })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not load quota.'))
      const data = await response.json() as { items: QuotaItem[] }
      setItems(data.items)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load quota.')
    } finally { setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  async function refresh() {
    setRefreshing(true); setError(''); setNotice('')
    try {
      const response = await fetch('/admin/v1/quota/refresh', { method: 'POST', credentials: 'same-origin' })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error(await responseError(response, 'Could not request a quota refresh.'))
      setNotice('Quota refresh requested. It runs in the background and never blocks inference.')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not request a quota refresh.')
    } finally { setRefreshing(false) }
  }

  const limited = items.filter(item => item.remaining !== undefined || item.resetAt)

  return <div className="quota-content" aria-busy={loading}>
    <div className="quota-toolbar"><span className="count-label" role="status">{loading ? 'Loading…' : `${items.length} account${items.length === 1 ? '' : 's'} · ${limited.length} with limit data`}</span><button className="button-primary" type="button" disabled={refreshing} onClick={() => void refresh()}>{refreshing ? 'Requesting…' : 'Refresh quota'}</button><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Reload</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    <section className="detail-panel" aria-labelledby="quota-title">
      <div className="section-heading"><div><h2 id="quota-title">Provider accounts</h2><p>Remaining limit and reset state where the provider reports it. Refresh is asynchronous.</p></div></div>
      {items.length === 0 && !loading ? <p className="empty-keys">No provider connections configured.</p> : <div className="quota-list">{items.map(item => <article className="quota-row" key={item.connectionId}>
        <div className="quota-main"><strong>{item.name}</strong><code>{item.providerId}</code></div>
        <span className={`state-tag ${statusTone(item.status)}`}>{item.status}</span>
        <span className="quota-remaining">{item.remaining !== undefined ? `${numberFormat.format(item.remaining)} remaining` : 'limit not reported'}</span>
        <span className="quota-reset">{item.resetAt ? `resets ${when(item.resetAt)} (${relative(item.resetAt)})` : item.cooldownUntil ? `cooldown until ${when(item.cooldownUntil)}` : 'no reset time'}</span>
        <span className="count-label">{item.observedAt ? `observed ${when(item.observedAt)}` : 'not yet observed'}</span>
        {!item.enabled && <span className="state-tag state-warn">disabled</span>}
      </article>)}</div>}
    </section>
  </div>
}
