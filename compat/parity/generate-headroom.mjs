// Records what the oracle's Headroom client (open-sse/rtk/headroom.js) sends to a
// /v1/compress helper and what it does with the answer, by running the real
// compressWithHeadroom (and the real request translators it calls) against a
// scripted fetch. internal/transforms/headroom replays the file (#271). Run:
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-headroom.mjs           # rewrite the pin
//   node compat/parity/generate-headroom.mjs --check   # fail if the oracle now differs
//
// No helper is contacted and no provider is called.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { loadTree, pin } from './oracle.mjs';

const oracle = await loadTree('open-sse/rtk/headroom.js', {
  // register() only fills a table; capabilities would pull the provider registry, which
  // needs a host. A model with no declared capability is what the cases here use.
  stubs: { '/open-sse/translator/index.js': { register() {} }, '/open-sse/providers/capabilities.js': { getCapabilitiesForModel: () => ({}) },
    // appConstants.js reads package.json through import.meta; only this string is used.
    '/open-sse/config/appConstants.js': { CLAUDE_SYSTEM_PROMPT: "You are Claude Code, Anthropic's official CLI for Claude." } },
  globals: { fetch: (...args) => globalThis.__fetch(...args), AbortSignal },
});

const long = 'long text '.repeat(40);
const ok = messages => ({ status: 200, json: { messages, tokens_before: 100, tokens_after: 40, tokens_saved: 60 } });
const chat = (extra = {}) => ({ model: 'gpt-4o', messages: [{ role: 'system', content: 'be brief' }, { role: 'user', content: long }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }], ...extra });
const cases = [
  { name: 'chat: messages go to /v1/compress and come back', format: 'openai', url: 'http://headroom:8787', model: 'gpt-4o', body: chat(), reply: ok([{ role: 'system', content: 'be brief' }, { role: 'user', content: 'short' }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }]), replay: true },
  { name: 'chat: trailing slash and a path prefix', format: 'openai', url: 'http://headroom:8787/api/', model: 'gpt-4o', body: chat(), reply: ok([{ role: 'system', content: 'be brief' }, { role: 'user', content: 'short' }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }]), replay: true },
  { name: 'chat: compressUserMessages sends config', format: 'openai', url: 'http://headroom:8787', model: 'gpt-4o', compressUserMessages: true, body: chat(), reply: ok([{ role: 'system', content: 'be brief' }, { role: 'user', content: 'short' }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }]), replay: true },
  { name: 'chat: reply without statistics', format: 'openai', url: 'http://headroom:8787', model: 'gpt-4o', body: chat(), reply: { status: 200, json: { messages: [{ role: 'system', content: 'be brief' }, { role: 'user', content: 'short' }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }] } }, replay: true },
  { name: 'chat: reply that is not saving bytes is flagged as phantom', format: 'openai', url: 'http://headroom:8787', model: 'gpt-4o', body: chat(), reply: ok([{ role: 'system', content: 'be brief' }, { role: 'user', content: long.slice(0, long.length - 2) }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }]), replay: true },
  { name: 'chat: HTTP error', format: 'openai', url: 'http://headroom:8787', model: 'gpt-4o', body: chat(), reply: { status: 503, json: {} }, replay: true },
  { name: 'chat: connection failure', format: 'openai', url: 'http://headroom:8787', model: 'gpt-4o', body: chat(), reply: { throws: 'connect ECONNREFUSED' }, replay: true },
  { name: 'chat: reply without messages[]', format: 'openai', url: 'http://headroom:8787', model: 'gpt-4o', body: chat(), reply: { status: 200, json: { ok: true } }, replay: true },
  { name: 'chat: disabled', format: 'openai', enabled: false, url: 'http://headroom:8787', model: 'gpt-4o', body: chat(), reply: ok([]), replay: true },
  { name: 'chat: no URL', format: 'openai', url: '', model: 'gpt-4o', body: chat(), reply: ok([]), replay: true },
  { name: 'gemini body is not a supported shape', format: 'gemini', url: 'http://headroom:8787', model: 'gemini-2.5-pro', body: { contents: [{ role: 'user', parts: [{ text: long }] }] }, reply: ok([]), replay: true },
  { name: 'responses: message items', format: 'openai-responses', url: 'http://headroom:8787', model: 'gpt-5', body: { model: 'gpt-5', input: [{ type: 'message', role: 'user', content: long }, { type: 'message', role: 'assistant', content: 'ok' }] }, reply: ok([{ role: 'user', content: 'short' }, { role: 'assistant', content: 'ok' }]), replay: true },
  { name: 'responses: a tool item is not safe to compress', format: 'openai-responses', url: 'http://headroom:8787', model: 'gpt-5', body: { model: 'gpt-5', input: [{ type: 'message', role: 'user', content: long }, { type: 'function_call', call_id: 'c', name: 'f', arguments: '{}' }] }, reply: ok([]), replay: true },
  { name: 'claude: system string and messages', format: 'claude', url: 'http://headroom:8787', model: 'claude-sonnet-4-5', body: { model: 'claude-sonnet-4-5', max_tokens: 100, system: 'be brief', messages: [{ role: 'user', content: long }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }] }, reply: ok([{ role: 'system', content: 'be brief' }, { role: 'user', content: 'short' }, { role: 'assistant', content: 'ok' }, { role: 'user', content: 'again' }]), replay: true },
  { name: 'claude: system blocks', format: 'claude', url: 'http://headroom:8787', model: 'claude-sonnet-4-5', body: { model: 'claude-sonnet-4-5', max_tokens: 100, system: [{ type: 'text', text: 'rule one' }, { type: 'text', text: 'rule two' }], messages: [{ role: 'user', content: long }] }, reply: ok([{ role: 'system', content: 'rule one\n\nrule two' }, { role: 'user', content: 'short' }]), replay: false },
  { name: 'claude: tool use and tool result are translated to tool_calls and a tool message', format: 'claude', url: 'http://headroom:8787', model: 'claude-sonnet-4-5', body: { model: 'claude-sonnet-4-5', max_tokens: 100, messages: [{ role: 'user', content: 'run it' }, { role: 'assistant', content: [{ type: 'tool_use', id: 't1', name: 'sh', input: { cmd: 'ls' } }] }, { role: 'user', content: [{ type: 'tool_result', tool_use_id: 't1', content: long }] }] }, reply: ok([{ role: 'user', content: 'run it' }, { role: 'assistant', content: '', tool_calls: [{ id: 't1', type: 'function', function: { name: 'sh', arguments: '{"cmd":"ls"}' } }] }, { role: 'tool', tool_call_id: 't1', content: 'short' }]), replay: false },
];

const run = async test => {
  const calls = [];
  globalThis.__fetch = async (url, init) => {
    calls.push({ url, method: init.method, headers: init.headers, payload: JSON.parse(init.body) });
    if (test.reply.throws) throw Object.assign(new Error('fetch failed'), { cause: { code: 'ECONNREFUSED', message: test.reply.throws } });
    return new Response(JSON.stringify(test.reply.json), { status: test.reply.status });
  };
  const body = structuredClone(test.body);
  const diagnostics = {};
  const stats = await oracle.compressWithHeadroom(body, { enabled: test.enabled ?? true, url: test.url, model: test.model, format: test.format, compressUserMessages: test.compressUserMessages, diagnostics });
  return {
    name: test.name, format: test.format, url: test.url, model: test.model, compressUserMessages: test.compressUserMessages === true, replay: test.replay,
    body: test.body, reply: test.reply, calls, stats, after: body, diagnostics,
    log: oracle.formatHeadroomLog(stats), sizeLog: oracle.formatHeadroomSizeLog(diagnostics), phantom: oracle.isHeadroomPhantomSavings(stats, diagnostics),
  };
};

const results = [];
for (const test of cases) results.push(await run(test));
const pinned = JSON.stringify({ oracle: 'raufimusaddiq/literouter', sha: pin, cases: results }, null, 1) + '\n';
const file = new URL('../../internal/transforms/headroom/testdata/compress_pin.json', import.meta.url);
if (process.argv.includes('--check')) {
  const committed = JSON.parse(readFileSync(file, 'utf8'));
  assert.deepEqual({ ...JSON.parse(pinned), sha: null }, { ...committed, sha: null }, 'oracle Headroom behavior differs from the pin; regenerate');
  console.log(`${pin}: Headroom /v1/compress behavior matches the pin (${results.length} cases)`);
} else {
  writeFileSync(file, pinned);
  console.log(`${pin}: ${results.length} Headroom cases`);
}
