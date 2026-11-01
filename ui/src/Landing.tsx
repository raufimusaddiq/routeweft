import { LanguageButton } from './LanguageSelector'
import { Mark } from './Mark'

// The public page at the root (#347): what Routeweft is and one way in. It shows only while signed out and
// only at the bare root; every deep link, and any valid session, goes straight to the console. "Sign in" is a
// link to the console's own entry (#overview), where the existing SignIn screen collects the credentials, so
// authentication is untouched and there is no second form. Nothing here is read from the API: the page makes no claim
// about performance, uptime, price or provider coverage.
export function Landing() {
  return (
    <main id="main-content" className="landing" tabIndex={-1} aria-labelledby="landing-title">
      <header className="landing-bar">
        <span className="landing-brand"><Mark /><span>Routeweft</span></span>
        <div className="landing-language">
          <span id="landing-language-label" className="landing-label">Language</span>
          <LanguageButton id="landing-language-button" labelId="landing-language-label" />
        </div>
      </header>
      <div className="landing-body">
        <p className="landing-label">CONTROL PLANE</p>
        <h1 id="landing-title">Routeweft</h1>
        <section className="landing-slab accent-surface" aria-label="Sign in">
          <p className="landing-lead">Routes requests across your provider connections and reports what happened.</p>
          <p className="landing-note">Sign in to manage providers, keys and combos, and to see usage, quota and logs.</p>
          <a className="button-primary landing-action" href="#overview">Sign in</a>
        </section>
      </div>
    </main>
  )
}

export function LandingChecking() {
  return <main id="main-content" className="landing landing-checking" tabIndex={-1}><p className="landing-label" role="status">Checking admin session…</p></main>
}
