import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react'
import { fetchAll } from './ProviderPaging'
import { buildChoices, describeFailure, keysAllowingCombo, memberFromInput, memberIsKnown, memberIsReachable, type AccessState, type CatalogInput, type Choice } from './keyAccess'

type Member = { providerId: string; modelId: string; position: number; selected: boolean }
type Combo = { id: string; name: string; strategy: string; stickyLimit: number; judgeModel: string; fusionEnabled: boolean; members: Member[] }
type StoredKey = { id: string; name: string; access?: AccessState }
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

async function failure(action: string, response: Response) {
  return describeFailure(action, response.status, await responseError(response, ''))
}

export function Combos({ onUnauthorized }: Props) {
  const [combos, setCombos] = useState<Combo[]>([])
  const [choices, setChoices] = useState<Choice[]>([])
  const [keys, setKeys] = useState<StoredKey[]>([])
  const [adapters, setAdapters] = useState<Adapter[]>([])
  const [draft, setDraft] = useState(blankCombo)
  const [editing, setEditing] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [memberPick, setMemberPick] = useState('')
  const [memberError, setMemberError] = useState('')
  const [adapterPick, setAdapterPick] = useState<Record<string, string>>({})

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      // Every list is read to its last page, so nothing past the first 100 rows is hidden.
      const [loadedCombos, models, aliases, disabled, providers, connections, loadedKeys, adapterResponse] = await Promise.all([
        fetchAll<Combo>('combos', onUnauthorized),
        fetchAll<CatalogInput['models'][number]>('models', onUnauthorized),
        fetchAll<CatalogInput['aliases'][number]>('aliases', onUnauthorized),
        fetchAll<CatalogInput['disabled'][number]>('models/disabled', onUnauthorized),
        fetchAll<CatalogInput['providers'][number]>('providers', onUnauthorized),
        fetchAll<CatalogInput['connections'][number]>('connections', onUnauthorized),
        fetchAll<StoredKey>('keys', onUnauthorized),
        fetch('/admin/v1/capability-adapters', { credentials: 'same-origin' }),
      ])
      if (adapterResponse.status === 401) { onUnauthorized(); return }
      if (!adapterResponse.ok) throw new Error(await failure('Could not load capacity adapters', adapterResponse))
      if (!loadedCombos || !models || !aliases || !disabled || !providers || !connections || !loadedKeys) return
      const adapterData = await adapterResponse.json() as { items: Adapter[] }
      setCombos(loadedCombos)
      setChoices(buildChoices({ providers, connections, models, disabled, aliases, combos: [] }, { all: true }).filter(choice => choice.kind !== 'combo'))
      setKeys(loadedKeys)
      setAdapters(adapterData.items)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load Combo configuration.')
    } finally {
      setLoading(false)
    }
  }, [onUnauthorized])

  useEffect(() => { void load() }, [load])

  // Members and adapter pool entries are picked by typing; models and aliases both qualify.
  const options = choices

  async function send(path: string, method: string, body?: unknown) {
    const response = await fetch(`/admin/v1${path}`, { method, credentials: 'same-origin', headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) })
    if (response.status === 401) { onUnauthorized(); throw new Error('Admin session expired.') }
    if (!response.ok) throw new Error(await failure('Combo action failed', response))
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
    setMemberError('')
  }

  function startEdit(combo: Combo) {
    setDraft({ id: combo.id, name: combo.name, strategy: combo.strategy || 'fallback', stickyLimit: combo.stickyLimit || 1, judgeModel: combo.judgeModel, fusionEnabled: combo.fusionEnabled, members: combo.members.map(member => ({ ...member })) })
    setEditing(true)
  }

  function addMember() {
    const member = memberFromInput(memberPick, choices)
    if (!member) { setMemberError(`“${memberPick.trim()}” is not a known model or alias. Pick one from the list.`); return }
    setMemberError('')
    if (draft.members.some(item => item.providerId === member.providerId && item.modelId === member.modelId)) { setMemberError('That member is already in this Combo.'); return }
    setDraft({ ...draft, members: [...draft.members, { ...member, position: draft.members.length, selected: true }] })
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
    const original = combos.find(combo => combo.id === draft.id)
    const renamedFrom = original && original.name !== draft.name.trim() ? keysAllowingCombo(original.name, keys) : []
    if (original && renamedFrom.length > 0 && !window.confirm(`Renaming “${original.name}” to “${draft.name.trim()}”: these restricted keys list the old name and will lose access: ${renamedFrom.join(', ')}. Their allowlists are not edited. Rename anyway?`)) return
    const unknown = draft.members.filter(member => !memberIsKnown(member, choices))
    if (unknown.length > 0 && !window.confirm(`Not a known, enabled model or alias: ${unknown.map(member => `${member.providerId}/${member.modelId}`).join(', ')}. The server may reject this Combo. Save anyway?`)) return
    const body = { name: draft.name.trim(), strategy: draft.strategy, stickyLimit: Number(draft.stickyLimit) || 1, judgeModel: draft.judgeModel.trim(), fusionEnabled: draft.fusionEnabled, members: draft.members.map((member, order) => ({ providerId: member.providerId, modelId: member.modelId, position: order, selected: member.selected })) }
    await action('combo', draft.id ? 'Combo updated.' : 'Combo created.', async () => {
      await send(draft.id ? `/combos/${encodeURIComponent(draft.id)}` : '/combos', draft.id ? 'PUT' : 'POST', body)
      setEditing(false); setDraft(blankCombo)
    })
  }

  async function deleteCombo(combo: Combo) {
    const users = keysAllowingCombo(combo.name, keys)
    const warning = users.length > 0 ? `\n\nThese restricted keys list this Combo: ${users.join(', ')}. After deletion that entry matches nothing, so those keys lose it. Their allowlists are not edited.` : ''
    if (!window.confirm(`Delete Combo “${combo.name}”?${warning}`)) return
    await action(combo.id, 'Combo deleted.', () => send(`/combos/${encodeURIComponent(combo.id)}`, 'DELETE').then(() => { if (draft.id === combo.id) { setEditing(false); setDraft(blankCombo) } }))
  }

  async function saveAdapter(capability: string, enabled: boolean, pool: Adapter['pool']) {
    await action(`adapter-${capability}`, `${capability} adapter saved.`, () => send(`/capability-adapters/${encodeURIComponent(capability)}`, 'PUT', { enabled, pool }).then(() => undefined))
  }

  function adapterFor(capability: string) {
    return adapters.find(item => item.capability === capability) || { capability, enabled: false, pool: [] }
  }

  return <div className="combos-content" aria-busy={loading}>
    <datalist id="combo-member-choices">{choices.slice(0, 500).map(choice => <option key={`${choice.kind}-${choice.id}`} value={choice.id}>{choice.detail}</option>)}</datalist>
    <datalist id="combo-model-choices">{choices.filter(choice => choice.kind === 'model').slice(0, 500).map(choice => <option key={choice.id} value={choice.id} />)}</datalist>
    <div className="combos-toolbar"><span className="count-label">{loading ? 'Loading…' : `${combos.length} Combo${combos.length === 1 ? '' : 's'}`}</span><button className="button-primary" type="button" onClick={startCreate}>New Combo</button><button className="button-secondary" type="button" disabled={loading} onClick={() => void load()}>Refresh</button></div>
    {error && <p className="auth-error" role="alert">{error}</p>}{notice && <p className="inline-notice" role="status">{notice}</p>}

    <div className="combos-layout">
      <section className="detail-panel combo-list-panel" aria-label="Combo list">
        {combos.length === 0 && !loading ? <p className="empty-keys">No Combos yet.</p> : <div className="combo-list">{combos.map(combo => <article className={`combo-row${draft.id === combo.id ? ' combo-row-selected' : ''}`} key={combo.id}>
          <div className="combo-main"><strong>{combo.name}</strong><small>{combo.members.filter(member => member.selected).length} selected · {combo.members.length} member{combo.members.length === 1 ? '' : 's'}</small></div>
          <span className="state-tag">{combo.strategy || 'fallback'}</span>
          {combo.fusionEnabled && <span className="state-tag state-good">Fusion</span>}
          {(() => { const users = keysAllowingCombo(combo.name, keys); return users.length > 0 ? <span className="state-tag combo-inuse" title={users.join(', ')}>Allowed by {users.length} key{users.length === 1 ? '' : 's'}</span> : null })()}
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
          <div className="member-picker"><input aria-label="Add Combo member" list="combo-member-choices" autoComplete="off" placeholder="provider/model or alias name" value={memberPick} onChange={event => { setMemberPick(event.target.value); setMemberError('') }} onKeyDown={event => { if (event.key === 'Enter') { event.preventDefault(); addMember() } }} /><button className="button-secondary" type="button" disabled={!memberPick.trim()} onClick={addMember}>Add member</button></div>
          {memberError && <p className="auth-error" role="alert">{memberError}</p>}
          <ol className="member-list">{draft.members.map((member, index) => <li className={`member-row${member.selected ? '' : ' member-row-deselected'}`} key={`${member.providerId}/${member.modelId}`}>
            <span className="member-order">{index + 1}</span>
            <code>{member.providerId} / {member.modelId}</code>
            <span className={`state-tag${member.selected ? ' state-good' : ' state-warn'}`}>{member.selected ? 'Selected' : 'Deselected'}</span>
            {!memberIsKnown(member, choices) ? <span className="state-tag state-warn">Unknown or disabled</span> : !memberIsReachable(member, choices) ? <span className="state-tag state-warn">No enabled connection</span> : null}
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
          <div className="member-picker"><input aria-label={`Add ${capability} adapter member`} list="combo-model-choices" autoComplete="off" placeholder="provider/model" value={pick} onChange={event => setAdapterPick({ ...adapterPick, [capability]: event.target.value })} /><button className="button-secondary" type="button" disabled={!pick.trim()} onClick={() => { const member = memberFromInput(pick, choices.filter(choice => choice.kind === 'model')); if (!member) { setError(`“${pick.trim()}” is not a known model.`); return } void saveAdapter(capability, adapter.enabled, [...adapter.pool, member]); setAdapterPick({ ...adapterPick, [capability]: '' }) }}>Add to pool</button></div>
        </div>
      })}
    </section>
  </div>
}
