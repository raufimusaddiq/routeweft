// The Codex onboarding golden path (#215), as pure functions the Providers page
// calls: the sign-in window, what a save did, bulk import and Reauthenticate.
// The account's quota after login is quotaView.quotaLine. Nothing here holds or
// renders a credential.

import type { SignInMethod } from './signIn'

// --- Sign-in window -------------------------------------------------------

// The part of a browser window the sign-in uses. window.open returns one, or
// null when a popup blocker refused it.
export type SignInWindow = { closed: boolean; opener: unknown; close(): void; location: { replace(url: string): void } }
export type WindowOpener = (url: string, target: string, features: string) => SignInWindow | null

// Opens an empty window for the provider's page. It has to happen in the click
// itself: browsers block a window opened after an await, and the authorize URL
// is known only after /start answers, so the window is opened first and sent
// there afterwards (sendSignInWindow). The window loses its opener so the
// provider's page cannot navigate this dashboard. null means blocked; the
// operator then opens the link by hand and pastes back as before.
export function openSignInWindow(open: WindowOpener): SignInWindow | null {
  let popup: SignInWindow | null = null
  try { popup = open('about:blank', 'routeweft-sign-in', 'popup,width=600,height=700') } catch { popup = null }
  if (!popup) return null
  try { popup.opener = null } catch { /* the window still works without it */ }
  return popup
}

// Only a web address is sent to the window or offered as a link: an authorize
// URL is the provider's page, never script or data.
export function signInURL(value: string | undefined): string | null {
  if (!value) return null
  try {
    const url = new URL(value)
    return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : null
  } catch { return null }
}

// Sends the window opened in the click to the provider's page. 'blocked' means
// no window is showing it (refused, closed by the operator, or a bad URL), so the
// panel says to use the link.
export function sendSignInWindow(popup: SignInWindow | null, url: string): 'opened' | 'blocked' {
  const target = signInURL(url)
  if (!popup || popup.closed || !target) { closeSignInWindow(popup); return 'blocked' }
  try { popup.location.replace(target); return 'opened' } catch { return 'blocked' }
}

// Closes the sign-in window once the attempt is over. Best effort: a window still
// on about:blank (a start that failed) closes, but once it is on the provider's
// origin it no longer has this dashboard as opener, so the browser ignores the
// request (that is the price of openSignInWindow cutting the opener) and the
// operator closes it; the result panel says so.
export function closeSignInWindow(popup: SignInWindow | null): void {
  if (!popup || popup.closed) return
  try { popup.close() } catch { /* already gone */ }
}

// --- What a save did --------------------------------------------------------

export type SaveOutcome = 'added' | 'updated' | 'reauthenticated'
export type Onboarded = { id: string; name: string; identity: string; outcome?: SaveOutcome | string; plan?: string }

const account = (connection: Onboarded) => connection.identity || connection.name

// Says whether the sign-in added an account, renewed a stored duplicate (the
// reference's rule: the same Codex account updates its row in place), or
// renewed the connection the operator chose.
export function outcomeMessage(connection: Onboarded): string {
  switch (connection.outcome) {
    case 'added': return `Signed in as ${account(connection)}. Added as a new connection.`
    case 'updated': return `Signed in as ${account(connection)}, which was already connected as “${connection.name}”. That connection now uses the new credential; its name, priority, proxy and enabled state are unchanged, and no duplicate was added.`
    case 'reauthenticated': return `Reauthenticated “${connection.name}” as ${account(connection)}. The new credential replaced the old one.`
    default: return `Signed in as ${account(connection)}; the connection is saved.`
  }
}

// Shown before a sign-in starts, where a sign-in can update a stored account
// instead of adding one. Only Codex has that rule here (#241).
export function duplicateNote(providerId: string): string | null {
  if (providerId !== 'codex') return null
  return 'Signing in with an account that is already connected updates that connection instead of adding a second one. To renew one connection on purpose, use Reauthenticate on it below.'
}

// --- Reauthenticate ---------------------------------------------------------

// A connection can be reauthenticated when the provider has a browser sign-in
// and the connection holds an OAuth credential that sign-in would produce. Where
// the browser sign-ins are variants (Kiro's Google and GitHub), only a connection
// one of them made qualifies: the server renews it with that variant and refuses
// any other (a Kiro Builder ID connection), so no button is offered for it (#219).
export function canReauthenticate(methods: SignInMethod[] | undefined, connection: { authKind: string; signInMethod?: string }): boolean {
  return reauthMethod(methods, connection) !== undefined
}

// The browser method a Reauthenticate of this connection uses, if any.
export function reauthMethod(methods: SignInMethod[] | undefined, connection: { authKind: string; signInMethod?: string }): SignInMethod | undefined {
  if (connection.authKind !== 'oauth') return undefined
  const browser = (methods ?? []).filter(method => method.mode === 'browser' && method.authKind === 'oauth')
  if (!browser.some(method => method.variant)) return browser[0]
  return browser.find(method => method.variant === connection.signInMethod)
}

// --- Bulk import ------------------------------------------------------------

export type BulkParse = { accounts: unknown[] } | { error: string }

// Reads the pasted export as the reference's dialog does (BulkImportCodexModal.js
// normalizeToArray): an array, an object with an accounts array, or one account
// object. A parse error is described without quoting the text: JSON.parse's own
// message can include part of the input, which here is a token.
export function parseBulkInput(text: string): BulkParse {
  const trimmed = text.trim()
  if (!trimmed) return { error: 'Paste the exported accounts first.' }
  let parsed: unknown
  try { parsed = JSON.parse(trimmed) } catch { return { error: 'This is not valid JSON. Check for a missing comma, bracket or quote.' } }
  let accounts: unknown[] | null = null
  if (Array.isArray(parsed)) accounts = parsed
  else if (parsed && typeof parsed === 'object') {
    const wrapped = (parsed as { accounts?: unknown }).accounts
    accounts = Array.isArray(wrapped) ? wrapped : [parsed]
  }
  if (!accounts || accounts.length === 0) return { error: 'No accounts were found in the pasted JSON.' }
  return { accounts }
}

export type BulkItem = { index: number; ok: boolean; id?: string; outcome?: string; expired?: boolean; refreshable?: boolean; error?: string }
export type BulkResponse = { total?: number; success: number; failed: number; results: BulkItem[] }
export type BulkRow = { item: number; text: string; ok: boolean }

// One line per pasted account, numbered from 1 as the operator counts them, plus
// a headline. Only the server's outcome and error text appear, never an item.
export function bulkSummary(response: BulkResponse): { headline: string; rows: BulkRow[] } {
  const added = response.results.filter(item => item.ok && item.outcome !== 'updated').length
  const updated = response.results.filter(item => item.ok && item.outcome === 'updated').length
  const parts = [`${added} added`]
  if (updated) parts.push(`${updated} already connected and updated`)
  if (response.failed) parts.push(`${response.failed} failed`)
  const stale = response.results.filter(item => item.ok && item.expired).length
  if (stale) parts.push(`${stale} saved with an expired credential`)
  // An item stored with an already-expired credential says so (#218): it was
  // saved, as the reference saves it, but may not serve requests.
  const expired = (item: BulkItem) => !item.expired ? '' : item.refreshable ? '; its credential had expired and is renewed with its refresh token before use' : '; its credential had expired and has no refresh token, so it cannot serve requests until replaced'
  const rows = response.results.map(item => ({
    item: item.index + 1,
    ok: item.ok,
    text: item.ok ? (item.outcome === 'updated' ? 'already connected; its connection took the new credential' : 'added as a new connection') + expired(item) : `failed: ${item.error || 'not imported'}`,
  }))
  return { headline: `${parts.join(', ')}.`, rows }
}
