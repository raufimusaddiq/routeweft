// Records what the pinned GLM, Kimi, MiniMax and CodeBuddy quota readers return,
// so the Go tests can be offline and still differential (#223). Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-regional-quota.mjs
//
// It evaluates the pin's own open-sse/services/usage/{shared,glm,kimi,minimax,
// codebuddy-cn}.js, read with `git show` at the pin, with the network and the
// clock scripted. Endpoint URLs and the CodeBuddy identity headers come from the
// pinned registry entries; buildKimiHeaders is the pin's own function with a
// fixed host. The process runs in UTC, because the pin parses Tencent's
// "YYYY-MM-DD hh:mm:ss" dates in the host's zone. Nothing is sent and no
// credential is used. The Go tests read
// internal/providers/usage/testdata/regional_quota_pin.json; no test regenerates it.
process.env.TZ = 'UTC';

const assert = (await import('node:assert/strict')).default;
const { writeFileSync } = await import('node:fs');
const vm = (await import('node:vm')).default;
const { source } = await import('./oracle.mjs');

assert.equal(new Date('2026-10-01 00:00:00').toISOString(), '2026-10-01T00:00:00.000Z', 'the generator must run in UTC');

const NOW = Date.parse('2026-10-20T12:00:00.000Z');
const out = new URL('../../internal/providers/usage/testdata/regional_quota_pin.json', import.meta.url);

let clock = NOW;
class FakeDate extends Date {
  constructor(...args) { super(...(args.length ? args : [clock])); }
  static now() { return clock; }
}

// Evaluate pinned code. Imports are replaced by `deps`; every top-level
// declaration in `names` is returned. `export` keywords are dropped.
function evaluateCode(path, code, deps, names) {
  code = code.replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export\s+/gm, '');
  assert.doesNotMatch(code, /^\s*(?:import|export)\b/m, `${path}: unsupported module syntax`);
  const context = vm.createContext({ URL, Map, Set, Promise, JSON, Math, Date: FakeDate, Error, Number, String, Array, Object,
    RegExp, console: { log() {}, warn() {}, error() {} } });
  context.deps = deps;
  return new vm.Script(`(({${Object.keys(deps).join(',')}}) => {${code}\nreturn {${names.join(',')}};})(deps)`, { filename: path })
    .runInContext(context, { timeout: 10000 });
}
const evaluate = (path, deps, names) => evaluateCode(path, source(path), deps, names);

// A `key: {...}` literal of a registry entry's transport; it holds only strings.
function registryBlock(id, key) {
  const text = source(`open-sse/providers/registry/${id}.js`);
  const match = text.match(new RegExp(`\\n {4}${key}: (\\{\\n[\\s\\S]*?\\n {4}\\}),\\n`));
  assert.ok(match, `${id}: ${key} block`);
  return Function(`return ${match[1]};`)();
}

const IDS = ['glm', 'glm-cn', 'minimax', 'minimax-cn', 'codebuddy-cn', 'codebuddy-intl'];
const PROVIDERS = Object.fromEntries(IDS.map(id => [id, { usage: registryBlock(id, 'usage') }]));
for (const id of ['codebuddy-cn', 'codebuddy-intl']) PROVIDERS[id].headers = registryBlock(id, 'headers');

// buildKimiHeaders (config/appConstants.js) on a fixed host.
const appConstants = source('open-sse/config/appConstants.js');
const kimiHeadersSource = appConstants.match(/\nexport function buildKimiHeaders\(deviceId\) \{\n[\s\S]*?\n\}\n/);
assert.ok(kimiHeadersSource, 'buildKimiHeaders');
const { buildKimiHeaders } = evaluateCode('open-sse/config/appConstants.js#buildKimiHeaders', kimiHeadersSource[0],
  { platform: () => 'linux', arch: () => 'x64', hostname: () => 'fixture-host', getAppPackageVersion: () => '0.0.0-fixture' },
  ['buildKimiHeaders']);

// One scripted network: each request takes the next step. A step is
// {status, body} or {network: message}. Authorization and x-api-key values are
// recorded by scheme only, so the file carries no token.
function network(steps) {
  const calls = [];
  const proxyAwareFetch = async (url, options = {}) => {
    const step = steps[calls.length];
    assert.ok(step, `unscripted request to ${url}`);
    const headers = Object.fromEntries(Object.entries(options.headers ?? {}).map(([k, v]) => {
      const name = k.toLowerCase();
      if (name === 'authorization') return [k, String(v).split(' ')[0] + ' <token>'];
      if (name === 'x-api-key') return [k, '<token>'];
      return [k, v];
    }));
    calls.push({ url, method: options.method ?? 'GET', headers, body: options.body ?? null });
    if (step.network) throw new Error(step.network);
    const body = typeof step.body === 'string' ? step.body : JSON.stringify(step.body);
    return new Response(body, { status: step.status ?? 200, headers: { 'content-type': 'application/json' } });
  };
  return { calls, proxyAwareFetch };
}

function load(name, fetch) {
  const shared = evaluate('open-sse/services/usage/shared.js', { PROVIDERS, proxyAwareFetch: fetch },
    ['U', 'parseResetTime', 'toFiniteNumber']);
  const deps = { proxyAwareFetch: fetch, ...shared };
  if (name === 'glm') return evaluate('open-sse/services/usage/glm.js', deps, ['getGlmUsage']);
  if (name === 'kimi') return evaluate('open-sse/services/usage/kimi.js', { ...deps, buildKimiHeaders }, ['getKimiUsage']);
  if (name === 'minimax') return evaluate('open-sse/services/usage/minimax.js', deps, ['getMiniMaxUsage']);
  return evaluate('open-sse/services/usage/codebuddy-cn.js', { ...deps, PROVIDERS }, ['getCodeBuddyCnUsage', 'getCodeBuddyIntlUsage']);
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
  auth401: [{ status: 401, body: '{"message":"invalid token"}' }],
  auth403: [{ status: 403, body: '{"error":"forbidden"}' }],
  error500: [{ status: 500, body: 'upstream exploded' }],
  timeout: [{ network: 'The operation was aborted due to timeout' }],
  'invalid-json': [{ status: 200, body: 'not json' }],
};

// ---- GLM (U/glm.js:18-88) ---------------------------------------------------

const limit = (type, unit, number, percentage, nextResetTime = FUTURE_MS) => ({ type, unit, number, percentage, nextResetTime });
const glmCases = {
  'multiple-window': { data: { level: 'pro', limits: [limit('TOKENS_LIMIT', 3, 5, 20), limit('TOKENS_LIMIT', 6, 1, 45),
    limit('CREDIT_LIMIT', 5, 1000, 10), limit('TIME_LIMIT', 5, 1, 99)] } },
  'zero': { data: { level: 'lite', limits: [limit('TOKENS_LIMIT', 3, 5, 100), limit('TOKENS_LIMIT', 6, 1, 100)] } },
  'expired': { data: { level: 'lite', limits: [limit('TOKENS_LIMIT', 3, 5, 100, PAST_MS)] } },
  'empty': { data: { level: 'max', limits: [] } },
  'no-data': {},
  'data-not-object': { data: 'none', level: 'pro' },
  'duplicate-labels': { data: { level: 'MAX', limits: [limit('TOKENS_LIMIT', 3, 5, 10), limit('CREDIT_LIMIT', 1, 2, 30),
    limit('TOKENS_LIMIT', 6, 1, 40), limit('CREDIT_LIMIT', 3, 5, 70), limit('TOKENS_LIMIT', 1, 1, 50), limit('TOKENS_LIMIT', 2, 9, 60),
    limit('CREDIT_LIMIT', 1, 2, 80)] } },
  'raw-shapes': { data: { level: 7, limits: [null, 5, { type: 'TOKENS_LIMIT', unit: '3', number: '5', percentage: '30', nextResetTime: String(FUTURE_MS) },
    { type: 'CREDIT_LIMIT', unit: 3, percentage: null, nextResetTime: FUTURE_S }, { type: 'CREDIT_LIMIT', unit: 4, number: 2.5, percentage: true },
    { type: 'TOKENS_LIMIT', unit: 6, number: 1, percentage: 'abc', nextResetTime: 'soon' }] } },
  'out-of-range': { data: { level: 'pro', limits: [limit('TOKENS_LIMIT', 3, 5, 150), limit('TOKENS_LIMIT', 6, 1, -10, 0)] } },
  'reset-out-of-range': { data: { level: 'pro', limits: [limit('TOKENS_LIMIT', 3, 5, 10, 9e15)] } },
  'unicode-level': { data: { level: 'élite', limits: [limit('TOKENS_LIMIT', 3, 5, 10)] } },
};

// ---- Kimi (U/kimi.js:103-211) -----------------------------------------------

const kimiCases = {
  'multiple-window': { usage: { limit: 100, used: 30, remaining: 70, resetTime: FUTURE },
    limits: [{ window: { duration: 300, timeUnit: 'TIME_UNIT_MINUTE' }, detail: { limit: 50, remaining: 10, resetTime: FUTURE } }],
    user: { membership: { level: 'LEVEL_INTERMEDIATE' } } },
  'capitalized': { usage: { Limit: '200', Used: '20', Remaining: '150', ResetTime: String(FUTURE_S) },
    limits: [{ detail: { Limit: 40, Remaining: '4', reset_at: FUTURE_S * 1000 } }], user: { membership: { level: 'LEVEL_PREMIUM' } } },
  'remaining-precedence': { usage: { limit: 100, used: 10, remaining: 20, reset_at: FUTURE }, user: { membership: { level: 'LEVEL_STANDARD' } } },
  'remaining-blank': { usage: { limit: 100, used: 25, remaining: '', resetAt: String(FUTURE_S) } },
  'remaining-over-limit': { usage: { limit: 100, used: 0, remaining: 150 }, limits: [{ detail: { limit: 10, remaining: 30 } }] },
  'reset-aliases': { usage: { limit: 10, used: 1, resetTime: '', ResetTime: null, reset_at: FUTURE_S, resetAt: PAST },
    limits: [{ detail: { limit: 5, remaining: 5, resetTime: 0, ResetTime: FUTURE, reset_at: '', resetAt: FUTURE } }] },
  'ratelimit-last-wins': { limits: [{ detail: { limit: 60, remaining: 30 } }, null, 'x', { detail: { limit: 0, remaining: 0 } },
    { detail: { limit: 20 } }, { nodetail: true }], user: { membership: { level: 'LEVEL_' } } },
  'zero': { usage: { limit: 100, used: 100, remaining: 0, resetTime: FUTURE }, limits: [{ detail: { limit: 50, remaining: 0, resetTime: FUTURE } }],
    user: { membership: { level: 'LEVEL_BASIC' } } },
  'expired': { usage: { limit: 100, used: 100, remaining: 0, resetTime: PAST } },
  'empty': {},
  'empty-with-level': { usage: { limit: 0, used: 0 }, limits: [], user: { membership: { level: 'LEVEL_ADVANCED' } } },
  'level-number': { usage: { limit: 10, used: 1 }, user: { membership: { level: 3 } } },
  'level-custom': { usage: { limit: 10, used: 1 }, user: { membership: { level: 'Custom_Tier' } } },
  'usage-not-object': { usage: 'n/a', limits: { detail: { limit: 5 } } },
};
const kimiFailures = {
  ...failures,
  'auth403-permission': [{ status: 403, body: JSON.stringify({ code: 'permission_denied', message: 'permission denied',
    details: [{ type: 'type.googleapis.com/x', debug: { reason: 'REASON_FEATURE_NO_PERMISSION',
      localizedMessage: { message: 'Subscribe to Kimi Code to view usage.' } } }] }) }],
  'auth403-subscribe-text': [{ status: 403, body: 'Please subscribe first' }],
};
// The pin's auth branch (kimi.js:109-121): an API key wins over an access token.
const kimiModes = {
  apikey: { accessToken: null, apiKey: 'fixture-token', data: null },
  'apikey-and-token': { accessToken: 'fixture-token', apiKey: 'fixture-token', data: { deviceId: 'device-fixture' } },
  oauth: { accessToken: 'fixture-token', apiKey: null, data: { deviceId: ' device-fixture ' } },
};

// ---- MiniMax (U/minimax.js:133-234) ----------------------------------------

const ok = body => ({ status: 200, body: { base_resp: { status_code: 0, status_msg: 'success' }, ...body } });
const mm = {
  mseries: { model_name: 'MiniMax-M*', current_interval_total_count: 0, current_interval_usage_count: 0,
    current_interval_remaining_percent: 40, current_weekly_total_count: 0, current_weekly_remaining_percent: 70,
    remains_time: 3600000, weekly_end_time: FUTURE_MS },
  speech: { model_name: 'speech-02-hd', current_interval_total_count: 100, current_interval_usage_count: 30, end_time: FUTURE,
    current_weekly_total_count: 1000, current_weekly_usage_count: 900, weekly_remains_time: '7200000' },
  video: { model_name: 'image_to_video', current_interval_total_count: 10, current_interval_usage_count: 10, remains_time: 0, end_time: FUTURE_S },
};
const sp = (code, msg, extra = {}) => ({ status: 200, body: { base_resp: { status_code: code, ...(msg === undefined ? {} : { status_msg: msg }) }, ...extra } });
const mmQuota = { model_remains: [mm.speech] };
const minimaxCases = {
  'multiple-window': [ok({ model_remains: [mm.mseries, mm.speech, mm.video] })],
  'camel-envelope': [{ status: 200, body: { baseResp: { statusCode: 0, statusMsg: 'success' }, modelRemains: [
    { modelName: 'general', currentIntervalTotalCount: 200, currentIntervalUsageCount: 50, currentIntervalRemainingPercent: 90,
      remainsTime: '60000', currentWeeklyTotalCount: '400', currentWeeklyUsageCount: '100', weeklyEndTime: String(FUTURE_S) },
    { modelName: 'tts_turbo', currentIntervalRemainingPercent: '33.3', endTime: FUTURE }] } }],
  'snake-over-camel': [ok({ model_remains: [{ model_name: 'MiniMax-M2', modelName: 'Ignored', current_interval_total_count: 'abc',
    currentIntervalTotalCount: 100, current_interval_remaining_percent: null, currentIntervalRemainingPercent: 150,
    current_weekly_remaining_percent: -5, current_weekly_total_count: 0 }] })],
  'zero': [ok({ model_remains: [{ model_name: 'MiniMax-M2', current_interval_total_count: 100, current_interval_usage_count: 100, end_time: FUTURE },
    { model_name: 'MiniMax-M*', current_interval_remaining_percent: 0, remains_time: 60000 }] })],
  'expired': [ok({ model_remains: [{ model_name: 'MiniMax-M2', current_interval_total_count: 100, current_interval_usage_count: 100, end_time: PAST }] })],
  'empty': [ok({ model_remains: [] })],
  'no-quota-models': [ok({ model_remains: [{ model_name: 'MiniMax-M2', current_interval_total_count: 0, current_interval_remaining_percent: null },
    { model_name: 'x', current_interval_remaining_percent: 'n/a' }] })],
  'duplicate-names': [ok({ model_remains: [{ model_name: 'speech-hd', current_interval_total_count: 100, current_interval_usage_count: 10 },
    { model_name: 'speech_hd', current_interval_total_count: 100, current_interval_usage_count: 60 }, { model_name: '', current_interval_total_count: 5 },
    { model_name: 42, current_interval_total_count: 5, current_interval_usage_count: 1 }, { model_name: 'to-be_tts-hd', current_interval_total_count: 5 }] })],
  'fallback-404': [{ status: 404, body: '{}' }, ok({ model_remains: [mm.speech, mm.mseries] })],
  'fallback-405': [{ status: 405, body: '' }, ok({ model_remains: [mm.video] })],
  'fallback-500': [{ status: 502, body: 'bad gateway' }, ok({ model_remains: [mm.speech] })],
  'fallback-network': [{ network: 'connect ECONNREFUSED' }, ok({ model_remains: [mm.speech] })],
  'no-fallback-429': [{ status: 429, body: '{"base_resp":{"status_code":1002,"status_msg":"rate limit"}}' }],
  'no-fallback-400': [{ status: 400, body: '{}' }],
  'no-fallback-empty': [ok({ model_remains: [] })],
  'no-fallback-status': [ok({ base_resp: { status_code: 2013, status_msg: 'invalid params' }, model_remains: [mm.speech] })],
  'no-fallback-invalid-json': [{ status: 200, body: 'not json' }],
  'both-500': [{ status: 500, body: '' }, { status: 503, body: '' }],
  'last-404': [{ status: 404, body: '' }, { status: 404, body: '' }],
  'timeout': [{ network: 'The operation was aborted due to timeout' }, { network: 'The operation was aborted due to timeout' }],
  'auth401': [{ status: 401, body: '{}' }],
  'auth403': [{ status: 403, body: '{}' }],
  'auth-1004': [ok({ base_resp: { status_code: 1004, status_msg: 'login fail' } })],
  'auth-1004-camel': [{ status: 200, body: { baseResp: { statusCode: '1004' } } }],
  'auth-1004-on-500': [{ status: 500, body: { base_resp: { status_code: 1004 } } }],
  // status_msg and body handling (U/minimax.js:155-187), #223 follow-up.
  'status-code-empty-msg': [sp(2013, '')],
  'status-code-no-msg': [sp(2013)],
  'status-code-padded-msg': [sp(2013, '  invalid params  ')],
  'status-code-js-trim': [sp(2013, '\u3000\ufeffbad\u00a0\u2028')],
  'status-code-camel-msg': [{ status: 200, body: { baseResp: { statusCode: 2013, statusMsg: 'camel msg' } } }],
  'status-code-null-snake-msg': [{ status: 200, body: { base_resp: { status_code: 2013, status_msg: null, statusMsg: 'camel fallback' } } }],
  'status-code-numeric-msg': [{ status: 200, body: { base_resp: { status_code: '2013', status_msg: 42 } } }],
  'status-code-object-msg': [{ status: 200, body: { base_resp: { status_code: 2013, status_msg: { a: 1 } } } }],
  'status-code-with-quota': [sp(2013, 'partial', mmQuota)],
  'status-code-true': [{ status: 200, body: { base_resp: { status_code: true }, model_remains: [mm.speech] } }],
  'status-code-nan': [{ status: 200, body: { base_resp: { status_code: 'abc', status_msg: 'weird' }, model_remains: [mm.speech] } }],
  'status-code-hex-1004': [{ status: 200, body: { base_resp: { status_code: '0x3EC' } } }],
  'status-code-on-500-fallback': [{ status: 500, body: { base_resp: { status_code: 2013, status_msg: 'busy' } } }, sp(0, 'success', mmQuota)],
  'status-code-on-429': [{ status: 429, body: { base_resp: { status_code: 1002, status_msg: 'rate limit' } } }],
  'status-ok-msg-no-data': [sp(0, 'insufficient balance', { model_remains: [] })],
  'status-ok-no-models-key': [sp(0, 'success')],
  'status-ok-msg-with-quota': [sp(0, 'quota low', mmQuota)],
  'status-ok-msg-auth-word': [sp(0, 'Token Plan not active', mmQuota)],
  'status-snake-empty-shadows-camel': [{ status: 200, body: { base_resp: {}, baseResp: { statusCode: 2013, statusMsg: 'hidden' }, model_remains: [mm.speech] } }],
  'status-snake-null-camel-error': [{ status: 200, body: { base_resp: null, baseResp: { statusCode: 2013, statusMsg: 'camel err' } } }],
  'status-base-resp-falsy': [{ status: 200, body: { base_resp: false, baseResp: { statusCode: 2013 }, model_remains: [mm.speech] } }],
  'body-empty': [{ status: 200, body: '' }],
  'body-null': [{ status: 200, body: 'null' }],
  'body-array': [{ status: 200, body: '[]' }],
  'body-html': [{ status: 200, body: '<html>maintenance</html>' }],
  'body-html-auth-word': [{ status: 200, body: '<html>Unauthorized</html>' }],
  'body-byte-order-mark': [{ status: 200, body: '\ufeff' + JSON.stringify({ base_resp: { status_code: 0 }, model_remains: [mm.speech] }) }],
  'auth-text': [ok({ base_resp: { status_code: 0, status_msg: 'Invalid API key' }, model_remains: [mm.speech] })],
  'auth-text-in-body': [ok({ model_remains: [{ ...mm.speech, model_name: 'coding plan pool' }] })],
  'auth-text-after-fallback': [{ status: 500, body: '' }, { status: 200, body: { base_resp: { status_code: 0 }, note: 'Token Plan inactive' } }],
  'auth-text-on-404': [{ status: 404, body: 'unauthorized' }],
};

// ---- CodeBuddy (U/codebuddy-cn.js:46-146) -----------------------------------

const envelope = accounts => ({ code: 0, msg: 'ok', data: { Response: { Data: { Accounts: accounts }, RequestId: 'req-fixture' } } });
const iso = ms => new Date(ms).toISOString();
const refill = (extra = {}) => ({ PackageName: 'Pro', SubProductName: 'CodeBuddy Pro', CycleStartTime: '2026-10-01 00:00:00',
  CycleEndTime: '2026-10-31 23:59:59', DeductionEndTime: Date.parse('2027-10-01T00:00:00Z'), CycleCapacitySizePrecise: '500',
  CycleCapacityUsedPrecise: '6.54', CycleCapacitySize: 500, CycleCapacityUsed: 7, CapacitySize: 6000, CapacityUsed: 1200, ...extra });
const bonus = (endMs, extra = {}) => ({ PackageName: 'Bonus', CycleStartTime: iso(NOW - 10 * DAY), CycleEndTime: iso(endMs),
  DeductionEndTime: endMs, CapacitySizePrecise: '100', CapacityUsedPrecise: '12', CycleCapacitySize: 1, CycleCapacityUsed: 1, ...extra });
const codebuddyCases = {
  'multiple-window': envelope([bonus(NOW + 20 * DAY), refill(), bonus(NOW + 5 * DAY, { PackageName: 'Event' }),
    refill({ PackageName: 'Weekly Pack', CycleStartTime: iso(NOW - 2 * DAY), CycleEndTime: iso(NOW + 5 * DAY), CycleCapacitySizePrecise: '50', CycleCapacityUsedPrecise: '5' })]),
  'cadences': envelope([refill({ CycleEndTime: '2026-11-15 00:00:00', PackageName: 'Second' }),
    refill({ CycleStartTime: iso(NOW - 12 * 3600000), CycleEndTime: iso(NOW + 12 * 3600000), PackageName: 'Daily' }),
    refill({ PackageName: 'First' }), refill({ CycleStartTime: null, PackageName: 'NoStart' })]),
  'zero': envelope([refill({ CycleCapacityUsedPrecise: '500' }), bonus(NOW + DAY, { CapacityUsedPrecise: '100' })]),
  'zero-capacity': envelope([bonus(NOW + DAY, { CapacitySizePrecise: '0', CapacityUsedPrecise: '0' })]),
  'expired': envelope([bonus(PAST_MS, { CapacityUsedPrecise: '100' })]),
  'empty': envelope([]),
  'no-data': { code: 0, data: {} },
  'code-missing': { data: { Response: { Data: { Accounts: [refill()] } } } },
  'code-nonzero': { code: 11, msg: 'login expired', data: { Response: { Data: { Accounts: [refill()] } } } },
  'code-string': { code: '0', data: { Response: { Data: { Accounts: [refill()] } } } },
  'precise-fields': envelope([bonus(NOW + DAY, { CapacitySizePrecise: null, CapacitySize: 80, CapacityUsedPrecise: 'abc', CapacityUsed: 9 }),
    bonus(NOW + 2 * DAY, { CapacitySizePrecise: ' 1e2 ', CapacityUsedPrecise: '', CapacityUsed: 3 })]),
  'time-shapes': envelope([refill({ CycleEndTime: FUTURE_S, DeductionEndTime: String(FUTURE_MS + 30 * DAY) }),
    bonus(0, { CycleEndTime: String(FUTURE_MS), DeductionEndTime: '2027-01-01' }), bonus(0, { CycleEndTime: null, DeductionEndTime: null }),
    bonus(0, { CycleEndTime: 'not a date', DeductionEndTime: FUTURE_MS + 30 * DAY })]),
  'refill-gap-boundary': envelope([refill({ CycleEndTime: iso(FUTURE_MS), DeductionEndTime: FUTURE_MS + 2 * DAY }),
    refill({ CycleEndTime: iso(FUTURE_MS + DAY), DeductionEndTime: FUTURE_MS + 3 * DAY + 1, PackageName: 'Refill' })]),
  'plan-fallbacks': envelope([bonus(NOW + DAY, { PackageName: '', SubProductName: 'Sub Bonus' })]),
  'plan-default': envelope([bonus(NOW + DAY, { PackageName: null, SubProductName: '' })]),
};

// ---- run ----------------------------------------------------------------

const record = { pin: '1c3b9f47837d42ea64992063309064af755a14a3', now: new Date(NOW).toISOString(), tz: 'UTC',
  glm: {}, kimi: {}, minimax: {}, codebuddy: {} };

async function run(steps, call) {
  clock = NOW;
  const net = network(steps);
  const outcome = await settle(call(net.proxyAwareFetch));
  assert.equal(net.calls.length, steps.length, `every scripted step is read: ${JSON.stringify(net.calls.map(c => c.url))}`);
  return { upstream: steps, ...outcome, requests: net.calls };
}

const single = body => [{ status: 200, body }];

for (const provider of ['glm', 'glm-cn']) {
  record.glm[provider] = {};
  for (const [name, steps] of Object.entries({ ...Object.fromEntries(Object.entries(glmCases).map(([k, v]) => [k, single(v)])), ...failures })) {
    record.glm[provider][name] = await run(steps, fetch => load('glm', fetch).getGlmUsage('fixture-token', provider));
  }
}

for (const [mode, credential] of Object.entries(kimiModes)) {
  record.kimi[mode] = {};
  const cases = mode === 'apikey' ? { ...Object.fromEntries(Object.entries(kimiCases).map(([k, v]) => [k, single(v)])), ...kimiFailures }
    : { 'multiple-window': single(kimiCases['multiple-window']) };
  for (const [name, steps] of Object.entries(cases)) {
    record.kimi[mode][name] = await run(steps, fetch => load('kimi', fetch).getKimiUsage(credential.accessToken, credential.apiKey, null, credential.data));
  }
}

for (const provider of ['minimax', 'minimax-cn']) {
  record.minimax[provider] = {};
  for (const [name, steps] of Object.entries(minimaxCases)) {
    record.minimax[provider][name] = await run(steps, fetch => load('minimax', fetch).getMiniMaxUsage('fixture-token', provider));
  }
}

for (const provider of ['codebuddy-cn', 'codebuddy-intl']) {
  record.codebuddy[provider] = {};
  for (const [name, steps] of Object.entries({ ...Object.fromEntries(Object.entries(codebuddyCases).map(([k, v]) => [k, single(v)])), ...failures })) {
    record.codebuddy[provider][name] = await run(steps, fetch => {
      const reader = load('codebuddy', fetch);
      return provider === 'codebuddy-cn' ? reader.getCodeBuddyCnUsage('fixture-token', null, {}) : reader.getCodeBuddyIntlUsage(null, 'fixture-token', {});
    });
  }
}

writeFileSync(out, `${JSON.stringify(record, null, 2)}\n`);
console.log(`wrote ${out.pathname}`);
