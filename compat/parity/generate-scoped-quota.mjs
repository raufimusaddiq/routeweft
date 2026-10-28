// Records what the pinned Codex, Claude and GitHub Copilot quota readers return, so
// the Go tests can be offline and still differential (#222). Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-scoped-quota.mjs
//
// It evaluates the pin's own open-sse/services/usage/{shared,codex,claude,github}.js,
// read with `git show` at the pin, with the network and the clock scripted. The
// endpoint URLs and the GitHub identity headers come from the pinned registry
// entries. Nothing is sent and no credential is used. The Go tests read
// internal/providers/usage/testdata/scoped_quota_pin.json; no test regenerates it.
import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
import vm from 'node:vm';
import { source } from './oracle.mjs';

const NOW = Date.parse('2026-10-20T12:00:00.000Z');
const out = new URL('../../internal/providers/usage/testdata/scoped_quota_pin.json', import.meta.url);

let clock = NOW;
class FakeDate extends Date {
  constructor(...args) { super(...(args.length ? args : [clock])); }
  static now() { return clock; }
}

// Evaluate one pinned module. Imports are replaced by `deps`; every top-level
// declaration in `names` is returned. `export` keywords are dropped.
function evaluate(path, deps, names) {
  const code = source(path).replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export\s+/gm, '');
  assert.doesNotMatch(code, /^\s*(?:import|export)\b/m, `${path}: unsupported module syntax`);
  const context = vm.createContext({ URL, Map, Set, Promise, JSON, Math, Date: FakeDate, Error, Number, String, Array, Object,
    RegExp, console: { log() {}, warn() {}, error() {} } });
  context.deps = deps;
  return new vm.Script(`(({${Object.keys(deps).join(',')}}) => {${code}\nreturn {${names.join(',')}};})(deps)`, { filename: path })
    .runInContext(context, { timeout: 10000 });
}

// The `usage: {...}` literal of a registry entry; it holds only strings.
function usageBlock(id) {
  const text = source(`open-sse/providers/registry/${id}.js`);
  const match = text.match(/\n {4}usage: (\{\n[\s\S]*?\n {4}\}),\n/);
  assert.ok(match, `${id}: usage block`);
  return Function(`return ${match[1]};`)();
}
// A string field of the registry entry's `oauth:` block.
function oauthField(id, field) {
  const text = source(`open-sse/providers/registry/${id}.js`);
  const oauth = text.slice(text.indexOf('\n  oauth: {'));
  assert.ok(oauth.length > 1, `${id}: oauth block`);
  const match = oauth.match(new RegExp(`\\n {4}${field}: "([^"]*)"`));
  assert.ok(match, `${id}: oauth.${field}`);
  return match[1];
}

const PROVIDERS = { codex: { usage: usageBlock('codex') }, claude: { usage: usageBlock('claude') }, github: { usage: usageBlock('github') } };
const PROVIDER_OAUTH = { github: { apiVersion: oauthField('github', 'apiVersion'), userAgent: oauthField('github', 'userAgent') } };
const ANTHROPIC_API_VERSION = source('open-sse/providers/shared.js').match(/export const ANTHROPIC_API_VERSION = "([^"]*)"/)[1];

// One scripted network: each request takes the next step. A step is
// {status, body} or {network: message}. Authorization values are recorded by
// scheme only, so the file carries no token.
function network(steps) {
  const calls = [];
  const proxyAwareFetch = async (url, options = {}) => {
    const step = steps[calls.length];
    assert.ok(step, `unscripted request to ${url}`);
    const headers = Object.fromEntries(Object.entries(options.headers ?? {}).map(([k, v]) =>
      [k, k.toLowerCase() === 'authorization' ? String(v).split(' ')[0] + ' <token>' : v]));
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
  if (name === 'codex') return evaluate('open-sse/services/usage/codex.js', deps, ['getCodexUsage']);
  if (name === 'claude') return evaluate('open-sse/services/usage/claude.js', { ...deps, ANTHROPIC_API_VERSION }, ['getClaudeUsage']);
  return evaluate('open-sse/services/usage/github.js', { ...deps, PROVIDER_OAUTH }, ['getGitHubUsage']);
}

const settle = async promise => {
  try { return { result: JSON.parse(JSON.stringify(await promise)) }; } catch (error) { return { error: error.message }; }
};

const FUTURE = '2026-10-21T00:00:00Z';
const PAST = '2026-10-19T00:00:00Z';
const FUTURE_S = Date.parse(FUTURE) / 1000;

// ---- Codex (U/codex.js:107-143) --------------------------------------------

const win = (used, reset = FUTURE) => ({ used_percent: used, reset_at: reset });
const codexCases = {
  'multiple-window': { plan_type: 'plus', rate_limit: { limit_reached: false, primary_window: win(42), secondary_window: win(10, FUTURE_S) },
    code_review_rate_limit: { primary_window: win(5) }, spark_rate_limit: { limit_reached: true, primary_window: win(100), secondary_window: win(60) },
    rate_limit_reset_credits: { available_count: 2 } },
  'zero': { plan_type: 'pro', rate_limit: { limit_reached: true, primary_window: win(100), secondary_window: win(100) } },
  'expired': { plan_type: 'pro', rate_limit: { limit_reached: true, primary_window: win(100, PAST), secondary_window: win(30, PAST) } },
  'empty': {},
  'by-limit-id': { plan_type: 'team', rate_limits_by_limit_id: { codex: { primary_window: win(20), secondary_window: win(40) },
    code_review: { rate_limit: { limit_reached: true, primary_window: win(70) } }, 'gpt-5.3-codex-spark': { primary_window: win(1) } } },
  'by-limit-id-aliases': { rate_limits_by_limit_id: { codex: { primary: win(11) }, codex_review: { secondary: win(12) }, gpt_5_3_codex_spark: { primary_window: win(13) } } },
  'by-limit-id-short-aliases': { rate_limits_by_limit_id: { review: { primary_window: win(21) }, spark: { primary_window: win(22) } } },
  'by-limit-id-shadows-additional': { rate_limit: { primary_window: win(1) }, rate_limits_by_limit_id: { other: {} },
    additional_rate_limits: [{ limit_name: 'code_review', primary_window: win(50) }, { limit_name: 'spark', primary_window: win(60) }] },
  'rate-limit-precedence': { rate_limit: { primary_window: win(1) }, rate_limits: { primary_window: win(2) }, rate_limits_by_limit_id: { codex: { primary_window: win(3) } } },
  'rate-limit-falsy': { rate_limit: false, rate_limits: { primary_window: win(2) } },
  'rate-limit-nested': { rate_limit: { rate_limit: { limit_reached: 1, primary_window: win(9) }, secondary_window: win(8) } },
  'review-spark-precedence': { code_review_rate_limit: { primary_window: win(31) }, review_rate_limit: { primary_window: win(32) },
    spark_rate_limit: null, gpt_5_3_codex_spark_rate_limit: { primary_window: win(33) } },
  'additional-first-field': { additional_rate_limits: [{ limit_name: 'weekly', id: 'review', primary_window: win(41) },
    { metered_feature: 'Codex_Review', rate_limit: { primary_window: win(42) } }, { id: 'gpt-5.3-codex-spark', secondary: win(43) },
    { limit_name: 'spark', limit: { primary_window: win(44) } }] },
  'additional-limit-key': { additional_rate_limits: [{ limit_name: 'spark', limit: { primary_window: win(44) } }, { limit_name: 'review', limit: { secondary_window: win(45) } }] },
  'window-fields': { rate_limit: {
    primary_window: { used_percent: 0, percent_used: 50, reset_at: null, resets_at: FUTURE },
    secondary_window: { used_percent: null, percent_used: '25', reset_at: 0, resetAt: FUTURE } },
    code_review_rate_limit: { primary_window: { used_percent: '42', resetAt: String(FUTURE_S) }, secondary_window: { used_percent: -5, reset_at: FUTURE_S * 1000 + 250 } },
    spark_rate_limit: { primary_window: { used_percent: 'abc', reset_at: 'not a date' }, secondary_window: 7 } },
  'primary-window-over-primary': { rate_limit: { primary_window: win(61), primary: win(62), secondary_window: null, secondary: win(63) } },
  'reset-credits-string': { plan_type: '', summary: { plan: 'enterprise' }, rate_limit_reset_credits: { available_count: '3' } },
  'reset-credits-negative': { rate_limit_reset_credits: { available_count: -2 } },
};

// ---- Claude (U/claude.js:27-199) -------------------------------------------

const util = (utilization, resets_at = FUTURE) => ({ utilization, resets_at });
const claudeOAuth = {
  'multiple-window': { five_hour: util(12.5), seven_day: util(40), seven_day_opus: util(70), seven_day_sonnet: util(5, null),
    limits: [{ kind: 'weekly_scoped', percent: 35, resets_at: FUTURE, scope: { model: { display_name: 'Fable' } } }],
    extra_usage: { is_enabled: true, monthly_limit: 5000, used_credits: 1250.5, utilization: 25.01 } },
  'zero': { five_hour: util(100), seven_day: util(100), extra_usage: { is_enabled: false, monthly_limit: null, used_credits: null, utilization: null } },
  'expired': { five_hour: util(100, PAST), seven_day: util(20, PAST) },
  'empty': {},
  'limits-entries': { limits: [
    { kind: 'weekly_scoped', percent: 150, resets_at: FUTURE_S, scope: { model: { display_name: '  Opus Max  ' } } },
    { kind: 'weekly_scoped', percent: -5, scope: { model: { display_name: 'Haiku' } } },
    { kind: 'weekly_scoped', resets_at: FUTURE, scope: { model: { display_name: 'NoPercent' } } },
    { kind: 'weekly_scoped', percent: '40', scope: { model: { display_name: 'StringPercent' } } },
    { kind: 'weekly_scoped', percent: 10, scope: { model: { display_name: '' } } },
    { kind: 'daily', percent: 10, scope: { model: { display_name: 'Daily' } } },
    { kind: 'weekly_scoped', percent: 20, scope: { model: { display_name: 7 } } },
    null] },
  'limits-override-keyed': { seven_day_fable: util(10), limits: [{ kind: 'weekly_scoped', percent: 90, scope: { model: { display_name: 'FABLE' } } }] },
  'utilization-shapes': { five_hour: { utilization: '50' }, seven_day: { utilization: null }, seven_day_opus: util(150, String(FUTURE_S)),
    seven_day_oauth_apps: util(-10, '2026-10-21T00:00:00.123Z'), seven_day_cowork: 5, extra_usage: null },
};
const claudeCases = {};
for (const [name, body] of Object.entries(claudeOAuth)) claudeCases[name] = { steps: [{ status: 200, body }] };
Object.assign(claudeCases, {
  'oauth-429-legacy-org': { steps: [{ status: 429, body: {} }, { status: 200, body: { organization_id: 'org-1', organization_name: 'Acme', plan: 'team' } },
    { status: 200, body: { data: [{ usage: 1 }] } }] },
  'oauth-401-settings-403': { steps: [{ status: 401, body: {} }, { status: 403, body: {} }] },
  'oauth-500-settings-no-org': { steps: [{ status: 500, body: {} }, { status: 200, body: { organization_name: 'Acme' } }] },
  'oauth-500-org-usage-403': { steps: [{ status: 500, body: {} }, { status: 200, body: { organization_id: 'org-1', plan: 'pro' } }, { status: 403, body: {} }] },
  'timeout': { steps: [{ network: 'The operation was aborted due to timeout' }] },
  'invalid-json': { steps: [{ status: 200, body: 'not json' }] },
});

// A stale read: a good read, then a soft failure once the five-minute cache
// expired, then a later read (claude.js:30-55).
const claudeSequences = {
  'stale-after-soft-failure': {
    calls: [
      { advanceMs: 0, steps: [{ status: 200, body: claudeOAuth['multiple-window'] }] },
      { advanceMs: 301000, steps: [{ status: 500, body: {} }, { status: 403, body: {} }] },
      { advanceMs: 301000, steps: [] },
    ],
  },
  'cooldown-skips-oauth': {
    calls: [
      { advanceMs: 0, force: true, steps: [{ status: 429, body: {} }, { status: 403, body: {} }] },
      { advanceMs: 60000, force: true, steps: [{ status: 403, body: {} }] },
      { advanceMs: 180000, force: true, steps: [{ status: 200, body: claudeOAuth.zero }] },
    ],
  },
};

// ---- GitHub (U/github.js:19-100) -------------------------------------------

const snap = (entitlement, remaining, unlimited) => ({ entitlement, remaining, ...(unlimited === undefined ? {} : { unlimited }) });
const githubCases = {
  'multiple-window': { copilot_plan: 'individual', quota_reset_date: '2026-11-01', access_type_sku: 'plus_monthly_subscriber',
    quota_snapshots: { chat: snap(0, 0, true), completions: snap(0, 0, true), premium_interactions: snap(300, 120.5, false) } },
  'zero': { copilot_plan: 'business', quota_reset_date: FUTURE, quota_snapshots: { chat: snap(300, 0), completions: snap(300, 0), premium_interactions: snap(300, 0) } },
  'expired': { copilot_plan: 'business', quota_reset_date: PAST, quota_snapshots: { chat: snap(300, 0), completions: snap(300, 0), premium_interactions: snap(300, 0) } },
  'empty-snapshots': { copilot_plan: 'individual', quota_snapshots: {} },
  'missing-snapshot': { copilot_plan: 'individual', quota_reset_date: FUTURE, quota_snapshots: { premium_interactions: snap(50, 10) } },
  'zero-entitlement': { copilot_plan: 'individual', quota_reset_date: FUTURE, quota_snapshots: { chat: snap(0, 0, false), completions: snap(0, 0), premium_interactions: snap(300, 300) } },
  'string-amounts': { copilot_plan: 'individual', quota_reset_date: FUTURE, quota_snapshots: { chat: snap('300', '100'), completions: snap(100, 25, 0), premium_interactions: snap(10, 4, 'yes') } },
  'free': { access_type_sku: 'free_limited_copilot', limited_user_reset_date: '2026-11-05', monthly_quotas: { chat: 50, completions: 2000 },
    limited_user_quotas: { chat: 10, completions: 0 } },
  'free-plan-precedence': { copilot_plan: 'free', access_type_sku: 'free_limited_copilot', limited_user_reset_date: String(FUTURE_S),
    monthly_quotas: { chat: 50 }, limited_user_quotas: { chat: 50, completions: 7 } },
  'free-empty': { limited_user_quotas: {} },
  'snapshots-over-free': { copilot_plan: 'individual', quota_snapshots: { chat: snap(10, 5) }, monthly_quotas: { chat: 50 } },
  'unrecognized': { copilot_plan: 'individual' },
};

// ---- run ----------------------------------------------------------------

const record = { pin: '1c3b9f47837d42ea64992063309064af755a14a3', now: new Date(NOW).toISOString(), codex: {}, claude: {}, github: {} };

const failures = { auth401: { status: 401, body: '{"message":"Bad credentials"}' }, auth403: { status: 403, body: '{"error":"forbidden"}' },
  error500: { status: 500, body: 'upstream exploded' }, timeout: { network: 'The operation was aborted due to timeout' }, 'invalid-json': { status: 200, body: 'not json' } };

for (const [name, body] of Object.entries({ ...codexCases, ...failures })) {
  clock = NOW;
  const step = failures[name] ?? { status: 200, body };
  const net = network([step]);
  const { getCodexUsage } = load('codex', net.proxyAwareFetch);
  record.codex[name] = { upstream: step, ...await settle(getCodexUsage('fixture-token')), requests: net.calls };
}

for (const [name, body] of Object.entries({ ...githubCases, ...failures })) {
  clock = NOW;
  const step = failures[name] ?? { status: 200, body };
  const net = network([step]);
  const { getGitHubUsage } = load('github', net.proxyAwareFetch);
  record.github[name] = { upstream: step, ...await settle(getGitHubUsage('fixture-token', {})), requests: net.calls };
}

const claudeFailures = { auth401: { steps: [{ status: 401, body: {} }, { status: 401, body: {} }] } };
for (const [name, test] of Object.entries({ ...claudeCases, ...claudeFailures })) {
  clock = NOW;
  const net = network(test.steps);
  const { getClaudeUsage } = load('claude', net.proxyAwareFetch);
  record.claude[name] = { upstream: test.steps, ...await settle(getClaudeUsage('fixture-token')), requests: net.calls };
  assert.equal(net.calls.length, test.steps.length, `claude ${name}: every scripted step is read`);
}

record.claudeSequences = {};
for (const [name, sequence] of Object.entries(claudeSequences)) {
  clock = NOW;
  const steps = sequence.calls.flatMap(call => call.steps);
  const net = network(steps);
  const { getClaudeUsage } = load('claude', net.proxyAwareFetch);
  const calls = [];
  for (const call of sequence.calls) {
    clock += call.advanceMs;
    const before = net.calls.length;
    const outcome = await settle(getClaudeUsage('fixture-token', null, { force: call.force === true }));
    calls.push({ advanceMs: call.advanceMs, force: call.force === true, upstream: call.steps, ...outcome, requests: net.calls.slice(before) });
  }
  assert.equal(net.calls.length, steps.length, `claude sequence ${name}: every scripted step is read`);
  record.claudeSequences[name] = { calls };
}

writeFileSync(out, `${JSON.stringify(record, null, 2)}\n`);
console.log(`wrote ${out.pathname}`);
