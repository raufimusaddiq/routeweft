// A connection's validation lifecycle as the read model reports it (#220). The
// four things an operator needs to tell apart stay apart: whether the provider
// accepted the credential (testStatus), whether a model answered the last check
// (lastTestedAt, modelsAvailable), what quota was observed (plan,
// quotaObservedAt) and the provider's readiness, which is the catalog's and
// shown elsewhere. Nothing here is credential material.
export type TestStatus = 'untested' | 'active' | 'failed'

export type ConnectionLifecycle = {
  testStatus?: TestStatus
  lastTestedAt?: string
  lastError?: string
  modelsAvailable?: number
  authOrigin?: string
  expiresAt?: string
  plan?: string
  quotaObservedAt?: string
}

export const TEST_STATUS_LABEL: Record<TestStatus, string> = { untested: 'Untested', active: 'Credential accepted', failed: 'Check failed' }

export function testStatusOf(connection: ConnectionLifecycle): TestStatus {
  return connection.testStatus === 'active' || connection.testStatus === 'failed' ? connection.testStatus : 'untested'
}

// The state-tag tone: color is for state only (UI_STYLE).
export function testStatusTone(status: TestStatus) {
  return status === 'active' ? ' state-good' : status === 'failed' ? ' state-bad' : ''
}

const ORIGIN_LABEL: Record<string, string> = { manual: 'entered by hand', oauth: 'browser sign-in', device: 'device sign-in', import: 'imported' }

// relative renders a time against now in either direction.
export function relative(iso: string, now: number) {
  const stamp = new Date(iso).getTime()
  if (Number.isNaN(stamp)) return ''
  const seconds = Math.round((stamp - now) / 1000)
  const size = Math.abs(seconds)
  const amount = size < 90 ? `${size} s` : size < 5400 ? `${Math.round(size / 60)} min` : size < 129600 ? `${Math.round(size / 3600)} h` : `${Math.round(size / 86400)} d`
  return seconds < 0 ? `${amount} ago` : `in ${amount}`
}

// MiMo Desktop session renewal (#299): whether quota reads may read the account
// session from MiMo Desktop on the server, and when one last did. A time only.
export type DesktopRenewal = { desktopSessionRenewal?: boolean; desktopSessionReadAt?: string }

export function desktopRenewalLine(connection: DesktopRenewal, now = Date.now()) {
  const read = connection.desktopSessionReadAt ? relative(connection.desktopSessionReadAt, now) : ''
  if (connection.desktopSessionRenewal) return `Quota reads may renew the account session from MiMo Desktop on this server${read ? ` · last read ${read}` : ''}`
  return read ? `Session last read from MiMo Desktop ${read}; renewal is off` : ''
}

// The confirmation that is the operator's consent to turn renewal on.
export function desktopRenewalConsent(name: string) {
  return `Allow quota reads of “${name}” to read MiMo Desktop’s account session on this server when the connection has none or its session stops working? It is read at most every 30 minutes, sealed into this connection and never shown. You can turn it off at any time.`
}

// lifecycleLine is the one-line summary under a connection row.
export function lifecycleLine(connection: ConnectionLifecycle, now = Date.now()) {
  const parts: string[] = []
  parts.push(connection.lastTestedAt ? `Checked ${relative(connection.lastTestedAt, now)}` : 'Never checked')
  if (connection.modelsAvailable !== undefined && connection.modelsAvailable !== null) parts.push(`${connection.modelsAvailable} model${connection.modelsAvailable === 1 ? '' : 's'} listed`)
  if (connection.expiresAt) {
    const when = relative(connection.expiresAt, now)
    if (when) parts.push(new Date(connection.expiresAt).getTime() <= now ? `Token expired ${when}` : `Token expires ${when}`)
  }
  if (connection.authOrigin && ORIGIN_LABEL[connection.authOrigin]) parts.push(`Credential ${ORIGIN_LABEL[connection.authOrigin]}`)
  if (connection.plan) parts.push(`Plan ${connection.plan}`)
  if (connection.quotaObservedAt) parts.push(`Quota seen ${relative(connection.quotaObservedAt, now)}`)
  return parts.join(' · ')
}

// relinkPatch is the PATCH body for a connection edit: only the fields that
// changed, and a replacement credential only when one was typed, in the field
// the connection's auth kind uses. A blank credential keeps the stored one.
export function relinkPatch(connection: { name: string; identity: string; authKind: string }, edit: { name: string; identity: string; credential: string }) {
  const patch: { name?: string; identity?: string; secret?: { accessToken?: string; cookie?: string } } = {}
  const name = edit.name.trim(), identity = edit.identity.trim(), credential = edit.credential.trim()
  if (name && name !== connection.name) patch.name = name
  if (identity && identity !== connection.identity) patch.identity = identity
  if (credential) patch.secret = connection.authKind === 'cookie' ? { cookie: credential } : { accessToken: credential }
  return patch
}

// canRelink reports whether a credential can be typed in to replace the stored
// one. A sign-in connection is renewed by Reauthenticate instead, and a no-auth
// connection has nothing to replace.
export function canRelink(connection: { authKind: string }) {
  return connection.authKind === 'api-key' || connection.authKind === 'cookie'
}
