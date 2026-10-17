import { useEffect, useMemo, useState } from 'react'
import { fetchAll } from './ProviderPaging'
import { buildChoices, filterChoices, staleEntries, toggleAllow, type AccessState, type CatalogInput, type Choice } from './keyAccess'

const visibleStep = 40

type CatalogState = { choices: Choice[]; loading: boolean; error: string; retry: () => void }

/** Loads what a key could be granted, once, when `active` first becomes true. */
export function useAccessCatalog(active: boolean, onUnauthorized: () => void): CatalogState {
  const [state, setState] = useState({ choices: [] as Choice[], loading: false, error: '' })
  const [loaded, setLoaded] = useState(false)
  // A failed load leaves `loaded` false; bumping `attempt` (Retry button) refetches.
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    if (!active || loaded) return
    let live = true
    setState(current => ({ ...current, loading: true, error: '' }))
    void (async () => {
      try {
        const [providers, connections, models, disabled, aliases, combos] = await Promise.all([
          fetchAll<CatalogInput['providers'][number]>('providers', onUnauthorized),
          fetchAll<CatalogInput['connections'][number]>('connections', onUnauthorized),
          fetchAll<CatalogInput['models'][number]>('models', onUnauthorized),
          fetchAll<CatalogInput['disabled'][number]>('models/disabled', onUnauthorized),
          fetchAll<CatalogInput['aliases'][number]>('aliases', onUnauthorized),
          fetchAll<CatalogInput['combos'][number]>('combos', onUnauthorized),
        ])
        if (!live) return
        if (!providers || !connections || !models || !disabled || !aliases || !combos) return
        setState({ choices: buildChoices({ providers, connections, models, disabled, aliases, combos }), loading: false, error: '' })
        setLoaded(true)
      } catch (cause) {
        if (live) setState({ choices: [], loading: false, error: cause instanceof Error ? cause.message : 'Could not load models and Combos.' })
      }
    })()
    return () => { live = false }
  }, [active, loaded, attempt, onUnauthorized])
  return { ...state, retry: () => setAttempt(count => count + 1) }
}

type PickerProps = {
  idPrefix: string
  value: AccessState
  catalog: CatalogState
  disabled?: boolean
  onChange: (next: AccessState) => void
}

const kindLabel = { combo: 'Combo', alias: 'Alias', model: 'Model' } as const

/** Restricted/unrestricted choice plus the allowlist picker. Restricted is always an explicit choice. */
export function AccessPicker({ idPrefix, value, catalog, disabled, onChange }: PickerProps) {
  const [term, setTerm] = useState('')
  const [shown, setShown] = useState(visibleStep)
  useEffect(() => { setShown(visibleStep) }, [term])
  const matches = useMemo(() => filterChoices(catalog.choices, term), [catalog.choices, term])
  const stale = useMemo(() => (catalog.loading || catalog.error ? [] : staleEntries(value.allow, catalog.choices)), [value.allow, catalog])
  const granted = new Set(value.allow)

  return <fieldset className="access-picker" disabled={disabled}>
    <legend>Model access</legend>
    <div className="access-modes" role="radiogroup" aria-label="Model access mode">
      <label><input type="radio" name={`${idPrefix}-mode`} checked={!value.restricted} onChange={() => onChange({ restricted: false, allow: value.allow })} /><span><strong>Unrestricted</strong><small>Can use every model, alias and Combo.</small></span></label>
      <label><input type="radio" name={`${idPrefix}-mode`} checked={value.restricted} onChange={() => onChange({ restricted: true, allow: value.allow })} /><span><strong>Restricted</strong><small>Can use only what you select below.</small></span></label>
    </div>

    {value.restricted && <div className="access-allow">
      <p className="access-summary" role="status">
        {value.allow.length === 0 ? <span className="state-tag state-warn">Nothing selected: this key can’t call any model</span> : <span className="state-tag">{value.allow.length} selected</span>}
        <span className="access-note">Allowing a model also admits aliases of it. An alias or Combo is allowed only by its own name.</span>
      </p>

      {stale.length > 0 && <div className="access-stale">
        <p>No longer reachable (disabled, removed or without a connection). Remove or keep them:</p>
        <ul>{stale.map(id => <li key={id}><code>{id}</code><button className="button-ghost" type="button" onClick={() => onChange({ ...value, allow: toggleAllow(value.allow, id, false) })}>Remove</button></li>)}</ul>
      </div>}

      <label className="access-search" htmlFor={`${idPrefix}-search`}>Find a model, alias or Combo</label>
      <input id={`${idPrefix}-search`} type="search" autoComplete="off" value={term} onChange={event => setTerm(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') event.preventDefault() }} placeholder="Name or provider" />

      {catalog.error && <p className="auth-error" role="alert">{catalog.error} <button className="button-ghost" type="button" onClick={catalog.retry}>Retry</button></p>}
      {catalog.loading ? <p className="page-status" role="status">Loading models and Combos…</p> : matches.length === 0 && !catalog.error ? <p className="empty-keys">{catalog.choices.length === 0 ? 'No reachable models yet. Add a provider connection first.' : 'Nothing matches this search.'}</p> : (
        <ul className="access-list" aria-label="Models, aliases and Combos this key can be granted">
          {matches.slice(0, shown).map(choice => <li key={choice.id}>
            <label><input type="checkbox" checked={granted.has(choice.id)} onChange={event => onChange({ ...value, allow: toggleAllow(value.allow, choice.id, event.target.checked) })} /><code>{choice.id}</code><span className="state-tag">{kindLabel[choice.kind]}</span><small>{choice.detail}</small></label>
          </li>)}
        </ul>
      )}
      {matches.length > shown && <button className="button-secondary access-more" type="button" onClick={() => setShown(count => count + visibleStep)}>Show {Math.min(visibleStep, matches.length - shown)} more ({matches.length - shown} hidden)</button>}
    </div>}
  </fieldset>
}
