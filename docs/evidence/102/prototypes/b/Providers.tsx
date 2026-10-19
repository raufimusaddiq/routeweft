import { useMemo, useState } from 'react'
import { Dot, Status, type Tone } from './kit2'
import { ErrorNotice, SkeletonRows } from './kit'
import { compact, num, pct, providerTraffic, rate, useData } from './proto'

type Sort = 'traffic' | 'name' | 'accounts'

export function Providers({ onUnauthorized }: { onUnauthorized: () => void }) {
  const data = useData(onUnauthorized)
  const [sort, setSort] = useState<Sort>('traffic')
  const [pick, setPick] = useState('')
  const [filter, setFilter] = useState('')
  const traffic = useMemo(() => providerTraffic(data.month), [data.month])
  const rows = useMemo(() => data.providers.map(p => {
    const accounts = data.connections.filter(c => c.providerId === p.id)
    const t = traffic.get(p.id)
    return { p, accounts, enabled: accounts.filter(c => c.enabled).length, requests: t?.requests ?? 0, errors: t?.errors ?? 0 }
  }).filter(r => !filter || r.p.id.includes(filter.toLowerCase())), [data.providers, data.connections, traffic, filter])
  if (data.loading && !data.providers.length) return <div role="status" aria-label="Loading providers"><SkeletonRows rows={10} columns={4} /></div>
  if (data.error && !data.providers.length) return <ErrorNotice message={data.error} onRetry={data.reload} />

  const cmp = (a: typeof rows[number], b: typeof rows[number]) => sort === 'name' ? a.p.id.localeCompare(b.p.id) : sort === 'accounts' ? b.accounts.length - a.accounts.length || a.p.id.localeCompare(b.p.id) : b.requests - a.requests || a.p.id.localeCompare(b.p.id)
  const live = rows.filter(r => r.accounts.length).sort(cmp)
  const rest = rows.filter(r => !r.accounts.length).sort((a, b) => a.p.id.localeCompare(b.p.id))
  const selected = rows.find(r => r.p.id === (pick || live[0]?.p.id)) ?? live[0]
  const attention = live.filter(r => r.enabled < r.accounts.length || rate(r.errors, r.requests) > 8)
  const tone = (r: typeof rows[number]): Tone => !r.accounts.length ? 'idle' : r.enabled === 0 ? 'bad' : r.enabled < r.accounts.length || rate(r.errors, r.requests) > 8 ? 'warn' : 'good'
  const total = Math.max(1, ...live.map(r => r.requests))
  const sheetModels = (data.month?.models ?? []).filter(m => selected && m.key.startsWith(selected.p.id + '/'))

  const item = (r: typeof rows[number]) => <li key={r.p.id}><button type="button" className={`ed-row${selected?.p.id === r.p.id ? ' ed-row-on' : ''}`} aria-pressed={selected?.p.id === r.p.id} onClick={() => setPick(r.p.id)}>
    <Status tone={tone(r)}><span className="ed-name">{r.p.id}</span></Status>
    <span className="ed-spec">{r.p.authModes.join(' / ')} · {r.p.transports.length} {r.p.transports.length === 1 ? 'transport' : 'transports'}</span>
    <span className="ed-bar"><span style={{ width: `${(r.requests / total) * 100}%` }} /></span>
    <span className="ed-fig">{r.requests ? num(r.requests) : '—'}</span>
    <span className="ed-sub">{r.accounts.length ? `${r.enabled}/${r.accounts.length} on` : 'no account'}</span>
  </button></li>

  return <div className="providers-content b-providers" aria-busy={data.loading}>
    <section className="ed-banner accent-surface" aria-label="Attention">
      <p className="ed-headline"><strong>{live.length}</strong> of {rows.length} providers carry traffic. {attention.length ? <><Dot tone="bad" /> <strong>{attention.map(r => r.p.id).join(', ')}</strong> {attention.length === 1 ? 'needs' : 'need'} attention.</> : 'All accounts are enabled.'}</p>
    </section>
    <div className="ed-sort" role="group" aria-label="Sort providers"><label htmlFor="provider-filter" className="tile-label">Find</label><input id="provider-filter" value={filter} onChange={e => setFilter(e.target.value)} placeholder="Provider" />
      <span className="tile-label">Sort</span>{(['traffic', 'name', 'accounts'] as Sort[]).map(k => <button key={k} type="button" aria-pressed={sort === k} className={`ed-link${sort === k ? ' ed-link-on' : ''}`} onClick={() => setSort(k)}>{k === 'traffic' ? '30 d traffic' : k[0].toUpperCase() + k.slice(1)}</button>)}</div>
    <div className="ed-split">
      <div>
        <h2 className="ed-group">In service <span>{live.length}</span></h2>
        <ul className="ed-rows">{live.map(item)}</ul>
        <h2 className="ed-group">Catalogue <span>{rest.length} without an account</span></h2>
        <ul className="ed-rows ed-rows-quiet">{rest.slice(0, 8).map(item)}</ul>
      </div>
      {selected && <aside className="ed-sheet" aria-label={`${selected.p.id} detail`}>
        <p className="ed-kicker">Provider</p>
        <h2 className="ed-sheet-title">{selected.p.id}</h2>
        <p className="ed-now-note"><Status tone={tone(selected)}>{selected.accounts.length === 0 ? 'No account' : `${selected.enabled} of ${selected.accounts.length} accounts enabled`}</Status></p>
        <p className="ed-headline ed-sheet-figure"><strong>{compact(selected.requests)}</strong> requests in 30 days, <strong>{selected.requests ? pct(rate(selected.errors, selected.requests)) : '0.0%'}</strong> failed</p>
        <h3 className="tile-label">Accounts</h3>
        <ul className="ed-rows">{selected.accounts.map(a => <li key={a.id} className="ed-account"><Status tone={a.enabled ? 'good' : 'idle'}>{a.name}</Status><code>{a.identity}</code><span className="ed-sub">priority {a.priority}</span></li>)}</ul>
        <h3 className="tile-label">Models in use</h3>
        <ul className="ed-rows">{sheetModels.slice(0, 5).map(m => <li key={m.key} className="ed-account"><code>{m.key.slice(selected.p.id.length + 1)}</code><span className="ed-sub">{num(m.requests)} req</span><span className="ed-sub">{compact(m.inputTokens + m.outputTokens)} tok</span></li>)}</ul>
      </aside>}
    </div>
  </div>
}
