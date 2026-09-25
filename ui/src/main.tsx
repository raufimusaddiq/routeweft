import React, { useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './style.css'

type Theme = 'light' | 'dark' | 'system'

const groups = [
  { label: 'Operate', items: [['Overview', 'dashboard'], ['Endpoint & Key', 'key'], ['Providers', 'hub']] },
  { label: 'Route', items: [['Combo & Capability Adapter', 'alt_route'], ['System One', 'account_tree']] },
  { label: 'Observe', items: [['Usage', 'monitoring'], ['Quota Tracker', 'speed'], ['Token Saver', 'compress']] },
  { label: 'System', items: [['Console Log', 'terminal'], ['Settings', 'settings']] },
] as const

const pages = Object.fromEntries(groups.flatMap(group => group.items.map(([label]) => [slug(label), label])))

function slug(label: string) {
  return label.toLowerCase().replaceAll(/[^a-z0-9]+/g, '-')
}

function readTheme(): Theme {
  try {
    const value = localStorage.getItem('routeweft-theme')
    return value === 'light' || value === 'dark' ? value : 'system'
  } catch {
    return 'system'
  }
}

function App() {
  const [theme, setTheme] = useState<Theme>(readTheme)
  const [page, setPage] = useState(() => pages[location.hash.slice(1)] ?? 'Overview')
  const [drawerOpen, setDrawerOpen] = useState(false)
  const menuButton = useRef<HTMLButtonElement>(null)

  // Only the mobile drawer should return focus to its trigger; on desktop the
  // menu button is hidden, so activating a link must keep focus visible.
  function closeDrawer(restoreFocus = true) {
    setDrawerOpen(open => {
      if (open && restoreFocus) menuButton.current?.focus()
      return false
    })
  }

  useEffect(() => {
    const updatePage = () => setPage(pages[location.hash.slice(1)] ?? 'Overview')
    addEventListener('hashchange', updatePage)
    return () => removeEventListener('hashchange', updatePage)
  }, [])

  useEffect(() => {
    if (theme !== 'system') return
    const preference = matchMedia('(prefers-color-scheme: dark)')
    const updateTheme = () => { document.documentElement.dataset.theme = preference.matches ? 'dark' : 'light' }
    updateTheme()
    preference.addEventListener('change', updateTheme)
    return () => preference.removeEventListener('change', updateTheme)
  }, [theme])

  useEffect(() => {
    if (!drawerOpen) return
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        closeDrawer()
      }
    }
    addEventListener('keydown', closeOnEscape)
    return () => removeEventListener('keydown', closeOnEscape)
  }, [drawerOpen])

  function changeTheme(value: Theme) {
    setTheme(value)
    const resolved = value === 'system'
      ? (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
      : value
    document.documentElement.dataset.theme = resolved
    try {
      localStorage.setItem('routeweft-theme', value)
    } catch {
      // Theme remains usable when browser storage is unavailable.
    }
  }

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">Skip to content</a>
      <button
        className="mobile-menu icon-button"
        type="button"
        ref={menuButton}
        aria-label={drawerOpen ? 'Close navigation' : 'Open navigation'}
        aria-expanded={drawerOpen}
        aria-controls="primary-navigation"
        onClick={() => setDrawerOpen(value => !value)}
      >
        <span className="material-symbols" aria-hidden="true">{drawerOpen ? 'close' : 'menu'}</span>
      </button>
      {drawerOpen && <button className="drawer-backdrop" type="button" tabIndex={-1} aria-label="Close navigation" onClick={() => closeDrawer()} />}
      <aside className={`sidebar${drawerOpen ? ' sidebar-open' : ''}`}>
          <a className="brand" href="#overview" aria-label="Routeweft overview" onClick={() => closeDrawer(false)}>
          <span className="brand-mark" aria-hidden="true">R</span>
          <span className="brand-copy"><strong>Routeweft</strong><small>CONTROL PLANE</small></span>
        </a>
        <nav id="primary-navigation" aria-label="Primary navigation">
          {groups.map(group => (
            <section className="nav-group" key={group.label} aria-labelledby={`nav-${slug(group.label)}`}>
              <h2 id={`nav-${slug(group.label)}`}>{group.label}</h2>
              {group.items.map(([label, icon]) => {
                const active = page === label
                return (
                  <a
                    className={`nav-link${active ? ' nav-link-active' : ''}`}
                    href={`#${slug(label)}`}
                    key={label}
                    aria-current={active ? 'page' : undefined}
                    onClick={() => closeDrawer(false)}
                  >
                    <span className="material-symbols" aria-hidden="true">{icon}</span>
                    <span>{label}</span>
                  </a>
                )
              })}
            </section>
          ))}
        </nav>
        <div className="sidebar-footer">
          <label htmlFor="theme-select">Appearance</label>
          <select id="theme-select" value={theme} onChange={event => changeTheme(event.target.value as Theme)}>
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </select>
          <span className="build-label"><span className="status-dot" /> Service shell</span>
        </div>
      </aside>
      <main id="main-content" className="workspace" tabIndex={-1}>
        <header className="mobile-header">
          <span className="brand-mark" aria-hidden="true">R</span>
          <span>Routeweft</span>
        </header>
        <div className="workspace-inner">
          <div className="page-intro">
            <p className="eyebrow">CONTROL PLANE / {page.toUpperCase()}</p>
            <h1 id="page-title">{page}</h1>
            <p className="page-description">Routeweft gateway operations, in one place.</p>
          </div>
          <section className="empty-workspace" aria-labelledby="workspace-title">
            <span className="material-symbols empty-icon" aria-hidden="true">tune</span>
            <div>
              <h2 id="workspace-title">Workspace shell</h2>
              <p>Operational views are added in their planned increments.</p>
            </div>
          </section>
        </div>
      </main>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode><App /></React.StrictMode>,
)
