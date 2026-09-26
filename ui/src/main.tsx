import React, { useCallback, useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { Combos } from './Combos'
import { EndpointAndKey } from './EndpointAndKey'
import { Overview } from './Overview'
import { Providers } from './Providers'
import { SignIn } from './SignIn'
import { SystemOne } from './SystemOne'
import { Usage } from './Usage'
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
  const [auth, setAuth] = useState<'checking' | 'signed-in' | 'signed-out' | 'unavailable'>('checking')
  const [authError, setAuthError] = useState('')
  const [username, setUsername] = useState('')
  const [signingOut, setSigningOut] = useState(false)
  const menuButton = useRef<HTMLButtonElement>(null)
  const handleUnauthorized = useCallback(() => {
    setUsername('')
    setAuth('signed-out')
  }, [])

  // Only the mobile drawer returns focus to its trigger; on desktop the menu
  // button is hidden, so navigation must keep focus on the activated link.
  function closeDrawer() {
    if (drawerOpen) menuButton.current?.focus()
    setDrawerOpen(false)
  }

  useEffect(() => {
    const updatePage = () => setPage(pages[location.hash.slice(1)] ?? 'Overview')
    addEventListener('hashchange', updatePage)
    return () => removeEventListener('hashchange', updatePage)
  }, [])

  async function checkSession() {
    setAuth('checking')
    setAuthError('')
    try {
      const response = await fetch('/admin/v1/auth/session', { credentials: 'same-origin' })
      if (response.status === 401) {
        setAuth('signed-out')
        return
      }
      if (!response.ok) throw new Error('Could not verify the admin session.')
      const session = await response.json() as { username: string }
      setUsername(session.username)
      setAuth('signed-in')
    } catch {
      setAuthError('Could not reach the Routeweft control API.')
      setAuth('unavailable')
    }
  }

  useEffect(() => { void checkSession() }, [])

  async function signOut() {
    setSigningOut(true)
    setAuthError('')
    try {
      const response = await fetch('/admin/v1/auth/logout', { method: 'POST', credentials: 'same-origin' })
      if (!response.ok) throw new Error('Sign-out failed. Try again.')
      setUsername('')
      setAuth('signed-out')
    } catch {
      setAuthError('Sign-out failed. The admin session may still be active.')
    } finally {
      setSigningOut(false)
    }
  }

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
        <a className="brand" href="#overview" aria-label="Routeweft overview" onClick={closeDrawer}>
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
                    onClick={closeDrawer}
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
            <div className="page-heading-row">
              <div>
                <p className="eyebrow">CONTROL PLANE / {page.toUpperCase()}</p>
                <h1 id="page-title">{page}</h1>
              </div>
              {auth === 'signed-in' && <button className="button-ghost sign-out" type="button" disabled={signingOut} onClick={() => void signOut()}>{signingOut ? 'Signing out…' : `Sign out${username ? ` · ${username}` : ''}`}</button>}
            </div>
            <p className="page-description">Routeweft gateway operations, in one place.</p>
          </div>
          {authError && auth !== 'unavailable' && <p className="auth-error" role="alert">{authError}</p>}
          {auth === 'checking' && <p className="page-status" role="status">Checking admin session…</p>}
          {auth === 'unavailable' && <section className="page-error" role="alert"><p>{authError}</p><button className="button-secondary" type="button" onClick={() => void checkSession()}>Retry</button></section>}
          {auth === 'signed-out' && <SignIn onSignedIn={name => { setUsername(name); setAuth('signed-in'); setAuthError('') }} />}
          {auth === 'signed-in' && page === 'Overview' && <Overview onUnauthorized={handleUnauthorized} />}
          {auth === 'signed-in' && page === 'Endpoint & Key' && <EndpointAndKey onUnauthorized={handleUnauthorized} />}
          {auth === 'signed-in' && page === 'Providers' && <Providers onUnauthorized={handleUnauthorized} />}
          {auth === 'signed-in' && page === 'Combo & Capability Adapter' && <Combos onUnauthorized={handleUnauthorized} />}
          {auth === 'signed-in' && page === 'System One' && <SystemOne onUnauthorized={handleUnauthorized} />}
          {auth === 'signed-in' && page === 'Usage' && <Usage onUnauthorized={handleUnauthorized} />}
          {auth === 'signed-in' && page !== 'Overview' && page !== 'Endpoint & Key' && page !== 'Providers' && page !== 'Combo & Capability Adapter' && page !== 'System One' && page !== 'Usage' && <section className="empty-workspace" aria-labelledby="workspace-title"><span className="material-symbols empty-icon" aria-hidden="true">tune</span><div><h2 id="workspace-title">Workspace shell</h2><p>Operational views are added in their planned increments.</p></div></section>}
        </div>
      </main>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode><App /></React.StrictMode>,
)
