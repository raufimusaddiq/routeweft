// Records what the pinned Grok CLI, Kiro, Qoder and Zed quota readers return,
// so the Go tests can be offline and still differential (#224). Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-account-quota.mjs
//
// It evaluates the pin's own open-sse/services/usage/{shared,grok-cli,
// grokCliQuotaFrame,kiro,misc,zed}.js, the Qoder PAT exchange in
// services/qoderModels.js with the dispatcher's qoder handler
// (services/usage.js:45-50), shared/zedAuth.js, and the usage page's
// parseQuotaData (ProviderLimits/utils.js), read with `git show` at the pin, with
// the network and the clock scripted. Endpoint URLs come from the pinned registry
// entries and constants. Nothing is sent and no credential is used. The Go tests
// read internal/providers/usage/testdata/account_quota_pin.json; no test
// regenerates it.
process.env.TZ = 'UTC';

const assert = (await import('node:assert/strict')).default;
const crypto = await import('node:crypto');
const { writeFileSync } = await import('node:fs');
const vm = (await import('node:vm')).default;
const { source } = await import('./oracle.mjs');

const NOW = Date.parse('2026-10-20T12:00:00.000Z');
const out = new URL('../../internal/providers/usage/testdata/account_quota_pin.json', import.meta.url);

let clock = NOW;
class FakeDate extends Date {
  constructor(...args) { super(...(args.length ? args : [clock])); }
  static now() { return clock; }
}

// Evaluate pinned code. Imports are replaced by `deps`; every top-level
// declaration in `names` is returned. `export` keywords are dropped.
function evaluateCode(path, code, deps, names) {
  code = code.replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export\s*\{[^}]*\}\s*from\s[^;]*;\s*$/gm, '').replace(/^export\s+/gm, '');
  assert.doesNotMatch(code, /^\s*(?:import|export)\b/m, `${path}: unsupported module syntax`);
  const context = vm.createContext({ URL, URLSearchParams, Map, Set, Promise, JSON, Math, Date: FakeDate, Error, Number, String,
    Array, Object, RegExp, Buffer, BigInt, console: { log() {}, warn() {}, error() {} } });
  context.deps = deps;
  return new vm.Script(`(({${Object.keys(deps).join(',')}}) => {${code}\nreturn {${names.join(',')}};})(deps)`, { filename: path })
    .runInContext(context, { timeout: 10000 });
}
const evaluate = (path, deps, names) => evaluateCode(path, source(path), deps, names);

// A `key: {...}` literal of a registry entry's transport; it holds only strings.
function registryBlock(id, key, scope = {}) {
  const text = source(`open-sse/providers/registry/${id}.js`);
  const match = text.match(new RegExp(`\\n {4}${key}: (\\{\\n[\\s\\S]*?\\n {4}\\}),\\n`));
  assert.ok(match, `${id}: ${key} block`);
  return Function(...Object.keys(scope), `return ${match[1]};`)(...Object.values(scope));
}

const grokConfig = evaluate('open-sse/config/grokCli.js', {}, ['GROK_CLI_VERSION', 'GROK_CLI_BASE_URL', 'GROK_CLI_CLIENT_IDENTIFIER',
  'GROK_CLI_USER_AGENT']);
const PROVIDERS = {
  'grok-cli': { usage: registryBlock('grok-cli', 'usage', { GROK_CLI_BASE_URL: grokConfig.GROK_CLI_BASE_URL }) },
  kiro: { usage: registryBlock('kiro', 'usage') },
  qoder: { usage: registryBlock('qoder', 'usage') },
};
const qoderConstants = evaluate('open-sse/shared/qoder/constants.js', {}, ['QODER_JOB_TOKEN_EXCHANGE_URL', 'QODER_USERINFO_URL',
  'QODER_IDE_VERSION', 'QODER_CLIENT_TYPE', 'QODER_MODEL_LIST_URL', 'QODER_CHAT_BASE_ALT']);

// resolveDefaultProfileArn and its table (config/kiroConstants.js:30-42); the
// rest of that file is the chat path.
const kiroConstants = source('open-sse/config/kiroConstants.js');
const arnSource = kiroConstants.match(/\nexport const KIRO_DEFAULT_PROFILE_ARNS = \{[\s\S]*?\n\};\n[\s\S]*?\nexport function resolveDefaultProfileArn\(authMethod\) \{\n[\s\S]*?\n\}\n/);
assert.ok(arnSource, 'resolveDefaultProfileArn');
const { resolveDefaultProfileArn } = evaluateCode('open-sse/config/kiroConstants.js#resolveDefaultProfileArn', arnSource[0], {},
  ['resolveDefaultProfileArn']);

// The usage page's normalizer, which decides what each quota row shows.
const page = evaluate('src/app/(dashboard)/dashboard/usage/components/ProviderLimits/utils.js', { getModelsByProviderId: () => [] },
  ['parseQuotaData', 'getRemainingPercentage']);

// One scripted network: each request takes the next step. A step is
// {status, body}, {status, base64} for a binary body, or {network: message}.
// Bearer values are recorded by scheme only, the Zed "<user> <token>" header as
// "<user> <token>", and the PAT exchange body without the PAT, so the file
// carries no token.
const TOKENS = new Set();
function redact(text) {
  let value = String(text);
  for (const token of TOKENS) value = value.split(token).join('<token>');
  return value;
}
function network(steps) {
  const calls = [];
  const proxyAwareFetch = async (url, options = {}) => {
    const step = steps[calls.length];
    assert.ok(step, `unscripted request to ${url}`);
    const headers = Object.fromEntries(Object.entries(options.headers ?? {}).map(([k, v]) => [k, redact(v)]));
    let body = options.body ?? null;
    if (Buffer.isBuffer(body)) body = { base64: body.toString('base64') };
    else if (body !== null) body = redact(body);
    calls.push({ url, method: options.method ?? 'GET', headers, body });
    if (step.network) throw new Error(step.network);
    const payload = step.base64 !== undefined ? Buffer.from(step.base64, 'base64')
      : typeof step.body === 'string' ? step.body : JSON.stringify(step.body);
    return new Response(payload, { status: step.status ?? 200, headers: { 'content-type': 'application/json' } });
  };
  return { calls, proxyAwareFetch };
}

function load(name, fetch) {
  const shared = evaluate('open-sse/services/usage/shared.js', { PROVIDERS, proxyAwareFetch: fetch },
    ['U', 'parseResetTime', 'toFiniteNumber']);
  const deps = { proxyAwareFetch: fetch, ...shared };
  if (name === 'grok-cli') {
    const frame = evaluate('open-sse/services/usage/grokCliQuotaFrame.js', {}, ['decodeGrokCreditsFrame']);
    return evaluate('open-sse/services/usage/grok-cli.js', { ...deps, ...frame, ...grokConfig }, ['getGrokCliUsage']);
  }
  if (name === 'kiro') return evaluate('open-sse/services/usage/kiro.js', { ...deps, resolveDefaultProfileArn }, ['getKiroUsage']);
  if (name === 'qoder') {
    const { getQoderUsage } = evaluate('open-sse/services/usage/misc.js', deps, ['getQoderUsage']);
    const { resolveQoderCredentials } = evaluate('open-sse/services/qoderModels.js', { createHash: crypto.createHash, proxyAwareFetch: fetch,
      buildCosyHeaders: () => ({}), ...qoderConstants }, ['resolveQoderCredentials']);
    // services/usage.js:45-50
    return { qoder: async c => {
      const resolved = await resolveQoderCredentials(c, c.proxyOptions).catch(() => null);
      return getQoderUsage(resolved?.accessToken || c.accessToken, c.proxyOptions);
    } };
  }
  const zedAuth = evaluate('open-sse/shared/zedAuth.js', { crypto, proxyAwareFetch: fetch }, ['fetchZedAuthenticatedUser']);
  return evaluate('open-sse/services/usage/zed.js', { ...deps, ...zedAuth }, ['getZedUsage']);
}

const settle = async promise => {
  try { return { result: JSON.parse(JSON.stringify(await promise)) }; } catch (error) { return { error: error.message }; }
};

const FUTURE = '2026-10-21T00:00:00Z';
const PAST = '2026-10-19T00:00:00Z';
const FUTURE_MS = Date.parse(FUTURE);
const FUTURE_S = FUTURE_MS / 1000;
const TOKEN = 'fixture-token';
TOKENS.add(TOKEN);

const failures = {
  auth401: [{ status: 401, body: '{"message":"invalid token"}' }],
  auth403: [{ status: 403, body: '{"error":"forbidden"}' }],
  error500: [{ status: 500, body: 'upstream exploded' }],
  timeout: [{ network: 'The operation was aborted due to timeout' }],
  'invalid-json': [{ status: 200, body: 'not json' }],
};

// Grok CLI (U/grok-cli.js:141-424, grokCliQuotaFrame.js)

// A synthetic JWT whose payload carries only a tier; it is not a credential.
const jwt = payload => `fixture.${Buffer.from(JSON.stringify(payload)).toString('base64url')}.sig`;
const varint = value => { const bytes = []; let v = BigInt(value); do { let b = Number(v & 0x7fn); v >>= 7n; if (v) b |= 0x80; bytes.push(b); } while (v); return bytes; };
const field = (number, wire, bytes) => [...varint((number << 3) | wire), ...bytes];
const delimited = (number, bytes) => field(number, 2, [...varint(bytes.length), ...bytes]);
const float32 = value => { const b = Buffer.alloc(4); b.writeFloatLE(value); return [...b]; };
const float64 = value => { const b = Buffer.alloc(8); b.writeDoubleLE(value); return [...b]; };
const timestamp = (seconds, nanos = 0) => [...field(1, 0, varint(seconds)), ...(nanos ? field(2, 0, varint(nanos)) : [])];
const frame = (flag, payload) => { const header = Buffer.alloc(5); header[0] = flag; header.writeUInt32BE(payload.length, 1); return [...header, ...payload]; };
const trailer = frame(0x80, [...Buffer.from('grpc-status:0\r\n')]);
const credits = (...inner) => delimited(1, inner.flat());
const grpc = bytes => ({ status: 200, base64: Buffer.from(bytes).toString('base64') });

const subscriber = { status: 200, body: { subscriptionTier: 'SuperGrok' } };
const free = { status: 200, body: { subscriptionTier: 'free' } };
const val = v => ({ val: v });
const capZero = { config: { onDemandCap: val(0), onDemandUsed: val(0), prepaidBalance: val(0), billingPeriodEnd: FUTURE } };
const grokCases = {
  'on-demand': [{ status: 200, body: { config: { onDemandCap: val(100), onDemandUsed: val(25), billingPeriodEnd: FUTURE } } }, subscriber],
  'monthly-included': [{ status: 200, body: { monthlyLimit: 1000, includedUsed: 200, currentPeriod: { end: FUTURE } } }, subscriber],
  'monthly-total-used': [{ status: 200, body: { config: { monthly_limit: val('500'), total_used: val(125) }, billing_period_end: FUTURE_S } }, subscriber],
  'monthly-no-used': [{ status: 200, body: { config: { monthlyLimit: 10 } } }, subscriber],
  'weekly-percent': [{ status: 200, body: { config: { creditUsagePercent: val(35), onDemandCap: val(0), onDemandUsed: val(0),
    productUsage: [{ usagePercent: 30 }, { usagePercent: 5 }], currentPeriod: { type: 'USAGE_PERIOD_TYPE_WEEKLY', end: FUTURE } } } }, subscriber],
  'weekly-clamped': [{ status: 200, body: { credit_usage_percent: 140, resetAt: FUTURE } }, subscriber],
  'weekly-negative': [{ status: 200, body: { config: { creditUsagePercent: -1 } } }, subscriber, grpc([])],
  'prepaid': [{ status: 200, body: { config: { onDemandCap: val(0), onDemandUsed: val(0), prepaidBalance: val(50), billingPeriodEnd: FUTURE } } }, subscriber],
  'multiple-window': [{ status: 200, body: { monthlyLimit: val(1000), includedUsed: val(100), credits: { total: 40, used: 10, resetAt: PAST },
    config: { onDemandCap: val(200), onDemandUsed: val(50), prepaidBalance: val(5), creditUsagePercent: val(20), billingPeriodEnd: FUTURE } } }, subscriber],
  'zero': [{ status: 200, body: { config: { onDemandCap: val(100), onDemandUsed: val(100), billingPeriodEnd: FUTURE } } }, subscriber],
  'over-used': [{ status: 200, body: { config: { onDemandCap: val(100), onDemandUsed: val(150), billingPeriodEnd: FUTURE } } }, subscriber],
  'expired': [{ status: 200, body: { config: { onDemandCap: val(100), onDemandUsed: val(100), billingPeriodEnd: PAST } } }, subscriber],
  'free-exhausted': [{ status: 200, body: capZero }, free],
  'free-exhausted-no-tier': [{ status: 200, body: capZero }, { status: 200, body: { subscriptionTier: null } }],
  'free-exhausted-user-failed': [{ status: 200, body: capZero }, { status: 500, body: '' }],
  'free-exhausted-user-network': [{ status: 200, body: capZero }, { network: 'socket hang up' }],
  'free-cap-no-used': [{ status: 200, body: { config: { onDemandCap: val(0) } } }, free, grpc([])],
  'paid-grpc-weekly': [{ status: 200, body: capZero }, subscriber, grpc([...frame(0, credits(field(1, 5, float32(0.35)),
    delimited(5, timestamp(FUTURE_S, 500000000)))), ...trailer])],
  'paid-grpc-omitted-ratio': [{ status: 200, body: capZero }, subscriber, grpc(frame(0, credits(delimited(5, timestamp(FUTURE_S)))))],
  'paid-grpc-double': [{ status: 200, body: capZero }, subscriber, grpc(frame(0, credits(field(1, 1, float64(0.996)))))],
  'paid-grpc-full': [{ status: 200, body: capZero }, subscriber, grpc(frame(0, credits(field(1, 5, float32(1.5)), delimited(5, timestamp(FUTURE_S)))))],
  'paid-grpc-trailer-first': [{ status: 200, body: capZero }, subscriber, grpc([...trailer, ...frame(0, credits(field(1, 5, float32(0.5))))])],
  'paid-grpc-unframed': [{ status: 200, body: capZero }, subscriber, grpc(credits(field(1, 5, float32(0.25)), delimited(5, timestamp(FUTURE_S))))],
  'paid-grpc-trailer-only': [{ status: 200, body: capZero }, subscriber, grpc(trailer)],
  'paid-grpc-negative': [{ status: 200, body: capZero }, subscriber, grpc(frame(0, credits(field(1, 5, float32(-0.1)))))],
  'paid-grpc-varint-ratio': [{ status: 200, body: capZero }, subscriber, grpc(frame(0, credits(field(1, 0, varint(1)))))],
  'paid-grpc-truncated': [{ status: 200, body: capZero }, subscriber, grpc(frame(0, [0x0a, 0x09, 0x0d, 0x00]))],
  'paid-grpc-empty': [{ status: 200, body: capZero }, subscriber, grpc([])],
  'paid-grpc-500': [{ status: 200, body: capZero }, subscriber, { status: 500, body: '' }],
  'paid-grpc-401': [{ status: 200, body: capZero }, subscriber, { status: 401, body: '' }],
  'paid-grpc-network': [{ status: 200, body: capZero }, subscriber, { network: 'The operation was aborted due to timeout' }],
  'paid-grpc-reset-zero': [{ status: 200, body: capZero }, subscriber, grpc(frame(0, credits(field(1, 5, float32(0.1)), delimited(5, []))))],
  'free-no-allotment': [{ status: 200, body: { config: {} } }, free, grpc([])],
  'credit-bags': [{ status: 200, body: { creditBalance: { remaining: val(30) }, config: { includedCredits: { limit: val(100), spent: val(60) } } } }, subscriber],
  'credit-bag-total-remaining': [{ status: 200, body: { usage: { cap: '80', left: 20, end: FUTURE } } }, subscriber],
  'credit-bag-remaining-zero': [{ status: 200, body: { config: { subscriptionCredits: { balance: 0 } } } }, subscriber],
  'credit-bag-not-object': [{ status: 200, body: { credits: [1, 2], config: { credits: 'x' } } }, free, grpc([])],
  'tier-format': [{ status: 200, body: { config: { onDemandCap: val(10), onDemandUsed: val(1) } } }, { status: 200, body: { subscription_tier: ' super_grok-heavy ' } }],
  'tier-nested': [{ status: 200, body: { config: { onDemandCap: val(10), onDemandUsed: val(1) } } }, { status: 200, body: { subscription: { tier: 'x-premium' } } }],
  'tier-config': [{ status: 200, body: { config: { onDemandCap: val(10), onDemandUsed: val(1), subscriptionTier: 'pro' } } }, { status: 200, body: {} }],
  'tier-not-string': [{ status: 200, body: { config: { onDemandCap: val(10) } } }, { status: 200, body: { subscriptionTier: 3 } }],
  'grok-code-access': [{ status: 200, body: { config: { onDemandCap: val(10) } } }, { status: 200, body: { hasGrokCodeAccess: true } }],
  'value-shapes': [{ status: 200, body: { config: { onDemandCap: { val: '40' }, onDemandUsed: '10', prepaidBalance: { val: null },
    creditUsagePercent: { notval: 3 } } } }, subscriber],
  'reset-shapes': [{ status: 200, body: { config: { onDemandCap: val(10), onDemandUsed: val(1), billingPeriodEnd: '', resetsAt: String(FUTURE_MS) } } }, subscriber],
  'body-null': [{ status: 200, body: 'null' }, subscriber],
  'body-array': [{ status: 200, body: '[]' }, subscriber, grpc([])],
  ...Object.fromEntries(Object.entries(failures).map(([k, v]) => [k, [...v, subscriber]])),
  'error-long-text': [{ status: 502, body: 'x'.repeat(300) }, subscriber],
  'error-empty-text': [{ status: 429, body: '' }, subscriber],
};
// Credentials per case; JWT-shaped tokens decide the pin's plan label
// (grok-cli.js:95-110). Every token is synthetic.
const grokTokens = {
  'jwt-supergrok': jwt({ tier: 1 }), 'jwt-heavy': jwt({ tier: 5 }), 'jwt-free': jwt({ tier: 0 }), 'jwt-unknown-tier': jwt({ tier: 9 }),
  'jwt-no-tier': jwt({ sub: 'x' }),
};
for (const token of Object.values(grokTokens)) TOKENS.add(token);
for (const name of Object.keys(grokTokens)) grokCases[name] = grokCases['on-demand'];
grokCases['jwt-paid-grpc'] = grokCases['paid-grpc-weekly'];
grokTokens['jwt-paid-grpc'] = jwt({ tier: 6 });
TOKENS.add(grokTokens['jwt-paid-grpc']);
const grokIdentity = {
  'identity-email-user': { email: 'fixture@example.invalid', userId: 'user-fixture' },
  'identity-principal': { principalId: 'principal-fixture' },
};
for (const name of Object.keys(grokIdentity)) grokCases[name] = grokCases['on-demand'];

// Kiro (U/kiro.js:12-188)

const breakdown = (resourceType, used, limit, extra = {}) => ({ resourceType, currentUsageWithPrecision: used, usageLimitWithPrecision: limit, ...extra });
const kiroOk = body => ({ status: 200, body });
const usage = { nextDateReset: FUTURE_S, subscriptionInfo: { subscriptionTitle: 'KIRO PRO' },
  usageBreakdownList: [breakdown('CREDIT', 12.5, 1000, { freeTrialInfo: { currentUsageWithPrecision: 20, usageLimitWithPrecision: 500, freeTrialExpiry: FUTURE_S + 86400 } })] };
const kiroCases = {
  'get-ok': [kiroOk(usage)],
  'multiple-window': [kiroOk({ nextDateReset: FUTURE, usageBreakdownList: [breakdown('CREDIT', 100, 1000), breakdown('AGENTIC_REQUEST', 50, 50,
    { freeTrialInfo: { currentUsageWithPrecision: 0, usageLimitWithPrecision: 100 } }), breakdown('VIBE', 1, 10, { freeTrialInfo: { freeTrialExpiry: PAST } })] })],
  'zero': [kiroOk({ nextDateReset: FUTURE, usageBreakdownList: [breakdown('CREDIT', 1000, 1000)] })],
  'over-used': [kiroOk({ nextDateReset: FUTURE, usageBreakdownList: [breakdown('CREDIT', 1200, 1000)] })],
  'expired': [kiroOk({ nextDateReset: PAST, usageBreakdownList: [breakdown('CREDIT', 1000, 1000)] })],
  'zero-limit': [kiroOk({ nextDateReset: FUTURE, usageBreakdownList: [breakdown('CREDIT', 0, 0), breakdown('BONUS', 5, null)] })],
  'empty': [kiroOk({ usageBreakdownList: [], subscriptionInfo: { subscriptionTitle: 'KIRO FREE' } })],
  'no-list': [kiroOk({ nextDateReset: FUTURE })],
  'reset-date-alias': [kiroOk({ resetDate: '2026-11-01', usageBreakdownList: [breakdown('CREDIT', 1, 10)] })],
  'reset-ms': [kiroOk({ nextDateReset: FUTURE_MS, usageBreakdownList: [breakdown('CREDIT', 1, 10, { freeTrialInfo: { currentUsageWithPrecision: 1, usageLimitWithPrecision: 2, freeTrialExpiry: '' } })] })],
  'value-shapes': [kiroOk({ nextDateReset: String(FUTURE_S), usageBreakdownList: [breakdown('CREDIT', '25', '100'), breakdown(undefined, 3, 30),
    breakdown('credit', 7, 70)] })],
  'free-trial-only': [kiroOk({ usageBreakdownList: [breakdown('CREDIT', 0, 0, { freeTrialInfo: { currentUsageWithPrecision: 40, usageLimitWithPrecision: 500, freeTrialExpiry: FUTURE } })] })],
  'get-500-post-ok': [{ status: 500, body: 'boom' }, kiroOk(usage)],
  'get-403-post-403-q-ok': [{ status: 403, body: '' }, { status: 403, body: '{}' }, kiroOk(usage)],
  'get-network-post-ok': [{ network: 'connect ETIMEDOUT' }, kiroOk(usage)],
  'get-invalid-json-post-ok': [{ status: 200, body: 'not json' }, kiroOk(usage)],
  'get-null-post-ok': [{ status: 200, body: 'null' }, kiroOk(usage)],
  'all-401': [{ status: 401, body: '' }, { status: 401, body: '' }, { status: 401, body: '' }],
  'auth-then-500': [{ status: 403, body: '' }, { status: 500, body: '' }, { status: 500, body: 'later' }],
  'all-500': [{ status: 500, body: '' }, { status: 502, body: 'bad gateway' }, { status: 503, body: 'unavailable' }],
  'timeout': [{ network: 'The operation was aborted due to timeout' }, { network: 'The operation was aborted due to timeout' },
    { network: 'The operation was aborted due to timeout' }],
  'all-invalid-json': [{ status: 200, body: 'not json' }, { status: 200, body: '' }, { status: 200, body: '<html>' }],
};
// The pin's auth branches (kiro.js:52-67, 157-177); `region` is ignored by the pin.
const kiroModes = {
  'builder-id': { authMethod: 'builder-id' },
  default: {},
  'builder-id-arn': { authMethod: 'builder-id', profileArn: 'arn:aws:codewhisperer:us-east-1:000000000000:profile/FIXTURE' },
  idc: { authMethod: 'idc', profileArn: 'arn:aws:codewhisperer:eu-central-1:000000000000:profile/FIXTURE', region: 'eu-central-1' },
  google: { authMethod: 'google' },
  github: { authMethod: 'github' },
  api_key: { authMethod: 'api_key' },
  'api_key-arn': { authMethod: 'api_key', profileArn: 'arn:aws:codewhisperer:us-east-1:000000000000:profile/KEY' },
  external_idp: { authMethod: 'external_idp' },
};

// Qoder (U/misc.js:230-290, qoderModels.js:66-163, usage.js:45-50)

const qoderUsage = { userQuota: { total: 1000, used: 652, remaining: 348, unit: 'credits' },
  orgResourcePackage: { total: 5000, used: 1000, remaining: 4000, unit: 'credits' }, totalUsagePercentage: 30.4, isQuotaExceeded: false, expiresAt: FUTURE_MS };
const qoderCases = {
  'multiple-window': [{ status: 200, body: qoderUsage }],
  'personal-only': [{ status: 200, body: { userQuota: { total: 300, used: 30, remaining: 270, unit: 'credits' }, orgResourcePackage: { total: 0, used: 0, remaining: 0 },
    totalUsagePercentage: 10, isQuotaExceeded: false, expiresAt: FUTURE_MS } }],
  'no-org': [{ status: 200, body: { userQuota: { total: 300, used: 30, remaining: 270 }, expiresAt: String(FUTURE_MS) } }],
  'zero': [{ status: 200, body: { userQuota: { total: 300, used: 300, remaining: 0, unit: 'credits' }, isQuotaExceeded: true, totalUsagePercentage: 100, expiresAt: FUTURE_MS } }],
  'exceeded-flag-only': [{ status: 200, body: { userQuota: { total: 300, used: 10, remaining: 290 }, isQuotaExceeded: 'yes', expiresAt: FUTURE_MS } }],
  'expired': [{ status: 200, body: { userQuota: { total: 300, used: 300, remaining: 0 }, isQuotaExceeded: true, expiresAt: Date.parse(PAST) } }],
  'remaining-disagrees': [{ status: 200, body: { userQuota: { total: 100, used: 10, remaining: 5, unit: 'pts' }, orgResourcePackage: { total: '200', used: '50', remaining: '999' },
    expiresAt: FUTURE_MS } }],
  'remaining-only': [{ status: 200, body: { userQuota: { remaining: 50 }, expiresAt: FUTURE_MS } }],
  'over-used': [{ status: 200, body: { userQuota: { total: 100, used: 150, remaining: -50 }, expiresAt: FUTURE_MS } }],
  'value-shapes': [{ status: 200, body: { userQuota: { total: 'abc', used: null, remaining: true, unit: '' }, orgResourcePackage: { total: 50, used: '5' },
    totalUsagePercentage: 'n/a', expiresAt: 'soon' } }],
  'expires-seconds': [{ status: 200, body: { userQuota: { total: 10, used: 1 }, expiresAt: FUTURE_S } }],
  'empty': [{ status: 200, body: {} }],
  'body-null': [{ status: 200, body: 'null' }],
  ...failures,
};
// Each connection kind (usage.js:45-50): a device token is used as is; a PAT is
// exchanged for a job token first.
const PAT = 'pt-fixture-pat';
const JOB = 'jt-fixture-job';
// The job token is the scripted exchange's answer, not a connection secret, so
// requests that carry it are recorded as sent.
TOKENS.add(PAT);
const exchangeOK = { status: 200, body: { token: JOB, expires_in: 3600000 } };
const userinfoOK = { status: 200, body: { id: 'user-fixture' } };
const qoderModes = {
  device: { credential: { accessToken: TOKEN }, prefix: [] },
  pat: { credential: { apiKey: PAT }, prefix: [exchangeOK, userinfoOK] },
};
const qoderPatCases = {
  'pat-userinfo-fails': [exchangeOK, { status: 500, body: '' }, { status: 200, body: qoderUsage }],
  'pat-exchange-401': [{ status: 401, body: '{"message":"bad pat"}' }],
  'pat-exchange-no-token': [{ status: 200, body: { expires_in: 10 } }],
  'pat-exchange-invalid-json': [{ status: 200, body: 'not json' }],
  'pat-exchange-network': [{ network: 'The operation was aborted due to timeout' }],
  'pat-as-access-token-exchange-fails': [{ status: 500, body: '' }, { status: 200, body: qoderUsage }],
  'pat-quota-401': [exchangeOK, userinfoOK, { status: 401, body: '' }],
};

// Zed (U/zed.js:116-222, shared/zedAuth.js:155-219)

const zedOk = body => [{ status: 200, body }];
const period = { subscription_period: { started_at: PAST, ended_at: FUTURE } };
const zedCases = {
  'multiple-window': zedOk({ plan: { plan_v3: 'zed_pro', ...period, usage: { edit_predictions: { used: 120, limit: { limited: 2000 } },
    model_requests: { used: 40, limit: { limited: 500 } } } } }),
  'unlimited': zedOk({ plan: { plan_v3: 'zed_pro', ...period, usage: { edit_predictions: { used: 999, limit: 'unlimited' },
    model_requests: { used: 3, limit: { unlimited: true } } } } }),
  'token-billing': zedOk({ plan: { plan_v3: 'zed_pro', ...period, usage: { edit_predictions: { used: 5, limit: 'unlimited' },
    model_requests: { used: 77, limit: { limited: 0 } } } } }),
  'token-billing-number': zedOk({ plan: { plan_v3: 'zed_student', usage: { edit_predictions: { used: 5, limit: 2000 }, model_requests: { used: 1, limit: 0 } } } }),
  'model-requests-no-limit': zedOk({ plan: { plan_v3: 'zed_pro', usage: { model_requests: { used: 3 } } } }),
  'zero': zedOk({ plan: { plan_v3: 'zed_free', ...period, usage: { edit_predictions: { used: 2000, limit: { limited: 2000 } } } } }),
  'over-used': zedOk({ plan: { plan_v3: 'zed_free', ...period, usage: { edit_predictions: { used: 2500, limit: 2000 } } } }),
  'expired': zedOk({ plan: { plan_v3: 'zed_free', subscription_period: { ended_at: PAST }, usage: { edit_predictions: { used: 2000, limit: 2000 } } } }),
  'zero-limit': zedOk({ plan: { plan_v3: 'zed_free', usage: { edit_predictions: { used: 0, limit: { limited: 0 } } } } }),
  'camel-case': zedOk({ plan: { plan_v2: 'zed_business', subscriptionPeriod: { endedAt: FUTURE_S }, trialStartedAt: PAST, hasOverdueInvoices: true,
    usage: { editPredictions: { used: '10', limit: ' 100 ' }, modelRequests: { used: 1, limit: { Limited: 50 } } } } }),
  'trial': zedOk({ plan: { plan_v3: 'zed_pro_trial', trial_started_at: PAST, ...period, usage: { edit_predictions: { used: 1, limit: 'unlimited' },
    model_requests: { used: 10, limit: { limited: 150 } } } } }),
  'trial-label': zedOk({ plan: { plan_v3: 'zed_pro', trial_started_at: PAST, usage: { edit_predictions: { used: 1, limit: 10 } } } }),
  'overdue': zedOk({ plan: { plan_v3: 'zed_pro', has_overdue_invoices: true, ...period, usage: { edit_predictions: { used: 1, limit: 'unlimited' },
    model_requests: { used: 1, limit: { limited: 0 } } } } }),
  'plan-custom': zedOk({ plan: { plan: 'enterprise_TEAM_plan', usage: { edit_predictions: { used: 1, limit: 10 } } } }),
  'plan-top-level': zedOk({ plan_v3: 'ZED_STUDENT', plan: { usage: { edit_predictions: { used: 1, limit: 10 } } } }),
  'limit-shapes': zedOk({ plan: { plan_v3: 'zed_pro', usage: { edit_predictions: { used: -4, limit: { limited: '30' } }, model_requests: { used: 'x', limit: ' unlimited ' } } } }),
  'empty': zedOk({ plan: { plan_v3: 'zed_free', usage: {} } }),
  'no-plan': zedOk({}),
  'body-empty': [{ status: 200, body: '' }],
  ...failures,
};
const zedModes = {
  user: { accessToken: TOKEN, data: { userId: 'user-fixture' } },
  'system-id': { accessToken: TOKEN, data: { userId: 'user-fixture', systemId: 'system-fixture' } },
  'no-user': { accessToken: TOKEN, data: {} },
  'padded-token': { accessToken: ` ${TOKEN} `, data: { userId: 'user-fixture' } },
};

// Record every case.

const record = { pin: '1c3b9f47837d42ea64992063309064af755a14a3', now: new Date(NOW).toISOString(), tz: 'UTC',
  grok: {}, kiro: {}, kiroProviderData: {}, qoder: {}, zed: {} };

async function run(provider, steps, call, extra = {}) {
  clock = NOW;
  const net = network(steps);
  const outcome = await settle(call(net.proxyAwareFetch));
  assert.equal(net.calls.length, steps.length, `every scripted step is read: ${JSON.stringify(net.calls.map(c => c.url))}`);
  const rows = outcome.result ? JSON.parse(JSON.stringify(page.parseQuotaData(provider, outcome.result)))
    .map(row => ({ ...row, remainingPercent: page.getRemainingPercentage(row) })) : null;
  return { ...extra, upstream: steps, ...outcome, page: rows, requests: net.calls };
}

for (const [name, steps] of Object.entries(grokCases)) {
  const token = grokTokens[name] ?? TOKEN;
  record.grok[name] = await run('grok-cli', steps, fetch => load('grok-cli', fetch).getGrokCliUsage(token, grokIdentity[name] ?? {}),
    { ...(grokTokens[name] ? { token } : {}), ...(grokIdentity[name] ? { providerData: grokIdentity[name] } : {}) });
}

for (const [mode, data] of Object.entries(kiroModes)) {
  record.kiro[mode] = {};
  record.kiroProviderData[mode] = data;
  const cases = ['builder-id', 'api_key'].includes(mode) ? kiroCases : { 'get-ok': kiroCases['get-ok'], 'all-401': kiroCases['all-401'] };
  for (const [name, steps] of Object.entries(cases)) {
    record.kiro[mode][name] = await run('kiro', steps, fetch => load('kiro', fetch).getKiroUsage(TOKEN, data));
  }
}

for (const [mode, { credential, prefix }] of Object.entries(qoderModes)) {
  record.qoder[mode] = {};
  for (const [name, steps] of Object.entries(qoderCases)) {
    record.qoder[mode][name] = await run('qoder', [...prefix, ...steps], fetch => load('qoder', fetch).qoder({ ...credential }));
  }
}
record.qoder['pat-flow'] = {};
for (const [name, steps] of Object.entries(qoderPatCases)) {
  const credential = name === 'pat-as-access-token-exchange-fails' ? { accessToken: PAT } : { apiKey: PAT };
  record.qoder['pat-flow'][name] = await run('qoder', steps, fetch => load('qoder', fetch).qoder(credential), { credential: Object.keys(credential)[0] });
}

for (const [mode, { accessToken, data }] of Object.entries(zedModes)) {
  record.zed[mode] = {};
  const cases = mode === 'user' ? zedCases : { 'multiple-window': zedCases['multiple-window'] };
  for (const [name, steps] of Object.entries(mode === 'no-user' ? { 'multiple-window': [] } : cases)) {
    record.zed[mode][name] = await run('zed', steps, fetch => load('zed', fetch).getZedUsage(accessToken, data));
  }
}

const text = `${JSON.stringify(record, null, 2)}\n`;
for (const token of [TOKEN, PAT]) assert.ok(!text.includes(token), 'the record carries no token');
writeFileSync(out, text);
console.log(`wrote ${out.pathname}`);
