// How a provider can be connected, as separate choices. The server describes each
// onboarding flow (signInMethods); the manual credential form covers every auth
// mode the provider's spec accepts. Each choice shows only its own fields, so a
// browser sign-in is not mixed with raw token inputs, and a flow that reads the
// identity from the provider does not ask for one.

export type SignInMode = 'browser' | 'device' | 'import'
export type Completion = 'code' | 'redirect-url' | 'token' | 'callback'
export type SignInMethod = { mode: SignInMode; authKind: string; resolvesIdentity: boolean; metaKeys?: string[]; completion?: Completion; loopbackRedirect?: string }
export type ProviderAuth = { auth: string; authModes: string[]; signInMethods?: SignInMethod[] }

export type ConnectMethod = {
  id: string
  kind: SignInMode | 'manual'
  label: string
  authKind: string
  // The identity field is shown only when the method cannot learn it.
  askIdentity: boolean
  metaKeys: string[]
  completion?: Completion
  loopbackRedirect?: string
}

const IMPORT_LABELS: Record<string, string> = { cookie: 'Import cookie', 'api-key': 'Import API key' }
const MANUAL_LABELS: Record<string, string> = { 'api-key': 'API key', cookie: 'Cookie / session', none: 'No authentication', oauth: 'Paste OAuth tokens' }

function flowLabel(method: SignInMethod, provider: string): string {
  if (method.mode === 'browser') return `Sign in with ${provider}`
  if (method.mode === 'device') return `Sign in with ${provider} (device code)`
  return IMPORT_LABELS[method.authKind] ?? 'Import token'
}

// The provider's primary mode comes first so a dual-auth provider whose default is
// an API key still opens on the API key, as the single select did before.
export function connectMethods(provider: ProviderAuth, providerName: string): ConnectMethod[] {
  const modes = provider.authModes.length ? provider.authModes : [provider.auth]
  const manual = (mode: string): ConnectMethod => ({ id: `manual-${mode}`, kind: 'manual', label: MANUAL_LABELS[mode] ?? mode, authKind: mode, askIdentity: true, metaKeys: [] })
  const flows = (provider.signInMethods ?? []).map((method): ConnectMethod => ({
    id: method.mode, kind: method.mode, label: flowLabel(method, providerName), authKind: method.authKind,
    askIdentity: !method.resolvesIdentity, metaKeys: method.metaKeys ?? [], completion: method.completion, loopbackRedirect: method.loopbackRedirect,
  }))
  const [primary, ...rest] = modes
  if (primary === 'oauth') return [...flows, ...modes.map(manual)]
  return [manual(primary), ...flows, ...rest.map(manual)]
}

// The final state of one sign-in attempt. Pending is the attempt itself.
export type AttemptPhase = 'success' | 'expired' | 'cancelled' | 'failed' | 'closed'

// An attempt past its server-issued expiry can no longer complete.
export function attemptExpired(expiresAt: string | undefined, now: number): boolean {
  if (!expiresAt) return false
  const at = Date.parse(expiresAt)
  return Number.isFinite(at) && at <= now
}

// What an error from completing or polling an attempt means. The server answers an
// expired state with the same oauth_state_invalid as a replayed one, so the
// attempt's own expiry decides which the operator is told.
export function phaseForError(code: string | undefined, expiresAt: string | undefined, now: number): 'expired' | 'failed' {
  if (code === 'oauth_expired') return 'expired'
  if (code === 'oauth_state_invalid' && attemptExpired(expiresAt, now)) return 'expired'
  return 'failed'
}

// What the server's answer to a cancel means. Only the server can say an attempt
// ended, so anything it did not confirm leaves the attempt tracked:
//   cancelled  the server ended it; nothing was saved from it.
//   saving     the provider already approved it and a save is in progress; the
//              poll reports whether it landed.
//   closed     the server no longer holds it (expired or already used), so it was
//              not this cancel that ended it and nothing is claimed either way.
//   retry      no answer (network) or any other failure: it may still be open.
export type CancelResult = 'cancelled' | 'saving' | 'closed' | 'retry'
export function cancelResult(status: number | undefined, code: string | undefined): CancelResult {
  if (status !== undefined && status >= 200 && status < 300) return 'cancelled'
  if (status === 409 && code === 'oauth_completing') return 'saving'
  if (status === 400 && code === 'oauth_state_invalid') return 'closed'
  return 'retry'
}
