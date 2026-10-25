// Records what the pinned Qoder serving path does, so the Go tests can be offline
// and still differential. Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-qoder.mjs
//
// It evaluates the pin's own executors/qoder.js, services/qoderModels.js and
// shared/qoder/{constants,encoding,sse,attachments,contextTier}.js, read with
// `git show` at the pin, with the network, the clock, uuid, Math.random and the
// COSY signer scripted. Nothing is sent and no credential is used. The Go tests read
// internal/providers/wire/testdata/qoder_pin.json; no test regenerates it.
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { writeFileSync } from 'node:fs';
import vm from 'node:vm';
import { source } from './oracle.mjs';

const NOW = 1_700_000_000_000; // Tue, 14 Nov 2023 22:13:20 GMT
const out = new URL('../../internal/providers/wire/testdata/qoder_pin.json', import.meta.url);

// Evaluate one pinned module. Imports are replaced by `deps`; every top-level
// declaration in `names` is returned. `export` keywords are dropped.
function evaluate(path, deps, names, extra = {}) {
  const code = source(path).replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export default .*;\s*$/gm, '').replace(/^export\s+/gm, '');
  assert.doesNotMatch(code, /^\s*(?:import|export)\b/m, `${path}: unsupported module syntax`);
  const context = vm.createContext({ Buffer, TextDecoder, TextEncoder, Response, ReadableStream, AbortController, AbortSignal,
    URL, URLSearchParams, Set, Map, Promise, JSON, Math: extra.Math ?? Math, Date: extra.Date ?? Date, Error, Number, String, Array, Object,
    Int16Array, RegExp, console: extra.console ?? { log() {}, warn() {}, error() {} }, process: { env: extra.env ?? {} },
    setTimeout: extra.setTimeout ?? (() => 0), clearTimeout() {}, queueMicrotask });
  context.deps = deps;
  return new vm.Script(`(({${Object.keys(deps).join(',')}}) => {${code}\nreturn {${names.join(',')}};})(deps)`, { filename: path })
    .runInContext(context, { timeout: 10000 });
}

const take = (text, re, what) => { const m = text.match(re); assert.ok(m, what); return m[0]; };

class FakeDate extends Date {
  constructor(...args) { super(...(args.length ? args : [NOW])); }
  static now() { return NOW; }
}
const fakeMath = Object.create(Math, { random: { value: () => 0.5 } });

// uuid: a counter, so the Nth id of a run is known.
const uuidSequence = () => { let n = 0; return () => `00000000-0000-4000-8000-${String(++n).padStart(12, '0')}`; };

const constants = evaluate('open-sse/shared/qoder/constants.js', {}, [
  'QODER_JOB_TOKEN_EXCHANGE_URL', 'QODER_USERINFO_URL', 'QODER_MODEL_LIST_URL', 'QODER_CHAT_BASE', 'QODER_CHAT_BASE_ALT',
  'QODER_CHAT_SIG_PATH', 'QODER_IMAGE_UPLOAD_SIG_PATH', 'QODER_MAX_PAYLOAD_BYTES', 'QODER_INLINE_FALLBACK_MAX_BYTES',
  'QODER_CONTEXT_TIER_HEADROOM', 'QODER_CONTEXT_TIER_ENV', 'QODER_CONTEXT_TIER_MODES', 'qoderInferenceBase',
  'QODER_IDE_VERSION', 'QODER_CLIENT_TYPE',
]);
const blocks = evaluate('open-sse/translator/schema/blocks.js', {}, ['OPENAI_BLOCK', 'CLAUDE_BLOCK']);
const imageSource = source('open-sse/translator/concerns/image.js');
const parseDataUri = new Function(`${take(imageSource, /const DATA_URI_RE = [^\n]*\n/, 'DATA_URI_RE')}
${take(imageSource, /export function parseDataUri[\s\S]*?\n\}/, 'parseDataUri').replace('export ', '')}
return parseDataUri;`)();
const encodeDataUri = new Function(`${take(imageSource, /export function encodeDataUri[\s\S]*?\n\}/, 'encodeDataUri').replace('export ', '')}
return encodeDataUri;`)();
const MAX_IMAGE_BYTES = Function(`return ${take(source('open-sse/config/mediaConfig.js'), /MAX_IMAGE_BYTES = ([^;]*);/, 'MAX_IMAGE_BYTES').replace(/^.*= /, '').replace(/;$/, '')}`)();
const runtime = source('open-sse/config/runtimeConfig.js');
const HTTP_STATUS = Function(`${take(runtime, /export const HTTP_STATUS = \{[\s\S]*?\n\};/, 'HTTP_STATUS').replace('export ', '')}\nreturn HTTP_STATUS;`)();
const SSE_DONE = 'data: [DONE]\n\n';
const { qoderEncodeBody } = evaluate('open-sse/shared/qoder/encoding.js', {}, ['qoderEncodeBody']);
const sse = evaluate('open-sse/shared/qoder/sse.js', {}, ['canonicalizeQoderUsage', 'createQoderSseCoalescer'], { Date: FakeDate });
const tier = evaluate('open-sse/shared/qoder/contextTier.js', {
  QODER_CONTEXT_TIER_HEADROOM: constants.QODER_CONTEXT_TIER_HEADROOM, QODER_CONTEXT_TIER_MODES: constants.QODER_CONTEXT_TIER_MODES,
}, ['resolveQoderContextTier', 'applyQoderContextTier', 'estimateQoderPromptTokens', 'getQoderContextTiers', 'parseTierTokenCount']);

// ---- scripted world -------------------------------------------------------

function world({ fetches = [], upload = null, catalog = {}, refreshCatalog = null, env = {}, errors = [] } = {}) {
  const calls = { fetch: [], signed: [], upload: [], resolve: [], cancel: 0, warn: [] };
  const uuid = uuidSequence();
  const log = { warn: (tag, message) => calls.warn.push(`${tag}: ${message}`), info() {}, error() {} };
  const proxyAwareFetch = async (url, options, proxyOptions) => {
    const step = fetches[Math.min(calls.fetch.length, fetches.length - 1)];
    const body = options.body;
    calls.fetch.push({
      url, method: options.method, proxyOptions: proxyOptions ?? null,
      headers: Object.fromEntries(Object.entries(options.headers ?? {}).filter(([k]) => !/^(authorization|cosy-|login-version|x-request-id)/i.test(k) || /^cosy-(version|clienttype)$/i.test(k) || k === 'Authorization' && step?.keepAuth)),
      body: body == null ? null : typeof body === 'string' ? body : Buffer.from(body).toString('latin1'),
    });
    if (step?.network) throw new Error(step.network);
    return new Response(step.body ?? '', { status: step.status ?? 200, headers: step.headers ?? {} });
  };
  const buildCosyHeaders = (body, url, creds) => {
    calls.signed.push({ body: Buffer.from(body).toString('latin1'), url, creds });
    return { Authorization: 'COSY.stub', 'Cosy-Stub': '1' };
  };
  const models = {
    isQoderPat: token => typeof token === 'string' && token.startsWith('pt-'),
    resolveQoderCredentials: async credentials => credentials,
    getQoderModelConfig: async (_credentials, key) => { calls.resolve.push(`get:${key}`); return catalog[key] ? { ...catalog[key], key } : null; },
    resolveQoderModels: async (_credentials, options) => {
      calls.resolve.push(`resolve:${options?.forceRefresh ? 'force' : 'cache'}`);
      return refreshCatalog ? { rawConfigs: new Map(Object.entries(refreshCatalog)) } : null;
    },
  };
  return { calls, uuid, log, proxyAwareFetch, buildCosyHeaders, models, upload, env, errors };
}

const dates = { Date: FakeDate, Math: fakeMath };

// ---- 1. encoding ----------------------------------------------------------

const encodingCases = [
  '', '61', '6162', '616263', '61626364', '6162636465', '616263646566',
  Buffer.from('{"a":1}').toString('hex'), Buffer.from('héllo 世界 😀 \n\t"\\').toString('hex'),
  Buffer.from(Array.from({ length: 256 }, (_, i) => i)).toString('hex'),
  Buffer.from(Array.from({ length: 1000 }, (_, i) => (i * 37) % 251)).toString('hex'),
  'fffefd', '000000', 'fbef', 'ffff', 'fb',
].map(hex => ({ hex, encoded: qoderEncodeBody(Buffer.from(hex, 'hex')) }));

// ---- 2. usage and coalescing ----------------------------------------------

const usageCases = [
  null, 5, 'x', [], {}, { prompt_tokens: 1 }, { completion_tokens: 2 }, { input_tokens: 3, output_tokens: 4 },
  { prompt_tokens: 10, completion_tokens: 5, total_tokens: 99 }, { prompt_tokens: 10, completion_tokens: 5, total_tokens: null },
  { prompt_tokens: 10, completion_tokens: 5, total_tokens: 'abc' }, { prompt_tokens: '7', completion_tokens: '8' },
  { prompt_tokens: null, input_tokens: 4, completion_tokens: 1 }, { prompt_tokens: true, completion_tokens: false },
  { prompt_tokens: 10, completion_tokens: 5, prompt_tokens_details: { cached_tokens: 3 } },
  { prompt_tokens: 10, completion_tokens: 5, cached_tokens: 4 }, { prompt_tokens: 10, completion_tokens: 5, prompt_cache_hit_tokens: 2 },
  { prompt_tokens: 10, completion_tokens: 5, cache_read_input_tokens: 6 }, { prompt_tokens: 10, completion_tokens: 5, cached_tokens: 0 },
  { prompt_tokens: 10, completion_tokens: 5, cached_tokens: 'nope' }, { prompt_tokens: 10, completion_tokens: 5, cached_tokens: null },
  { prompt_tokens: 10, completion_tokens: 5, prompt_tokens_details: { cached_tokens: 3, audio_tokens: 1 }, cached_tokens: 9 },
  { prompt_tokens: 10, completion_tokens: 5, prompt_tokens_details: { audio_tokens: 1 }, cache_creation_input_tokens: 7 },
  { prompt_tokens: 10, completion_tokens: 5, prompt_tokens_details: { cache_creation_tokens: 2 }, cache_creation_input_tokens: 7 },
  { prompt_tokens: 10, completion_tokens: 5, prompt_tokens_details: [1] }, { prompt_tokens: 10, completion_tokens: 5, prompt_tokens_details: 'x' },
  { prompt_tokens: 10, completion_tokens: 5, completion_tokens_details: { reasoning_tokens: 3 } },
  { prompt_tokens: 10, completion_tokens: 5, reasoning_tokens: 8, completion_tokens_details: { reasoning_tokens: 3 } },
  { prompt_tokens: 10, completion_tokens: 5, completion_tokens_details: [] }, { prompt_tokens: 1e21, completion_tokens: 1.5e-7 },
  { prompt_tokens: 1.5, completion_tokens: 2.25, total_tokens: 3.75 }, { prompt_tokens: -3, completion_tokens: 0 },
  { total_tokens: 12 }, { prompt_tokens: 1, completion_tokens: 1, extra: 'dropped' },
].map(usage => ({ usage, canonical: sse.canonicalizeQoderUsage(usage) }));

// ---- 3. first-frame errors and billing --------------------------------------

function loadExecutor(w, { env = {}, extraDeps = {} } = {}) {
  const console_ = { log() {}, warn() {}, error: (...args) => w.calls.warn.push(`console.error ${args.join(' ')}`) };
  return evaluate('open-sse/executors/qoder.js', {
    qoderEncodeBody, buildCosyHeaders: w.buildCosyHeaders, uuidv4: w.uuid, createHash: crypto.createHash,
    BaseExecutor: class { constructor(id, config) { this.id = id; this.config = config; } },
    PROVIDERS: { qoder: {} }, proxyAwareFetch: w.proxyAwareFetch, SSE_DONE,
    FETCH_CONNECT_TIMEOUT_MS: 30000, HTTP_STATUS,
    QODER_CHAT_SIG_PATH: constants.QODER_CHAT_SIG_PATH, QODER_CONTEXT_TIER_ENV: constants.QODER_CONTEXT_TIER_ENV,
    qoderInferenceBase: constants.qoderInferenceBase,
    getQoderModelConfig: w.models.getQoderModelConfig, resolveQoderModels: w.models.resolveQoderModels,
    isQoderPat: w.models.isQoderPat, resolveQoderCredentials: w.models.resolveQoderCredentials,
    OPENAI_BLOCK: blocks.OPENAI_BLOCK, CLAUDE_BLOCK: blocks.CLAUDE_BLOCK, encodeDataUri,
    createQoderSseCoalescer: sse.createQoderSseCoalescer,
    rewriteQoderMessageAttachments: extraDeps.rewriteQoderMessageAttachments,
    resolveQoderContextTier: tier.resolveQoderContextTier, applyQoderContextTier: tier.applyQoderContextTier,
    ...extraDeps.rest,
  }, ['QoderExecutor', '__test__'], { ...dates, env, console: console_ });
}

function loadAttachments(w) {
  return evaluate('open-sse/shared/qoder/attachments.js', {
    createHash: crypto.createHash, uuidv4: w.uuid, proxyAwareFetch: w.proxyAwareFetch, parseDataUri,
    OPENAI_BLOCK: blocks.OPENAI_BLOCK, CLAUDE_BLOCK: blocks.CLAUDE_BLOCK, MAX_IMAGE_BYTES, buildCosyHeaders: w.buildCosyHeaders,
    QODER_IMAGE_UPLOAD_SIG_PATH: constants.QODER_IMAGE_UPLOAD_SIG_PATH,
    QODER_INLINE_FALLBACK_MAX_BYTES: constants.QODER_INLINE_FALLBACK_MAX_BYTES,
    QODER_MAX_PAYLOAD_BYTES: constants.QODER_MAX_PAYLOAD_BYTES, qoderInferenceBase: constants.qoderInferenceBase,
  }, ['rewriteQoderMessageAttachments', 'buildMultipartFile', 'defaultUploadImage', '__test__'], dates);
}

// A real executor with the real attachments module and a scripted catalog.
function realExecutor(w) {
  const attachments = loadAttachments(w);
  const executor = loadExecutor(w, { extraDeps: { rewriteQoderMessageAttachments: attachments.rewriteQoderMessageAttachments } });
  return { executor, attachments };
}

const frame = (statusCodeValue, body) => `data: ${JSON.stringify({ statusCodeValue, body })}\n\n`;
const chunk = (delta, extra = {}) => JSON.stringify({ id: 'chatcmpl-1', created: 1700000001, model: 'upstream-model', choices: [{ index: 0, delta, ...extra }] });
const usageChunk = usage => JSON.stringify({ id: 'chatcmpl-1', created: 1700000001, model: 'upstream-model', choices: [], usage });
const dup = '{"code":"103","message":"Duplicate request"}';

async function readAll(response) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let text = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) return text + decoder.decode();
    text += decoder.decode(value, { stream: true });
  }
}

async function wrap(chunks, { keepOpen = false, model = 'qoder/auto', status = 200, ok = true, fail = null } = {}) {
  const w = world();
  const { executor } = realExecutor(w);
  let cancelled = 0;
  const pieces = chunks.map(piece => (typeof piece === 'string' ? new TextEncoder().encode(piece) : Buffer.from(piece.hex, 'hex')));
  const body = new ReadableStream({
    // pull, so a failure arrives after the chunks before it have been read
    pull(controller) {
      if (pieces.length) return controller.enqueue(pieces.shift());
      if (fail) return controller.error(new Error(fail));
      if (!keepOpen) controller.close();
      return new Promise(() => {}); // an open socket that never speaks again
    },
    cancel() { cancelled++; },
  });
  const response = new Response(body, { status: ok ? status : 500 });
  let wrapped;
  try {
    wrapped = await executor.__test__.wrapQoderSSE(response, model);
  } catch (error) {
    // The peek reads outside any guard, so a socket failure before the first line rejects.
    return { rejected: error.message, cancelled, console: w.calls.warn };
  }
  const text = await readAll(wrapped);
  return { status: wrapped.status, contentType: wrapped.headers.get('content-type'), body: text, cancelled, console: w.calls.warn };
}

const sseScenarios = {
  'plain text then done': [frame(200, chunk({ role: 'assistant', content: 'he' })) + frame(200, chunk({ content: 'llo' })) + 'data: [DONE]\n\n'],
  'inner done frame': [frame(200, chunk({ content: 'hi' })) + frame(200, '[DONE]')],
  'finish on delta then usage': [frame(200, chunk({ content: 'hi' })) + frame(200, chunk({ finish_reason: 'stop' })) + frame(200, usageChunk({ prompt_tokens: 4, completion_tokens: 2, cached_tokens: 1 }))],
  'finish on choice then usage': [frame(200, chunk({ content: 'hi' })) + frame(200, chunk({}, { finish_reason: 'length' })) + frame(200, usageChunk({ input_tokens: 4, output_tokens: 2 }))],
  'finish with content then usage': [frame(200, chunk({ content: 'hi' }, { finish_reason: 'stop' })) + frame(200, usageChunk({ prompt_tokens: 4, completion_tokens: 2, total_tokens: 6 }))],
  'finish with content and no usage then done': [frame(200, chunk({ content: 'hi' }, { finish_reason: 'stop' })) + 'data: [DONE]\n\n'],
  'empty finish and no usage then done': [frame(200, chunk({ content: 'hi' })) + frame(200, chunk({}, { finish_reason: 'stop' })) + 'data: [DONE]\n\n'],
  'usage and no finish then done': [frame(200, chunk({ content: 'hi' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 1 })) + 'data: [DONE]\n\n'],
  'usage with no finish then eof': [frame(200, chunk({ content: 'hi' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 1 }))],
  'usage ahead of a finish that carries content': [frame(200, chunk({ content: 'hi' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 1 })) + frame(200, chunk({ content: 'bye' }, { finish_reason: 'stop' })) + frame(200, chunk({ content: 'late' }))],
  'usage ahead of finish': [frame(200, chunk({ content: 'hi' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 1 })) + frame(200, chunk({ finish_reason: 'stop' }))],
  'finish in top level': [frame(200, chunk({ content: 'hi' })) + frame(200, JSON.stringify({ finish_reason: 'stop' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 1 }))],
  'tool calls then finish then usage': [frame(200, chunk({ tool_calls: [{ index: 0, id: 'c1', function: { name: 'f', arguments: '{}' } }] })) + frame(200, chunk({}, { finish_reason: 'tool_calls' })) + frame(200, usageChunk({ prompt_tokens: 9, completion_tokens: 3 }))],
  'reasoning content': [frame(200, chunk({ reasoning_content: 'think' })) + frame(200, chunk({ content: 'x' })) + 'data: [DONE]\n\n'],
  'role only delta is valuable': [frame(200, chunk({ role: 'assistant' })) + frame(200, chunk({ content: '' }, { finish_reason: 'stop' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 0 }))],
  'object body': [frame(200, JSON.parse(chunk({ content: 'obj' }))) + 'data: [DONE]\n\n'],
  'non json inner is raw text': [frame(200, 'plain text') + 'data: [DONE]\n\n'],
  'inner with newlines is flattened': [frame(200, 'a\r\nb\nc') + 'data: [DONE]\n\n'],
  'inner scalar json': [frame(200, '5') + frame(200, 'null') + frame(200, '"s"') + frame(200, '[1]') + 'data: [DONE]\n\n'],
  'empty inner is skipped': [frame(200, '') + frame(200, chunk({ content: 'x' })) + 'data: [DONE]\n\n'],
  'missing body is skipped': [`data: {"statusCodeValue":200}\n\n` + frame(200, chunk({ content: 'x' })) + 'data: [DONE]\n\n'],
  'numeric string status is ok': [`data: {"statusCodeValue":"200","body":${JSON.stringify(chunk({ content: 'x' }))}}\n\n` + 'data: [DONE]\n\n'],
  'zero status counts as ok after the first frame': [frame(200, chunk({ content: 'a' })) + frame(0, chunk({ content: 'b' })) + 'data: [DONE]\n\n'],
  'missing status counts as ok': [`data: {"body":${JSON.stringify(chunk({ content: 'a' }))}}\n\n` + 'data: [DONE]\n\n'],
  'comments and events are ignored': [': keepalive\n\nevent: message\n' + frame(200, chunk({ content: 'x' })) + ': again\n\ndata: [DONE]\n\n'],
  'crlf lines': [frame(200, chunk({ content: 'x' })).replace(/\n/g, '\r\n') + 'data: [DONE]\r\n\r\n'],
  'data without a space': [`data:{"statusCodeValue":200,"body":${JSON.stringify(chunk({ content: 'x' }))}}\n\ndata:[DONE]\n\n`],
  'garbage line is dropped': [frame(200, chunk({ content: 'a' })) + 'data: {not json\n\n' + frame(200, chunk({ content: 'b' })) + 'data: [DONE]\n\n'],
  'null envelope ends the stream': [frame(200, chunk({ content: 'a' })) + 'data: null\n\n' + frame(200, chunk({ content: 'b' }))],
  'scalar envelope is ignored': [frame(200, chunk({ content: 'a' })) + 'data: 5\n\n' + frame(200, chunk({ content: 'b' })) + 'data: [DONE]\n\n'],
  'eof without done': [frame(200, chunk({ content: 'a' }))],
  'eof without newline': [frame(200, chunk({ content: 'a' })).trimEnd()],
  'eof without newline on the first frame': ['data: [DONE]'],
  'done first': ['data: [DONE]\n\n'],
  'empty body': [''],
  'only comments': [': one\n\n: two\n\n'],
  'fragmented frames': [frame(200, chunk({ content: 'ab' })).slice(0, 17), frame(200, chunk({ content: 'ab' })).slice(17, 40), frame(200, chunk({ content: 'ab' })).slice(40) + 'data: [DONE]\n\n'],
  'multibyte split across chunks': [{ hex: Buffer.from(frame(200, chunk({ content: 'héllo 世界' }))).subarray(0, 60).toString('hex') }, { hex: Buffer.from(frame(200, chunk({ content: 'héllo 世界' }))).subarray(60).toString('hex') }, 'data: [DONE]\n\n'],
  'a frame after done is dropped': [frame(200, chunk({ content: 'a' })) + 'data: [DONE]\n\n' + frame(200, chunk({ content: 'late' }))],
  'a frame after the usage terminal is dropped': [frame(200, chunk({ content: 'a' }, { finish_reason: 'stop' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 1 })) + frame(200, chunk({ content: 'late' }))],
  // errors on the first frame
  'first frame duplicate 403': [frame(403, dup)],
  'first frame duplicate fragmented': [frame(403, dup).slice(0, 35), frame(403, dup).slice(35)],
  'first frame after heartbeat': [': keepalive\r\n\r\n', frame(403, dup)],
  'first frame error without newline': [frame(403, dup).trimEnd()],
  'first frame object body': [frame(403, JSON.parse(dup))],
  'first frame billing 110': [frame(403, '{"code":110,"message":"daily"}')],
  'first frame billing string 112': [frame(400, '{"code":"112","message":"quota"}')],
  'first frame billing 10605': [frame(429, '{"code":"10605"}')],
  'first frame billing pricingUrl': [frame(500, 'Upgrade at PricingURL now')],
  'first frame billing regex shape': [frame(500, 'x {"code" : "112" } y')],
  'first frame code 103 is not billing': [frame(500, '{"code":"103"}')],
  'first frame status 401': [frame(401, 'upstream failure')],
  'first frame status 429': [frame(429, 'slow down')],
  'first frame status 503': [frame(503, '')],
  'first frame status 302': [frame(302, 'moved')],
  'first frame status 600': [frame(600, 'invalid')],
  'first frame status 0': [frame(0, 'zero')],
  'first frame status null': ['data: {"statusCodeValue":null,"body":"nulled"}\n\n'],
  'first frame status string': ['data: {"statusCodeValue":"429","body":"slow"}\n\n'],
  'first frame status empty string': ['data: {"statusCodeValue":"","body":"empty"}\n\n'],
  'first frame status fraction': [frame(403.5, 'frac')],
  'first frame status absent': [`data: {"body":"x"}\n\n` + 'data: [DONE]\n\n'],
  'first frame status object': ['data: {"statusCodeValue":{},"body":"x"}\n\ndata: [DONE]\n\n'],
  'first frame status array': ['data: {"statusCodeValue":[],"body":"x"}\n\n'],
  'first frame status array of one': ['data: {"statusCodeValue":[404],"body":"x"}\n\n'],
  'first frame status true': ['data: {"statusCodeValue":true,"body":"x"}\n\n'],
  'first frame status hex string': ['data: {"statusCodeValue":"0x1F4","body":"x"}\n\n'],
  'first frame no body': ['data: {"statusCodeValue":500}\n\n'],
  'first frame null body': ['data: {"statusCodeValue":500,"body":null}\n\n'],
  'first frame number body': ['data: {"statusCodeValue":500,"body":42}\n\n'],
  'first frame unparseable stays a stream': ['data: {oops\n\n' + frame(200, chunk({ content: 'x' })) + 'data: [DONE]\n\n'],
  'first frame error with emoji': [frame(500, 'é世😀 failed')],
  'first frame error long': [frame(500, 'x'.repeat(1000))],
  'first frame null envelope': ['data: null\n\n' + frame(200, chunk({ content: 'x' })) + 'data: [DONE]\n\n'],
  // errors after the first frame
  'later billing error': [frame(200, chunk({ content: 'a' })) + frame(403, '{"code":"112","message":"quota"}') + frame(200, chunk({ content: 'late' }))],
  'later billing pricingUrl': [frame(200, chunk({ content: 'a' })) + frame(403, 'pricingUrl:x')],
  'later error': [frame(200, chunk({ content: 'a' })) + frame(500, 'boom') + frame(200, chunk({ content: 'late' }))],
  'later error long': [frame(200, chunk({ content: 'a' })) + frame(500, 'y'.repeat(300))],
  'later error cut inside a surrogate pair': [frame(200, chunk({ content: 'a' })) + frame(500, 'z'.repeat(199) + '😀tail')],
  'later error no body': [frame(200, chunk({ content: 'a' })) + 'data: {"statusCodeValue":502}\n\n'],
  'later error object body': [frame(200, chunk({ content: 'a' })) + frame(502, { error: 'obj' })],
  'later error after pending usage': [frame(200, chunk({ content: 'a' })) + frame(200, usageChunk({ prompt_tokens: 1, completion_tokens: 1 })) + frame(500, 'late failure')],
  // keepalive
  'keepalive socket is cancelled at done': [frame(200, chunk({ content: 'a' })) + 'data: [DONE]\n\n'],
};
const keepOpenScenarios = new Set(['keepalive socket is cancelled at done', 'first frame duplicate 403', 'later billing error', 'later error',
  'a frame after done is dropped', 'a frame after the usage terminal is dropped', 'finish on delta then usage', 'finish with content then usage', 'inner done frame']);
const sseResults = [];
for (const [name, chunks] of Object.entries(sseScenarios)) {
  // The socket stays open after the terminal frame, as the agent's keepalive does; the
  // scenarios that never reach a terminal frame would wait on it forever.
  const keepOpen = keepOpenScenarios.has(name);
  const run = await Promise.race([wrap(chunks, { keepOpen }), new Promise((_, reject) => setTimeout(() => reject(new Error(`scenario hangs: ${name}`)), 2000).unref())]);
  sseResults.push({ name, chunks, keepOpen, ...run });
}
// A read error from the socket and an upstream that is not ok.
sseResults.push({ name: 'socket error after content', chunks: [frame(200, chunk({ content: 'a' }))], keepOpen: false, fail: 'reset', ...(await wrap([frame(200, chunk({ content: 'a' }))], { fail: 'reset' })) });
sseResults.push({ name: 'socket error before any frame', chunks: [], keepOpen: false, fail: 'reset', ...(await wrap([], { fail: 'reset' })) });
// The upstream names no id, created or model, so the terminal chunk invents them.
for (const [name, model, chunks] of [
  ['model name is carried', 'qoder/kmodel_latest', [frame(200, JSON.stringify({ choices: [{ delta: { content: 'a' }, finish_reason: 'stop' }] })) + frame(200, JSON.stringify({ choices: [], usage: { prompt_tokens: 1, completion_tokens: 1 } }))]],
  ['terminal without upstream ids', 'qoder/auto', [frame(200, JSON.stringify({ choices: [{ delta: { content: 'a' } }] })) + frame(200, JSON.stringify({ choices: [{ delta: {}, finish_reason: 'stop' }], usage: { prompt_tokens: 1, completion_tokens: 1 } }))]],
]) sseResults.push({ name, chunks, keepOpen: false, model, ...(await wrap(chunks, { model })) });

const billingInputs = [
  '', null, undefined, 5, '{"code":"110"}', '{"code":110}', '{"code":"112"}', '{"code":112}', '{"code":"10605"}', '{"code":10605}',
  '{"code":"500"}', '{"message":"pricingUrl"}', '{"message":"PRICINGURL"}', 'PricingUrl', 'not json', '{"code":"112" ,"x":1}',
  'prefix {"code":"112"} suffix', 'prefix {"code" : "10605"} suffix', '{"code":"110x"}', '[]', 'null', '{"code":null}', '{"code":[110]}',
  '{"code":" 110 "}', '"112"', '{"code":"  112"}',
].map(input => ({ input: input === undefined ? { undefined: true } : input, billing: loadExecutor(world()).__test__.isBillingBlock(input) }));

// ---- 4. envelope -----------------------------------------------------------

const baseConfig = { key: 'auto', display_name: 'Auto', is_reasoning: false, max_input_tokens: 180000, max_output_tokens: 8192, source: 'system', is_vl: true, enable: true };
const tierConfig = { ...baseConfig, key: 'qmodel_38max', max_input_tokens: 180000, max_output_tokens: 64000, is_reasoning: true,
  context_config: [{ name: '200K', tokenCount: 200000, isDefault: true }, { name: '400K', token_count: 400000 }, { name: '1M', tokenCount: '1M' }] };
const credentials = { accessToken: 'dt-synthetic', displayName: 'Synthetic', email: 'synthetic@example.invalid', providerSpecificData: { userId: 'user-1', machineId: 'machine-1' } };
const png = Buffer.from('89504e470d0a1a0a0000000d49484452', 'hex').toString('base64');
const user = content => ({ role: 'user', content });
const longText = 'lorem ipsum dolor sit amet '.repeat(30);

const envelopeScenarios = [
  ['one user turn', { model: 'qoder/auto', body: { messages: [user('hello')] } }],
  ['bare model name', { model: 'auto', body: { messages: [user('hello')] } }],
  ['prefix only stripped at the start', { model: 'x/qoder/auto', body: { messages: [user('hello')] }, catalog: { 'x/qoder/auto': { ...baseConfig, key: 'x/qoder/auto' } } }],
  ['system hoisted and joined', { model: 'qoder/auto', body: { messages: [{ role: 'system', content: 'be brief' }, user('hi'), { role: 'system', content: [{ type: 'text', text: 'and kind' }, { text: 'loose' }, { type: 'image_url' }] }, { role: 'assistant', content: 'ok' }, user('again')] } }],
  ['empty system text is dropped', { model: 'qoder/auto', body: { messages: [{ role: 'system', content: '' }, { role: 'system', content: null }, { role: 'system', content: 7 }, { role: 'system', content: { a: 1 } }, { role: 'system', content: true }, user('hi')] } }],
  ['developer role stays', { model: 'qoder/auto', body: { messages: [{ role: 'developer', content: 'dev' }, user('hi')] } }],
  ['text parts are joined', { model: 'qoder/auto', body: { messages: [user([{ type: 'text', text: 'a' }, { type: 'text', text: 'b' }, { type: 'text', text: '' }, { text: 'c' }, null, 5, { type: 'other' }])] } }],
  ['content is a number or object', { model: 'qoder/auto', body: { messages: [user(42), { role: 'assistant', content: { k: 'v' } }, { role: 'user', content: null }, { role: 'user' }, { role: 'user', content: false }] } }],
  ['numbers as content are written as javascript writes them', { model: 'qoder/auto', rawBody: '{"messages":[{"role":"user","content":1e-7},{"role":"user","content":0.000001},{"role":"user","content":1e21},{"role":"user","content":123456789012345680000},{"role":"user","content":-0.5},{"role":"user","content":1.5e300},{"role":"user","content":100},{"role":"user","content":0.1},{"role":"user","content":true}]}' }],
  ['non-object messages', { model: 'qoder/auto', body: { messages: [null, 5, 'str', [], [1, 2], user('hi')] } }],
  ['no messages', { model: 'qoder/auto', body: {} }],
  ['messages not an array', { model: 'qoder/auto', body: { messages: 'nope' } }],
  ['image url string and object', { model: 'qoder/auto', body: { messages: [user([{ type: 'text', text: 'look' }, { type: 'image_url', image_url: 'https://img.invalid/a.png' }, { type: 'image_url', image_url: { url: 'https://img.invalid/b.png', detail: 'high' } }, { type: 'text', text: 'after' }])] } }],
  ['image url without a url falls to text', { model: 'qoder/auto', body: { messages: [user([{ type: 'image_url', image_url: {}, text: 'fallback' }, { type: 'image_url', image_url: { url: '' } }])] } }],
  ['claude image url', { model: 'qoder/auto', body: { messages: [user([{ type: 'text', text: 'x' }, { type: 'image', source: { type: 'url', url: 'https://img.invalid/c.png' } }])] } }],
  ['claude image base64 is uploaded', { model: 'qoder/auto', body: { messages: [user([{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: png } }, { type: 'text', text: 'what is this' }])] }, upload: { url: 'https://oss.invalid/up1.png' } }],
  ['claude image without media type', { model: 'qoder/auto', body: { messages: [user([{ type: 'image', source: { type: 'base64', data: png } }])] }, upload: { url: 'https://oss.invalid/up1.png' } }],
  ['data uri image upload fails and is kept inline', { model: 'qoder/auto', body: { messages: [user([{ type: 'image_url', image_url: { url: `data:image/png;base64,${png}` } }])] }, upload: { throws: 'down' } }],
  ['data uri image is uploaded', { model: 'qoder/auto', body: { messages: [user([{ type: 'text', text: 'pic' }, { type: 'image_url', image_url: { url: `data:image/png;base64,${png}` } }])] }, upload: { url: 'https://oss.invalid/up2.png' } }],
  ['same image twice uploads once', { model: 'qoder/auto', body: { messages: [user([{ type: 'image_url', image_url: { url: `data:image/png;base64,${png}` } }]), user([{ type: 'image_url', image_url: `data:image/png;base64,${png}` }])] }, upload: { url: 'https://oss.invalid/once.png' } }],
  ['file and document blocks become stubs', { model: 'qoder/auto', body: { messages: [user([{ type: 'file', file: { filename: 'a.pdf', file_data: 'data:application/pdf;base64,QUJD' } }, { type: 'document', title: 'Doc', source: { type: 'base64', media_type: 'application/pdf', data: 'QUJD' } }, { type: 'text', text: 'read these' }])] } }],
  ['file image is uploaded', { model: 'qoder/auto', body: { messages: [user([{ type: 'file', file: { name: 'p.png', file_data: `data:image/png;base64,${png}` } }])] }, upload: { url: 'https://oss.invalid/file.png' } }],
  ['ollama images sidecar', { model: 'qoder/auto', body: { messages: [{ role: 'user', content: 'see', images: ['https://img.invalid/d.png'] }, { role: 'user', content: [{ type: 'text', text: 'x' }], images: ['https://img.invalid/e.png'] }] } }],
  ['max tokens default is the model maximum', { model: 'qoder/auto', body: { messages: [user('hi')] } }],
  ['max tokens clamps down', { model: 'qoder/auto', body: { messages: [user('hi')], max_tokens: 100 } }],
  ['max tokens above the model stays at the model', { model: 'qoder/auto', body: { messages: [user('hi')], max_tokens: 99999 } }],
  ['max completion tokens clamps further', { model: 'qoder/auto', body: { messages: [user('hi')], max_tokens: 500, max_completion_tokens: 200 } }],
  ['max tokens as a string is ignored', { model: 'qoder/auto', body: { messages: [user('hi')], max_tokens: '100', max_completion_tokens: -4 } }],
  ['model without a maximum', { model: 'qoder/bare', body: { messages: [user('hi')] }, catalog: { bare: { key: 'bare' } } }],
  ['model maximum as a string', { model: 'qoder/str', body: { messages: [user('hi')] }, catalog: { str: { key: 'str', max_output_tokens: '4096', is_reasoning: 1 } } }],
  ['model maximum fractional', { model: 'qoder/frac', body: { messages: [user('hi')] }, catalog: { frac: { key: 'frac', max_output_tokens: 100.5 } } }],
  ['config without a key gets one', { model: 'qoder/nokey', body: { messages: [user('hi')] }, catalog: { nokey: { display_name: 'No key', source: 'user' } } }],
  ['config source is user', { model: 'qoder/mine', body: { messages: [user('hi')] }, catalog: { mine: { key: 'mine', source: 'user', extra: { nested: [1, { a: 2 }] } } } }],
  ['tools array', { model: 'qoder/auto', body: { messages: [user('hi')], tools: [{ type: 'function', function: { name: 'f', description: 'd', parameters: { type: 'object', properties: { b: { type: 'string' }, a: { type: 'number' }, 2: { type: 'x' }, 1: { type: 'y' } } } } }] } }],
  ['tools empty array', { model: 'qoder/auto', body: { messages: [user('hi')], tools: [] } }],
  ['tools not an array', { model: 'qoder/auto', body: { messages: [user('hi')], tools: { not: 'array' } } }],
  ['tool turns pass through', { model: 'qoder/auto', body: { messages: [user('call it'), { role: 'assistant', content: null, tool_calls: [{ id: 'c1', type: 'function', function: { name: 'f', arguments: '{"x":1}' } }] }, { role: 'tool', tool_call_id: 'c1', content: 'done' }, user('thanks')] } }],
  ['unknown fields survive on messages', { model: 'qoder/auto', body: { messages: [{ role: 'user', content: 'hi', name: 'bob', cache_control: { type: 'ephemeral' }, x_extra: [1, 2] }], temperature: 0.3, stream: false } }],
  ['key order and numbers are preserved', { model: 'qoder/auto', body: { messages: [{ content: [{ text: 'a', type: 'text' }], role: 'user', z: 1.0, y: 1e21, x: 0.000001, w: 1e-7, v: -0, u: 123456789012345680000 }] } }],
  ['numeric keys are ordered', { model: 'qoder/auto', rawBody: '{"messages":[{"role":"user","content":"hi","b":1,"10":"x","2":"y","a":2,"4294967295":"big","4294967294":"last","01":"lead","-1":"neg","1.5":"frac"}]}' }],
  ['duplicate keys keep their first position', { model: 'qoder/auto', rawBody: '{"messages":[{"role":"user","content":"a","z":1,"content":"b","role":"user"}],"tools":[{"x":1,"y":2,"x":3}]}' }],
  ['tools order feeds the record id', { model: 'qoder/auto', rawBody: '{"messages":[{"role":"user","content":"hi"}],"tools":[{"b":1,"a":2}]}' }],
  ['unicode and control characters', { model: 'qoder/auto', body: { messages: [user('é世界😀  \u0001\u007f"\\/<>&\t\r\n')] } }],
  ['long last user text is cut for the name', { model: 'qoder/auto', body: { messages: [user(longText)] } }],
  ['name cut inside a surrogate pair', { model: 'qoder/auto', body: { messages: [user('a'.repeat(29) + '😀 tail')] } }],
  ['name of exactly thirty', { model: 'qoder/auto', body: { messages: [user('b'.repeat(30))] } }],
  ['name of thirty one', { model: 'qoder/auto', body: { messages: [user('b'.repeat(31))] } }],
  ['cjk name', { model: 'qoder/auto', body: { messages: [user('世'.repeat(40))] } }],
  ['last user skips non user and nonstring content', { model: 'qoder/auto', body: { messages: [user('first'), { role: 'assistant', content: 'a' }, { role: 'user', content: 5 }] } }],
  ['last user is an array', { model: 'qoder/auto', body: { messages: [user('first'), user([{ type: 'image_url', image_url: 'https://img.invalid/x.png' }, { type: 'text', text: 'second' }])] } }],
  ['no user turn', { model: 'qoder/auto', body: { messages: [{ role: 'assistant', content: 'a' }] } }],
  ['record id depends on the image', { model: 'qoder/auto', body: { messages: [user([{ type: 'image_url', image_url: 'https://img.invalid/other.png' }])] } }],
  ['record id with tools and max tokens', { model: 'qoder/auto', body: { messages: [user('hi')], tools: [{ a: 1 }], max_tokens: 50 } }],
  ['user id is absent', { model: 'qoder/auto', body: { messages: [user('hi')] }, credentials: { ...credentials, providerSpecificData: { machineId: 'm' } } }],
  ['user id is numeric', { model: 'qoder/auto', body: { messages: [user('hi')] }, credentials: { ...credentials, providerSpecificData: { userId: 42 } } }],
  ['non string role cannot be hashed', { model: 'qoder/auto', body: { messages: [{ role: 5, content: 'hi' }] } }],
  ['tier: prompt fits the default', { model: 'qoder/qmodel_38max', body: { messages: [user('hi')] }, catalog: { qmodel_38max: tierConfig } }],
  ['tier: auto escalates to 400K', { model: 'qoder/qmodel_38max', body: { messages: [user('x'.repeat(900000))] }, catalog: { qmodel_38max: tierConfig } }],
  ['tier: auto escalates to the largest', { model: 'qoder/qmodel_38max', body: { messages: [user('x'.repeat(2000000))] }, catalog: { qmodel_38max: tierConfig } }],
  ['tier: cjk counts one token each', { model: 'qoder/qmodel_38max', body: { messages: [user('世'.repeat(190000))] }, catalog: { qmodel_38max: tierConfig } }],
  ['tier: under the headroom stays', { model: 'qoder/qmodel_38max', body: { messages: [user('x'.repeat(612000))] }, catalog: { qmodel_38max: tierConfig } }],
  ['tier: over the headroom escalates', { model: 'qoder/qmodel_38max', body: { messages: [user('x'.repeat(627000))] }, catalog: { qmodel_38max: tierConfig } }],
  ['tier: forced max', { model: 'qoder/qmodel_38max', body: { messages: [user('hi')] }, catalog: { qmodel_38max: tierConfig }, env: { QODER_CONTEXT_TIER: 'max' } }],
  ['tier: forced default', { model: 'qoder/qmodel_38max', body: { messages: [user('x'.repeat(2000000))] }, catalog: { qmodel_38max: tierConfig }, env: { QODER_CONTEXT_TIER: 'default' } }],
  ['tier: forced by name', { model: 'qoder/qmodel_38max', body: { messages: [user('hi')] }, catalog: { qmodel_38max: tierConfig }, env: { QODER_CONTEXT_TIER: '400k' } }],
  ['tier: forced by count', { model: 'qoder/qmodel_38max', body: { messages: [user('hi')] }, catalog: { qmodel_38max: tierConfig }, env: { QODER_CONTEXT_TIER: '1000000' } }],
  ['tier: unknown name falls to auto', { model: 'qoder/qmodel_38max', body: { messages: [user('hi')] }, catalog: { qmodel_38max: tierConfig }, env: { QODER_CONTEXT_TIER: '9T' } }],
  ['tier: upper case mode words', { model: 'qoder/qmodel_38max', body: { messages: [user('hi')] }, catalog: { qmodel_38max: tierConfig }, env: { QODER_CONTEXT_TIER: 'MAX' } }],
  ['tier: camel case config and duplicate counts', { model: 'qoder/camel', body: { messages: [user('x'.repeat(900000))] }, catalog: { camel: { key: 'camel', maxInputTokens: '180K', contextConfig: [{ label: 'a', maxInputTokens: 200000 }, { displayName: 'dup', context_length: '200K', default: true }, { id: 'big', contextLength: 800000 }, { name: 'bad', tokenCount: 'zzz' }, null, 'x'] } } }],
  ['tier: config without tiers', { model: 'qoder/auto', body: { messages: [user('x'.repeat(900000))] }, env: { QODER_CONTEXT_TIER: 'max' } }],
  ['tier: nothing bigger to escalate to', { model: 'qoder/small', body: { messages: [user('x'.repeat(900000))] }, catalog: { small: { key: 'small', max_input_tokens: 1000000, context_config: [{ name: '200K', tokenCount: 200000 }, { name: '1M', tokenCount: 1000000 }] } } }],
  ['tier: unnamed tiers get names', { model: 'qoder/unnamed', body: { messages: [user('hi')] }, catalog: { unnamed: { key: 'unnamed', context_config: [{ tokenCount: 1500000 }, { tokenCount: 250000 }, { tokenCount: 123456 }] } }, env: { QODER_CONTEXT_TIER: '1.5M' } }],
  ['tier: extra and model_config mirrored', { model: 'qoder/qmodel_38max', body: { messages: [user('x'.repeat(900000))] }, catalog: { qmodel_38max: Object.fromEntries(Object.entries(tierConfig).filter(([k]) => k !== 'max_input_tokens')) } }],
  ['missing config is an error', { model: 'qoder/ghost', body: { messages: [user('hi')] } }],
  ['missing config found on refresh', { model: 'qoder/ghost', body: { messages: [user('hi')] }, refresh: { ghost: { key: 'ghost', display_name: 'Ghost', max_output_tokens: 10 } } }],
  ['refresh config gets its key', { model: 'qoder/late', body: { messages: [user('hi')] }, refresh: { late: { display_name: 'Late' } } }],
];

async function buildEnvelope(scenario) {
  const w = world({ catalog: scenario.catalog ?? { auto: baseConfig }, refreshCatalog: scenario.refresh ?? null });
  const uploads = [];
  const uploadFn = scenario.upload ? async ({ buffer, mediaType }) => {
    uploads.push({ mediaType, bytes: buffer.length, sha256: crypto.createHash('sha256').update(buffer).digest('hex') });
    if (scenario.upload.throws) throw new Error(scenario.upload.throws);
    return scenario.upload.url;
  } : null;
  const executorWithEnv = loadExecutor(w, { env: scenario.env ?? {}, extraDeps: { rewriteQoderMessageAttachments: loadAttachments(w).rewriteQoderMessageAttachments } });
  const body = scenario.rawBody ? JSON.parse(scenario.rawBody) : scenario.body;
  const result = { name: scenario.name, scenario: { model: scenario.model, body: scenario.rawBody ?? JSON.stringify(scenario.body), credentials: scenario.credentials ?? credentials, catalog: scenario.catalog ?? { auto: baseConfig }, refresh: scenario.refresh ?? null, env: scenario.env ?? {}, upload: scenario.upload ?? null } };
  try {
    const built = await executorWithEnv.__test__.buildQoderRequestBody({ model: scenario.model, body: structuredClone(body), credentials: scenario.credentials ?? credentials, log: w.log, uploadFn });
    result.key = built.qoderKey;
    result.payload = JSON.stringify(built.payload);
  } catch (error) {
    result.error = error.message;
  }
  result.uploads = uploads;
  result.resolve = w.calls.resolve;
  result.warn = w.calls.warn;
  return result;
}
const envelopes = [];
for (const [name, scenario] of envelopeScenarios) envelopes.push(await buildEnvelope({ name, ...scenario }));

// ---- 5. attachments ---------------------------------------------------------

const big = (n, mime = 'image/png') => `data:${mime};base64,${Buffer.alloc(n, 7).toString('base64')}`;
const attachmentScenarios = [
  ['plain strings are untouched', [user('hi'), { role: 'assistant', content: 'yo' }], { url: 'u' }],
  ['http image is kept', [user([{ type: 'image_url', image_url: 'http://img.invalid/a.png' }, { type: 'image_url', image_url: { url: 'https://img.invalid/b.png' } }])], { url: 'u' }],
  ['image url without a url is dropped', [user([{ type: 'image_url', image_url: {} }, { type: 'image_url' }, { type: 'text', text: 'kept' }])], { url: 'u' }],
  ['all blocks dropped leaves an empty string', [user([{ type: 'image_url' }])], { url: 'u' }],
  ['unreadable data uri', [user([{ type: 'image_url', image_url: 'data:image/png,notbase64' }])], { url: 'u' }],
  ['non image data uri', [user([{ type: 'image_url', image_url: { url: 'data:application/pdf;base64,QUJDRA==' } }])], { url: 'u' }],
  ['image upload', [user([{ type: 'image_url', image_url: { url: `data:image/png;base64,${png}` } }])], { url: 'https://oss.invalid/i.png' }],
  ['image upload mime case', [user([{ type: 'image_url', image_url: `data:IMAGE/JPEG;base64,${png}` }])], { url: 'https://oss.invalid/i.jpg' }],
  ['base64 with whitespace', [user([{ type: 'image', source: { type: 'base64', media_type: 'image/webp', data: png.slice(0, 8) + '\n ' + png.slice(8) } }])], { url: 'https://oss.invalid/w.webp' }],
  ['upload failure keeps a small image', [user([{ type: 'image_url', image_url: `data:image/png;base64,${png}` }])], { throws: 'HTTP 500' }],
  ['upload failure stubs a large image', [user([{ type: 'image_url', image_url: big(600 * 1024) }])], { throws: 'HTTP 500' }],
  ['upload returns nothing keeps a small image', [user([{ type: 'image_url', image_url: `data:image/png;base64,${png}` }])], { url: '' }],
  ['upload returns nothing stubs a large image', [user([{ type: 'image_url', image_url: big(600 * 1024) }])], { url: null }],
  ['image at the inline fallback limit', [user([{ type: 'image_url', image_url: big(512 * 1024) }])], { throws: 'x' }],
  ['image one byte over the inline fallback limit', [user([{ type: 'image_url', image_url: big(512 * 1024 + 3) }])], { throws: 'x' }],
  ['image over the upload cap is stubbed without uploading', [user([{ type: 'image_url', image_url: big(10 * 1024 * 1024 + 3) }])], { url: 'u' }],
  ['image at the upload cap is uploaded', [user([{ type: 'image_url', image_url: big(10 * 1024 * 1024) }])], { url: 'https://oss.invalid/cap.png' }],
  ['empty base64 payload', [user([{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: ' \n ' } }])], { url: 'u' }],
  ['malformed base64 is uploaded as node decodes it', [user([{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AB*CD-_E=F' } }])], { url: 'https://oss.invalid/m.png' }],
  ['url safe base64', [user([{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: '-_-_-_8=' } }])], { url: 'https://oss.invalid/u.png' }],
  ['claude image url and unknown source', [user([{ type: 'image', source: { type: 'url', url: 'https://img.invalid/c.png' } }, { type: 'image', source: { type: 'file' } }, { type: 'image' }])], { url: 'u' }],
  ['openai image block type', [user([{ type: 'image', source: { type: 'base64', data: png } }])], { url: 'https://oss.invalid/o.png' }],
  ['file block image uploads', [user([{ type: 'file', file: { filename: 'a.png', file_data: `data:image/png;base64,${png}` } }])], { url: 'https://oss.invalid/f.png' }],
  ['file block image upload fails is a stub', [user([{ type: 'file', file: { filename: 'a.png', file_data: `data:image/png;base64,${png}` } }])], { throws: 'x' }],
  ['file block bare base64 with format', [user([{ type: 'file', file: { name: 'a.gif', file_data: png, format: 'image/gif' } }])], { url: 'https://oss.invalid/g.gif' }],
  ['file block pdf is a stub', [user([{ type: 'file', file: { filename: 'a.pdf', file_data: 'data:application/pdf;base64,QUJDRA==' } }])], { url: 'u' }],
  ['file block without data', [user([{ type: 'file', file: {} }, { type: 'file', file: { file_id: 'f' } }])], { url: 'u' }],
  ['file block without file falls through', [user([{ type: 'file' }])], { url: 'u' }],
  ['document pdf is a stub', [user([{ type: 'document', title: 'T', source: { type: 'base64', media_type: 'application/pdf', data: 'QUJDRA==' } }])], { url: 'u' }],
  ['document default name and mime', [user([{ type: 'document', source: { type: 'base64', data: 'QUJDRA==' } }])], { url: 'u' }],
  ['document image uploads', [user([{ type: 'document', source: { type: 'base64', media_type: 'image/png', data: png } }])], { url: 'https://oss.invalid/d.png' }],
  ['document with a url source falls through', [user([{ type: 'document', source: { type: 'url', url: 'https://doc.invalid' } }])], { url: 'u' }],
  ['large inline data uri in text is stripped', [user([{ type: 'text', text: 'before ' + big(600 * 1024, 'application/pdf') + ' after' }])], { url: 'u' }],
  ['small inline data uri in long text is kept', [user([{ type: 'text', text: 'x'.repeat(9000) + ' data:image/png;base64,QUJD ' }])], { url: 'u' }],
  ['text with data uri under the length gate', [user([{ type: 'text', text: 'data:image/png;base64,' + 'A'.repeat(100) }])], { url: 'u' }],
  ['string content with a large data uri', [user('see ' + big(600 * 1024, 'application/zip') + ' end')], { url: 'u' }],
  ['string content under the gate', [user('see data:image/png;base64,QUJD end')], { url: 'u' }],
  ['unparseable data uri in text is cut by length', [user('p ' + 'data:x;base64,' + 'A'.repeat(700 * 1024) + '\n z ' + 'tail ' + 'x'.repeat(9000))], { url: 'u' }],
  ['sidecar images fold into content', [{ role: 'user', content: 'see', images: ['a', 5] }, { role: 'user', content: [{ type: 'text', text: 't' }], images: [] }, { role: 'user', images: ['b'] }], { url: 'u' }],
  ['non array content is untouched', [{ role: 'user', content: 5 }, { role: 'user', content: null }, { role: 'user' }, { role: 'user', content: { a: 1 } }], { url: 'u' }],
  ['non object messages are skipped', [null, 5, 'x', user('hi')], { url: 'u' }],
  ['empty messages', [], { url: 'u' }],
  ['over budget strips leftover inline images', [user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'image_url', image_url: big(400 * 1024) }]), user([{ type: 'text', text: 'keep data:image/png;base64,QUJD me' }, { type: 'image_url', image_url: 'https://img.invalid/ok.png' }]), user('and data:image/png;base64,QUJDRA== string')], { throws: 'x' }],
  ['over budget text blocks are stripped', [user([{ type: 'text', text: 'a data:image/png;base64,QUJD b' }, { type: 'text', text: 'x'.repeat(6 * 1024 * 1024 + 10) }])], { url: 'u' }],
];
const attachmentResults = [];
for (const [name, messages, upload] of attachmentScenarios) {
  const w = world();
  const { rewriteQoderMessageAttachments } = loadAttachments(w);
  const uploads = [];
  const uploadFn = async ({ buffer, mediaType }) => {
    uploads.push({ mediaType, bytes: buffer.length, sha256: crypto.createHash('sha256').update(buffer).digest('hex') });
    if (upload.throws) throw new Error(upload.throws);
    return upload.url;
  };
  const input = structuredClone(messages);
  const stats = await rewriteQoderMessageAttachments(input, { credentials, log: w.log, uploadFn });
  attachmentResults.push({ name, messages, upload, uploads, stats, result: input, warn: w.calls.warn });
}

// The wire request of the default uploader, and the upload response readers.
const uploadWire = [];
for (const [name, credentialsIn, status, body] of [
  ['device token ok', credentials, 200, JSON.stringify({ result: { imageUrls: ['https://oss.invalid/1.png'] } })],
  ['job token goes to api2', { ...credentials, accessToken: 'jt-abc' }, 200, JSON.stringify({ imageUrl: 'https://oss.invalid/2.png' })],
  ['http failure', credentials, 413, 'too large'],
  ['http failure long body', credentials, 500, 'e'.repeat(400)],
  ['not json', credentials, 200, 'nope'],
  ['no url', credentials, 200, '{}'],
]) {
  const w = world({ fetches: [{ status, body }] });
  const { defaultUploadImage } = loadAttachments(w);
  let error = null;
  let url = null;
  try { url = await defaultUploadImage({ buffer: Buffer.from('PNGDATA'), mediaType: 'image/png', credentials: credentialsIn, proxyOptions: null, signal: null }); } catch (e) { error = e.message; }
  uploadWire.push({ name, credentials: credentialsIn, status, body, url, error, calls: w.calls.fetch, signed: w.calls.signed });
}
const uploadResponses = [];
{
  const { __test__ } = loadAttachments(world());
  for (const json of [
    null, 'x', [], {}, { result: { imageUrls: ['a'] } }, { result: { image_urls: ['b'] } }, { imageUrls: ['c'] }, { image_urls: ['d'] }, { imageUrls: [''], imageUrl: 'e' },
    { imageUrls: [5], url: 'f' }, { result: { url: 'g' } }, { result: { ossUrl: 'h' }, url: 'x' }, { result: {}, oss_url: 'i' }, { originalUrl: 'j' }, { originUrl: 'k' },
    { link: 'l' }, { image: 'm' }, { imageUrl: 5, image_url: 'n' }, { result: 'str', url: 'o' }, { result: [], url: 'p' }, { body: '{"url":"q"}' }, { body: 'notjson' }, { body: '{"body":"{\\"url\\":\\"r\\"}"}' },
    { result: { imageUrl: '' }, imageUrl: 's' },
  ]) uploadResponses.push({ json, url: __test__.extractUrlFromUploadResponse(json) });
}

// ---- 6. PAT exchange and the catalog ----------------------------------------

function loadModels(w, { Datex = FakeDate } = {}) {
  return evaluate('open-sse/services/qoderModels.js', {
    createHash: crypto.createHash, proxyAwareFetch: w.proxyAwareFetch, buildCosyHeaders: w.buildCosyHeaders,
    QODER_MODEL_LIST_URL: constants.QODER_MODEL_LIST_URL, QODER_CHAT_BASE_ALT: constants.QODER_CHAT_BASE_ALT,
    QODER_JOB_TOKEN_EXCHANGE_URL: constants.QODER_JOB_TOKEN_EXCHANGE_URL, QODER_USERINFO_URL: constants.QODER_USERINFO_URL,
    QODER_IDE_VERSION: constants.QODER_IDE_VERSION, QODER_CLIENT_TYPE: constants.QODER_CLIENT_TYPE,
  }, ['isQoderPat', 'resolveQoderCredentials', 'resolveQoderModels', 'getQoderModelConfig'], { Date: Datex, Math: fakeMath, setTimeout: () => 0 });
}

const tokenOk = (extra = {}) => ({ status: 200, body: JSON.stringify({ token: 'jt-job1', refresh_token: 'rt', ...extra }) });
const userOk = { status: 200, body: JSON.stringify({ id: 'user-from-info' }) };
const pat = 'pt-synthetic';
const patCredentials = { apiKey: pat, displayName: 'P', email: 'p@example.invalid', providerSpecificData: { userId: 'stored-user', machineId: 'machine-1' } };

class Clock extends FakeDate {
  static offset = 0;
  constructor(...args) { super(...(args.length ? args : [NOW + Clock.offset])); }
  static now() { return NOW + Clock.offset; }
}

const patScenarios = [
  ['exchange then userinfo', [tokenOk(), userOk], [{}]],
  ['userinfo failure falls back to the stored user', [tokenOk(), { status: 500, body: 'no' }], [{}]],
  ['userinfo network error', [tokenOk(), { network: 'down' }], [{}]],
  ['userinfo user_id key', [tokenOk(), { status: 200, body: JSON.stringify({ user_id: 'u2' }) }], [{}]],
  ['userinfo userId key', [tokenOk(), { status: 200, body: JSON.stringify({ userId: 'u3' }) }], [{}]],
  ['userinfo not json', [tokenOk(), { status: 200, body: 'x' }], [{}]],
  ['userinfo numeric id', [tokenOk(), { status: 200, body: JSON.stringify({ id: 77 }) }], [{}]],
  ['no stored user and no userinfo', [tokenOk(), { status: 404, body: '' }], [{ providerSpecificData: { machineId: 'm' } }]],
  ['stored data overrides authMethod', [tokenOk(), userOk], [{ providerSpecificData: { authMethod: 'custom', userId: 's' } }]],
  ['second call hits the cache', [tokenOk(), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['cache is per token', [tokenOk(), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, { apiKey: 'pt-other' }]],
  ['expires_at in the past re-exchanges', [tokenOk({ expires_at: new Date(NOW + 60_000).toISOString() }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['expires_at inside the refresh buffer', [tokenOk({ expires_at: new Date(NOW + 299_000).toISOString() }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['expires_at outside the refresh buffer', [tokenOk({ expires_at: new Date(NOW + 301_000).toISOString() }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  // the second call is made 120 s later, so these leave 270 s, exactly 300 s and 310 s of the token
  ['token with 270 s left is exchanged again', [tokenOk({ expires_at: new Date(NOW + 390_000).toISOString() }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['token with exactly the buffer left is exchanged again', [tokenOk({ expires_at: new Date(NOW + 420_000).toISOString() }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['token with 310 s left is reused', [tokenOk({ expires_at: new Date(NOW + 430_000).toISOString() }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['expires_at not a date uses the day', [tokenOk({ expires_at: 'soon' }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['expires_in is milliseconds', [tokenOk({ expires_in: 600_000 }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['expires_in in seconds is nearly instant', [tokenOk({ expires_in: 3600 }), userOk, tokenOk({ token: 'jt-job2' }), userOk], [{}, {}]],
  ['expires_in zero uses the day', [tokenOk({ expires_in: 0 }), userOk], [{}, {}]],
  ['expires_in string uses the day', [tokenOk({ expires_in: '600000' }), userOk], [{}, {}]],
  ['expires_at wins over expires_in', [tokenOk({ expires_at: new Date(NOW + 900_000).toISOString(), expires_in: 1 }), userOk], [{}, {}]],
  ['exchange http failure', [{ status: 401, body: 'denied '.repeat(60) }], [{}]],
  ['exchange http failure empty', [{ status: 500, body: '' }], [{}]],
  ['exchange without a token', [{ status: 200, body: '{}' }], [{}]],
  ['exchange token empty', [{ status: 200, body: '{"token":""}' }], [{}]],
  ['exchange network error', [{ network: 'econnreset' }], [{}]],
  ['non pat is passed through', [], [{ apiKey: undefined, accessToken: 'dt-x', providerSpecificData: { userId: 'u' } }]],
  ['jt token is passed through', [], [{ apiKey: 'jt-x', accessToken: undefined, providerSpecificData: { userId: 'u' } }]],
  ['pat in access token', [tokenOk(), userOk], [{ apiKey: undefined, accessToken: pat }]],
  ['failed exchange is not cached', [{ status: 500, body: 'no' }, tokenOk(), userOk], [{}, {}], { ignoreErrors: true }],
];
const patResults = [];
for (const [name, fetches, calls, options = {}] of patScenarios) {
  const w = world({ fetches: fetches.length ? fetches : [{ status: 200, body: '' }] });
  Clock.offset = 0;
  const models = loadModels(w, { Datex: Clock });
  const results = [];
  for (const [i, override] of calls.entries()) {
    const input = { ...patCredentials, ...override };
    if (override.apiKey === undefined && 'apiKey' in override) delete input.apiKey;
    if (name.includes('re-exchanges') || name.includes('refresh buffer') || name.includes('expires_in') || name.includes('expires_at') || name.includes('token with')) Clock.offset = i === 0 ? 0 : 120_000;
    try {
      const resolved = await models.resolveQoderCredentials(input, { proxy: 'p' }, null);
      results.push({ input, clock: Clock.offset, resolved: JSON.parse(JSON.stringify(resolved, (k, v) => (v === undefined ? null : v))), hadApiKey: 'apiKey' in resolved && resolved.apiKey !== undefined });
    } catch (error) {
      results.push({ input, clock: Clock.offset, error: error.message });
    }
  }
  void options;
  patResults.push({ name, fetches, calls: results, fetched: w.calls.fetch });
}
Clock.offset = 0;

// The catalog: model list request, parsing, caching, in-flight sharing.
const catalogBody = body => ({ status: 200, body: JSON.stringify(body) });
const chat = [
  { key: 'auto', display_name: 'Auto', max_input_tokens: 180000, max_output_tokens: 8192, is_vl: true, is_reasoning: false, description: 'd', source: 'system' },
  { key: 'hidden', enable: false, display_name: 'Hidden', max_output_tokens: 4 },
  { key: 'plain' }, { display_name: 'no key' }, null, 5, { key: '' },
  { key: 'dup', display_name: 'First' }, { key: 'dup', display_name: 'Second', enable: false },
];
const catalogScenarios = [
  ['device token lists from api3', [catalogBody({ chat })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1', machineId: 'm' } } }]],
  ['job token lists from api2', [catalogBody({ chat })], [{ credentials: { accessToken: 'jt-x', providerSpecificData: { userId: 'u1', machineId: 'm' } } }]],
  ['pat is exchanged first', [tokenOk(), userOk, catalogBody({ chat })], [{ credentials: patCredentials }]],
  ['pat exchange failure lists nothing', [{ status: 500, body: 'no' }], [{ credentials: patCredentials }]],
  ['no user id lists nothing', [catalogBody({ chat })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: {} } }]],
  ['no access token lists nothing', [catalogBody({ chat })], [{ credentials: { providerSpecificData: { userId: 'u' } } }]],
  ['http failure lists nothing', [{ status: 403, body: 'no' }], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['body without chat lists nothing', [catalogBody({ other: [] })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['body not json lists nothing', [{ status: 200, body: 'x' }], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['network error rejects', [{ network: 'down' }], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['second call is cached', [catalogBody({ chat })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }, { credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['cache expires after an hour', [catalogBody({ chat }), catalogBody({ chat: [{ key: 'new' }] })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }, { advance: 60 * 60 * 1000 + 1, credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['cache holds just under an hour', [catalogBody({ chat }), catalogBody({ chat: [{ key: 'new' }] })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }, { advance: 60 * 60 * 1000 - 1, credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['force refresh bypasses the cache', [catalogBody({ chat }), catalogBody({ chat: [{ key: 'new' }] })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }, { force: true, credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
  ['cache is per user', [catalogBody({ chat }), catalogBody({ chat: [{ key: 'other' }] })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }, { credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u2' } } }]],
  ['failed fetch is not cached', [{ status: 500, body: '' }, catalogBody({ chat })], [{ credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }, { credentials: { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } } }]],
];
const catalogResults = [];
for (const [name, fetches, calls] of catalogScenarios) {
  const w = world({ fetches });
  Clock.offset = 0;
  const models = loadModels(w, { Datex: Clock });
  const results = [];
  for (const call of calls) {
    Clock.offset += call.advance ?? 0;
    try {
      const catalog = await models.resolveQoderModels(call.credentials, { forceRefresh: call.force, log: w.log });
      results.push(catalog ? { models: catalog.models, keys: [...catalog.rawConfigs.keys()], rawConfigs: Object.fromEntries(catalog.rawConfigs) } : null);
    } catch (error) {
      results.push({ error: error.message });
    }
  }
  catalogResults.push({ name, fetches, calls: calls.map(c => ({ advance: c.advance ?? 0, force: !!c.force, credentials: c.credentials })), results, fetched: w.calls.fetch, signed: w.calls.signed });
}
Clock.offset = 0;
{
  // Concurrent misses share one request; a forced refresh does not.
  const w = world({ fetches: [catalogBody({ chat })] });
  const models = loadModels(w, { Datex: Clock });
  const c = { accessToken: 'dt-x', providerSpecificData: { userId: 'u1' } };
  const [a, b, d] = await Promise.all([models.resolveQoderModels(c, {}), models.resolveQoderModels(c, {}), models.resolveQoderModels(c, { forceRefresh: true })]);
  catalogResults.push({ name: 'concurrent misses share one request', fetches: [], calls: [], results: [a === b, a === d], fetched: w.calls.fetch.map(f => f.url), signed: [] });
}

// ---- 7. the executor: guards, request and encoding ---------------------------

const executeScenarios = [
  ['device token request', { credentials, model: 'qoder/auto' }],
  ['job token goes to api2', { credentials: { ...credentials, accessToken: 'jt-x', apiKey: 'jt-x' }, model: 'qoder/auto' }],
  ['model source user', { credentials, model: 'qoder/mine', catalog: { mine: { key: 'mine', source: 'user' } } }],
  ['no user id', { credentials: { ...credentials, providerSpecificData: { machineId: 'm' } }, model: 'qoder/auto' }],
  ['no access token', { credentials: { ...credentials, accessToken: undefined }, model: 'qoder/auto' }],
  ['no machine id still signs with the pin generated one', { credentials: { ...credentials, providerSpecificData: { userId: 'u' } }, model: 'qoder/auto' }],
  ['missing model config is a 400', { credentials, model: 'qoder/ghost' }],
  ['upstream not ok is passed through', { credentials, model: 'qoder/auto', upstream: { status: 502, body: 'bad gateway' } }],
  ['upstream ok is unwrapped', { credentials, model: 'qoder/auto', upstream: { status: 200, body: frame(200, chunk({ content: 'hi' }, { finish_reason: 'stop' })) + frame(200, usageChunk({ prompt_tokens: 3, completion_tokens: 1 })) } }],
  ['upstream first frame error', { credentials, model: 'qoder/auto', upstream: { status: 200, body: frame(403, dup) } }],
  ['pat exchange failure is a 401', { credentials: patCredentials, model: 'qoder/auto', pat: { fail: 'exchange boom' } }],
  ['pat exchange success', { credentials: patCredentials, model: 'qoder/auto', pat: { token: 'jt-exchanged', userId: 'exchanged-user' } }],
  ['network error propagates', { credentials, model: 'qoder/auto', upstream: { network: 'socket hang up' } }],
];
const executeResults = [];
for (const [name, scenario] of executeScenarios) {
  const w = world({ catalog: scenario.catalog ?? { auto: baseConfig }, fetches: [scenario.upstream ? (scenario.upstream.network ? { network: scenario.upstream.network } : scenario.upstream) : { status: 200, body: 'data: [DONE]\n\n' }] });
  if (scenario.pat) {
    w.models.resolveQoderCredentials = async creds => {
      if (scenario.pat.fail) throw new Error(scenario.pat.fail);
      return { ...creds, accessToken: scenario.pat.token, apiKey: undefined, providerSpecificData: { ...creds.providerSpecificData, userId: scenario.pat.userId } };
    };
  }
  const attachments = loadAttachments(w);
  const executor = new (loadExecutor(w, { extraDeps: { rewriteQoderMessageAttachments: attachments.rewriteQoderMessageAttachments } }).QoderExecutor)();
  const input = { model: scenario.model, body: { messages: [{ role: 'user', content: 'hello' }] }, stream: true, credentials: scenario.credentials, signal: null, log: w.log, proxyOptions: { proxy: 'p' } };
  let result;
  try {
    const run = await executor.execute(input);
    const text = await run.response.text();
    result = { status: run.response.status, contentType: run.response.headers.get('content-type'), body: text, url: run.url, headers: run.headers, transformedBody: run.transformedBody ? JSON.stringify(run.transformedBody) : null };
  } catch (error) {
    result = { error: error.message };
  }
  const signed = w.calls.signed.map(s => ({ ...s, plain: undefined }));
  const sent = w.calls.fetch.filter(f => f.url.includes('agent_chat_generation'));
  executeResults.push({ name, scenario: { model: scenario.model, credentials: scenario.credentials, catalog: scenario.catalog ?? { auto: baseConfig }, upstream: scenario.upstream ?? null, pat: scenario.pat ?? null }, result, signed, sent });
}

// ---- 8. context tier helpers --------------------------------------------------

const tierCases = [];
for (const [name, config, prompt, preference] of [
  ['no tiers', {}, { messages: [] }, ''], ['count strings', tierConfig, { messages: [user('x'.repeat(400))] }, ''],
  ['est cjk', tierConfig, { messages: [user('世界'.repeat(100))] }, ''], ['est mixed', tierConfig, { system: 'é', messages: [user('a😀b')], tools: [{ a: 1 }] }, ''],
  ['est surrogates', tierConfig, { messages: [user('😀'.repeat(10))] }, ''],
]) {
  tierCases.push({ name, config, prompt, preference, tiers: tier.getQoderContextTiers(config), estimated: tier.estimateQoderPromptTokens(prompt), choice: tier.resolveQoderContextTier(config, prompt, { preference }) });
}
const tierCounts = ['200K', '1M', '1.5M', '204800', 204800, '1.5', ' 2k ', '0', '-5', 'abc', '', null, undefined, 1.9, 0, -1, '1e3', '0x10', 'K', '5 K', '12.5M', NaN, Infinity].map(v => ({ in: typeof v === 'number' && !Number.isFinite(v) ? { number: String(v) } : v === undefined ? { undefined: true } : v, out: tier.parseTierTokenCount(v) }));

// Long repeated runs (the 10 MB images, the 2 MB prompts) are stored as `@@rep:"unit":n@@`;
// the Go test expands them in the inputs and the expected outputs alike.
const MARKER = /@@rep:("(?:[^"\\]|\\.)*"):(\d+)@@/g;
const expand = text => text.replace(MARKER, (_, unit, n) => JSON.parse(unit).repeat(Number(n)));
const compress = text => {
  const parts = [];
  let plain = 0;
  for (let i = 0; i < text.length;) {
    let packed = 0;
    for (let unit = 1; unit <= 4 && !packed; unit++) {
      let end = i + unit;
      while (end < text.length && text.charCodeAt(end) === text.charCodeAt(end - unit)) end++;
      const reps = Math.floor((end - i) / unit);
      if (reps >= 25) {
        parts.push(text.slice(plain, i), `@@rep:${JSON.stringify(text.slice(i, i + unit))}:${reps}@@`);
        i += reps * unit;
        plain = i;
        packed = 1;
      }
    }
    if (!packed) i++;
  }
  parts.push(text.slice(plain));
  return parts.join('');
};
const squeeze = value => {
  if (typeof value === 'string') {
    assert.ok(!value.includes('@@rep:'), 'a fixture string already holds a marker');
    const packed = value.length > 200 ? compress(value) : value;
    assert.equal(expand(packed), value);
    return packed;
  }
  if (Array.isArray(value)) return value.map(squeeze);
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, squeeze(v)]));
  return value;
};

const pinJson = JSON.stringify(squeeze({
  now: NOW, endpoints: {
    jobTokenExchange: constants.QODER_JOB_TOKEN_EXCHANGE_URL, userinfo: constants.QODER_USERINFO_URL, modelList: constants.QODER_MODEL_LIST_URL,
    chatBase: constants.QODER_CHAT_BASE, chatBaseAlt: constants.QODER_CHAT_BASE_ALT, chatSigPath: constants.QODER_CHAT_SIG_PATH,
    imageUploadSigPath: constants.QODER_IMAGE_UPLOAD_SIG_PATH, maxPayloadBytes: constants.QODER_MAX_PAYLOAD_BYTES,
    inlineFallbackMaxBytes: constants.QODER_INLINE_FALLBACK_MAX_BYTES, contextTierHeadroom: constants.QODER_CONTEXT_TIER_HEADROOM,
  },
  encoding: encodingCases, usage: usageCases, sse: sseResults, billing: billingInputs, envelopes, attachments: attachmentResults, uploadWire, uploadResponses,
  pat: patResults, catalog: catalogResults, execute: executeResults, tiers: tierCases, tierCounts,
}));
writeFileSync(out, pinJson + '\n');
console.log(`wrote ${out.pathname}: ${pinJson.length} bytes`);
