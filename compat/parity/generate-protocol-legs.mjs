// Records what the oracle's own conversion legs (open-sse/translator/request/*.js,
// response/*.js) produce for a corpus of synthetic requests and upstream streams,
// so Routeweft's legs can be compared with them case by case (#274). The legs are
// the functions each module passes to register(); they run unmodified, loaded from
// the pinned commit. The corpus is compat/parity/legs-corpus.mjs. Run:
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-protocol-legs.mjs           # rewrite the pin
//   node compat/parity/generate-protocol-legs.mjs --check   # fail if the oracle now differs
//
// No provider is called. internal/parity/legs_test.go replays the pin and holds the
// divergence ledger (compat/parity/divergences.json) every difference must be listed in.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { loadTree, pin, git } from './oracle.mjs';
import { capabilityOracle } from './capabilities.mjs';
import * as corpus from './legs-corpus.mjs';
import { foldChat, foldGemini, foldMessages, foldResponses } from './legs-fold.mjs';

export const requestLegs = new Map();
export const responseLegs = new Map();
const register = (from, to, request, response) => {
  if (request) requestLegs.set(`${from}>${to}`, request);
  if (response) responseLegs.set(`${from}>${to}`, response);
};
const stubs = {
  '/open-sse/translator/index.js': { register },
  // Only the legs are run here, not the pipeline around them, so the provider
  // registry (a cycle through the executors) and the capability table are not needed.
  '/open-sse/providers/index.js': { PROVIDERS: {} },
  '/open-sse/providers/thinkingLevels.js': { getThinkingLevels: () => [] },
  // The real capability table (as generate-capabilities.mjs loads it): the legs read it.
  '/open-sse/providers/capabilities.js': (await capabilityOracle()).oracle,
  // appConstants.js reads package.json through import.meta; only this string is used.
  '/open-sse/config/appConstants.js': { CLAUDE_SYSTEM_PROMPT: "You are Claude Code, Anthropic's official CLI for Claude." },
};
// Everything a leg draws from the host is fixed, so the pin is repeatable: the clock,
// Math.random and every UUID. The counters restart for each case.
let uuids = 0; let seed = 1;
const uuid = () => `00000000-0000-4000-8000-${String(++uuids).padStart(12, '0')}`;
const FixedDate = class extends Date { constructor(...args) { super(...(args.length ? args : [1700000000000])); } static now() { return 1700000000000; } };
const FixedMath = Object.assign(Object.create(Math), { random: () => { seed = (seed * 16807) % 2147483647; return seed / 2147483647; } });
stubs.uuid = { v4: uuid };
stubs.crypto = stubs['node:crypto'] = { randomUUID: uuid, default: { randomUUID: uuid } };
const globals = { Buffer, Uint8Array, structuredClone, Date: FixedDate, Math: FixedMath, crypto: { randomUUID: uuid, getRandomValues: array => { for (let i = 0; i < array.length; i++) array[i] = Math.floor(FixedMath.random() * 256); return array; } },
  global: {}, process: { env: {}, cwd: () => '/work', platform: 'linux', arch: 'x64' }, setInterval: () => ({ unref() {} }), clearInterval() {}, setTimeout: () => ({ unref() {} }), clearTimeout() {} };
const modules = git('ls-tree', '-r', '--name-only', pin, 'open-sse/translator/request', 'open-sse/translator/response').trim().split('\n').filter(Boolean);
for (const file of modules) await loadTree(file, { stubs, globals });

// _toolNameMap is a side channel from the leg to the caller (it is removed before the
// body is sent), not part of the converted body.
const plain = value => JSON.parse(JSON.stringify(value, (key, item) => (key === '_toolNameMap' ? undefined : item instanceof Map ? Object.fromEntries(item) : item instanceof Set ? [...item] : item)));
const envelope = body => ({ project: 'proj-1', model: 'x', userAgent: 'agent', requestId: 'req-1', request: body });
const kiroCredentials = { providerSpecificData: { authMethod: 'builder-id', profileArn: 'arn:aws:codewhisperer:us-east-1:000000000000:profile/SYNTHETIC' } };
const cloudCredentials = { projectId: 'proj-1', _clientSessionId: '1234567890' };

// Each request leg: the corpus it is fed, how the case becomes the leg's input, and
// the credentials it needs. The model is the same everywhere.
const legSpecs = [
  { leg: 'openai>claude', corpus: 'chat' }, { leg: 'claude>openai', corpus: 'messages' },
  { leg: 'openai>openai-responses', corpus: 'chat' }, { leg: 'openai-responses>openai', corpus: 'responses' },
  { leg: 'openai>gemini', corpus: 'chat', credentials: cloudCredentials }, { leg: 'openai>vertex', corpus: 'chat', credentials: cloudCredentials },
  { leg: 'openai>gemini-cli', corpus: 'chat', credentials: cloudCredentials }, { leg: 'openai>antigravity', corpus: 'chat', credentials: cloudCredentials },
  { leg: 'gemini>openai', corpus: 'gemini' }, { leg: 'gemini-cli>openai', corpus: 'gemini', wrap: envelope }, { leg: 'antigravity>openai', corpus: 'gemini', wrap: envelope },
  { leg: 'openai>ollama', corpus: 'chat' }, { leg: 'openai>cursor', corpus: 'chat' }, { leg: 'openai>commandcode', corpus: 'chat' },
  { leg: 'openai>kiro', corpus: 'chat', credentials: kiroCredentials }, { leg: 'claude>kiro', corpus: 'messages', credentials: kiroCredentials },
];
const requests = [];
for (const spec of legSpecs) {
  const fn = requestLegs.get(spec.leg);
  assert.ok(fn, `the oracle registers no request leg ${spec.leg}`);
  for (const item of corpus[spec.corpus]) {
    const input = spec.wrap ? spec.wrap(structuredClone(item.body)) : structuredClone(item.body);
    const stream = item.stream === true;
    let output; let throws = null;
    uuids = 0; seed = 1;
    try { output = plain(fn('x', structuredClone(input), stream, spec.credentials ? structuredClone(spec.credentials) : null)); } catch (error) { throws = String(error?.message ?? error).slice(0, 200); }
    requests.push({ leg: spec.leg, case: item.name, stream, input, credentials: spec.credentials ?? null, output: output ?? null, throws });
  }
}

// ---- response legs ------------------------------------------------------------
// The oracle converts one upstream chunk at a time, with a state object from the
// dispatcher's initState (translator/index.js:267-312), taken from the oracle as it is.
const { FORMATS } = await loadTree('open-sse/translator/formats.js');
const indexSource = (await import('./oracle.mjs')).source('open-sse/translator/index.js');
const initStateSource = indexSource.slice(indexSource.indexOf('export function initState'), indexSource.indexOf('// Kept for backward compatibility'));
const initState = new Function('FORMATS', 'Date', `${initStateSource.replace('export function', 'function')}; return initState;`)(FORMATS, FixedDate);

const asEvents = items => items;
const responseSpecs = [
  { leg: 'claude>openai', client: 'openai', corpus: 'claudeStreams', field: 'events', fold: foldChat },
  { leg: 'gemini>openai', client: 'openai', corpus: 'geminiStreams', field: 'chunks', fold: foldChat },
  { leg: 'vertex>openai', client: 'openai', corpus: 'geminiStreams', field: 'chunks', fold: foldChat },
  { leg: 'gemini-cli>openai', client: 'openai', corpus: 'geminiStreams', field: 'chunks', wrap: chunk => ({ response: chunk }), fold: foldChat },
  { leg: 'antigravity>openai', client: 'openai', corpus: 'geminiStreams', field: 'chunks', wrap: chunk => ({ response: chunk }), fold: foldChat },
  { leg: 'ollama>openai', client: 'openai', corpus: 'ollamaStreams', field: 'lines', fold: foldChat },
  { leg: 'openai-responses>openai', client: 'openai', corpus: 'responsesStreams', field: 'events', fold: foldChat },
  { leg: 'openai>claude', client: 'claude', corpus: 'chatStreams', field: 'chunks', fold: foldMessages },
  { leg: 'openai>openai-responses', client: 'openai-responses', corpus: 'chatStreams', field: 'chunks', fold: foldResponses },
  { leg: 'openai>antigravity', client: 'antigravity', corpus: 'chatStreams', field: 'chunks', fold: foldGemini },
];
const responses = [];
for (const spec of responseSpecs) {
  const fn = responseLegs.get(spec.leg);
  assert.ok(fn, `the oracle registers no response leg ${spec.leg}`);
  for (const item of corpus[spec.corpus]) {
    uuids = 0; seed = 1;
    const state = initState(spec.client);
    const input = item[spec.field].map(chunk => (spec.wrap ? spec.wrap(structuredClone(chunk)) : structuredClone(chunk)));
    const output = []; let throws = null;
    try {
      for (const chunk of structuredClone(input)) {
        const converted = fn(chunk, state);
        if (converted) output.push(...plain(Array.isArray(converted) ? converted : [converted]));
      }
    } catch (error) { throws = String(error?.message ?? error).slice(0, 200); }
    let fold = null;
    if (!throws) { try { fold = spec.fold(output); } catch (error) { throws = `fold: ${error.message}`; } }
    responses.push({ leg: spec.leg, case: item.name, input, output, fold, throws });
  }
}

const pinned = JSON.stringify({ oracle: 'raufimusaddiq/literouter', sha: pin, requests, responses }, null, 1) + '\n';
const file = new URL('../../internal/parity/testdata/legs_pin.json', import.meta.url);
if (process.argv.includes('--check')) {
  const committed = JSON.parse(readFileSync(file, 'utf8'));
  assert.deepEqual({ ...JSON.parse(pinned), sha: null }, { ...committed, sha: null }, 'oracle protocol legs differ from the pin; regenerate');
  console.log(`${pin}: protocol legs match the pin (${requests.length} request cases, ${responses.length} response cases)`);
} else {
  writeFileSync(file, pinned);
  console.log(`${pin}: ${requests.length} request cases over ${legSpecs.length} legs, ${responses.length} response streams over ${responseSpecs.length} legs`);
}
