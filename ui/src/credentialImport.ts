// Specialized credential import (#218), as pure functions the Providers page
// calls: what each provider's import takes and where the operator finds it, a
// schema check before anything is sent, and reading a file the operator chose.
// Routeweft never reads a credential from a disk by itself: the operator pastes
// it or picks the file in the browser, and the server validates it again. No
// message here quotes the input, which is a credential.

// The server's bounds (internal/control/api/oauth.go oauthMaxImportInput and
// decodeBulkBody). Checked here first so an oversized file is refused before it
// is sent.
export const IMPORT_MAX_BYTES = 64 * 1024
export const BULK_MAX_BYTES = 1024 * 1024

// One way a provider's import can be supplied. json means the input is a JSON
// document (a file's contents), entered in a multi-line masked field with a file
// picker; otherwise it is one masked line.
export type ImportShape = {
  id: string
  label: string
  json: boolean
  // The field's label.
  field: string
  // Where to find the credential, in order.
  steps: string[]
  // Operator fields the import cannot do without, as metaKeys.
  requiredMeta: string[]
  // A problem the operator can fix before sending, or null.
  check(text: string, meta: Record<string, string>): string | null
}

const isObject = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === 'object' && !Array.isArray(value)
const text = (document: Record<string, unknown>, ...keys: string[]) => {
  for (const key of keys) {
    const value = document[key]
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return ''
}

// Parses a pasted or chosen JSON object. A parse error is described without
// JSON.parse's own message, which can quote part of the input.
export function parseObject(input: string): { document: Record<string, unknown> } | { error: string } {
  let parsed: unknown
  try { parsed = JSON.parse(input.trim()) } catch { return { error: 'This is not valid JSON. Check that the whole file was pasted, with no missing bracket, comma or quote.' } }
  if (!isObject(parsed)) return { error: 'This must be one JSON object, the contents of the file between { and }.' }
  return { document: parsed }
}

const UUID_HEX = /^[a-fA-F0-9]{32,}$/
const MICROSOFT_HOSTS = new Set(['login.microsoftonline.com', 'login.microsoft.com', 'login.windows.net'])

function missing(document: Record<string, unknown>, fields: Array<[string, string[]]>): string | null {
  const absent = fields.filter(([, keys]) => !text(document, ...keys)).map(([name]) => name)
  return absent.length ? `The file has no ${absent.join(', ')}. Use the unedited export.` : null
}

const notJSON = (input: string) => input.trim().startsWith('{') || input.trim().startsWith('[')

const SHAPES: Record<string, ImportShape[]> = {
  cursor: [{
    id: 'token', label: 'Cursor IDE token', json: false, field: 'Access token', requiredMeta: ['machineId'],
    steps: [
      'Cursor keeps its sign-in in a local SQLite file, state.vscdb: ~/Library/Application Support/Cursor/User/globalStorage on macOS, %APPDATA%\\Cursor\\User\\globalStorage on Windows, ~/.config/Cursor/User/globalStorage on Linux.',
      'Open it with a SQLite viewer (or sqlite3 state.vscdb "SELECT key, value FROM itemTable WHERE key IN (\'cursorAuth/accessToken\',\'storage.serviceMachineId\')") on that computer.',
      'Paste cursorAuth/accessToken below as the access token, and storage.serviceMachineId as the Machine id.',
      'Cursor issues no refresh token for this: the import lasts about a day, then import a current token again.',
    ],
    check(input, meta) {
      if (input.trim().length < 50) return 'A Cursor access token is at least 50 characters; copy the whole value.'
      const machine = (meta.machineId ?? '').replace(/-/g, '').trim()
      if (!machine) return 'Enter the Machine id: Cursor rejects every request without it.'
      if (!UUID_HEX.test(machine)) return 'The Machine id is a UUID, 32 hexadecimal digits with or without dashes.'
      return null
    },
  }],
  kiro: [
    {
      id: 'api-key', label: 'API key', json: false, field: 'Kiro API key', requiredMeta: [],
      steps: ['Paste a Kiro API key. It is checked against the Amazon Q model catalog in the AWS region (us-east-1 when blank) before it is stored.'],
      check: input => notJSON(input) ? 'This looks like a JSON file; choose Kiro IDE token or CLIProxyAPI export instead.' : null,
    },
    {
      id: 'token', label: 'Kiro IDE token file', json: true, field: 'Token file (JSON)', requiredMeta: [],
      steps: [
        'Sign in to the Kiro IDE on any computer. It writes ~/.aws/sso/cache/kiro-auth-token.json there.',
        'Choose that file below, or paste its contents. Routeweft proves it by refreshing it, which also mints the access token it stores.',
        'For an AWS IAM Identity Center (organization) sign-in, the token file names a clientIdHash: add the clientId and clientSecret from ~/.aws/sso/cache/<clientIdHash>.json to the JSON, with the region if it is not us-east-1. Without them an organization token cannot refresh.',
        'Optional: add profileArn from the IDE’s profile.json if requests need a specific profile.',
      ],
      check(input) {
        const parsed = parseObject(input)
        if ('error' in parsed) return parsed.error
        const document = parsed.document
        if (!text(document, 'refreshToken', 'refresh_token')) return 'The file has no refreshToken. Choose kiro-auth-token.json from ~/.aws/sso/cache.'
        if (text(document, 'token_endpoint', 'tokenEndpoint', 'auth_method')) return 'This is a CLIProxyAPI export; choose CLIProxyAPI export instead.'
        const id = text(document, 'clientId', 'client_id'), secret = text(document, 'clientSecret', 'client_secret')
        if (Boolean(id) !== Boolean(secret)) return 'An Identity Center token needs both clientId and clientSecret from its client registration file.'
        return null
      },
    },
    {
      id: 'cli-proxy', label: 'CLIProxyAPI export (Microsoft)', json: true, field: 'CLIProxyAPI auth file (JSON)', requiredMeta: [],
      steps: [
        'For a Kiro account that signs in through Microsoft (external_idp), export its auth file from CLIProxyAPI.',
        'Choose that file below, or paste its contents. It carries the token pair, so no sign-in is made; its token_endpoint must be a Microsoft login address, which is where the refresh token is sent.',
      ],
      check(input) {
        const parsed = parseObject(input)
        if ('error' in parsed) return parsed.error
        const document = parsed.document
        const method = text(document, 'auth_method', 'authMethod')
        if (method && method !== 'external_idp') return 'Only external_idp Kiro accounts can be imported this way.'
        const absent = missing(document, [['access_token', ['access_token', 'accessToken']], ['refresh_token', ['refresh_token', 'refreshToken']], ['client_id', ['client_id', 'clientId']], ['token_endpoint', ['token_endpoint', 'tokenEndpoint']], ['profile_arn', ['profile_arn', 'profileArn']]])
        if (absent) return absent
        const scopes = document.scopes ?? document.scope
        if (!(Array.isArray(scopes) ? scopes.some(item => typeof item === 'string' && item.trim()) : typeof scopes === 'string' && scopes.trim())) return 'The file has no scopes. Use the unedited export.'
        let host = ''
        try {
          const endpoint = new URL(text(document, 'token_endpoint', 'tokenEndpoint'))
          host = endpoint.protocol === 'https:' && !endpoint.port && !endpoint.username ? endpoint.hostname.toLowerCase() : ''
        } catch { host = '' }
        if (!MICROSOFT_HOSTS.has(host)) return 'The token_endpoint must be an https Microsoft login address (login.microsoftonline.com).'
        return null
      },
    },
  ],
  'xiaomi-mimo': [
    {
      id: 'auth-json', label: 'MiMo auth.json', json: true, field: 'auth.json (JSON)', requiredMeta: [],
      steps: [
        'Xiaomi MiMo Desktop and MiMoCode keep the account’s key in auth.json: ~/.local/share/mimocode/auth.json on Linux, ~/Library/Application Support/mimocode/auth.json on macOS, %APPDATA%\\Xiaomi MiMo\\auth.json on Windows.',
        'Choose that file below, or paste its contents. The key under xiaomi.key and the account uid are read from it.',
        'To rotate several accounts on their own session, add the account’s mimoPassToken, mimoUserId and mimoCUserId (from MiMo Desktop’s cookies) to the JSON. Without them the key still works.',
      ],
      check(input) {
        const parsed = parseObject(input)
        if ('error' in parsed) return parsed.error
        const document = parsed.document
        const nested = isObject(document.xiaomi) ? document.xiaomi : {}
        const key = text(document, 'apiKey') || text(nested, 'key')
        if (!key) return 'The file has no key under xiaomi.key. Sign in to MiMo Desktop again and choose the new auth.json.'
        if (!key.startsWith('sk-')) return 'The key under xiaomi.key does not start with sk-, so it is not a MiMo API key.'
        return null
      },
    },
    {
      id: 'key', label: 'API key', json: false, field: 'MiMo API key', requiredMeta: [],
      steps: ['Paste a MiMo API key (it starts with sk-). Fill the account uid if you know it; it names the connection.'],
      check: input => !input.trim().startsWith('sk-') ? 'A MiMo API key starts with sk-.' : null,
    },
  ],
  iflow: [{
    id: 'cookie', label: 'Browser cookie', json: false, field: 'Cookie', requiredMeta: [],
    steps: [
      'Open platform.iflow.cn in your browser and sign in.',
      'Open the developer tools (F12) → Application (Storage in Firefox) → Cookies → platform.iflow.cn.',
      'Copy the whole cookie string; it must include BXAuth. Only BXAuth is kept. Routeweft uses it to mint the account’s API key, which requests use.',
    ],
    check: input => /BXAuth=[^;\s]/.test(input) ? null : 'The cookie must include BXAuth=…; copy the whole cookie string.',
  }],
  gitlab: [{
    id: 'pat', label: 'Personal access token', json: false, field: 'Personal access token', requiredMeta: [],
    steps: [
      'Create a personal access token on your GitLab instance (User settings → Access tokens, at /-/user_settings/personal_access_tokens) with the scopes api, read_user and ai_features.',
      'Leave Server base URL blank for gitlab.com, or enter your self-managed GitLab’s address. The token is checked against that instance’s /api/v4/user, which names the account.',
    ],
    check: input => input.trim() ? null : 'Paste the personal access token.',
  }],
  'grok-cli': [{
    id: 'account', label: 'Grok CLI account', json: true, field: 'Account (JSON)', requiredMeta: [],
    steps: [
      'Paste or choose one Grok CLI account object: access_token, and ideally refresh_token, id_token and email (snake_case or camelCase).',
      'For several accounts at once, choose Bulk import accounts instead.',
    ],
    check(input) {
      const parsed = parseObject(input)
      if ('error' in parsed) return parsed.error
      return text(parsed.document, 'access_token', 'accessToken') ? null : 'The account has no access_token.'
    },
  }],
  codex: [{
    id: 'token', label: 'ChatGPT access token', json: false, field: 'Access token', requiredMeta: [],
    steps: ['Paste a ChatGPT access token. The account and plan are read from it. A token import is always added as its own connection; it has no refresh token, so sign in for a lasting connection.'],
    check: input => input.trim() ? null : 'Paste the access token.',
  }],
}

// The import shapes a provider offers. A provider without its own entry gets
// one masked line, as before (the server still validates it).
export function importShapes(provider: string): ImportShape[] {
  return SHAPES[provider] ?? [{ id: 'token', label: 'Credential', json: false, field: 'Token', requiredMeta: [], steps: [], check: input => input.trim() ? null : 'Paste the credential.' }]
}

// The required operator fields still empty.
export function missingMeta(shape: ImportShape, meta: Record<string, string>): string[] {
  return shape.requiredMeta.filter(key => !(meta[key] ?? '').trim())
}

// --- Files the operator chooses -------------------------------------------

// The part of a browser File this reads.
export type ChosenFile = { name: string; size: number; text(): Promise<string> }
export type FileRead = { text: string; label: string } | { error: string }

const sizeLabel = (bytes: number) => bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KB`

// Reads a file the operator picked, refusing one past the server's bound, an
// empty one, and a binary one (Cursor's state.vscdb itself, chosen by mistake).
// The label names the file and its size, never its contents.
export async function readCredentialFile(file: ChosenFile, maxBytes: number): Promise<FileRead> {
  if (file.size > maxBytes) return { error: `${file.name} is ${sizeLabel(file.size)}; at most ${sizeLabel(maxBytes)} can be imported. Choose the account’s own file.` }
  let content: string
  try { content = await file.text() } catch { return { error: `${file.name} could not be read.` } }
  if (!content.trim()) return { error: `${file.name} is empty.` }
  if (content.includes('\u0000')) return { error: `${file.name} is not a text file. Choose the exported JSON, not a database.` }
  return { text: content, label: `${file.name} (${sizeLabel(file.size)})` }
}

// Reads several exported files for bulk import and joins their accounts into one
// array, as the reference's bulk dialog does (BulkImportGrokCliModal.js).
export async function readBulkFiles(files: ChosenFile[], maxBytes: number): Promise<{ accounts: unknown[]; label: string } | { error: string }> {
  if (!files.length) return { error: 'Choose at least one .json file.' }
  const total = files.reduce((sum, file) => sum + file.size, 0)
  if (total > maxBytes) return { error: `The files are ${sizeLabel(total)} together; at most ${sizeLabel(maxBytes)} can be imported at once.` }
  const accounts: unknown[] = []
  for (const file of files) {
    const read = await readCredentialFile(file, maxBytes)
    if ('error' in read) return read
    let parsed: unknown
    try { parsed = JSON.parse(read.text) } catch { return { error: `${file.name} is not valid JSON.` } }
    if (Array.isArray(parsed)) accounts.push(...parsed)
    else if (isObject(parsed) && Array.isArray(parsed.accounts)) accounts.push(...parsed.accounts)
    else accounts.push(parsed)
  }
  if (!accounts.length) return { error: 'No accounts were found in the chosen files.' }
  return { accounts, label: `${files.length} file${files.length === 1 ? '' : 's'}, ${accounts.length} account${accounts.length === 1 ? '' : 's'}` }
}

// --- After the import ------------------------------------------------------

// What the operator must know about a credential that had already expired when
// it was imported: the server stores it, as the reference does, and says whether
// a refresh token came with it.
export function expiredNote(saved: { expired?: boolean; refreshable?: boolean }): string | null {
  if (!saved.expired) return null
  return saved.refreshable
    ? 'This credential had already expired. It came with a refresh token, which is used to renew it before it serves a request; if that fails, import a current one.'
    : 'This credential had already expired and came without a refresh token, so it cannot serve requests. Import a current one, or sign in instead.'
}

// What MiMo Desktop discovery found on the server (#296); never a credential.
export type HostDiscovery = { source?: string; accountSession?: boolean }

// Said after a discovery saved a connection: where the key came from, and
// whether Desktop's account session (which reads the weekly quota) came with it.
export function hostDiscoveryNote(discovery: HostDiscovery | undefined): string {
  const from = discovery?.source ? `The key was read from ${discovery.source} on the server. ` : ''
  return discovery?.accountSession
    ? `${from}MiMo Desktop’s account session was attached, so the weekly quota can be read.`
    : `${from}No MiMo Desktop account session could be read; Desktop locks it while it runs. Quit Desktop and find it again to attach it; the key works without it.`
}

// Said after a save for a provider this build does not serve, so a stored
// credential is not mistaken for a working route (Cursor, #229).
export function savedReadinessNote(status: string, providerLabel: string): string | null {
  if (status === 'ready') return null
  if (status === 'experimental') return `${providerLabel} is experimental in this build: the credential is saved and requests are served, but the provider is not yet qualified for production.`
  if (status === 'unavailable') return `The credential is saved, but this build does not route requests to ${providerLabel} yet, so this connection will not serve traffic.`
  return `The credential is saved, but this build does not say whether it can serve ${providerLabel}.`
}
