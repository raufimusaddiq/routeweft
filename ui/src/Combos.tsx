import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'

type Member = { providerId: string; modelId: string; position: number; selected: boolean }
type Combo = { id: string; name: string; strategy: string; stickyLimit: number; judgeModel: string; fusionEnabled: boolean; members: Member[] }
type Model = { providerId: string; id: string; disabled: boolean; source?: string }
type Adapter = { capability: string; enabled: boolean; pool: { providerId: string; modelId: string }[] }
type Props = { onUnauthorized: () => void }

// vision and audio-input are the initially visible capacity adapters; pdf and
// video-input stay available for compatibility without standalone media APIs
// (PRD-COMBO-004).
const visibleCapabilities = ['vision', 'audio-input']
const strategies = [['fallback', 'Ordered fallback'], ['round-robin', 'Round-robin'], ['sticky-round-robin', 'Sticky round-robin']]

const blankCombo = { id: '', name: '', strategy: 'fallback', stickyLimit: 1, judgeModel: '', fusionEnabled: false, members: [] as Member[] }

async function responseError(response: Response, fallback: string) {
  try {
    const body = await response.json() as { error?: { message?: string } }
    return body.error?.message || fallback
  } catch {
    return fallback
  }
}

export function Combos({ onUnauthorized }: Props) {
  const [combos, setCombos] = useState<Combo[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [aliases, setAliases] = useState<{ alias: string; providerId: string; modelId: string }[]>([])
  const [adapters, setAdapters] = useState<Adapter[]>([])
  const [draft, setDraft] = useState(blankCombo)
  const [editing, setEditing] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [memberPick, setMemberPick] = useState('')
  const [adapterPick, setAdapterPick] = useState<Record<string, string>>({})

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const paths = ['/combos?page=1&pageSize=100', '/models?page=1&pageSize=100', '/aliases?page=1&pageSize=100', '/capability-adapters']
      const responses = await Promise.all(paths.map(path => fetch(`/admin/v1${path}`, { credentials: 'same-origin' })))
      if (responses.some(response => response.status === 401)) { onUnauthorized(); return }
      const failed = responses.find(response => !response.ok)
      if (failed) throw new Error(await responseError(failed, 'Could not load Combo configuration.'))
      const [comboData, modelData, aliasData, adapterData] = await Promise.all(responses.map(response => response.json())) as Array<{ items: unknown[] }>
      setCombos(comboData.items as Combo[])
      setModels((modelData.items as Model[]).filter(model => !model.disabled))
      setAliases(aliasData.items as { alias: string; providerId: string; modelId: string }[])
      setAdapters(adapterData.items as Adapter[])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load Combo configuration.')
    } finally {
      setLoading(false)
    }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  const options = useMemo(() => {
    const listed = models.map(model => ({ value: `${model.providerId}\x1f${model.id}`, label: `${model.providerId} / ${model.id}` }))
    // Aliases are usable as Combo members (PRD-COMBO-001); they resolve to their
    // target provider/model at dispatch.
    for (const item of aliases) listed.push({ value: `${item.providerId}\x1f${item.alias}`, label: `${item.providerId} / ${item.alias} (alias)` })
    return listed
  }, [models, aliases])

  async function send(path: string, method: string, body?: unknown) {
    const response = await fetch(`/admin/v1${path}`, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
    if (response.status === 401) { onUnauthorized(); throw new Error('Admin session expired.') }
    if (!response.ok) throw new Error(await responseError(response, 'Combo action failed.'))
    return response.status === 204 ? null : response.json()
  }

  async function action(id: string, message: string, callback: () => Promise<void>) {
    setBusy(id); setError(''); setNotice('')
    try { await callback(); setNotice(message); await load() }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Combo action failed.') }
    finally { setBusy('') }
  }

  function startCreate() {
    setDraft(blankCombo)
    setEditing(true)
    setMemberPick('')
  }

  function startEdit(combo: Combo) {
    setDraft({ id: combo.id, name: combo.name, strategy: combo.strategy || 'fallback', stickyLimit: combo.stickyLimit || 1, judgeModel: combo.judgeModel, fusionEnabled: combo.fusionEnabled, members: combo.members.map(member => ({ ...member })) })
    setEditing(true)
  }

  function addMember() {
    if (!memberPick) return
    const [providerId, modelId] = memberPick.split('\x1f')
    if (draft.members.some(member => member.providerId === providerId && member.modelId === modelId)) return
    setDraft({ ...draft, members: [...draft.members, { providerId, modelId, position: draft.members.length, selected: true }] })
    setMemberPick('')
  }

  function reorder(index: number, delta: number) {
    const next = index + delta
    if (next < 0 || next >= draft.members.length) return
    const members = [...draft.members]
    ;[members[index], members[next]] = [members[next], members[index]]
    setDraft({ ...draft, members: members.map((member, order) => ({ ...member, position: order })) })
  }

  function toggleMember(index: number) {
    setDraft({ ...draft, members: draft.members.map((member, order) => order === index ? { ...member, selected: !member.selected } : member) })
  }

  function removeMember(index: number) {
    setDraft({ ...draft, members: draft.members.filter((_, order) => order !== index).map((member, order) => ({ ...member, position: order })) })
  }

  async function saveCombo(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!draft.name.trim() || draft.members.length === 0) return
    const body = { name: draft.name.trim(), strategy: draft.strategy, stickyLimit: Number(draft.stickyLimit) || 1, judgeModel: draft.judgeModel.trim(), fusionEnabled: draft.fusionEnabled, members: draft.members.map((member, order) => ({ providerId: member.providerId, modelId: member.modelId, position: order, selected: member.selected })) }
    await action('combo', draft.id ? 'Combo updated.' : 'Combo created.', async () => {
      await send(draft.id ? `/combos/${encodeURIComponent(draft.id)}` : '/combos', draft.id ? 'PUT' : 'POST', body)
      setEditing(false); setDraft(blankCombo)
    })
  }

  async function deleteCombo(combo: Combo) {
    if (!window.confirm(`Delete Combo “${combo.name}”?`)) return
    await action(combo.id, 'Combo deleted.', () => send(`/combos/${encodeURIComponent(combo.id)}`, 'DELETE').then(() => { if (draft.id === combo.id) { setEditing(false); setDraft(blankCombo) } }))
  }

  async function saveAdapter(capability: string, enabled: boolean, pool: Adapter['pool']) {
    await action(`adapter-${capability}`, `${capability} adapter saved.`, () => send(`/capability-adapters/${encodeURIComponent(capability)}`, 'PUT', { enabled, pool }).then(() => undefined))
  }

  function adapterFor(capability: string) {
    return adapters.find(item => item.capability === capability) || { capability, enabled: false, pool: [] }
  }

  return <div className="combos-content" aria-busy={loading}>
    <div className="combos-toolbar"><span className="count-label">{loading ? 'Loading…' : `${combos.length} Combo${combos.length === 1 ? '' : 's'}`}</span><button className="button-primary" type="button" onClick={startCreate}>New Combo</button><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    <div className="combos-layout">
      <section className="detail-panel combo-list-panel" aria-label="Combo list">
        {combos.length === 0 && !loading ? <p className="empty-keys">No Combos yet.</p> : <div className="combo-list">{combos.map(combo => <article className={`combo-row${draft.id === combo.id ? ' combo-row-selected' : ''}`} key={combo.id}>
          <div className="combo-main"><strong>{combo.name}</strong><small>{combo.members.filter(member => member.selected).length} selected · {combo.members.length} member{combo.members.length === 1 ? '' : 's'}</small></div>
          <span className="state-tag">{combo.strategy || 'fallback'}</span>
          {combo.fusionEnabled && <span className="state-tag state-good">Fusion</span>}
          <div className="combo-actions"><button className="button-secondary" type="button" onClick={() => startEdit(combo)}>Edit</button><button className="button-danger" type="button" disabled={busy !== ''} onClick={() => void deleteCombo(combo)}>Delete</button></div>
        </article>)}</div>}
      </section>

      <section className="detail-panel">
        {!editing && <div className="detail-panel-empty"><h2>Select a Combo</h2><p>Create or edit a Combo to set ordered members, strategy and Fusion.</p></div>}
        {editing && <form className="combo-form" onSubmit={event => void saveCombo(event)}>
          <div className="section-heading"><div><h2>{draft.id ? 'Edit Combo' : 'New Combo'}</h2><p>Ordered members route first-to-last; deselecting keeps a member without routing it.</p></div><button className="button-ghost" type="button" onClick={() => { setEditing(false); setDraft(blankCombo) }}>Close</button></div>
          <div className="combo-form-grid">
            <label>Name<input required value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} /></label>
            <label>Strategy<select value={draft.strategy} onChange={event => setDraft({ ...draft, strategy: event.target.value })}>{strategies.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
            <label>Sticky limit<input type="number" min="1" value={draft.stickyLimit} onChange={event => setDraft({ ...draft, stickyLimit: Number(event.target.value) || 1 })} /></label>
            <label>Judge model<input placeholder="Defaults to first member" value={draft.judgeModel} onChange={event => setDraft({ ...draft, judgeModel: event.target.value })} /></label>
            <label className="combo-toggle"><input type="checkbox" checked={draft.fusionEnabled} onChange={event => setDraft({ ...draft, fusionEnabled: event.target.checked })} />Enable Fusion</label>
          </div>
          <div className="member-picker"><select aria-label="Add Combo member" value={memberPick} onChange={event => setMemberPick(event.target.value)}><option value="">Choose a model or alias</option>{options.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select><button className="button-secondary" type="button" disabled={!memberPick} onClick={addMember}>Add member</button></div>
          <ol className="member-list">{draft.members.map((member, index) => <li className={`member-row${member.selected ? '' : ' member-row-deselected'}`} key={`${member.providerId}/${member.modelId}`}>
            <span className="member-order">{index + 1}</span>
            <code>{member.providerId} / {member.modelId}</code>
            <span className={`state-tag${member.selected ? ' state-good' : ' state-warn'}`}>{member.selected ? 'Selected' : 'Deselected'}</span>
            <div className="member-actions"><button className="button-secondary" type="button" disabled={index === 0} onClick={() => reorder(index, -1)} aria-label={`Move ${member.modelId} up`} title="Move up">↑</button><button className="button-secondary" type="button" disabled={index === draft.members.length - 1} onClick={() => reorder(index, 1)} aria-label={`Move ${member.modelId} down`} title="Move down">↓</button><button className="button-secondary" type="button" onClick={() => toggleMember(index)}>{member.selected ? 'Deselect' : 'Select'}</button><button className="button-danger" type="button" onClick={() => removeMember(index)}>Remove</button></div>
          </li>)}{draft.members.length === 0 && <li className="empty-keys">Add at least one member.</li>}</ol>
          <button className="button-primary" type="submit" disabled={busy === 'combo' || !draft.name.trim() || draft.members.length === 0}>{busy === 'combo' ? 'Saving…' : draft.id ? 'Save Combo' : 'Create Combo'}</button>
        </form>}
      </section>
    </div>

    <section className="detail-panel adapter-panel" aria-labelledby="adapter-title">
      <div className="section-heading"><div><h3 id="adapter-title">Capacity adapters</h3><p>Used only when Combo members cannot satisfy a required capability (PRD-COMBO-004).</p></div></div>
      {visibleCapabilities.map(capability => {
        const adapter = adapterFor(capability)
        const pick = adapterPick[capability] || ''
        return <div className="adapter-card" key={capability}>
          <div className="adapter-header"><div><strong>{capability}</strong><small>{adapter.enabled ? (adapter.pool.length ? `${adapter.pool.length} pool member${adapter.pool.length === 1 ? '' : 's'}` : 'Enabled · empty pool is a no-op') : 'Disabled'}</small></div><button className="button-secondary" type="button" disabled={busy === `adapter-${capability}`} onClick={() => void saveAdapter(capability, !adapter.enabled, adapter.pool)}>{adapter.enabled ? 'Disable' : 'Enable'}</button></div>
          {adapter.pool.length > 0 && <ul className="adapter-pool">{adapter.pool.map(member => <li key={`${member.providerId}/${member.modelId}`}><code>{member.providerId} / {member.modelId}</code><button className="button-danger" type="button" disabled={busy === `adapter-${capability}`} onClick={() => void saveAdapter(capability, adapter.enabled, adapter.pool.filter(item => item !== member))}>Remove</button></li>)}</ul>}
          <div className="member-picker"><select aria-label={`Add ${capability} adapter member`} value={pick} onChange={event => setAdapterPick({ ...adapterPick, [capability]: event.target.value })}><option value="">Choose a model</option>{options.filter(option => !option.label.includes('(alias)')).map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select><button className="button-secondary" type="button" disabled={!pick} onClick={() => { const [providerId, modelId] = pick.split('\x1f'); void saveAdapter(capability, adapter.enabled, [...adapter.pool, { providerId, modelId }]); setAdapterPick({ ...adapterPick, [capability]: '' }) }}>Add to pool</button></div>
        </div>
      })}
    </section>
  </div>
}
