// Records what the oracle does to a Combo's member order, so the Go tests can be
// offline and still differential (#270). Run:
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-combo-routing.mjs          # rewrite the pin
//   node compat/parity/generate-combo-routing.mjs --check  # fail if the oracle now differs
//
// Nothing is re-implemented here. The oracle's own open-sse/services/combo.js and
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
const deps = {
  checkFallbackError: () => ({ shouldFallback: true, cooldownMs: 0 }),
  formatRetryAfter: () => '',
  unavailableResponse: (status, message) => new Response(JSON.stringify({ error: { message } }), { status }),
  getCapabilitiesForModel: caps,
  extractTextContent: () => '',
};
const combo = await load('open-sse/services/combo.js', deps);
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

const pinned = { oracle: 'raufimusaddiq/literouter', sha: pin, capabilities: Object.fromEntries(Object.entries(T).map(([k, v]) => [k, kebab(v)])), detect, order };
if (process.argv.includes('--check')) {
  // The committed file records the commit it was generated at, so a moved main is
  // not a difference; a changed answer is. A failure means the oracle's Combo
  // routing changed: read the diff, port it, then regenerate.
  const committed = JSON.parse(readFileSync(out, 'utf8'));
  assert.deepEqual({ ...pinned, sha: null }, { ...committed, sha: null }, `oracle Combo routing differs from ${out.pathname}`);
  console.log(`${pin}: Combo routing matches the committed pin (generated at ${committed.sha})`);
} else {
  writeFileSync(out, `${JSON.stringify(pinned, null, 2)}\n`);
  console.log(`${pin}: ${detect.length} detection cases, ${order.length} order scenarios -> ${out.pathname}`);
}
