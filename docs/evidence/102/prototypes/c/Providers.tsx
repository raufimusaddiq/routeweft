import { useMemo, useState } from 'react'
import { Status, type Tone } from './kit2'
import { ErrorNotice, SkeletonRows } from './kit'
import { clock, compact, num, pct, providerTraffic, rate, statusClass, useData } from './proto'
import { LiveStrip, Panel } from './Overview'

type Sort = 'traffic' | 'name' | 'accounts'

export function Providers({ onUnauthorized }: { onUnauthorized: () => void }) {
  const data = useData(onUnauthorized)
  const [sort, setSort] = useState<Sort>('traffic')
  const [pick, setPick] = useState('')
  const [filter, setFilter] = useState('')
  const traffic = useMemo(() => providerTraffic(data.month), [data.month])
  const rows = useMemo(() => data.providers.map(p => { const accounts = data.connections.filter(c => c.providerId === p.id); const t = traffic.get(p.id); return { p, accounts, enabled: accounts.filter(c => c.enabled).length, requests: t?.requests ?? 0, errors: t?.errors ?? 0 } }).filter(r => !filter || r.p.id.includes(filter.toLowerCase())), [data.providers, data.connections, traffic, filter])
  if (data.loading && !data.providers.length) return <div role="status" aria-label="Loading providers"><SkeletonRows rows={10} columns={4} /></div>
  if (data.error && !data.providers.length) return <ErrorNotice message={data.error} onRetry={data.reload} />
  const cmp = (a: typeof rows[number], b: typeof rows[number]) => sort === 'name' ? a.p.id.localeCompare(b.p.id) : sort === 'accounts' ? b.accounts.length - a.accounts.length || a.p.id.localeCompare(b.p.id) : b.requests - a.requests || a.p.id.localeCompare(b.p.id)
  const live = rows.filter(r => r.accounts.length).sort(cmp)
  const rest = rows.filter(r => !r.accounts.length).sort((a, b) => a.p.id.localeCompare(b.p.id))
  const sel = rows.find(r => r.p.id === (pick || live[0]?.p.id)) ?? live[0]
  const tone = (r: typeof rows[number]): Tone => !r.accounts.length ? 'idle' : r.enabled === 0 ? 'bad' : r.enabled < r.accounts.length || rate(r.errors, r.requests) > 8 ? 'warn' : 'good'
  const models = (data.month?.models ?? []).filter(m => sel && m.key.startsWith(sel.p.id + '/'))
  const mTop = Math.max(1, ...models.map(m => m.requests))
  const recent = data.events.filter(e => sel && e.providerId === sel.p.id).slice(0, 5)
  const totalReq = Math.max(1, data.month?.totals.requests ?? 1)
  const row = (r: typeof rows[number]) => <li key={r.p.id}><button type="button" className={`c-row${sel?.p.id === r.p.id ? ' c-row-on' : ''}`} aria-pressed={sel?.p.id === r.p.id} onClick={() => setPick(r.p.id)}><Status tone={tone(r)}><code>{r.p.id}</code></Status><span className="c-row-n">{r.requests ? compact(r.requests) : '—'}</span></button></li>
  return <div className="providers-content c-providers" aria-busy={data.loading}>
    <LiveStrip data={data} />
    <div className="c-split">
      <Panel title="Providers" meta={`${live.length} connected · ${rest.length} catalog`} className="c-list">
        <div className="c-tools"><label htmlFor="provider-filter" className="sr-only">Find provider</label><input id="provider-filter" value={filter} onChange={e => setFilter(e.target.value)} placeholder="Find provider" />
          <select aria-label="Sort providers" value={sort} onChange={e => setSort(e.target.value as Sort)}><option value="traffic">Sort: 30 d traffic</option><option value="name">Sort: name</option><option value="accounts">Sort: accounts</option></select></div>
        <div className="well well-flush"><h3 className="c-group">Connected <span>{live.length}</span></h3><ul className="c-rows">{live.map(row)}</ul>
          <h3 className="c-group">Catalog <span>{rest.length}</span></h3><ul className="c-rows c-rows-quiet">{rest.slice(0, 10).map(row)}</ul></div>
      </Panel>
      {sel && <div className="c-detail">
        <Panel title={sel.p.id} meta={sel.p.authModes.join(' / ')} className="c-head">
          <div className="c-facts">
            <div><h3 className="tile-label">30 days</h3><p className="pn-figure"><span className="tile-number">{compact(sel.requests)}</span><span className="tile-unit">req</span></p><p className="tile-note">{pct((sel.requests / totalReq) * 100)} of all traffic</p></div>
            <div><h3 className="tile-label">Error rate</h3><p className="pn-figure"><span className="tile-number">{sel.requests ? pct(rate(sel.errors, sel.requests)) : '0.0%'}</span></p><p className="tile-note">{num(sel.errors)} failed</p></div>
            <div><h3 className="tile-label">Accounts</h3><p className="pn-figure"><span className="tile-number">{sel.enabled}/{sel.accounts.length}</span><span className="tile-unit">enabled</span></p><p className="tile-note"><Status tone={tone(sel)}>{tone(sel) === 'good' ? 'Healthy' : tone(sel) === 'idle' ? 'Not connected' : 'Needs attention'}</Status></p></div>
          </div>
        </Panel>
        <Panel title="Accounts" meta="priority order"><div className="well well-flush"><ul className="c-rows">{sel.accounts.map(a => <li key={a.id} className="c-acct"><Status tone={a.enabled ? 'good' : 'idle'}>{a.name}</Status><code>{a.identity}</code><span>{a.authKind}</span><span>priority {a.priority}</span></li>)}</ul></div></Panel>
        <div className="c-two">
          <Panel title="Models" meta="30 d"><div className="well"><table className="grid-table"><thead><tr><th scope="col">Model</th><th scope="col" className="num">Req</th><th scope="col">Share</th></tr></thead><tbody>{models.slice(0, 5).map(m => <tr key={m.key}><td><code>{m.key.slice(sel.p.id.length + 1)}</code></td><td className="num">{num(m.requests)}</td><td><span className="bar"><span style={{ width: `${(m.requests / mTop) * 100}%` }} /></span></td></tr>)}</tbody></table></div></Panel>
          <Panel title="Latest requests" meta="this provider"><div className="well"><table className="grid-table"><thead><tr><th scope="col">Time</th><th scope="col">Status</th><th scope="col" className="num">Latency</th></tr></thead><tbody>{recent.map(e => <tr key={e.id}><td>{clock(e.createdAt)}</td><td><Status tone={statusClass(e.status)}>{e.status}</Status></td><td className="num">{e.durationMs ?? 0} ms</td></tr>)}</tbody></table></div></Panel>
        </div>
      </div>}
    </div>
  </div>
}
