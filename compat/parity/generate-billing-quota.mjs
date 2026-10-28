// Records what the pinned Command Code, Kenari, Ollama, OpenCode Go, Vercel AI
// Gateway and Xiaomi MiMo quota readers return, so the Go tests can be offline
// and still differential (#225). Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-billing-quota.mjs
//
// It evaluates the pin's own open-sse/services/usage/{shared,commandcode,kenari,
// misc,opencode-go,xiaomi-mimo}.js and open-sse/shared/mimoAccount.js, read with
// `git show` at the pin, with the network and the clock scripted. Endpoint URLs
// come from the pinned registry entries. MiMo Desktop's cookie store is never
// read: the scripted file system has no such file, and a read with an account
// credential fails the run if it looks for one. Nothing is sent and no real
// credential is used. The Go tests read
// internal/providers/usage/testdata/billing_quota_pin.json; no test regenerates it.
process.env.TZ = 'UTC';

const assert = (await import('node:assert/strict')).default;
const { writeFileSync } = await import('node:fs');
const crypto = (await import('node:crypto')).default;
const path = (await import('node:path')).default;
const vm = (await import('node:vm')).default;
const { source } = await import('./oracle.mjs');

const NOW = Date.parse('2026-10-20T12:00:00.000Z');
const out = new URL('../../internal/providers/usage/testdata/billing_quota_pin.json', import.meta.url);

let clock = NOW;
class FakeDate extends Date {
  constructor(...args) { super(...(args.length ? args : [clock])); }
  static now() { return clock; }
}

// Evaluate pinned code. Imports are replaced by `deps`; every top-level
// declaration in `names` is returned. `export` keywords are dropped.
function evaluate(file, deps, names) {
  const code = source(file).replace(/^export\s*\{[^}]*\}\s*from\s[^;]*;\s*$/gm, '').replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export\s+/gm, '');
  assert.doesNotMatch(code, /^\s*(?:import|export)\b/m, `${file}: unsupported module syntax`);
  const context = vm.createContext({ URL, URLSearchParams, Map, Set, Promise, JSON, Math, Date: FakeDate, Error, Number, String,
    Array, Object, RegExp, AbortSignal, encodeURIComponent, process: { platform: 'linux', pid: 1, env: {} },
    console: { log() {}, warn() {}, error() {} } });
  context.deps = deps;
  return new vm.Script(`(({${Object.keys(deps).join(',')}}) => {${code}\nreturn {${names.join(',')}};})(deps)`, { filename: file })
    .runInContext(context, { timeout: 10000 });
}

// A `key: {...}` literal of a registry entry's transport; it holds only strings.
function registryBlock(id, key) {
  const text = source(`open-sse/providers/registry/${id}.js`);
  const match = text.match(new RegExp(`\\n {4}${key}: (\\{\\n[\\s\\S]*?\\n {4}\\}),\\n`));
  assert.ok(match, `${id}: ${key} block`);
  return Function(`return ${match[1]};`)();
}
const PROVIDERS = Object.fromEntries(['opencode-go', 'vercel-ai-gateway'].map(id => [id, { usage: registryBlock(id, 'usage') }]));

// One scripted network: each request takes the next step. A step is
// {status, body, headers} or {network: message}. Authorization values are
// recorded by scheme only, so the file carries no key.
function network(steps) {
  const calls = [];
  const proxyAwareFetch = async (url, options = {}) => {
    const step = steps[calls.length];
    assert.ok(step, `unscripted request to ${url}`);
    const headers = Object.fromEntries(Object.entries(options.headers ?? {}).map(([k, v]) =>
      k.toLowerCase() === 'authorization' ? [k, String(v).split(' ')[0] + ' <token>'] : [k, v]));
    calls.push({ url, method: options.method ?? 'GET', headers, body: options.body ?? null });
    if (step.network) throw new Error(step.network);
    const body = typeof step.body === 'string' ? step.body : JSON.stringify(step.body);
    const responseHeaders = new Headers({ 'content-type': 'application/json' });
    for (const [name, values] of Object.entries(step.headers ?? {})) for (const value of values) responseHeaders.append(name, value);
    return new Response(body, { status: step.status ?? 200, headers: responseHeaders });
  };
  return { calls, proxyAwareFetch };
}

// MiMo Desktop's cookie store does not exist here; `desktop` counts the looks.
const desktop = { looks: 0 };
const fs = { existsSync: () => { desktop.looks++; return false; } };
const os = { homedir: () => '/nonexistent-home', tmpdir: () => '/nonexistent-tmp' };

function load(name, fetch) {
  const shared = evaluate('open-sse/services/usage/shared.js', { PROVIDERS, proxyAwareFetch: fetch },
    ['U', 'parseResetTime', 'toFiniteNumber']);
  const deps = { proxyAwareFetch: fetch, ...shared };
  switch (name) {
    case 'commandcode': return evaluate('open-sse/services/usage/commandcode.js', deps, ['getCommandCodeUsage']).getCommandCodeUsage;
    case 'kenari': return evaluate('open-sse/services/usage/kenari.js', deps, ['getKenariUsage']).getKenariUsage;
    case 'opencode-go': return evaluate('open-sse/services/usage/opencode-go.js', deps, ['getOpenCodeGoUsage']).getOpenCodeGoUsage;
    case 'ollama': return evaluate('open-sse/services/usage/misc.js', deps, ['getOllamaUsage']).getOllamaUsage;
    case 'vercel-ai-gateway': return evaluate('open-sse/services/usage/misc.js', deps, ['getVercelAiGatewayUsage']).getVercelAiGatewayUsage;
  }
  const account = evaluate('open-sse/shared/mimoAccount.js', { fs, os, path, crypto, proxyAwareFetch: fetch }, ['getMimoAccountUsage']);
  return evaluate('open-sse/services/usage/xiaomi-mimo.js', { proxyAwareFetch: fetch, ...account }, ['getXiaomiMimoUsage']).getXiaomiMimoUsage;
}

const settle = async promise => {
  try { return { result: JSON.parse(JSON.stringify(await promise)) }; } catch (error) { return { error: error.message }; }
};

const FUTURE = '2026-10-21T00:00:00Z';
const PAST = '2026-10-19T00:00:00Z';
const FUTURE_MS = Date.parse(FUTURE);
const PAST_MS = Date.parse(PAST);
const FUTURE_S = FUTURE_MS / 1000;
const DAY = 86400000;

const failures = {
  auth401: [{ status: 401, body: '{"message":"invalid key"}' }],
  auth403: [{ status: 403, body: '{"error":"forbidden"}' }],
  error500: [{ status: 500, body: 'upstream exploded' }],
  timeout: [{ network: 'The operation was aborted due to timeout' }],
  'invalid-json': [{ status: 200, body: 'not json' }],
};
const single = body => [{ status: 200, body }];
const singles = cases => Object.fromEntries(Object.entries(cases).map(([k, v]) => [k, single(v)]));

// ---- Command Code (U/commandcode.js:33-134) ---------------------------------

const ok = body => ({ status: 200, body });
const cc = (whoami, credits, subs) => [ok(whoami), ok(credits), ok(subs)];
const org = { org: { id: 'org_1' } };
const credits = (monthly, purchased, free, windowLimits) => ({ credits: { monthlyCredits: monthly, purchasedCredits: purchased, freeCredits: free }, ...(windowLimits ? { windowLimits } : {}) });
const sub = (planId, currentPeriodEnd = FUTURE) => ({ data: { planId, currentPeriodEnd } });
const commandcodeCases = {
  'multiple-window': cc(org, credits(6, 1, 1, { fiveHour: { used: 2, cap: 10, resetAt: FUTURE }, weekly: { used: 30, cap: 50, resetAt: FUTURE_S } }), sub('individual-go')),
  'zero': cc(org, credits(0, 0, 0, { fiveHour: { used: 10, cap: 10, resetAt: FUTURE }, weekly: { used: 50, cap: 50, resetAt: FUTURE } }), sub('individual-pro')),
  'expired': cc(org, credits(0, 0, 0, { weekly: { used: 50, cap: 50, resetAt: PAST } }), sub('individual-max', PAST)),
  'empty': cc({}, {}, {}),
  'remaining-over-cap': cc(org, credits(10, 15, 0), sub('individual-go')),
  'subscriptions-array': cc(org, credits(5, 0, 0), { data: [{ planId: 'individual-max' }, { planId: 'individual-go' }] }),
  'plan-unknown': cc(org, credits(7, 0, 0), sub('enterprise-custom')),
  'plan-every-name': cc(org, credits(3, 0, 0), sub('teams-pro')),
  'org-escaping': cc({ org: { id: 'a b&c=d/é~*+' } }, credits(1, 0, 0), sub('individual-pro-v1')),
  'org-number': cc({ org: { id: 42 } }, credits(1, 0, 0), sub('individual-goat')),
  'org-empty': cc({ org: { id: '' } }, credits(1, 0, 0), sub('individual-provider')),
  'org-null': cc({ org: null }, credits(1, 0, 0), sub('individual-ultra')),
  'amount-strings': cc(org, credits('6.5', ' 2 ', 'x', { fiveHour: { used: '3', cap: '0x10', resetAt: String(FUTURE_S) }, weekly: { used: '', cap: '40' } }), sub('individual-go', String(FUTURE_MS))),
  'window-shapes': cc(org, credits(1, 0, 0, { fiveHour: null, weekly: { used: 5, cap: 0, resetAt: 'not a date' } }), sub('individual-go', 0)),
  'window-zero-and-scalar': cc(org, credits(1, 0, 0, { fiveHour: { used: 0, cap: 0 }, weekly: 'full' }), sub('individual-go', 'not a date')),
  'window-overused': cc(org, credits(1, 0, 0, { fiveHour: { used: 12, cap: 10, resetAt: FUTURE }, weekly: { used: -5, cap: 10 } }), sub('individual-go')),
  'whoami-invalid-json': [ok('not json'), ok(credits(2, 0, 0)), ok(sub('individual-go'))],
  'credits-invalid-json': [ok(org), ok('not json'), ok(sub('individual-go'))],
  'subscriptions-invalid-json': [ok(org), ok(credits(2, 0, 0)), ok('not json')],
  'credits-auth401': [ok(org), { status: 401, body: '{}' }, ok(sub('individual-go'))],
  'subscriptions-auth403': [ok(org), ok(credits(2, 0, 0)), { status: 403, body: '{}' }],
  'credits-error500': [ok(org), { status: 500, body: 'boom' }, ok(sub('individual-go'))],
  'subscriptions-error500': [ok(org), ok(credits(2, 0, 0)), { status: 502, body: 'boom' }],
  'credits-timeout': [ok(org), { network: 'The operation was aborted due to timeout' }, ok(sub('individual-go'))],
  ...Object.fromEntries(Object.entries(failures).filter(([k]) => k !== 'invalid-json')),
};

// ---- Kenari (U/kenari.js:9-56) ----------------------------------------------

const rp = (used, remaining, resets_at = FUTURE) => ({ used_rp: used, remaining_rp: remaining, resets_at });
const kenariCases = {
  'multiple-window': { plan: { name: 'Pro', windows: { month: rp(250000, 750000), week: rp(50000, 50000, FUTURE_MS) } } },
  // Raw bodies, so the keys arrive in this order: JavaScript lists array-index
  // keys first, ascending, and a repeated key keeps its first place.
  'window-order': `{"plan":{"name":"Pro","windows":{"week":${JSON.stringify(rp(1, 3))},"month":${JSON.stringify(rp(1, 1))},`
    + `"7":${JSON.stringify(rp(3, 1))},"custom":${JSON.stringify(rp(0, 10))},"10":${JSON.stringify(rp(1, 9))},"02":${JSON.stringify(rp(2, 2))}}}}`,
  'duplicate-keys': `{"plan":{"name":"Pro","windows":{"week":${JSON.stringify(rp(1, 1))},"month":${JSON.stringify(rp(1, 3))},"week":${JSON.stringify(rp(9, 1))}}}}`,
  'zero': { plan: { name: 'Pro', windows: { month: rp(1000000, 0), week: rp(100000, 0) } } },
  'zero-capacity': { plan: { name: 'Pro', windows: { week: rp(0, 0), month: rp(250000, 750000) } } },
  'expired': { plan: { name: 'Pro', windows: { week: rp(100000, 0, PAST) } } },
  'empty': { plan: { name: 'Free', windows: {} } },
  'no-windows': { plan: { name: 'Free' } },
  'no-plan': {},
  'plan-empty-string': { plan: '' },
  'plan-name-shapes': { plan: { name: 5, windows: { week: rp(1, 1) } } },
  'plan-name-empty': { plan: { name: '', windows: { week: rp(1, 1) } } },
  'amount-strings': { plan: { name: 'Pro', windows: { month: rp('1000', ' 3000 ', String(FUTURE_S)), week: rp('0x10', '16', '') } } },
  'rounding': { plan: { name: 'Pro', windows: { month: rp(1, 2), week: rp(2, 1, 0) } } },
  'negative': { plan: { name: 'Pro', windows: { week: rp(100, -50), month: rp(-10, 30) } } },
  'label-collision': { plan: { name: 'Pro', windows: { month: rp(1, 3), week: rp(1, 1), 'Monthly (IDR)': rp(3, 1) } } },
  'window-null': { plan: { name: 'Pro', windows: { month: null } } },
  'amount-missing': { plan: { name: 'Pro', windows: { week: { resets_at: FUTURE }, month: { used_rp: 5, resets_at: FUTURE } } } },
};

// ---- OpenCode Go (U/opencode-go.js:16-107) ----------------------------------

const pct = (percent, resetsAt = FUTURE) => ({ percent, resetsAt });
const opencodeCases = {
  'multiple-window': { usage: { rolling: pct(20), weekly: pct('45.5', FUTURE_S), monthly: pct(0, String(FUTURE_MS)) } },
  'window-order': { usage: { monthly: pct(10), weekly: pct(20), rolling: pct(30), daily: pct(40) } },
  'zero': { usage: { rolling: pct(100), weekly: pct(100), monthly: pct(100) } },
  'expired': { usage: { rolling: pct(100, PAST) } },
  'clamped': { usage: { rolling: pct(150), weekly: pct(-5), monthly: pct('0x10') } },
  'empty': { usage: {} },
  'no-usage': {},
  'usage-array': { usage: [] },
  'usage-string': { usage: 'none' },
  'percent-shapes': { usage: { rolling: null, weekly: pct('  '), monthly: pct(true) } },
  'percent-mixed': { usage: { rolling: pct('abc'), weekly: [1], monthly: pct('12', 'not a date') } },
};
const opencodeFailures = {
  ...failures,
  'auth403-entitlement': [{ status: 403, body: { error: { type: 'EntitlementError', message: 'Subscribe to OpenCode Go' } } }],
  'auth403-not-json': [{ status: 403, body: 'forbidden' }],
};

// ---- Ollama (U/misc.js:27-144) ----------------------------------------------

const usage = limits => ({ status: 200, body: { limits } });
const me = body => ({ status: 200, body });
const ollamaCases = {
  'multiple-window': [usage({ session: { usage: 0.25 }, weekly: { usage: 0.5 } }), me({ Plan: 'pro' })],
  'free-monthly': [usage({ monthly: { usage: 0.333 } }), me({ Plan: 'free', CreatedAt: '2026-03-31T10:20:30.500Z' })],
  'free-next-month': [usage({ monthly: { usage: 0.1 } }), me({ Plan: 'FREE', CreatedAt: '2025-04-15T08:00:00Z' })],
  'free-created-number': [usage({ monthly: { usage: 0.1 } }), me({ Plan: 'free', CreatedAt: Date.parse('2026-01-25T00:00:00Z') })],
  'free-created-invalid': [usage({ monthly: { usage: 0.1 } }), me({ Plan: 'free', CreatedAt: 'not a date' })],
  'free-created-future': [usage({ monthly: { usage: 0 } }), me({ Plan: 'free', CreatedAt: '2026-11-05T00:00:00Z' })],
  'free-created-far-future': [usage({ monthly: { usage: 0 } }), me({ Plan: 'free', CreatedAt: '2027-03-05T00:00:00Z' })],
  'free-no-created': [usage({ monthly: { usage: 0.2 } }), me({ Plan: 'free' })],
  'paid-monthly': [usage({ monthly: { usage: 0.2 } }), me({ Plan: 'pro', CreatedAt: '2026-03-31T10:20:30Z' })],
  'zero': [usage({ session: { usage: 1 }, weekly: { usage: 1.0 } }), me({ Plan: 'pro' })],
  // A window's reset is never reported, and the signup anniversary falling now
  // moves to the next month: nothing the pin reads can expire.
  'expired': [usage({ session: { usage: 1, resetAt: PAST }, monthly: { usage: 1 } }), me({ Plan: 'free', CreatedAt: '2026-09-20T12:00:00Z' })],
  'empty': [usage({}), me({ Plan: 'pro' })],
  'no-limits': [{ status: 200, body: {} }, me({ Plan: 'pro' })],
  'body-null': [{ status: 200, body: 'null' }, me({ Plan: 'pro' })],
  'ratio-shapes': [usage({ session: { usage: '0.4' }, weekly: { usage: null }, monthly: { usage: 'abc' } }), me({ Plan: 'max' })],
  'ratio-coercion': [usage({ session: { usage: true }, weekly: { usage: [] }, monthly: { usage: '' } }), me({ Plan: 'pro' })],
  'ratio-clamped': [usage({ session: { usage: 1.5 }, weekly: { usage: -0.2 }, monthly: { usage: 0.335 } }), me({ Plan: 'pro' })],
  'limit-shapes': [usage({ session: 5, weekly: 'x', monthly: [] }), me({ Plan: 'pro' })],
  'window-order': [usage({ monthly: { usage: 0.3 }, weekly: { usage: 0.2 }, session: { usage: 0.1 }, daily: { usage: 0.9 } }), me({ Plan: 'pro' })],
  'me-error': [usage({ session: { usage: 0.25 } }), { status: 500, body: 'boom' }],
  'me-invalid-json': [usage({ session: { usage: 0.25 } }), { status: 200, body: 'not json' }],
  'me-timeout': [usage({ session: { usage: 0.25 } }), { network: 'The operation was aborted due to timeout' }],
  'me-plan-shapes': [usage({ session: { usage: 0.25 } }), me({ Plan: 5 })],
  'me-plan-unicode': [usage({ session: { usage: 0.25 } }), me({ Plan: 'éLITE' })],
  'me-plan-empty': [usage({ session: { usage: 0.25 } }), me({ Plan: '' })],
  ...failures,
};
// The free plan's monthly reset depends on the clock; these run at another now.
const ollamaNow = {
  'free-month-end': ['2026-11-20T12:00:00.000Z', [usage({ monthly: { usage: 0.5 } }), me({ Plan: 'free', CreatedAt: '2026-01-31T23:59:59.999Z' })]],
  'free-leap-day': ['2027-02-10T00:00:00.000Z', [usage({ monthly: { usage: 0.5 } }), me({ Plan: 'free', CreatedAt: '2024-02-29T06:00:00Z' })]],
  'free-year-end': ['2026-12-31T00:00:00.000Z', [usage({ monthly: { usage: 0.5 } }), me({ Plan: 'free', CreatedAt: '2025-01-31T00:00:00Z' })]],
};

// ---- Vercel AI Gateway (U/misc.js:161-228) ---------------------------------

const vercelCases = {
  'multiple-window': { balance: '3.25', total_used: '1.75' },
  'zero': { balance: '0', total_used: '5' },
  // Nothing the pin reads carries a reset, so a reported one is ignored.
  'expired': { balance: '0.00', total_used: '5.00', resetAt: PAST },
  'over-allocation': { balance: '95.50', total_used: '4.50' },
  'empty': { balance: '0', total_used: '0' },
  'no-fields': {},
  'body-null': null,
  'negative': { balance: '-1', total_used: '2' },
  'amount-shapes': { balance: 2, total_used: '' },
  'amount-coercion': { balance: '0x2', total_used: true },
  'amount-invalid': { balance: 'abc', total_used: '1' },
};

// ---- Xiaomi MiMo (U/xiaomi-mimo.js:22-125, shared/mimoAccount.js:96-264) ------

const ACCOUNT = { mimoPassToken: 'fixture-pass-token', mimoUserId: 'fixture-user', mimoCUserId: 'fixture-cuser' };
const STS = 'https://mimo-server-cn.xiaomimimo.com/api/sts?sign=fixture-sign&followup=https%3A%2F%2Fmimo.example';
const sso = [
  { status: 302, body: '', headers: { location: [`https://account.xiaomi.com/pass/serviceLogin?sid=mimopc&callback=${encodeURIComponent(STS)}`] } },
  { status: 200, body: '&&&START&&&{"nonce":"fixture-nonce","ssecurity":"fixture-ssecurity","location":"https://account.xiaomi.com/pass/serviceLoginAuth2?sid=passportapi&_sign=fixture"}' },
  { status: 302, body: '', headers: { 'set-cookie': ['serviceToken=fixture-account-service; Path=/; Secure'] } },
  { status: 200, body: `&&&START&&&{"location":"https://mimo-server-cn.xiaomimimo.com/api/sts?ticket=fixture-ticket&sign=fixture-sign"}` },
  { status: 302, body: '', headers: { 'set-cookie': ['serviceToken=fixture-mimo-service; Path=/', 'mimopc_ph=fixture-ph', 'mimopc_slh=fixture-slh', 'userId=fixture-user'] } },
];
const weekly = (data, code = 0) => ({ status: 200, body: { code, data } });
const keyed = { accessToken: 'fixture-token', data: null };
const both = { accessToken: 'fixture-token', data: ACCOUNT };
const accountOnly = { accessToken: null, data: ACCOUNT };
const mimoCases = {
  // The key path (no account credential).
  'key/multiple-window': [keyed, [weekly({ percent: 94, resetDate: '2026-10-26', resetAt: FUTURE_S, monthly: { percent: 10 } })]],
  'key/zero': [keyed, [weekly({ percent: 0, resetDate: '2026-10-26' })]],
  'key/expired': [keyed, [weekly({ percent: 0, resetDate: '2026-10-19' })]],
  'key/empty': [keyed, [weekly({})]],
  'key/percent-string': [keyed, [weekly({ percent: '94', resetDate: '2026-10-26' })]],
  'key/percent-rounding': [keyed, [weekly({ percent: 33.5, resetDate: 'not a date' })]],
  'key/percent-clamped': [keyed, [weekly({ percent: 150 })]],
  'key/code-string': [keyed, [weekly({ percent: 50 }, '0')]],
  'key/code-nonzero': [keyed, [weekly({ percent: 50 }, 401)]],
  'key/data-missing': [keyed, [{ status: 200, body: { code: 0 } }]],
  'key/reset-shapes': [keyed, [weekly({ percent: 50, resetDate: '2026-10-26T08:00:00Z' })]],
  ...Object.fromEntries(Object.entries(failures).map(([name, steps]) => [`key/${name}`, [keyed, steps]])),
  'key/blank': [{ accessToken: '  ', data: null }, []],
  'none': [{ accessToken: null, data: null }, []],
  // The account path: the SSO exchange, then the usage read.
  'account/multiple-window': [both, [...sso, weekly({ percent: 94, resetDate: '2026-10-26', resetAt: FUTURE_S })]],
  'account/reset-date': [accountOnly, [...sso, weekly({ percent: 40, resetDate: '2026-10-26', resetAt: 0 })]],
  'account/zero': [accountOnly, [...sso, weekly({ percent: 0, resetAt: FUTURE_S })]],
  'account/expired': [accountOnly, [...sso, weekly({ percent: 0, resetAt: PAST_MS / 1000 })]],
  'account/reset-out-of-range': [both, [...sso, weekly({ percent: 50, resetAt: 1e14 })]],
  'account/empty': [both, [...sso, weekly({}), weekly({ percent: 70, resetDate: '2026-10-26', resetAt: FUTURE_S })]],
  'account/percent-string': [both, [...sso, weekly({ percent: '94' }), weekly({ percent: 60 })]],
  'account/code-string': [both, [...sso, weekly({ percent: 94 }, '0'), weekly({ percent: 60 })]],
  'account/auth401': [both, [...sso, { status: 401, body: '{}' }, weekly({ percent: 60 })]],
  'account/error500': [both, [...sso, { status: 500, body: 'boom' }, weekly({ percent: 60 })]],
  'account/timeout': [both, [...sso, { network: 'The operation was aborted due to timeout' }, weekly({ percent: 60 })]],
  'account/invalid-json': [both, [...sso, { status: 200, body: 'not json' }, weekly({ percent: 60 })]],
  'account/failed-no-key': [accountOnly, [...sso, { status: 500, body: 'boom' }]],
  'account/sso-no-redirect': [both, [{ status: 200, body: '{}' }, weekly({ percent: 60 })]],
  'account/sso-no-nonce': [both, [sso[0], { status: 200, body: '&&&START&&&{"location":"https://account.xiaomi.com/pass/x"}' }, weekly({ percent: 60 })]],
  'account/sso-no-sts': [both, [...sso.slice(0, 3), { status: 200, body: '&&&START&&&{"location":"https://account.xiaomi.com/elsewhere"}' }, weekly({ percent: 60 })]],
  'account/sso-no-service-token': [both, [...sso.slice(0, 4), { status: 302, body: '' }, weekly({ percent: 60 })]],
};
// Reads in sequence on one loaded module: the account session is kept for 30
// minutes per passToken (mimoAccount.js:29-33,181-218). Each read is
// [milliseconds after the first, credential, steps].
const mimoSessions = {
  'session-reused': [[0, accountOnly, [...sso, weekly({ percent: 90 })]], [29 * 60000, accountOnly, [weekly({ percent: 80 })]]],
  'session-expired': [[0, accountOnly, [...sso, weekly({ percent: 90 })]], [30 * 60000, accountOnly, [...sso, weekly({ percent: 80 })]]],
  'session-failure-not-cached': [[0, both, [{ status: 200, body: '{}' }, weekly({ percent: 60 })]], [60000, both, [...sso, weekly({ percent: 80 })]]],
  'session-per-pass-token': [[0, accountOnly, [...sso, weekly({ percent: 90 })]],
    [60000, { accessToken: null, data: { ...ACCOUNT, mimoPassToken: 'fixture-pass-token-2' } }, [...sso, weekly({ percent: 50 })]]],
  'session-kept-after-usage-failure': [[0, both, [...sso, { status: 401, body: '{}' }, weekly({ percent: 60 })]], [60000, both, [weekly({ percent: 80 })]]],
};

// ---- run ----------------------------------------------------------------

const record = { pin: '1c3b9f47837d42ea64992063309064af755a14a3', now: new Date(NOW).toISOString(), tz: 'UTC',
  commandcode: {}, kenari: {}, 'opencode-go': {}, ollama: {}, 'vercel-ai-gateway': {}, 'xiaomi-mimo': {} };

async function run(name, steps, call, now = NOW) {
  clock = now;
  const net = network(steps);
  const outcome = await settle(call(load(name, net.proxyAwareFetch)));
  assert.equal(net.calls.length, steps.length, `every scripted step is read: ${JSON.stringify(net.calls.map(c => c.url))}`);
  return { ...(now === NOW ? {} : { now: new Date(now).toISOString() }), upstream: steps, ...outcome, requests: net.calls };
}

for (const [name, steps] of Object.entries(commandcodeCases)) {
  record.commandcode[name] = await run('commandcode', steps, read => read('fixture-token'));
}
for (const [name, steps] of Object.entries({ ...singles(kenariCases), ...failures })) {
  record.kenari[name] = await run('kenari', steps, read => read('fixture-token'));
}
for (const [name, steps] of Object.entries({ ...singles(opencodeCases), ...opencodeFailures })) {
  record['opencode-go'][name] = await run('opencode-go', steps, read => read('fixture-token'));
}
for (const [name, steps] of Object.entries(ollamaCases)) {
  record.ollama[name] = await run('ollama', steps, read => read('fixture-token', {}));
}
for (const [name, [now, steps]] of Object.entries(ollamaNow)) {
  record.ollama[name] = await run('ollama', steps, read => read('fixture-token', {}), Date.parse(now));
}
for (const [name, body] of Object.entries(vercelCases)) {
  record['vercel-ai-gateway'][name] = await run('vercel-ai-gateway', single(body === null ? 'null' : body), read => read('fixture-token'));
}
for (const [name, steps] of Object.entries(failures)) {
  record['vercel-ai-gateway'][name] = await run('vercel-ai-gateway', steps, read => read('fixture-token'));
}
// The key is recorded as <token>; a blank one as sent.
const redact = key => key === 'fixture-token' ? '<token>' : key;
for (const [name, [credential, steps]] of Object.entries(mimoCases)) {
  const looks = desktop.looks;
  record['xiaomi-mimo'][name] = { credential: { accessToken: redact(credential.accessToken), account: credential.data !== null },
    ...await run('xiaomi-mimo', steps, read => read(credential.accessToken, credential.data)) };
  assert.equal(desktop.looks - looks, credential.data ? 0 : 1, `${name}: the desktop cookie store is looked for only without an account credential`);
}
for (const [name, reads] of Object.entries(mimoSessions)) {
  const steps = reads.flatMap(([, , s]) => s);
  const net = network(steps);
  const read = load('xiaomi-mimo', net.proxyAwareFetch);
  const results = [];
  for (const [after, credential, s] of reads) {
    clock = NOW + after;
    const before = net.calls.length;
    const outcome = await settle(read(credential.accessToken, credential.data));
    results.push({ after, credential: { accessToken: redact(credential.accessToken), account: true, passToken: credential.data.mimoPassToken },
      ...outcome, requests: net.calls.slice(before) });
    assert.equal(net.calls.length - before, s.length, `${name}: read takes its own steps`);
  }
  record['xiaomi-mimo'][name] = { upstream: steps, reads: results };
}

writeFileSync(out, `${JSON.stringify(record, null, 2)}\n`);
console.log(`wrote ${out.pathname}`);
