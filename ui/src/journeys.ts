// Provider-aware browser sign-in journeys (#216). The sign-in screen is shared;
// what differs per provider is said here: where the browser returns, what the
// operator copies back, what to set up first (a GitLab application, a Google
// project, a free loopback port) and what was checked after sign-in. Pure
// functions over the server's read model, so the screen cannot promise a step
// the server does not take. Nothing here holds or renders a credential.

import type { Completion } from './signIn'

// The part of a browser method the journey reads (signIn.ConnectMethod).
export type JourneyMethod = {
  completion?: Completion
  loopbackRedirect?: string
  redirectUri?: string
  redirectUnconfigured?: boolean
}

export type Journey = {
  // Shown before the sign-in starts: what to set up or know first.
  before: string[]
  // Shown once it started: how to finish it.
  hint: string
  // The paste-back field's label, and whether it is masked: a field that takes
  // a bare token or code is; an address is shown so it can be checked.
  pasteLabel: string
  masked: boolean
  // Operator fields the start cannot do without (GitLab's client id), so the
  // button waits for them instead of the provider refusing an empty one.
  requiredMeta: string[]
}

const PASTE_LABEL: Record<Completion, string> = { code: 'Code from the provider', 'redirect-url': 'Address from the browser', token: 'Token, cookie or key', callback: 'Address from the browser' }

// A pinned loopback redirect is a port on the operator's own computer. Routeweft
// never listens there (a server cannot receive a request for the browser's own
// localhost), so the operator is told the page will not load and why.
export function loopbackHint(redirect: string): string {
  return `Sign in on the provider page. Your browser is then sent to ${redirect}. That address is on the computer running this browser, not on the Routeweft server, so the page will not load; that is expected. Copy the full address from the address bar and paste it below.`
}

const UNCONFIGURED = 'Set the public origin first: this provider returns your browser to an address on this server, and none is configured, so the sign-in cannot start.'

// Where the browser lands after the provider, for a method that returns to this
// server's public origin, with the query the provider adds.
function returnsTo(method: JourneyMethod, query: string): string {
  return method.redirectUri ? `${method.redirectUri}?${query}…` : 'this server’s callback address'
}

function generic(method: JourneyMethod): Journey {
  const completion = method.completion ?? 'code'
  let hint: string
  if (method.loopbackRedirect && completion === 'redirect-url') hint = loopbackHint(method.loopbackRedirect)
  else if (completion === 'code' && method.redirectUri) hint = `Sign in on the provider page. Your browser then returns to ${returnsTo(method, 'code=')}. Copy that full address from the address bar and paste it below; the code alone also works.`
  else if (completion === 'code') hint = 'Sign in on the provider page, then copy the code it shows and paste it below.'
  else if (completion === 'token') hint = 'Paste the token, cookie or key the provider issued. It is validated before it is stored.'
  else hint = 'Sign in on the provider page. Your browser then goes to a local address that will not load; copy that full address and paste it below.'
  return {
    before: method.redirectUnconfigured ? [UNCONFIGURED] : [],
    hint,
    pasteLabel: PASTE_LABEL[completion],
    masked: completion === 'code' || completion === 'token',
    requiredMeta: [],
  }
}

// The journey for one provider's browser method. meta is the operator's current
// input (Zed's port names the address its browser is sent to).
export function browserJourney(provider: string, method: JourneyMethod, meta: Record<string, string> = {}): Journey {
  const base = generic(method)
  const before = (...notes: string[]) => [...base.before, ...notes]
  switch (provider) {
    case 'claude':
      return { ...base, hint: 'Approve the sign-in on claude.ai. Anthropic’s console then shows an authentication code, which ends in #state. Copy all of it and paste it below.', pasteLabel: 'Code from Claude' }
    case 'xai': {
      const redirect = method.loopbackRedirect || 'http://127.0.0.1:56121/callback'
      return {
        ...base,
        before: before(`xAI returns your browser to ${redirect}, the loopback address xAI registered for its command-line sign-in. Port 56121 is on the computer running this browser, not on the Routeweft server, and Routeweft does not listen on it: nothing needs to be installed or kept free for this sign-in.`),
        hint: `Sign in on the xAI page. Your browser is then sent to ${redirect}?code=…, which will not load; that is expected. Copy the full address from the address bar and paste it below. If xAI shows a code instead of redirecting, paste that code. If a program on this computer listens on port 56121 (a Grok CLI sign-in, for example), it receives the address instead of your browser; stop it and start again.`,
        pasteLabel: 'Address or code from xAI',
        masked: false,
      }
    }
    case 'gemini-cli':
    case 'antigravity': {
      const onboarding = provider === 'antigravity' ? ' When it finds one, Routeweft also finishes the account’s Code Assist onboarding in the background, as the Antigravity IDE does.' : ''
      return {
        ...base,
        before: before(`After you sign in, Routeweft asks Google for this account’s Code Assist project, which every request needs. The result says which project was found, or that none was, and lets you name one.${onboarding}`),
        hint: `Sign in with Google. Your browser then returns to ${returnsTo(method, 'code=')}. Copy that full address from the address bar and paste it below; the code alone also works.`,
        pasteLabel: 'Address or code from Google',
        masked: false,
      }
    }
    case 'gitlab':
      return {
        ...base,
        before: before(
          'Choose the GitLab instance: leave Server base URL blank for gitlab.com, or enter your self-managed GitLab’s address.',
          `Register an OAuth application on that instance (User settings → Applications, or Admin area → Applications) with the redirect URI ${method.redirectUri || 'of this server (set the public origin first)'} and the scopes api and read_user.`,
          'A public application (Confidential unchecked) needs only its client id: PKCE protects the exchange. A confidential application also needs its client secret, which is sent only to that instance’s token endpoint.',
          'No OAuth application? Choose Import personal access token instead and paste a token with the api scope.',
        ),
        hint: `Authorize the application on GitLab. Your browser then returns to ${returnsTo(method, 'code=')}. Copy that full address from the address bar and paste it below.`,
        pasteLabel: 'Address or code from GitLab',
        masked: false,
        requiredMeta: ['clientId'],
      }
    case 'cline':
    case 'clinepass': {
      const pass = provider === 'clinepass' ? ' ClinePass uses the same Cline account sign-in; its connection lists only ClinePass (cline-pass/) models.' : ''
      return {
        ...base,
        before: before(`Cline signs you in through WorkOS. The code it returns carries the account’s tokens; requests send the access token with Cline’s workos: prefix, as the Cline extension does.${pass}`),
        hint: `Sign in on Cline’s page. Your browser then returns to ${returnsTo(method, 'code=')}. Copy that full address from the address bar and paste it below.`,
        pasteLabel: 'Address or code from Cline',
        masked: false,
      }
    }
    case 'iflow':
      return {
        ...base,
        before: before('After you sign in, Routeweft reads this account’s iFlow API key, which requests use. A sign-in that returns no key fails and saves nothing.'),
        pasteLabel: 'Address or code from iFlow',
        masked: false,
      }
    case 'kimchi':
      return {
        ...base,
        before: before('The token Kimchi returns is checked with the Kimchi gateway before it is stored.'),
        hint: `Sign in on Kimchi. Your browser then returns to ${returnsTo(method, 'token=')}. Copy that full address, or only the token, and paste it below.`,
        pasteLabel: 'Address or token from Kimchi',
        masked: true,
      }
    case 'xiaomi-mimo':
      return {
        ...base,
        before: before('The address Xiaomi returns carries the account’s key encrypted to a key this server made for this sign-in alone, so it can finish only this attempt.'),
        hint: `Sign in on Xiaomi MiMo. Your browser then returns to ${returnsTo(method, 'u=')}. Copy that full address and paste it below.`,
      }
    case 'zed': {
      const port = (meta.nativeAppPort ?? '').trim() || '58443'
      return {
        ...base,
        before: before(`Zed sends your browser to http://127.0.0.1:${port}/ (the Native app port), on the computer running this browser. If a program there listens on that port (the Zed app’s own sign-in, for example), it receives the address instead of your browser; quit it or choose another port. The access token in that address is encrypted to a key this server made for this sign-in alone.`),
        hint: `Sign in on zed.dev. Your browser is then sent to http://127.0.0.1:${port}/?user_id=…&access_token=…, which will not load; that is expected. Copy the full address from the address bar and paste it below.`,
      }
    }
  }
  return base
}

// The import method's label and note where the provider's import is a named
// alternative to its browser sign-in.
export function importJourney(provider: string): { label?: string; note?: string } {
  if (provider === 'gitlab') return { label: 'Import personal access token', note: 'Paste a GitLab personal access token with the api scope, for the instance in Server base URL (gitlab.com when blank). It is checked against that instance’s /api/v4/user, which names the account. Use this when you cannot register an OAuth application.' }
  return {}
}

// What a successful sign-in already checked, by provider, beyond the exchange.
export function checkedNote(provider: string): string {
  switch (provider) {
    case 'kimchi': return 'The Kimchi gateway accepted the token before it was stored.'
    case 'iflow': return 'The account’s iFlow API key was read before the connection was saved.'
    case 'gitlab': return 'GitLab named the account from /api/v4/user.'
  }
  return 'Use Check connection to send a test request with the new credential.'
}

// The Code Assist project a Google sign-in reported (#216): what to show, and
// whether the operator must name one before requests can work.
export type ProjectState = { project?: string; projectMissing?: boolean }
export function projectNote(result: ProjectState): string | null {
  if (result.projectMissing) return 'Google reported no Code Assist project for this account, and every request needs one. Name the project below, or set up Gemini Code Assist for this account (signing in to the Gemini CLI once does) and Reauthenticate.'
  return null
}

// A Google Cloud project id: 6-30 lowercase letters, digits and hyphens,
// starting with a letter, optionally behind an organization domain
// ("example.com:my-project"). Checked before it is saved so a typo is caught here.
export function validProjectId(value: string): boolean {
  return /^([a-z0-9.-]+:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$/.test(value.trim())
}

// The identity field for a method that usually learns the account: optional,
// and why. needIdentity is set after a sign-in came back without one.
export function identityPrompt(providerLabel: string, mode: 'required' | 'optional'): string {
  return mode === 'required'
    ? `${providerLabel} does not report which account signed in, so name it here, for example its email.`
    : `${providerLabel} usually reports the account. If it does not, the sign-in stops without saving and asks for this, so you can fill it now if you know it.`
}

// --- Device approval (#217) ---------------------------------------------------

// What the device approval screen says for one provider: what to know before the
// code is requested, how to approve once it is shown, and which operator fields
// the start cannot do without. Pure, like browserJourney: the screen promises
// only steps the server takes.
export type DeviceJourney = {
  before: string[]
  approve: string
  requiredMeta: string[]
}

const DEVICE_APPROVE = 'Open the page below and confirm this code. This dashboard finishes the connection on its own once you approve.'
const SAME_ACCOUNT = 'Approving with an account that is already connected renews that connection instead of adding a second one; another account is added beside it.'

// The journey for one provider's device method. meta is the operator's current
// input (Kiro's AWS SSO method decides whether a start URL is needed).
export function deviceJourney(provider: string, meta: Record<string, string> = {}): DeviceJourney {
  const base: DeviceJourney = { before: [SAME_ACCOUNT], approve: DEVICE_APPROVE, requiredMeta: [] }
  const before = (...notes: string[]) => [...notes, SAME_ACCOUNT]
  switch (provider) {
    case 'github':
      return { ...base, before: before('After you approve, Routeweft reads the GitHub account and mints the Copilot token requests use. An account without a Copilot subscription is still connected, and the result says no Copilot token was issued.') }
    case 'grok-cli':
      return { ...base, before: before('This is the Grok CLI device sign-in on auth.x.ai. The account’s email and subscription tier are read from xAI after you approve. For the xAI API browser sign-in, choose xAI instead.') }
    case 'kimi':
      return { ...base, before: before('Kimi does not report which account approved, so name it above. The device id this sign-in uses is stored with the connection: Kimi’s refresh and quota need the same one.') }
    case 'kilocode':
      return { ...base, before: before('Kilo reports the approving account’s email and organization. Kilo’s free models are listed live from its gateway, not from this build.'), approve: 'Open the page below; Kilo shows the same code there. Confirm it, and this dashboard finishes the connection on its own.' }
    case 'codebuddy-cn':
    case 'codebuddy-intl': {
      const intl = provider === 'codebuddy-intl'
      const here = intl ? 'CodeBuddy international (www.codebuddy.ai)' : 'CodeBuddy China (copilot.tencent.com)'
      const other = intl ? 'An account on copilot.tencent.com signs in with CodeBuddy (CN) instead' : 'An account on codebuddy.ai signs in with CodeBuddy (Intl) instead'
      return {
        ...base,
        before: before(`This is ${here}. ${other}: the two regions are separate providers, and the same account on both is two connections.`, 'CodeBuddy does not report which account signed in, so name it above.'),
        approve: 'Open the page below and sign in. CodeBuddy shows no code to confirm; this dashboard finishes the connection on its own once you approve.',
      }
    }
    case 'kiro': {
      const idc = (meta.authMethod ?? '').trim() === 'idc'
      return {
        ...base,
        before: before(
          idc
            ? 'IAM Identity Center: enter your organization’s AWS SSO start URL and the region its Identity Center runs in (us-east-1 when blank).'
            : 'AWS Builder ID: sign in with a personal Builder ID. For an organization’s IAM Identity Center, choose idc as the AWS SSO method and enter its start URL and region.',
          'Kiro can also be connected by importing a token or an API key: choose that method instead.',
        ),
        requiredMeta: idc ? ['startUrl'] : [],
      }
    }
    case 'qoder':
      return { ...base, before: before('Saving a Qoder connection does not make Qoder served: this build does not route requests to Qoder yet, whatever accounts are stored. Its readiness above says so.') }
  }
  return base
}

// The code and link the operator copies. A provider may show no code (CodeBuddy)
// or put it in the link (verification_uri_complete); both are shown as given.
export function deviceCopyItems(flow: { userCode?: string; verificationUrl?: string }): Array<{ key: 'code' | 'link'; label: string; value: string }> {
  const items: Array<{ key: 'code' | 'link'; label: string; value: string }> = []
  if (flow.userCode?.trim()) items.push({ key: 'code', label: 'Code', value: flow.userCode.trim() })
  if (flow.verificationUrl && /^https?:\/\//i.test(flow.verificationUrl)) items.push({ key: 'link', label: 'Sign-in page', value: flow.verificationUrl })
  return items
}

// The browser waits this long past the server's deadline for an answer before it
// stops polling on its own. The server ends the attempt at the deadline, so this
// only bounds a server that never answers; an approval saving right at the limit
// still has time to report.
export const DEVICE_DEADLINE_GRACE_MS = 60_000

// Whether the browser should stop polling an attempt on its own.
export function devicePollingOver(expiresAt: string | undefined, now: number): boolean {
  if (!expiresAt) return false
  const deadline = Date.parse(expiresAt)
  return Number.isFinite(deadline) && now > deadline + DEVICE_DEADLINE_GRACE_MS
}

// The delay before the next poll: the interval the server last reported, never
// below two seconds (the server's own floor) nor above thirty (its slow_down cap).
export function devicePollDelay(intervalSeconds: number | undefined): number {
  const seconds = Number.isFinite(intervalSeconds) ? Number(intervalSeconds) : 5
  return Math.min(Math.max(seconds, 2), 30) * 1000
}

// What a GitHub sign-in reported about Copilot (#217): an account the Copilot
// token mint refused is connected but cannot serve until it has Copilot.
export function copilotNote(result: { providerId?: string; copilotMissing?: boolean }): string | null {
  if (result.providerId !== 'github' || !result.copilotMissing) return null
  return 'GitHub issued no Copilot token for this account, so it has no Copilot entitlement Routeweft could see. Requests fail until the account has a Copilot subscription. Once it has one, sign in again with a device code: this connection is renewed in place.'
}
