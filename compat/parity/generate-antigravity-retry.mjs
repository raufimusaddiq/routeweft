// Records what the pinned Antigravity executor does with a failed answer, so the
// Go tests can be offline and still differential. Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-antigravity-retry.mjs
//
// It evaluates the pin's own executors/base.js (execute, tryRetry),
// executors/antigravity.js (computeRetryDelay and what it calls, transformRequest),
// the retry tables of config/runtimeConfig.js and providers/registry/antigravity.js,
// and ANTIGRAVITY_PROMPT_REWRITES, read with `git show` at the pin. The network,
// the clock and the timers are scripted; nothing is sent and no credential is used.
// The Go tests read the file this writes; no test regenerates it.
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { writeFileSync } from 'node:fs';
import vm from 'node:vm';
import { source, pin } from './oracle.mjs';

const NOW = 1_700_000_000_000; // Tue, 14 Nov 2023 22:13:20 GMT
const out = new URL('../../internal/providers/wire/testdata/antigravity_pin.json', import.meta.url);

const imports = /^import\s[\s\S]*?;\s*$/gm;
const take = (text, re, what) => { const m = text.match(re); assert.ok(m, what); return m[0]; };
const bare = text => text.replace(imports, '').replace(/^export default .*;\s*$/gm, '').replace(/^export\s+/gm, '');

const runtime = source('open-sse/config/runtimeConfig.js');
const connectMs = Function(`return ${take(runtime, /FETCH_CONNECT_TIMEOUT_MS", ([^)]*)\)/, 'connect timeout')
  .replace(/^.*?", /, '').replace(/\)$/, '')}`)();
const configs = [
  take(runtime, /export const HTTP_STATUS = \{[\s\S]*?\n\};/, 'HTTP_STATUS'),
  take(runtime, /export const RETRY_CONFIG = \{[\s\S]*?\n\};/, 'RETRY_CONFIG'),
  take(runtime, /export const DEFAULT_RETRY_CONFIG = \{[\s\S]*?\n\};/, 'DEFAULT_RETRY_CONFIG'),
  take(runtime, /export function resolveRetryEntry[\s\S]*?\n\}/, 'resolveRetryEntry'),
].map(bare).join('\n');
const rewrites = bare(take(source('open-sse/config/appConstants.js'), /export const ANTIGRAVITY_PROMPT_REWRITES = \[[\s\S]*?\n\];/, 'rewrites'));
const registry = source('open-sse/providers/registry/antigravity.js').replace(imports, '').replace('export default {', 'const registryEntry = {');

const names = ['crypto', 'Date', 'setTimeout', 'clearTimeout', 'AbortController', 'AbortSignal', 'structuredClone', 'proxyAwareFetch', 'FETCH_CONNECT_TIMEOUT_MS'];
const script = `(({${names.join(',')}, ...stubs}) => {
const { shouldRefreshCredentials, dbg, ANTHROPIC_API_VERSION, OPENAI_COMPAT_BASE, ANTHROPIC_COMPAT_BASE, resolveOpenAICompatibleApiType,
  OAUTH_ENDPOINTS, ANTIGRAVITY_HEADERS, AG_DEFAULT_TOOLS, AG_TOOL_SUFFIX, resolveSessionId, toNumericSessionId,
  cleanJSONSchemaForAntigravity, normalizeGeminiContents, DEFAULT_THINKING_AG_SIGNATURE, getGeminiThoughtSignatureSync,
  ANTIGRAVITY_IDE_BASE_URL, ANTIGRAVITY_IDE_USER_AGENT, ANTIGRAVITY_OAUTH_CLIENT } = stubs;
${configs}
${rewrites}
${registry}
// open-sse/providers/schema.js:55-65 resolveProvider: the entry's retry over DEFAULT_RETRY_CONFIG.
const PROVIDERS = { antigravity: { ...registryEntry.transport, retry: { ...DEFAULT_RETRY_CONFIG, ...registryEntry.transport.retry } } };
${bare(source('open-sse/executors/base.js'))}
${bare(source('open-sse/executors/antigravity.js'))}
return { AntigravityExecutor, PROVIDERS, DEFAULT_RETRY_CONFIG, ANTIGRAVITY_PROMPT_REWRITES };
})`;

function build(steps) {
  const waits = [];
  const calls = [];
  class FakeDate extends Date {
    constructor(...args) { super(...(args.length ? args : [NOW])); }
    static now() { return NOW; }
  }
  const stubs = {
    crypto, Date: FakeDate, AbortController, AbortSignal, structuredClone, FETCH_CONNECT_TIMEOUT_MS: connectMs,
    setTimeout: (fn, ms) => { if (ms !== connectMs) { waits.push(ms); queueMicrotask(fn); } return 0; },
    clearTimeout() {},
    proxyAwareFetch: async (url, options) => {
      const step = steps[Math.min(calls.length, steps.length - 1)];
      calls.push({ url, body: options.body });
      if (step.network) throw new Error(step.network);
      return new Response(step.body ?? '', { status: step.status, headers: step.headers ?? {} });
    },
    shouldRefreshCredentials: () => false, dbg() {}, ANTHROPIC_API_VERSION: '', OPENAI_COMPAT_BASE: '', ANTHROPIC_COMPAT_BASE: '',
    resolveOpenAICompatibleApiType: () => 'chat', OAUTH_ENDPOINTS: {}, ANTIGRAVITY_HEADERS: { 'User-Agent': 'synthetic' },
    AG_DEFAULT_TOOLS: [], AG_TOOL_SUFFIX: '_ide', resolveSessionId: () => '1', toNumericSessionId: id => id,
    cleanJSONSchemaForAntigravity: schema => schema, normalizeGeminiContents: contents => contents,
    DEFAULT_THINKING_AG_SIGNATURE: 'synthetic', getGeminiThoughtSignatureSync: () => null,
    ANTIGRAVITY_IDE_BASE_URL: 'https://synthetic.invalid', ANTIGRAVITY_IDE_USER_AGENT: 'synthetic', ANTIGRAVITY_OAUTH_CLIENT: {},
  };
  const context = vm.createContext({ Response, Headers, URL, URLSearchParams, Set, Promise, JSON, Math, Error, queueMicrotask });
  const exports = new vm.Script(script, { filename: 'antigravity-pin' }).runInContext(context)(stubs);
  return { executor: new exports.AntigravityExecutor(), exports, waits, calls };
}

const request = { request: { contents: [{ role: 'user', parts: [{ text: 'hello' }] }] } };
const credentials = { accessToken: 'synthetic', projectId: 'synthetic-project' };
const httpDate = seconds => new Date(NOW + seconds * 1000).toUTCString();
const unix = seconds => String(Math.floor(NOW / 1000) + seconds);
const ok = { status: 200, body: '{}' };
const plain = status => ({ status, body: '' });
const err = (status, message) => ({ status, body: JSON.stringify({ error: { message } }) });

const scenarios = {
  'ok': [ok],
  'a 429 repeated until the attempts run out': [plain(429)],
  'a 429 then served': [plain(429), ok],
  'retry-after seconds': [{ status: 429, headers: { 'retry-after': '3' } }, ok],
  'retry-after at the cap': [{ status: 429, headers: { 'retry-after': '10' } }, ok],
  'retry-after past the cap': [{ status: 429, headers: { 'retry-after': '11' } }, ok],
  'retry-after of a 500 past the cap': [{ status: 500, headers: { 'retry-after': '11' } }, ok],
  'retry-after zero skips the other headers': [{ status: 429, headers: { 'retry-after': '0', 'x-ratelimit-reset-after': '4' } }, ok],
  'retry-after garbage falls to the next header': [{ status: 429, headers: { 'retry-after': 'abc', 'x-ratelimit-reset-after': '4' } }, ok],
  'retry-after negative': [{ status: 429, headers: { 'retry-after': '-3', 'x-ratelimit-reset-after': '4' } }, ok],
  'retry-after fraction': [{ status: 429, headers: { 'retry-after': '5.9' } }, ok],
  'retry-after exponent': [{ status: 429, headers: { 'retry-after': '1e3' } }, ok],
  'retry-after list': [{ status: 429, headers: { 'retry-after': '5, 7' } }, ok],
  'retry-after huge': [{ status: 429, headers: { 'retry-after': '99999999999999999999' } }, ok],
  'retry-after http date': [{ status: 429, headers: { 'retry-after': httpDate(5) } }, ok],
  'retry-after http date in the past': [{ status: 429, headers: { 'retry-after': httpDate(-5), 'x-ratelimit-reset-after': '4' } }, ok],
  'retry-after iso date': [{ status: 429, headers: { 'retry-after': new Date(NOW + 6000).toISOString() } }, ok],
  'retry-after http date past the cap': [{ status: 429, headers: { 'retry-after': httpDate(60) } }, ok],
  'reset-after seconds': [{ status: 429, headers: { 'x-ratelimit-reset-after': '6' } }, ok],
  'reset-after zero': [{ status: 429, headers: { 'x-ratelimit-reset-after': '0', 'x-ratelimit-reset': unix(3) } }, ok],
  'reset timestamp': [{ status: 429, headers: { 'x-ratelimit-reset': unix(3) } }, ok],
  'reset timestamp in the past': [{ status: 429, headers: { 'x-ratelimit-reset': unix(-3) } }, ok],
  'reset timestamp garbage': [{ status: 429, headers: { 'x-ratelimit-reset': 'soon' } }, ok],
  'message seconds': [err(429, 'Your quota will reset after 7s'), ok],
  'message minutes': [err(429, 'Your quota will reset after 1m'), ok],
  'message hours minutes seconds': [err(429, 'Your quota will reset after 2h7m23s'), ok],
  'message zero': [err(429, 'Your quota will reset after 0s'), ok],
  'message without a duration': [err(429, 'Your quota will reset after tomorrow, and again after 5s'), ok],
  'message upper case': [err(429, 'RESET AFTER 4S'), ok],
  'message at top level': [{ status: 429, body: JSON.stringify({ message: 'reset after 2s' }) }, ok],
  'message as the error string': [{ status: 429, body: JSON.stringify({ error: 'reset after 3s' }) }, ok],
  'message in a nested object': [{ status: 429, body: JSON.stringify({ error: { code: 429, details: [{ metadata: { note: 'reset after 4s' } }] } }) }, ok],
  'message in a plain body': [{ status: 429, body: 'quota will reset after 5s' }, ok],
  'message beats nothing, header beats message': [{ status: 429, headers: { 'retry-after': '3' }, body: JSON.stringify({ error: { message: 'reset after 9s' } }) }, ok],
  'message in a non-object json': [{ status: 429, body: '["reset after 3s"]' }, ok],
  'a 500 repeated': [plain(500)],
  'a 502 repeated': [plain(502)],
  'a 503 repeated': [plain(503)],
  'a 504 repeated': [plain(504)],
  '502 then 503 then 504 share one count': [plain(502), plain(503), plain(504), plain(504)],
  '503 then 504 then 504': [plain(503), plain(504), plain(504), plain(504)],
  '429 then 504 then 504': [plain(429), plain(504), plain(504), plain(504)],
  '400 is not retried': [plain(400), ok],
  '401 is not retried': [plain(401), ok],
  '404 is not retried': [plain(404), ok],
  '408 is not retried': [plain(408), ok],
  '501 is not retried': [plain(501), ok],
  '500 with a capacity message': [err(500, 'high traffic, capacity'), ok],
  'a network error repeated': [{ network: 'socket hang up' }],
  'a network error then served': [{ network: 'socket hang up' }, ok],
  '429 then a network error then served': [plain(429), { network: 'socket hang up' }, ok],
  'a network error then 504s': [{ network: 'socket hang up' }, plain(504), plain(504), plain(504)],
};

const results = [];
for (const [name, steps] of Object.entries(scenarios)) {
  const { executor, waits, calls } = build(steps);
  let final;
  try {
    const { response } = await executor.execute({ model: 'gemini-3.8-flash-high', body: structuredClone(request), stream: true, credentials, signal: null, log: null });
    final = { status: response.status };
  } catch (error) {
    final = { error: error.message };
  }
  results.push({ name, steps, calls: calls.length, waits, final, sent: calls.map(c => c.body) });
}

// computeRetryDelay on its own: the status, the attempt and the answer.
const delays = [];
for (const attempt of [1, 2, 3]) {
  for (const status of [429, 500, 502, 503, 504]) {
    for (const [name, headers, body] of [['bare', {}, ''], ['capacity', {}, 'at capacity'], ['reset', {}, 'reset after 5s']]) {
      const { executor } = build([ok]);
      const delay = await executor.computeRetryDelay(new Response(body, { status, headers }), attempt);
      delays.push({ name, status, attempt, headers, body, delay });
    }
  }
}

// The parsers, and the error text they read.
const parsed = [];
{
  const { executor } = build([ok]);
  const headerCases = [
    {}, { 'retry-after': '1' }, { 'retry-after': ' 7 ' }, { 'retry-after': '+7' }, { 'retry-after': '7 seconds' }, { 'retry-after': '0x10' },
    { 'retry-after': '1.5' }, { 'retry-after': '0' }, { 'retry-after': '00' }, { 'retry-after': '-0' }, { 'retry-after': '-1' }, { 'retry-after': '-12' },
    { 'retry-after': '-13' }, { 'retry-after': '' }, { 'retry-after': 'bad' }, { 'retry-after': httpDate(5) }, { 'retry-after': httpDate(0) },
    { 'retry-after': new Date(NOW + 2500).toISOString() }, { 'retry-after': '2023-11-14' },
    { 'retry-after': 'Tuesday, 14-Nov-23 22:13:25 GMT' },
    { 'retry-after': 'bad', 'x-ratelimit-reset-after': '8' }, { 'retry-after': '0', 'x-ratelimit-reset-after': '8' },
    { 'x-ratelimit-reset-after': '9' }, { 'x-ratelimit-reset-after': '0', 'x-ratelimit-reset': unix(2) }, { 'x-ratelimit-reset-after': 'x', 'x-ratelimit-reset': unix(2) },
    { 'x-ratelimit-reset': unix(2) }, { 'x-ratelimit-reset': unix(0) }, { 'x-ratelimit-reset': unix(-2) }, { 'x-ratelimit-reset': '1e12' },
  ];
  // A non-positive integer falls to `new Date(text)`, which reads it as a month or a year.
  for (const text of ['0', '+0', '-0', '00', '-1', '-5', '-12', '-0012', '-13', '-31', '-32', '-49', '-50', '-99', '-100', '-999', '-1000', '-2023', '-2024', '-2030', '-9999', '-10000', '-99999', '-999999999999999', '-050', '-0050', '-0032', '000013', '0.', '0.5', '0 s', '- 5']) {
    headerCases.push({ 'retry-after': text }, { 'retry-after': text, 'x-ratelimit-reset-after': '8' });
  }
  for (const headers of headerCases) parsed.push({ kind: 'headers', headers, value: executor.parseRetryHeaders(new Headers(headers)) });
  const messages = ['reset after 2h7m23s', 'reset after 1h30m', 'reset after 45m', 'reset after 30s', 'Reset After 30S', 'reset after 0s', 'reset after 00h00m00s',
    'reset after', 'reset after  5s', 'reset  after 5s', 'resets after 5s', 'reset after 5m1h', 'reset after 5h5h', 'reset after 99999999999h', 'x reset after 3m y reset after 4m',
    'reset after 1s2m', 'reset after 2m3', '', 'quota', 'reset after ٣s', 'reset ſfter 3s', 'reſet after 3s'];
  for (const message of messages) parsed.push({ kind: 'message', message, value: executor.parseRetryFromErrorMessage(message) });
  const bodies = [
    '', 'null', '[]', '"reset after 3s"', '3', '{}', '{"error":{"message":"a"},"message":"b","error2":1}', '{"error":{"message":"a"}}', '{"error":"e","message":"m"}',
    '{"message":"m"}', '{"error":{"message":""}}', '{"error":{"message":7}}', '{"error":{"message":{"x":1}}}', '{"error":{"b":1,"a":[1,2,{"c":null}]}}',
    '{"error":{"10":1,"b":2,"2":3,"a":4}}', '{"error":{"a":1,"a":2}}', '{"error":{"s":"\\u00e9\\n\\u0001\\u2028<>&"}}', '{"error":{"n":[1.0,1e21,1e-7,-0,0.1,12345678901234567890,1E2]}}',
    '{"error":0}', '{"error":false}', '{"error":null,"message":"m"}', '{"error":{"message":"a"},"message":"a"}', 'not json reset after 5s', '{"error":{"message":"reset after 5s"}} trailing',
    '{"error":[1,"two"]}', '{"error":{"__proto__":{"x":1}}}',
  ];
  for (const body of bodies) {
    let json = null;
    try { json = body ? JSON.parse(body) : null; } catch { /* the pin ignores it */ }
    parsed.push({ kind: 'text', body, value: executor.extractErrorMessage(json, body) });
  }
}

// transformRequest: the system prompt rewrites and requestType.
const prompts = [
  "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
  "You are a Claude agent, built on Anthropic's Claude Agent SDK. Be brief. You are a Claude agent, built on Anthropic's Claude Agent SDK.",
  "you are a claude agent, built on anthropic's claude agent sdk.",
  'You are Hermes.', 'You are Hermes Agent.', 'you are HERMES agent.', 'You are Hermes, an intelligent AI assistant.', 'You are Hermes, an AI assistant.',
  'You are Hermes, an AI agent.', 'You are Hermes, built by Nous Research.', 'You are Hermes Agent, created by Nous Research.',
  'You are Hermes Agent, an AI agent built by Nous Research.', 'You are Hermes Agent, an intelligent AI assistant, created  by  Nous Research.',
  'You are Hermes Agent, an AI agent, built by Nous Research. Help.', 'You are Hermes, an unknown thing.', 'You are Hermes', 'You are Hermes Agent, an AI agent built by Nous Research.',
  'You are Hermes, an AI agent.', 'You are Hermes,​an AI agent.', 'You are Hermes Agent, built by　Nous Research.', 'You are Hermes Agent.', 'You are Hermes Agent, built by\u000bNous Research.',
  'You are Hermes﻿.', 'You are Hermeſ.', 'You are Hermes Agent, built by Nous Reſearch.',
  'x-anthropic-billing-header: cc_version=1; cch=abc\nYou are helpful.', 'x-anthropic-billing-header:\n\n\nrest', 'X-ANTHROPIC-BILLING-HEADER: v\r\nrest', 'x-anthropic-billing-header: a\r\n\r\nrest',
  'prefix\nx-anthropic-billing-header: v\nrest', 'prefix x-anthropic-billing-header: v\nrest', 'prefix\rx-anthropic-billing-header: v\rrest', 'prefix x-anthropic-billing-header: v rest',
  'x-anthropic-billing-header: a\nx-anthropic-billing-header: b\nrest', 'x-anthropic-billing-header: a\n\nx-anthropic-billing-header: b', ' x-anthropic-billing-header: a', 'x-anthropic-billing-header : a', 'x-anthropic-billing-header: a rest',
  'x-anthropic-billing-header: a\n\r\n\r\nrest', 'x-anthropic-billing-header: a\rb\nc',
  'I use OpenCode. opencode and OPENCODE and openCode and Opencode and oPENCODE', 'opencodeopencode', 'my-opencode-tool', 'open code', 'OpenCode', 'OPENCODE', 'opencode', 'oPeNcOdE',
  'sdk Anthropic\'s Claude Agent SDK. then You are a Claude agent, built on Anthropic\'s Claude Agent SDK. then OpenCode',
  'You are Hermes. x-anthropic-billing-header: a\nopencode', "You are a Claude agent, built on Anthropic's Claude Agent SDK.You are Hermes.",
  '', 'plain text', 'dollar $& $1 $$ stays',
];
const rewrote = [];
for (const text of prompts) {
  const { executor } = build([ok]);
  const body = { model: 'm', request: { systemInstruction: { parts: [{ text }, { text: 'second ' + text }, { notText: 1 }, { text: 5 }] }, contents: [{ role: 'user', parts: [{ text }] }] } };
  const sent = executor.transformRequest('m', body, true, credentials);
  rewrote.push({ in: text, parts: sent.request.systemInstruction.parts, content: sent.request.contents[0].parts[0].text });
}

// requestType: dropped from the envelope root, kept elsewhere.
const requestTypes = [];
for (const [name, value] of [['agent', 'agent'], ['image_gen', 'image_gen'], ['search', 'search'], ['null', null], ['empty', '']]) {
  const { executor } = build([ok]);
  const sent = executor.transformRequest('gemini-3.8-flash-high', { model: 'm', requestType: value, requestId: 'r', request: { requestType: value, contents: [{ role: 'user', parts: [{ text: 'x' }] }] } }, true, credentials);
  requestTypes.push({ name, rootHas: 'requestType' in sent, nestedHas: 'requestType' in sent.request, nested: sent.request.requestType ?? null });
}
{
  const { executor } = build([ok]);
  const sent = executor.transformRequest('gemini-3.8-flash-high', { model: 'm', request: { contents: [{ role: 'user', parts: [{ text: 'x' }] }] } }, true, credentials);
  requestTypes.push({ name: 'absent', rootHas: 'requestType' in sent, nestedHas: 'requestType' in sent.request, nested: null });
}

const { exports } = build([ok]);
writeFileSync(out, JSON.stringify({
  pin, now: NOW,
  retry: Object.fromEntries(Object.entries(exports.PROVIDERS.antigravity.retry).map(([status, entry]) => [status, entry])),
  scenarios: results, delays, parsed, rewrites: rewrote, requestTypes,
}, null, 1) + '\n');
console.log(`wrote ${results.length} scenarios, ${delays.length} delays, ${parsed.length} parser cases, ${rewrote.length} rewrites`);
