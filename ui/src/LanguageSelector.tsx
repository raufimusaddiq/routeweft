import { useEffect, useRef, useState, useSyncExternalStore, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { getLocale, setLocale, subscribeLocale } from './i18n'
import { LOCALES, LOCALE_INFO, type Locale } from './locale'

// The display-language control (#279, U10): the oracle's header flag button and its
// "Select Language" grid of every supported locale (LanguageSwitcher.js:92-190), as one
// button that opens an accessible dialog. The names are each language's own and are not
// translated (data-i18n-skip); the dialog's title and Close are, when the catalog has them.

const FOCUSABLE = 'button:not([disabled])'

function LanguageDialog({ onClose }: { onClose: () => void }) {
  const locale = useSyncExternalStore(subscribeLocale, getLocale)
  const [pending, setPending] = useState<Locale | null>(null)
  const [error, setError] = useState('')
  const panel = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const active = panel.current?.querySelector<HTMLButtonElement>('[aria-current="true"]')
    ;(active ?? panel.current?.querySelector<HTMLButtonElement>(FOCUSABLE))?.focus()
  }, [])

  async function choose(next: Locale) {
    if (pending) return
    if (next === locale) { onClose(); return }
    setPending(next); setError('')
    try {
      await setLocale(next)
      onClose()
    } catch {
      setError(`Could not load the ${LOCALE_INFO[next].name} translations. The language was not changed.`)
      setPending(null)
    }
  }

  function keyDown(event: KeyboardEvent) {
    if (event.key === 'Escape') { event.stopPropagation(); onClose(); return }
    if (event.key !== 'Tab') return
    const items = Array.from(panel.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])
    if (items.length === 0) return
    const first = items[0]
    const last = items[items.length - 1]
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }

  return createPortal(
    <div className="language-overlay" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}>
      <div className="language-dialog" ref={panel} role="dialog" aria-modal="true" aria-labelledby="language-title" onKeyDown={keyDown}>
        <div className="language-heading">
          <h2 id="language-title">Select Language</h2>
          <button className="icon-button" type="button" aria-label="Close" onClick={onClose}><span className="material-symbols" aria-hidden="true">close</span></button>
        </div>
        {error && <p className="auth-error" role="alert">{error}</p>}
        <ul className="language-grid" data-i18n-skip aria-label="Languages" aria-busy={pending !== null}>
          {LOCALES.map(code => {
            const info = LOCALE_INFO[code]
            const active = code === locale
            return <li key={code}>
              <button className={`language-option${active ? ' language-option-active' : ''}`} type="button" lang={code} aria-current={active ? 'true' : undefined} disabled={pending !== null} onClick={() => void choose(code)}>
                <span className="language-flag" aria-hidden="true">{info.flag}</span>
                <span className="language-name">{info.name}</span>
                {active && <><span className="material-symbols language-check" aria-hidden="true">check</span><span className="sr-only"> (current language)</span></>}
                {pending === code && <span className="sr-only"> (loading)</span>}
              </button>
            </li>
          })}
        </ul>
      </div>
    </div>,
    document.body,
  )
}

/** The button that shows the current language and opens the dialog; focus returns to it on close. */
export function LanguageButton({ className = '', id, labelId }: { className?: string; id: string; labelId?: string }) {
  const locale = useSyncExternalStore(subscribeLocale, getLocale)
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  const info = LOCALE_INFO[locale]
  function close() { setOpen(false); trigger.current?.focus() }
  return <>
    <button id={id} className={`language-button ${className}`.trim()} type="button" ref={trigger} aria-labelledby={labelId ? `${labelId} ${id}` : undefined} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(true)} data-i18n-skip>
      <span aria-hidden="true">{info.flag}</span>
      <span>{info.name}</span>
    </button>
    {open && <LanguageDialog onClose={close} />}
  </>
}
