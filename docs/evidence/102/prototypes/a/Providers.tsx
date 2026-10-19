import { useMemo, useState } from 'react'
import { Status, type Tone } from './kit2'
import { ErrorNotice, SkeletonRows } from './kit'
import { compact, num, pct, providerTraffic, rate, useData } from './proto'

type Sort = 'traffic' | 'name' | 'accounts'

export function Providers({ onUnauthorized }: { onUnauthorized: () => void }) {
  const data = useData(onUnauthorized)
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<Sort>('traffic')
  const [open, setOpen] = useState('openai')
  const [showAll, setShowAll] = useState(false)
  const traffic = useMemo(() => providerTraffic(data.month), [data.month])

  const rows = useMemo(() => data.providers.map(provider => {
    const accounts = data.connections.filter(connection => connection.providerId === provider.id)
    const enabled = accounts.filter(connection => connection.enabled).length
    const t = traffic.get(provider.id)
    return { provider, accounts, enabled, requests: t?.requests ?? 0, errors: t?.errors ?? 0 }
  }).filter(row => !filter || row.provider.id.includes(filter.toLowerCase()) || row.provider.transports.some(item => item.includes(filter.toLowerCase()))), [data.providers, data.connections, traffic, filter])

  const order = (a: typeof rows[number], b: typeof rows[number]) => sort === 'name' ? a.provider.id.localeCompare(b.provider.id) : sort === 'accounts' ? b.accounts.length - a.accounts.length || a.provider.id.localeCompare(b.provider.id) : b.requests - a.requests || a.provider.id.localeCompare(b.provider.id)
  const connected = rows.filter(row => row.accounts.length > 0).sort(order)
  const catalog = rows.filter(row => row.accounts.length === 0).sort((a, b) => a.provider.id.localeCompare(b.provider.id))
  const top = Math.max(1, ...connected.map(row => row.requests))

  if (data.loading && data.providers.length === 0) return <div role="status" aria-label="Loading providers"><SkeletonRows rows={12} columns={5} /></div>
  if (data.error && data.providers.length === 0) return <ErrorNotice message={data.error} onRetry={data.reload} />

  const tone = (row: typeof rows[number]): Tone => row.accounts.length === 0 ? 'idle' : row.enabled === 0 ? 'bad' : row.enabled < row.accounts.length || rate(row.errors, row.requests) > 8 ? 'warn' : 'good'
  const line = (row: typeof rows[number]) => {
    const isOpen = open === row.provider.id
    return [<tr key={row.provider.id} className={`prow${isOpen ? ' prow-open' : ''}`}>
      <td><button className="row-button" type="button" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? '' : row.provider.id)}><Status tone={tone(row)}><code>{row.provider.id}</code></Status></button></td>
      <td>{row.provider.authModes.map(mode => <span className="state-tag" key={mode}>{mode}</span>)}</td>
      <td>{row.provider.transports.length} {row.provider.transports.length === 1 ? 'transport' : 'transports'}</td>
      <td className="num">{row.accounts.length ? `${row.enabled}/${row.accounts.length}` : '—'}</td>
      <td className="num">{row.requests ? num(row.requests) : '—'}</td>
      <td><span className="bar"><span style={{ width: `${(row.requests / top) * 100}%` }} /></span></td>
      <td className="num">{row.requests ? pct(rate(row.errors, row.requests)) : '—'}</td>
    </tr>, isOpen && <tr key={`${row.provider.id}-detail`} className="pdetail"><td colSpan={7}>
      <div className="pdetail-grid">
        <div><h3 className="tile-label">Accounts</h3>
          {row.accounts.length === 0 ? <p className="tile-note">No account yet. Add a connection to route traffic to {row.provider.id}.</p> : <ul className="plist">{row.accounts.map(account => <li key={account.id}><Status tone={account.enabled ? 'good' : 'idle'}>{account.name}</Status><code>{account.identity}</code><span>priority {account.priority}</span><span>{account.authKind}</span></li>)}</ul>}
        </div>
        <div><h3 className="tile-label">30 days</h3><p className="tile-value"><span className="tile-number">{compact(row.requests)}</span><span className="tile-unit">req</span></p><p className="tile-note">{num(row.errors)} failed · {row.requests ? pct(rate(row.errors, row.requests)) : '0.0%'} error rate</p></div>
        <div><h3 className="tile-label">Catalog</h3><p className="tile-note">{row.provider.modelCatalog} model list · {row.provider.transports.join(', ')}</p></div>
      </div></td></tr>]
  }

  return <div className="providers-content a-providers" aria-busy={data.loading}>
    <div className="toolbar">
      <label htmlFor="provider-filter">Find</label>
      <input id="provider-filter" value={filter} onChange={event => setFilter(event.target.value)} placeholder="Provider or transport" />
      <div className="segmented" role="group" aria-label="Sort providers">{(['traffic', 'name', 'accounts'] as Sort[]).map(key => <button key={key} type="button" aria-pressed={sort === key} className={sort === key ? 'seg-on' : ''} onClick={() => setSort(key)}>{key === 'traffic' ? '30 d traffic' : key === 'name' ? 'Name' : 'Accounts'}</button>)}</div>
      <span className="count">{connected.length} connected · {catalog.length} in catalog</span>
    </div>
    <div className="table-scroll"><table className="grid-table providers-table">
      <thead><tr><th scope="col">Provider</th><th scope="col">Auth</th><th scope="col">Transports</th><th scope="col" className="num">Accounts</th><th scope="col" className="num">30 d req</th><th scope="col">Share</th><th scope="col" className="num">Errors</th></tr></thead>
      <tbody><tr className="group-row"><th colSpan={7} scope="colgroup"><span>Connected · {connected.length}</span><span>sorted by {sort === 'traffic' ? '30 d traffic' : sort}</span></th></tr>
        {connected.flatMap(line)}
        <tr className="group-row"><th colSpan={7} scope="colgroup"><span>Catalog · {catalog.length}</span><span>no account yet</span></th></tr>
        {(showAll ? catalog : catalog.slice(0, 6)).flatMap(line)}
        {!showAll && catalog.length > 6 && <tr><td colSpan={7}><button className="button-secondary" type="button" onClick={() => setShowAll(true)}>Show all {catalog.length}</button></td></tr>}
      </tbody>
    </table></div>
  </div>
}
