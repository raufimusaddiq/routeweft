import { FormEvent, useState } from 'react'

type Props = { onSignedIn: (username: string) => void }

export function SignIn({ onSignedIn }: Props) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    try {
      const response = await fetch('/admin/v1/auth/login', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })
      if (!response.ok) {
        setPassword('')
        setError(response.status === 401 ? 'Invalid username or password.' : 'Sign-in failed. Check the service and try again.')
        return
      }
      const session = await response.json() as { username: string }
      setPassword('')
      onSignedIn(session.username)
    } catch {
      setError('Could not reach Routeweft. Check the connection and try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section className="sign-in-panel" aria-labelledby="sign-in-title">
      <div className="sign-in-heading">
        <span className="material-symbols" aria-hidden="true">lock</span>
        <div><h2 id="sign-in-title">Sign in to Routeweft</h2><p>Use your Routeweft admin account.</p></div>
      </div>
      <form onSubmit={submit}>
        <label htmlFor="admin-username">Username</label>
        <input id="admin-username" name="username" autoComplete="username" required value={username} onChange={event => setUsername(event.target.value)} />
        <label htmlFor="admin-password">Password</label>
        <input id="admin-password" name="password" type="password" autoComplete="current-password" required value={password} onChange={event => setPassword(event.target.value)} />
        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="button-primary" type="submit" disabled={submitting}>
          {submitting ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </section>
  )
}
