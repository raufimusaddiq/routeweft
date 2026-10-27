// How a provider can be connected, as separate choices. The server describes each
// onboarding flow (signInMethods); the manual credential form covers every auth
// mode the provider's spec accepts. Each choice shows only its own fields, so a
// browser sign-in is not mixed with raw token inputs, and a flow that reads the
// identity from the provider does not ask for one.

export type SignInMode = 'browser' | 'device' | 'import'
export type Completion = 'code' | 'redirect-url' | 'token' | 'callback'
export type SignInMethod = { mode: SignInMode; authKind: string; resolvesIdentity: boolean; infersIdentity?: boolean; variant?: string; metaKeys?: string[]; completion?: Completion; loopbackRedirect?: string; redirectUri?: string; redirectUnconfigured?: boolean }
export type ProviderAuth = { id?: string; auth: string; authModes: string[]; signInMethods?: SignInMethod[]; bulkImport?: boolean }

export type ConnectMethod = {
  id: string
  kind: SignInMode | 'manual' | 'bulk'
  label: string
  authKind: string
  // The identity field is required only when the method cannot learn it, and
  // offered as optional when the method usually learns it (#216).
  askIdentity: boolean
  identityOptional?: boolean
  metaKeys: string[]
  // The auth method a browser method is fixed to (Kiro's google or github), sent
  // as meta.authMethod; the operator is offered no field for it (#219).
  variant?: string
  fixedMeta?: Record<string, string>
  completion?: Completion
  loopbackRedirect?: string
  redirectUri?: string
  redirectUnconfigured?: boolean
}

const IMPORT_LABELS: Record<string, string> = { cookie: 'Import cookie', 'api-key': 'Import API key' }
// A provider whose import is a named alternative to its browser sign-in.
const PROVIDER_IMPORT_LABELS: Record<string, string> = { gitlab: 'Import personal access token' }
const MANUAL_LABELS: Record<string, string> = { 'api-key': 'API key', cookie: 'Cookie / session', none: 'No authentication', oauth: 'Paste OAuth tokens' }

// The account provider a browser variant signs in with (Kiro's social login).
const VARIANT_LABELS: Record<string, string> = { google: 'Google', github: 'GitHub' }

export function variantLabel(variant: string): string {
  return VARIANT_LABELS[variant] ?? variant
}

function flowLabel(method: SignInMethod, provider: string, id?: string): string {
  if (method.mode === 'browser' && method.variant) return `Sign in with ${variantLabel(method.variant)}`
  if (method.mode === 'browser') return `Sign in with ${provider}`
  if (method.mode === 'device') return `Sign in with ${provider} (device code)`
  return (id && PROVIDER_IMPORT_LABELS[id]) || IMPORT_LABELS[method.authKind] || 'Import token'
}

// The provider's primary mode comes first so a dual-auth provider whose default is
// an API key still opens on the API key, as the single select did before.
export function connectMethods(provider: ProviderAuth, providerName: string): ConnectMethod[] {
  const modes = provider.authModes.length ? provider.authModes : [provider.auth]
  const manual = (mode: string): ConnectMethod => ({ id: `manual-${mode}`, kind: 'manual', label: MANUAL_LABELS[mode] ?? mode, authKind: mode, askIdentity: true, metaKeys: [] })
  const flows = (provider.signInMethods ?? []).map((method): ConnectMethod => ({
    id: method.variant ? `${method.mode}-${method.variant}` : method.mode, kind: method.mode, label: flowLabel(method, providerName, provider.id), authKind: method.authKind,
    variant: method.variant, fixedMeta: method.variant ? { authMethod: method.variant } : undefined,
    askIdentity: !method.resolvesIdentity && !method.infersIdentity, identityOptional: Boolean(method.infersIdentity) && !method.resolvesIdentity,
    metaKeys: method.metaKeys ?? [], completion: method.completion, loopbackRedirect: method.loopbackRedirect,
    redirectUri: method.redirectUri, redirectUnconfigured: method.redirectUnconfigured,
  }))
  // Bulk import is its own journey (many exported accounts at once), offered only
  // where the server says its endpoint takes the provider.
  if (provider.bulkImport) flows.push({ id: 'bulk', kind: 'bulk', label: 'Bulk import accounts', authKind: 'oauth', askIdentity: false, metaKeys: [] })
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

// What an error from completing or polling an attempt means, from the server's
// answer alone, never from the clock: a save can be under way when the attempt's
// time runs out, so the time limit says nothing about whether anything was saved.
//   expired  the provider reported the code expired; the server ended the attempt
//            without saving.
//   closed   the server no longer holds the attempt (it expired or was already
//            used). That does not say whether an earlier request saved it.
//   failed   any other answer from the server, whose message says what happened.
// No answer at all is not an outcome: the caller keeps the attempt.
export function phaseForError(code: string | undefined): 'expired' | 'closed' | 'failed' {
  if (code === 'oauth_expired') return 'expired'
  if (code === 'oauth_state_invalid') return 'closed'
  return 'failed'
}

// Every error code the callback, poll and cancel endpoints answer, and whether
// the attempt is still open on the server afterwards, per endpoint
// (internal/control/api/oauth.go; signIn.test.ts reads that file and fails on a
// code missing here). The same code can differ by endpoint: a callback consumes
// its state before the exchange, so a failed save ends it, while a device poll
// releases an attempt whose save persisted nothing, so it stays open.
export type SignInStep = 'callback' | 'poll' | 'cancel'
type AttemptAfter = 'open' | 'ended'
export const SIGN_IN_CODES: Record<string, Partial<Record<SignInStep, AttemptAfter>>> = {
  // Refused before the attempt is read.
  cross_origin: { callback: 'open', poll: 'open', cancel: 'open' },
  unauthenticated: { callback: 'open', poll: 'open', cancel: 'open' },
  invalid_request: { callback: 'open', poll: 'open', cancel: 'open' },
  credentials_unavailable: { callback: 'open', poll: 'open' },
  oauth_unsupported: { callback: 'open', poll: 'open' },
  oauth_code_missing: { callback: 'open' },
  oauth_state_mismatch: { callback: 'open' },
  // The provider could not be reached; the attempt is kept for the next poll.
  oauth_poll_failed: { poll: 'open' },
  // The server no longer holds the attempt (unknown, expired, used, foreign).
  oauth_state_invalid: { callback: 'ended', poll: 'ended', cancel: 'ended' },
  // The provider ended it.
  oauth_denied: { callback: 'ended', poll: 'ended' },
  oauth_expired: { poll: 'ended' },
  oauth_sign_in_rejected: { poll: 'ended' },
  // Consumed, then the redirect could not be resolved.
  oauth_redirect_unconfigured: { callback: 'ended' },
  // A Reauthenticate (#215) consumed its state, then found another account
  // signed in, or its connection deleted meanwhile; nothing was saved.
  oauth_account_mismatch: { callback: 'ended' },
  connection_not_found: { callback: 'ended' },
  // Save failures: a callback already consumed the state; a poll released it.
  oauth_exchange_failed: { callback: 'ended', poll: 'open' },
  oauth_identity_unresolved: { callback: 'ended', poll: 'open' },
  invalid_auth_kind: { callback: 'ended', poll: 'open' },
  connection_save_failed: { callback: 'ended', poll: 'open' },
  // Saved but not published: the attempt is finished either way.
  connection_publish_failed: { callback: 'ended', poll: 'ended' },
  // A poll is saving the approval; the poll reports the result.
  oauth_completing: { cancel: 'open' },
}

// Whether a failed callback or poll left the attempt open on the server, so the
// dashboard keeps tracking it instead of reporting an outcome. No answer (a
// network failure, or a response without an API error code) may or may not have
// landed, and an unknown code is not a known end, so both keep it.
export function attemptStillOpen(code: string | undefined, step: SignInStep): boolean {
  if (code === undefined || !Object.hasOwn(SIGN_IN_CODES, code)) return true
  return SIGN_IN_CODES[code][step] !== 'ended'
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
