// Records what the pinned Antigravity executor does with an image model, so the
// Go tests can be offline and still differential. Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-antigravity-image.mjs
//
// It evaluates the pin's own executors/antigravity.js (isImageModel,
// parseImageConfig, buildUrl, transformRequest and buildIdeRequestId) and the
// image test of handlers/chatCore.js, read with `git show` at the pin. The clock
// and the session are fixed; nothing is sent and no credential is used. The Go
// tests read the file this writes; no test regenerates it.
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { writeFileSync } from 'node:fs';
import vm from 'node:vm';
import { source, pin } from './oracle.mjs';

const NOW = 1_700_000_000_000; // Tue, 14 Nov 2023 22:13:20 GMT
const SESSION = 'synthetic-session';
const out = new URL('../../internal/protocol/gemini/testdata/antigravity_image_pin.json', import.meta.url);

const imports = /^import\s[\s\S]*?;\s*$/gm;
const bare = text => text.replace(imports, '').replace(/^export default .*;\s*$/gm, '').replace(/^export\s+/gm, '');

// chatCore.js:151 forces the call non-streaming for this test as well as the executor's.
const chatCoreLine = source('open-sse/handlers/chatCore.js').match(/const isImageGenModel = modelType === "imageGen" \|\| (\/[^\n]*\/i)\.test\(model\);/);
assert.ok(chatCoreLine, 'chatCore.js image test');
const chatCoreImage = new RegExp(chatCoreLine[1].slice(1, -2), 'i');

const names = ['crypto', 'Date'];
const script = `(({${names.join(',')}, ...stubs}) => {
const { BaseExecutor, PROVIDERS, OAUTH_ENDPOINTS, ANTIGRAVITY_HEADERS, AG_DEFAULT_TOOLS, AG_TOOL_SUFFIX, ANTIGRAVITY_PROMPT_REWRITES, HTTP_STATUS,
  resolveSessionId, toNumericSessionId, proxyAwareFetch, cleanJSONSchemaForAntigravity, normalizeGeminiContents,
  DEFAULT_THINKING_AG_SIGNATURE, getGeminiThoughtSignatureSync } = stubs;
${bare(source('open-sse/executors/antigravity.js'))}
return { AntigravityExecutor, isImageModel, parseImageConfig, buildIdeRequestId };
})`;

class FakeDate extends Date {
  constructor(...args) { super(...(args.length ? args : [NOW])); }
  static now() { return NOW; }
}
class BaseExecutor {
  constructor(provider, config) { this.provider = provider; this.config = config; }
  getBaseUrls() { return ['https://synthetic.invalid']; }
}
const stubs = {
  crypto, Date: FakeDate, BaseExecutor, PROVIDERS: { antigravity: {} }, OAUTH_ENDPOINTS: {}, ANTIGRAVITY_HEADERS: {}, AG_DEFAULT_TOOLS: new Set(),
  AG_TOOL_SUFFIX: '_ide', ANTIGRAVITY_PROMPT_REWRITES: [], HTTP_STATUS: {}, resolveSessionId: () => SESSION, toNumericSessionId: id => id,
  proxyAwareFetch: () => { throw new Error('no network'); }, cleanJSONSchemaForAntigravity: schema => schema, normalizeGeminiContents: contents => contents,
  DEFAULT_THINKING_AG_SIGNATURE: 'synthetic', getGeminiThoughtSignatureSync: () => null,
};
const context = vm.createContext({ JSON, Math, Set, Map, Error, String, Number, Array, Object, parseInt, structuredClone });
const pinned = new vm.Script(script, { filename: 'antigravity-pin' }).runInContext(context)(stubs);
const executor = new pinned.AntigravityExecutor();
const credentials = { accessToken: 'synthetic', projectId: 'synthetic-project', connectionId: 'synthetic-connection' };

// Which models are image models, what the call is, the ratio and the model sent.
const modelNames = [
  'gemini-3-flash', 'gemini-3.8-flash-high', 'claude-sonnet-4-6', 'gpt-oss-120b-medium',
  'gemini-3.1-flash-image', 'gemini-3-pro-image-preview', 'gemini-2.5-flash-image-preview', 'imagen-4.0-generate-001', 'Imagen-4', 'GEMINI-IMAGE',
  'image-generation-x', 'gemini-image-16x9', 'gemini-image-9x16', 'gemini-image-1x1', 'gemini-image-16x16', 'gemini-image-17x16', 'gemini-image-0x5',
  'gemini-image-1024x768', 'gemini-image-1920x1080', 'gemini-image-768x1024', 'gemini-image-1024x0', 'gemini-image-0016x0009', 'gemini-image16x9',
  'gemini-image-16x9-preview', 'gemini-image-16X9', 'gemini-image-4x3x2', 'gemini-image-x9', 'gemini-image-1024x768x', 'claude-image', 'img', 'imag',
  'gemini-İmage', 'gemini-Kimage', 'gemini-IMAGE-3x2', 'gemini-image-9007199254740993x3',
];
const models = modelNames.map(model => {
  const image = pinned.isImageModel(model);
  const entry = {
    model, image, chatCoreImage: chatCoreImage.test(model),
    streamUrl: executor.buildUrl(model, true), plainUrl: executor.buildUrl(model, false),
  };
  if (image) {
    entry.imageConfig = pinned.parseImageConfig(model);
    entry.upstreamModel = executor.transformRequest(model, { request: { contents: [{ role: 'user', parts: [{ text: 'x' }] }] } }, false, credentials).model;
  }
  return entry;
});

// What the image request carries out of a translated Gemini body.
const bodies = {
  'text only': { request: { contents: [{ role: 'user', parts: [{ text: 'draw a cat' }] }] } },
  'conversation': { request: { contents: [
    { role: 'user', parts: [{ text: 'draw a cat' }] }, { role: 'model', parts: [{ text: 'here' }] }, { role: 'user', parts: [{ text: 'bluer' }, { text: 'please' }] },
  ] } },
  'no role': { request: { contents: [{ parts: [{ text: 'draw' }] }] } },
  'empty text kept': { request: { contents: [{ role: 'user', parts: [{ text: '' }] }] } },
  'signature dropped': { request: { contents: [{ role: 'model', parts: [{ text: 'a', thoughtSignature: 'sig' }] }, { role: 'user', parts: [{ text: 'b' }] }] } },
  'turn without text left out': { request: { contents: [{ role: 'user', parts: [{ text: 'a' }] }, { role: 'model', parts: [] }, { role: 'user', parts: [{ text: 'b' }] }] } },
  'everything else dropped': { request: {
    contents: [{ role: 'user', parts: [{ text: 'a' }, { inlineData: { mimeType: 'image/png', data: 'AA==' } }] }],
    systemInstruction: { role: 'user', parts: [{ text: 'system' }] },
    tools: [{ functionDeclarations: [{ name: 'f' }] }], toolConfig: { functionCallingConfig: { mode: 'AUTO' } },
    generationConfig: { temperature: 0.2, topP: 0.5, maxOutputTokens: 100, stopSequences: ['x'] }, safetySettings: [{ category: 'c', threshold: 'OFF' }],
  } },
};
const transforms = Object.entries(bodies).map(([name, body]) => {
  const sent = executor.transformRequest('gemini-3.1-flash-image-16x9', structuredClone(body), true, credentials);
  return { name, body, sent };
});

// The IDE request id of an image request: the model without its suffix and image_gen.
const ids = ['gemini-3.1-flash-image', 'gemini-3.1-flash-image-1024x768'].map(model => {
  const sent = executor.transformRequest(model, { request: { contents: [{ role: 'user', parts: [{ text: 'a' }] }, { role: 'model', parts: [{ text: 'b' }] }, { role: 'user', parts: [{ text: 'c' }] }] } }, true, credentials);
  return { model, upstreamModel: sent.model, session: sent.request.sessionId, turns: sent.request.contents.length, requestId: sent.requestId };
});

writeFileSync(out, JSON.stringify({ pin, now: NOW, models, transforms, ids }, null, 1) + '\n');
console.log(`wrote ${models.length} models, ${transforms.length} transforms, ${ids.length} request ids`);
