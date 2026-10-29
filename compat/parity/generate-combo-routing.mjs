// Records what the oracle does to a Combo's member order, so the Go tests can be
// offline and still differential (#270). Run:
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-combo-routing.mjs          # rewrite the pin
//   node compat/parity/generate-combo-routing.mjs --check  # fail if the oracle now differs
//
// Nothing is re-implemented here, with one exception noted at nested(). The oracle's own open-sse/services/combo.js and
// open-sse/services/capacityAdapter.js are read with `git show` at ORACLE_SHA and
// executed with a scripted capability table: detectRequiredCapabilities, then
// augmentModelsWithCapacityAdapter, then the real handleComboChat, whose
// handleSingleModel callback records the order it is asked to try each model in
// and answers 500 so that the oracle walks its whole fallback list. The single
// model branch of src/sse/handlers/chat.js:152-169 is the same call with
// getActiveAdapterStrategy. No provider is called and no credential is used.
// The Go tests read the file this writes; no test regenerates it.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { load, pin } from './oracle.mjs';

const out = new URL('../../internal/ingress/testdata/combo_routing_pin.json', import.meta.url);

// Capability names are the oracle's; the Go side spells two of them with a hyphen.
const KEBAB = { vision: 'vision', pdf: 'pdf', audioInput: 'audio-input', videoInput: 'video-input' };
const kebab = list => list.map(name => KEBAB[name] ?? name);

let table = {};
const caps = (provider, model) => {
  const names = table[`${provider}/${model}`] ?? [];
  return Object.fromEntries(names.map(name => [name, true]));
};
// extractTextContent is the oracle's own (translator/formats/gemini.js:113-119); the
// module's other imports are not reached by it.
const { extractTextContent } = await load('open-sse/translator/formats/gemini.js', { safeParseJSON: () => null, OPENAI_BLOCK: { TEXT: 'text' }, isGemini4Model: () => false });
const deps = {
  extractTextContent,
  checkFallbackError: () => ({ shouldFallback: true, cooldownMs: 0 }),
  formatRetryAfter: () => '',
  unavailableResponse: (status, message) => new Response(JSON.stringify({ error: { message } }), { status }),
  getCapabilitiesForModel: caps,
};
const delays = [];
const timers = { setTimeout: (fn, ms) => { delays.push(ms); fn(); return 0; }, clearTimeout() {} };
const errorConfig = await load('open-sse/config/errorConfig.js', {});
const fallback = await load('open-sse/services/accountFallback.js', errorConfig);
const combo = await load('open-sse/services/combo.js', { ...deps, checkFallbackError: fallback.checkFallbackError, formatRetryAfter: fallback.formatRetryAfter }, timers);
const adapter = await load('open-sse/services/capacityAdapter.js', { getCapabilitiesForModel: caps });
const log = { info() {}, warn() {} };

const image = { type: 'image_url', image_url: { url: 'data:image/png;base64,AAAA' } };
const audio = { type: 'input_audio', input_audio: { data: 'AAAA', format: 'wav' } };
const text = { type: 'text', text: 'hi' };
const user = content => ({ role: 'user', content });

// detectRequiredCapabilities over every body shape the oracle scans.
const bodies = {
  empty: {},
  textOnly: { messages: [user('hello')] },
  imageUrl: { messages: [user([text, image])] },
  imageOlderTurnOnly: { messages: [user([image]), { role: 'assistant', content: 'seen' }, user('and now?')] },
  imageInTrailingRun: { messages: [{ role: 'assistant', content: 'ok' }, user('look'), user([image])] },
  audioInput: { messages: [user([audio])] },
  videoUrl: { messages: [user([{ type: 'video_url', video_url: { url: 'x' } }])] },
  pdfFile: { messages: [user([{ type: 'file', file: { file_data: 'data:application/pdf;base64,AAAA' } }])] },
  fileWithoutMime: { messages: [user([{ type: 'file', file: { filename: 'a' } }])] },
  documentImageSource: { messages: [user([{ type: 'document', source: { media_type: 'image/png', data: 'AAAA' } }])] },
  documentAudioSource: { messages: [user([{ type: 'document', source: { data: 'data:audio/mp3;base64,AAAA' } }])] },
  fileInputAudio: { messages: [user([{ type: 'file', input_audio: { format: 'mp3' } }])] },
  ollamaImages: { messages: [{ role: 'user', content: 'x', images: ['AAAA'] }] },
  ollamaEmptyImages: { messages: [{ role: 'user', content: 'x', images: [] }] },
  attachmentMime: { messages: [{ role: 'user', content: 'x', experimental_attachments: [{ contentType: 'application/pdf' }] }] },
  attachmentDataUrl: { messages: [{ role: 'user', content: 'x', attachments: [{ url: 'data:audio/wav;base64,AAAA' }] }] },
  attachmentBareUrl: { messages: [{ role: 'user', content: 'x', attachments: [{ url: 'https://example.invalid/a' }] }] },
  messageImageField: { messages: [{ role: 'user', content: 'x', image_url: 'https://example.invalid/a.png' }] },
  messageAudioField: { messages: [{ role: 'user', content: 'x', audio: 'AAAA' }] },
  stringDataImage: { messages: [user('see data:image/png;base64,AAAA')] },
  stringDataAudio: { messages: [user('see data:audio/wav;base64,AAAA')] },
  stringDataPdf: { messages: [user('see data:application/pdf;base64,AAAA')] },
  stringImageBeatsAudio: { messages: [user('data:image/png;base64,A data:audio/wav;base64,B')] },
  responsesInputImage: { input: [{ role: 'user', content: [{ type: 'input_image', image_url: 'x' }] }] },
  responsesOlderImage: { input: [{ role: 'user', content: [{ type: 'input_image' }] }, { role: 'assistant', content: [] }, { role: 'user', content: [{ type: 'input_text', text: 'x' }] }] },
  geminiInline: { contents: [{ role: 'user', parts: [{ inlineData: { mimeType: 'image/jpeg', data: 'AAAA' } }] }] },
  geminiFileData: { contents: [{ role: 'user', parts: [{ fileData: { mimeType: 'application/pdf', fileUri: 'x' } }] }] },
  geminiModelTurnThenText: { contents: [{ role: 'user', parts: [{ inlineData: { mimeType: 'image/png' } }] }, { role: 'model', parts: [{ text: 'a' }] }, { role: 'user', parts: [{ text: 'b' }] }] },
  antigravityRequest: { request: { contents: [{ role: 'user', parts: [{ inlineData: { mimeType: 'audio/wav' } }] }] } },
  imageThenAudioOrder: { messages: [user([audio, image])] },
  imageAndPdfOrder: { messages: [user([{ type: 'file', source: { media_type: 'application/pdf' } }, image])] },
  notAnObject: [],
};
const detect = Object.entries(bodies).map(([name, body]) => ({ name, body, required: kebab([...combo.detectRequiredCapabilities(body)]) }));

// Member order. models are provider/model strings; capabilities are the scripted table.
const T = {
  'p/text': [], 'p/vision': ['vision'], 'q/vision2': ['vision'], 'p/vision-pdf': ['vision', 'pdf'],
  'a/vision': ['vision'], 'a/audio': ['audioInput'], 'a/both': ['vision', 'audioInput'], 'a/pdf': ['pdf'], 'a/text': [],
};
const imageBody = { messages: [user([image])] };
const audioBody = { messages: [user([audio])] };
const bothBody = { messages: [user([image, audio])] };
const plain = { messages: [user('hi')] };
const pool = (enabled, roundRobin, models) => ({ enabled, roundRobin, models });
const scenarios = [
  { name: 'fallback keeps order without a requirement', models: ['p/text', 'p/vision', 'q/vision2'], body: plain, strategy: 'fallback', calls: 2 },
  { name: 'capable members float, stably', models: ['p/text', 'p/vision', 'q/vision2'], body: imageBody, strategy: 'fallback', calls: 1 },
  { name: 'round-robin rotates before the reorder', models: ['p/vision', 'p/text', 'q/vision2'], body: imageBody, strategy: 'round-robin', sticky: 1, calls: 4 },
  { name: 'round-robin without a requirement', models: ['p/text', 'p/vision', 'q/vision2'], body: plain, strategy: 'round-robin', sticky: 1, calls: 5 },
  { name: 'sticky limit holds the start', models: ['p/text', 'p/vision', 'q/vision2'], body: plain, strategy: 'round-robin', sticky: 2, calls: 7 },
  { name: 'sticky limit of 3', models: ['p/text', 'p/vision'], body: plain, strategy: 'round-robin', sticky: 3, calls: 7 },
  { name: 'adapter added when no member is capable', models: ['p/text', 'a/text'], body: audioBody, strategy: 'fallback', adapters: { audioInput: pool(true, false, ['a/audio']) }, calls: 1 },
  { name: 'adapter not added when a member covers the request', models: ['p/text', 'p/vision'], body: imageBody, strategy: 'fallback', adapters: { vision: pool(true, false, ['a/vision']) }, calls: 1 },
  { name: 'adapter pool enabled but empty is a no-op', models: ['p/text'], body: audioBody, strategy: 'fallback', adapters: { audioInput: pool(true, false, []) }, calls: 1 },
  { name: 'adapter pool disabled is ignored', models: ['p/text'], body: audioBody, strategy: 'fallback', adapters: { audioInput: pool(false, false, ['a/audio']) }, calls: 1 },
  { name: 'pool model without the capability is left out', models: ['p/text'], body: audioBody, strategy: 'fallback', adapters: { audioInput: pool(true, false, ['a/text', 'a/audio']) }, calls: 1 },
  { name: 'pool model already in the combo is not duplicated', models: ['a/audio', 'p/text'], body: imageBody, strategy: 'fallback', adapters: { vision: pool(true, false, ['a/vision']) }, calls: 1 },
  { name: 'pools of all capabilities are flattened in the oracle key order', models: ['p/text'], body: bothBody, strategy: 'fallback', adapters: { audioInput: pool(true, false, ['a/both', 'a/audio']), vision: pool(true, false, ['a/vision', 'a/both']) }, calls: 1 },
  { name: 'an adapter model must satisfy every required capability', models: ['p/vision', 'p/text'], body: bothBody, strategy: 'fallback', adapters: { audioInput: pool(true, false, ['a/audio', 'a/both']) }, calls: 1 },
  { name: 'no adapter satisfies every capability', models: ['p/vision'], body: bothBody, strategy: 'fallback', adapters: { audioInput: pool(true, false, ['a/audio']), vision: pool(true, false, ['a/vision']) }, calls: 1 },
  { name: 'a pdf pool rescues a pdf request', models: ['p/text'], body: { messages: [user([{ type: 'file', source: { media_type: 'application/pdf' } }])] }, strategy: 'fallback', adapters: { pdf: pool(true, false, ['a/pdf']) }, calls: 1 },
  { name: 'adapter models rotate with the combo strategy, not their own flag', models: ['p/vision', 'p/text'], body: audioBody, strategy: 'round-robin', sticky: 1, adapters: { audioInput: pool(true, true, ['a/audio', 'a/both']) }, calls: 4 },
  { name: 'the pool roundRobin flag does not rotate a fallback combo', models: ['p/text'], body: audioBody, strategy: 'fallback', adapters: { audioInput: pool(true, true, ['a/audio', 'a/both']) }, calls: 3 },
  { name: 'single model rescued by a fill-first pool', solo: true, models: ['p/text'], body: imageBody, adapters: { vision: pool(true, false, ['a/vision', 'q/vision2']) }, calls: 3 },
  { name: 'single model rescued by a round-robin pool', solo: true, models: ['p/text'], body: imageBody, adapters: { vision: pool(true, true, ['a/vision', 'q/vision2']) }, calls: 4 },
  { name: 'single model needing two capabilities uses the first required pool', solo: true, models: ['p/text'], body: bothBody, adapters: { vision: pool(true, false, ['a/both', 'a/vision']), audioInput: pool(true, true, ['a/both']) }, calls: 3 },
  { name: 'single capable model is not rescued', solo: true, models: ['p/vision'], body: imageBody, adapters: { vision: pool(true, true, ['a/vision']) }, calls: 1 },
  { name: 'single model with no pool is not rescued', solo: true, models: ['p/text'], body: imageBody, adapters: {}, calls: 1 },
];

async function orderOf(scenario, settings) {
  const required = combo.detectRequiredCapabilities(scenario.body);
  const augmented = adapter.augmentModelsWithCapacityAdapter(scenario.models, required, settings);
  const tried = [];
  const handleSingleModel = async (_body, model) => { tried.push(model); return new Response('{"error":{"message":"scripted"}}', { status: 500 }); };
  if (scenario.solo && augmented.length <= 1) return { rescued: false, order: [...scenario.models] };
  await combo.handleComboChat({
    body: scenario.body, models: augmented, handleSingleModel, log, comboName: scenario.name,
    comboStrategy: scenario.solo ? adapter.getActiveAdapterStrategy(required, settings) : scenario.strategy,
    comboStickyLimit: scenario.solo ? undefined : scenario.sticky,
  });
  return { rescued: true, order: tried };
}

table = T;
const order = [];
for (const scenario of scenarios) {
  combo.resetComboRotation();
  const settings = { capacityAdapter: scenario.adapters ?? {} };
  const calls = [];
  for (let i = 0; i < scenario.calls; i++) calls.push(await orderOf(scenario, settings));
  order.push({
    name: scenario.name, solo: !!scenario.solo, models: scenario.models, strategy: scenario.strategy ?? null, sticky: scenario.sticky ?? null,
    required: kebab([...combo.detectRequiredCapabilities(scenario.body)]), body: scenario.body,
    adapters: scenario.adapters ?? {}, calls,
  });
}


// stripHistoryForContext (capacityAdapter.js:113-152), called directly.
const long = n => 'x'.repeat(n);
const msg = (role, content, extra = {}) => ({ role, content, ...extra });
const stripCases = [
  { name: 'no message array', body: { model: 'm', temperature: 0.2 }, window: 1000 },
  { name: 'empty messages', body: { messages: [] }, window: 1000 },
  { name: 'only system and a user turn', body: { messages: [msg('system', 'rules'), msg('user', 'hi')] }, window: 1000 },
  { name: 'older turns that fit are kept', body: { messages: [msg('system', 's'), msg('user', 'a'), msg('assistant', 'b'), msg('user', 'c')] }, window: 1000 },
  { name: 'more than six older turns drop the middle even when they fit', body: { messages: [...Array.from({ length: 10 }, (_, i) => msg(i % 2 ? 'assistant' : 'user', `turn ${i}`)), msg('user', 'now')] }, window: 100000 },
  { name: 'head overflow drops head turns from the end', body: { messages: [msg('system', long(40)), msg('user', long(100)), msg('assistant', long(100)), msg('user', long(100)), msg('assistant', long(100)), msg('user', long(30))] }, window: 150 },
  { name: 'everything but the tail dropped', body: { messages: [msg('system', long(10)), msg('user', long(400)), msg('assistant', long(400)), msg('user', long(600))] }, window: 100 },
  { name: 'developer role is a system message', body: { messages: [msg('developer', 'dev'), msg('user', long(300)), msg('assistant', long(300)), msg('user', 'now')] }, window: 100 },
  { name: 'system messages in the middle are hoisted', body: { messages: [msg('user', long(200)), msg('system', 'late rules'), msg('assistant', long(200)), msg('user', 'now')] }, window: 100 },
  { name: 'trailing user run of several items is kept', body: { messages: [msg('user', long(200)), msg('assistant', long(200)), msg('user', 'one'), msg('user', 'two'), msg('user', 'three')] }, window: 100 },
  { name: 'no assistant turn means no older history', body: { messages: [msg('user', long(500)), msg('user', long(500))] }, window: 100 },
  { name: 'block arrays count text and 50 for a block without text', body: { messages: [msg('user', [{ type: 'text', text: long(100) }, { type: 'image_url', image_url: { url: 'u' } }]), msg('assistant', [{ type: 'text', text: long(100) }]), msg('user', [{ type: 'text', text: 'now' }])] }, window: 60 },
  { name: 'non-string non-array content counts zero', body: { messages: [msg('user', null), msg('assistant', 5), msg('user', { text: long(1000) }), msg('assistant', 'ok'), msg('user', 'now')] }, window: 50 },
  { name: 'UTF-16 length: astral characters count twice', body: { messages: [msg('user', '😀'.repeat(150)), msg('assistant', '😀'.repeat(150)), msg('user', 'now')] }, window: 100 },
  { name: 'Responses input array', body: { model: 'm', input: [msg('user', long(300)), msg('assistant', long(300)), msg('user', 'now')], store: false }, window: 100 },
  { name: 'Gemini contents use parts', body: { contents: [{ role: 'user', parts: [{ text: long(300) }] }, { role: 'model', parts: [{ text: long(300) }] }, { role: 'user', parts: [{ text: 'now' }] }] }, window: 100 },
  { name: 'messages win over input', body: { messages: [msg('user', long(300)), msg('assistant', long(300)), msg('user', 'now')], input: [msg('user', 'keep me')] }, window: 100 },
  { name: 'unknown window defaults to 200000', body: { messages: [...Array.from({ length: 9 }, (_, i) => msg(i % 2 ? 'assistant' : 'user', long(10))), msg('user', 'now')] }, window: null },
  { name: 'unknown fields and message extras survive', body: { model: 'm', metadata: { a: [1, 2] }, messages: [msg('user', long(300), { name: 'n' }), msg('assistant', long(300), { tool_calls: [] }), msg('user', 'now', { extra: true })] }, window: 100 },
];
const strip = stripCases.map(c => ({ name: c.name, body: c.body, window: c.window, result: adapter.stripHistoryForContext(c.body, c.window ?? undefined) }));

// handleComboChat's answer when its models fail, and the waits it takes first.
// Each member is a scripted result: a status and a body (a string is the raw
// body, an object is JSON). Every status here is a failure.
const json = (status, text) => ({ status, body: JSON.stringify({ error: { message: text } }) });
const outcomeCases = [
  { name: 'one model fails transiently with a message that names the condition', members: [json(503, 'The model is overloaded')] },
  { name: 'a bare 503 is not waited on', members: [json(503, 'down')] },
  { name: 'a 502 with request not allowed waits five seconds', members: [json(502, 'request not allowed'), json(500, 'boom')] },
  { name: 'a 504 with a capacity message waits two seconds', members: [json(504, 'out of capacity'), { status: 200, body: '{"ok":true}' }] },
  { name: 'a 429 is a fallback but not a transient wait', members: [json(429, 'Rate limit hit'), json(500, 'last')] },
  { name: 'first status wins, last message wins', members: [json(502, 'bad gateway'), json(500, 'boom'), json(504, 'timeout')] },
  { name: 'missing credentials answer 503 whatever the first status', members: [json(404, 'a'), json(404, 'No credentials for provider: x')] },
  { name: 'a terminal client error is returned as it is', terminal: true, members: [json(400, 'bad request'), { status: 200, body: '{"ok":true}' }] },
  { name: 'a terminal error after a fallback is returned as it is', terminal: true, members: [json(503, 'down'), json(400, 'bad request')] },
  { name: 'error as a plain string', members: [{ status: 503, body: JSON.stringify({ error: 'plain string' }) }] },
  { name: 'message at the top level', members: [{ status: 503, body: JSON.stringify({ message: 'top level' }) }] },
  { name: 'error object without a message is written as JSON', members: [{ status: 503, body: JSON.stringify({ error: { code: 'x' } }) }] },
  { name: 'unparseable body falls back to the status text', members: [{ status: 503, body: 'not json', statusText: 'Service Unavailable' }] },
  { name: 'no body and no status text falls back to the status code', members: [{ status: 502, body: '', statusText: '' }] },
  { name: 'context overflow falls through without waiting', members: [json(503, 'maximum context length exceeded'), { status: 200, body: '{"ok":true}' }] },
  { name: 'quota text on a 503 waits two seconds', members: [json(503, 'quota exceeded for today'), json(503, 'quota exceeded for today')] },
];
const outcomes = [];
for (const scenario of outcomeCases) {
  delays.length = 0;
  let index = 0;
  const order = [];
  const models = scenario.members.map((_, i) => `m${i}`);
  const handleSingleModel = async (_body, model) => {
    order.push(model);
    const m = scenario.members[index++];
    return new Response(m.body, { status: m.status, statusText: m.statusText ?? 'Reason' });
  };
  combo.resetComboRotation();
  const response = await combo.handleComboChat({ body: { messages: [] }, models, handleSingleModel, log, comboName: scenario.name, comboStrategy: 'fallback' });
  let message = null;
  try { message = (await response.clone().json())?.error?.message ?? null; } catch { /* not JSON */ }
  outcomes.push({
    name: scenario.name, terminal: !!scenario.terminal, success: response.ok,
    members: scenario.members.map(m => ({ status: m.status, body: m.body, statusText: m.statusText ?? 'Reason' })),
    tried: order.length, status: response.status, message, waits: [...delays],
  });
}

// Nested Combos. chat.js:177-229 is not executable here (it imports the whole
// server), so its dispatch is restated in five lines: a bare name that is a
// Combo runs handleComboChat over that Combo's models, anything else is a leaf
// that records the attempt. The Combo walk itself is the oracle's own.
const nestedTable = {
  outer: ['inner', 'p/z'], inner: ['p/a', 'p/b'], rr: ['inner', 'p/z'], deep: ['mid', 'p/z'], mid: ['inner'],
  pair: ['left', 'right'], left: ['p/a', 'p/b'], right: ['p/c', 'p/d'],
};
const nestedStrategy = { rr: 'round-robin', inner: 'round-robin' };
async function nested(root, rootModels, results) {
  const tried = [];
  const walk = async (name, models, body) => combo.handleComboChat({
    body, models, comboName: name, comboStrategy: nestedStrategy[name] ?? 'fallback', comboStickyLimit: 1, log,
    handleSingleModel: async (b, model) => {
      if (!model.includes('/') && nestedTable[model]) return walk(model, nestedTable[model], b);
      tried.push(model);
      const status = results[model] ?? 200;
      return new Response(status === 200 ? '{"ok":true}' : JSON.stringify({ error: { message: 'scripted' } }), { status });
    },
  });
  const response = await walk(root, rootModels, { messages: [{ role: 'user', content: 'x' }] });
  return { tried, status: response.status };
}
const nestedScenarios = [
  { name: 'the nested Combo is the first member and serves', root: 'outer', calls: [{}, {}] },
  { name: 'the nested Combo fails through to the next outer member', root: 'outer', calls: [{ 'p/a': 503, 'p/b': 503 }] },
  { name: 'the nested Combo is not reached when an earlier member serves', root: 'pair', calls: [{}, {}, {}] },
  { name: 'a nested round-robin rotates only when reached', root: 'rr', calls: [{}, { 'p/a': 503, 'p/b': 503 }, {}, {}] },
  { name: 'a Combo nested two deep', root: 'deep', calls: [{ 'p/a': 503 }, { 'p/a': 503, 'p/b': 503 }] },
  { name: 'every leaf of a nested pair fails', root: 'pair', calls: [{ 'p/a': 503, 'p/b': 503, 'p/c': 503, 'p/d': 503 }] },
  { name: 'the first nested Combo fails and the second serves', root: 'pair', calls: [{ 'p/a': 503, 'p/b': 503 }, {}] },
];
const nestedOut = [];
for (const scenario of nestedScenarios) {
  combo.resetComboRotation();
  const calls = [];
  for (const results of scenario.calls) calls.push({ results, ...await nested(scenario.root, nestedTable[scenario.root], results) });
  nestedOut.push({ name: scenario.name, root: scenario.root, calls });
}


// Fusion (combo.js:471-625). The oracle's own handleFusionChat runs under a
// virtual clock: setTimeout is replaced, every panel call resolves at the virtual
// time its scenario gives it, and time advances only when nothing else can run.
// handleSingleModel records each call it is asked for (model, whether it is a
// panel call, the body, the virtual time) and answers as scripted.
function virtualClock() {
  let now = 0;
  let seq = 0;
  const queue = [];
  return {
    now: () => now,
    globals: {
      setTimeout(fn, ms = 0) { const timer = { at: now + Math.max(0, ms), seq: seq++, fn, off: false }; queue.push(timer); return timer; },
      clearTimeout(timer) { if (timer) timer.off = true; },
    },
    async run(promise) {
      let finished = false;
      let value;
      let failure;
      promise.then(v => { finished = true; value = v; }, e => { finished = true; failure = e; });
      for (;;) {
        await new Promise(resolve => setImmediate(resolve));
        if (finished) break;
        const next = queue.filter(t => !t.off).sort((a, b) => a.at - b.at || a.seq - b.seq)[0];
        if (!next) { await new Promise(resolve => setImmediate(resolve)); if (finished) break; throw new Error('fusion scenario stalled'); }
        next.off = true;
        now = next.at;
        next.fn();
      }
      if (failure) throw failure;
      return value;
    },
  };
}

const answerShapes = {
  openai: text => ({ choices: [{ message: { role: 'assistant', content: text } }] }),
  openaiBlocks: text => ({ choices: [{ message: { role: 'assistant', content: [{ type: 'text', text }, { type: 'image_url', image_url: { url: 'u' } }] } }] }),
  delta: text => ({ choices: [{ delta: { content: text } }] }),
  choiceText: text => ({ choices: [{ text }] }),
  claude: text => ({ type: 'message', content: [{ type: 'text', text }] }),
  gemini: text => ({ candidates: [{ content: { parts: [{ text }, { text: ' more' }] } }] }),
  responses: text => ({ output: [{ type: 'message', content: [{ type: 'output_text', text }] }, { type: 'reasoning' }] }),
  ollama: text => ({ message: { role: 'assistant', content: text } }),
};
const ok = (delay, text, shape = 'openai') => ({ delay, status: 200, body: JSON.stringify(answerShapes[shape](text)) });
const bad = (delay, status = 500) => ({ delay, status, body: JSON.stringify({ error: { message: 'scripted' } }) });
const plainUser = { messages: [{ role: 'user', content: 'question' }] };
const toolConversation = {
  model: 'panel', stream: true, stream_options: { include_usage: true }, tool_choice: 'auto',
  tools: [{ type: 'function', function: { name: 'lookup' } }],
  temperature: 0.3,
  messages: [
    { role: 'system', content: 'rules' },
    { role: 'user', content: 'find it' },
    { role: 'assistant', content: 'checking', tool_calls: [{ id: '1', function: { name: 'lookup', arguments: '{}' } }, { id: '2', name: 'other' }, { id: '3' }] },
    { role: 'tool', tool_call_id: '1', content: 'result text' },
    { role: 'function', name: 'f', content: [{ type: 'text', text: 'blocks' }, { type: 'text', text: ' joined' }] },
    { role: 'assistant', content: [{ type: 'text', text: 'let me' }, { type: 'tool_use', id: 'a', name: 'search', input: {} }] },
    { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'a', content: 'found' }, { type: 'text', text: 'thanks' }] },
    { role: 'assistant', content: null, tool_calls: [{ function: { name: 'x' } }] },
    { role: 'user', content: 'now answer' },
  ],
};
const fusionScenarios = [
  { name: 'every panel model answers; the judge gets the sources in panel order', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(60, 'alpha'), 'pb/b': ok(20, 'beta'), 'pc/c': ok(40, 'gamma') }, tuning: { stragglerGraceMs: 500, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'a straggler within the grace window is included', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(40, 'beta'), 'pc/c': ok(120, 'gamma') }, tuning: { minPanel: 2, stragglerGraceMs: 200, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'a straggler past the grace window is dropped', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(40, 'beta'), 'pc/c': ok(500, 'gamma') }, tuning: { minPanel: 2, stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'zero grace stops at the quorum', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(60, 'beta'), 'pc/c': ok(200, 'gamma') }, tuning: { minPanel: 2, stragglerGraceMs: 0, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'a higher minPanel waits for more answers before the grace window opens', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(60, 'beta'), 'pc/c': ok(200, 'gamma') }, tuning: { minPanel: 3, stragglerGraceMs: 50, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'minPanel below two is two', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(60, 'beta'), 'pc/c': ok(300, 'gamma') }, tuning: { minPanel: 0, stragglerGraceMs: 50, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'minPanel above the panel size is the panel size', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(60, 'beta') }, tuning: { minPanel: 9, stragglerGraceMs: 50, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'the hard timeout ends the panel; one answer is returned directly', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(900, 'beta'), 'pc/c': ok(900, 'gamma') }, tuning: { minPanel: 2, stragglerGraceMs: 100, panelHardTimeoutMs: 300 }, body: plainUser },
  { name: 'every panel model fails', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': bad(20), 'pb/b': bad(40, 503), 'pc/c': bad(60, 429) }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'every panel model times out', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': ok(900, 'alpha'), 'pb/b': ok(900, 'beta') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 200 }, body: plainUser },
  { name: 'failures do not count toward the quorum', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': bad(20), 'pb/b': ok(40, 'beta'), 'pc/c': ok(100, 'gamma') }, tuning: { minPanel: 2, stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'one success is answered by calling that model with the original request', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': bad(20), 'pb/b': ok(40, 'beta'), 'pc/c': bad(60) }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: toolConversation },
  { name: 'an answer with no text is not an answer', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': ok(20, '   '), 'pb/b': ok(40, 'beta') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'an unparseable answer is not an answer', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': { delay: 20, status: 200, body: 'not json' }, 'pb/b': ok(40, 'beta') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'no judge configured: the first panel model judges', panel: ['pa/a', 'pb/b'], judge: undefined, members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(40, 'beta') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'a blank judge is no judge', panel: ['pa/a', 'pb/b'], judge: '   ', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(40, 'beta') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'panel calls drop tools and flatten tool turns; the judge call keeps the request as it came', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(40, 'beta') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: toolConversation },
  { name: 'Responses input array', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha', 'responses'), 'pb/b': ok(40, 'beta', 'responses') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: { model: 'panel', stream: false, tools: [{ type: 'function', name: 'f' }], input: [{ role: 'user', content: 'hi' }, { role: 'tool', content: 'out' }, { role: 'user', content: 'again' }] } },
  { name: 'Gemini contents are not flattened and the judge turn uses parts', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha', 'gemini'), 'pb/b': ok(40, 'beta', 'gemini') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: { contents: [{ role: 'user', parts: [{ text: 'hi' }] }] } },
  { name: 'a body with no message array gets a messages array for the judge', panel: ['pa/a', 'pb/b'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha'), 'pb/b': ok(40, 'beta') }, tuning: { stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: { model: 'panel', prompt: 'old style' } },
  { name: 'answer shapes: Claude blocks, Gemini parts, Responses output, delta, choice text', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'from claude', 'claude'), 'pb/b': ok(40, 'from gemini', 'gemini'), 'pc/c': ok(60, 'from responses', 'responses') }, tuning: { minPanel: 3, stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'answer shapes: delta, choice text and text blocks beside other blocks', panel: ['pa/a', 'pb/b', 'pc/c'], judge: 'pj/j', members: { 'pa/a': ok(20, 'from delta', 'delta'), 'pb/b': ok(40, 'from text', 'choiceText'), 'pc/c': ok(60, 'from blocks', 'openaiBlocks') }, tuning: { minPanel: 3, stragglerGraceMs: 100, panelHardTimeoutMs: 2000 }, body: plainUser },
  { name: 'a single-model Fusion answers directly', panel: ['pa/a'], judge: 'pj/j', members: { 'pa/a': ok(20, 'alpha') }, tuning: {}, body: toolConversation },
];
const fusionOut = [];
for (const scenario of fusionScenarios) {
  const clock = virtualClock();
  const fusionCombo = await load('open-sse/services/combo.js', { ...deps, checkFallbackError: fallback.checkFallbackError, formatRetryAfter: fallback.formatRetryAfter }, clock.globals);
  const calls = [];
  const seenPanel = new Set();
  const handleSingleModel = (body, model, isPanel) => new Promise(resolve => {
    calls.push({ model, panel: !!isPanel, at: clock.now(), body: JSON.parse(JSON.stringify(body)) });
    const script = isPanel ? scenario.members[model] : { delay: 0, status: 200, body: JSON.stringify(answerShapes.openai('FINAL')) };
    clock.globals.setTimeout(() => resolve(new Response(script.body, { status: script.status })), script.delay);
  });
  void seenPanel;
  const response = await clock.run(fusionCombo.handleFusionChat({ body: scenario.body, models: scenario.panel, handleSingleModel, log, comboName: 'panel', judgeModel: scenario.judge, tuning: scenario.tuning }));
  fusionOut.push({
    name: scenario.name, panel: scenario.panel, judge: scenario.judge ?? null, tuning: scenario.tuning, body: scenario.body,
    members: scenario.members, calls, final: { status: response.status, body: await response.text() }, endedAt: clock.now(),
  });
}

const pinned = { oracle: 'raufimusaddiq/literouter', sha: pin, capabilities: Object.fromEntries(Object.entries(T).map(([k, v]) => [k, kebab(v)])), detect, order, strip, outcomes, nestedTable, nestedStrategy, nested: nestedOut, fusion: fusionOut };
if (process.argv.includes('--check')) {
  // The committed file records the commit it was generated at, so a moved main is
  // not a difference; a changed answer is. A failure means the oracle's Combo
  // routing changed: read the diff, port it, then regenerate.
  const committed = JSON.parse(readFileSync(out, 'utf8'));
  assert.deepEqual({ ...JSON.parse(JSON.stringify(pinned)), sha: null }, { ...committed, sha: null }, `oracle Combo routing differs from ${out.pathname}`);
  console.log(`${pin}: Combo routing matches the committed pin (generated at ${committed.sha})`);
} else {
  writeFileSync(out, `${JSON.stringify(pinned, null, 2)}\n`);
  console.log(`${pin}: ${detect.length} detection cases, ${order.length} order scenarios -> ${out.pathname}`);
}
