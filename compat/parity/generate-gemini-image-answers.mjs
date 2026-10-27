// Records what the pin sends a Chat, Responses or Messages client when a
// Gemini-family upstream (gemini, gemini-cli, vertex, antigravity) answers with
// an image, so the Go tests can be offline and still differential (#259). Run:
//
//   ORACLE_DIR=/path/to/pinned-oracle node compat/parity/generate-gemini-image-answers.mjs
//
// Buffered: handlers/chatCore/nonStreamingHandler.js translateNonStreamingResponse,
// the pin's whole non-streaming answer path. Streamed: the pin's own response
// legs, chained as translator/index.js:191-240 chains them: gemini-to-openai.js
// (one registration per Gemini-family format, :159-162), then openai-responses.js
// (:709) or openai-to-claude.js (:265). Everything is read with `git show` at the
// pin; nothing is sent and no credential is used. The Go tests read the file this
// writes; no test regenerates it.
import { writeFileSync } from 'node:fs';
import vm from 'node:vm';
import { source, pin, load } from './oracle.mjs';

const NOW = 1_700_000_000_000;
const out = new URL('../../internal/ingress/testdata/gemini_image_answers_pin.json', import.meta.url);

const { FORMATS } = await load('open-sse/translator/formats.js', {});
const { ROLE, GEMINI_ROLE } = await load('open-sse/translator/schema/roles.js', {});
const blocks = await load('open-sse/translator/schema/blocks.js', {});
const finishes = await load('open-sse/translator/schema/finishReasons.js', {});
const defaults = await load('open-sse/translator/schema/defaults.js', {});
const schema = { ROLE, GEMINI_ROLE, ...blocks, ...finishes, ...defaults };
const { buildChunk } = await load('open-sse/translator/concerns/chunk.js', {});
const usage = await load('open-sse/translator/concerns/usage.js', {});
const reasoning = await load('open-sse/translator/concerns/reasoning.js', schema);
const finish = await load('open-sse/translator/concerns/finishReason.js', { FORMATS, ...schema });
const { fallbackToolCallId } = await load('open-sse/translator/concerns/toolCall.js', { FORMATS });
// image.js also fetches remote images; only its pure data-URI encoder is used here.
const encodeDataUri = new vm.Script(`(${source('open-sse/translator/concerns/image.js').match(/export function encodeDataUri[\s\S]*?\n}\n/)[0].replace(/^export /, '')})`).runInNewContext({});

// A response leg registers itself instead of exporting; capture what it registers.
class FakeDate extends Date {
  constructor(...args) { super(...(args.length ? args : [NOW])); }
  static now() { return NOW; }
}
async function legs(path, dependencies) {
  const registered = {};
  const register = (from, to, _request, response) => { registered[`${from}:${to}`] = response; };
  const body = source(path).replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export\s+/gm, '');
  const names = Object.keys(dependencies);
  const context = vm.createContext({ JSON, Math, Set, Map, Error, String, Number, Array, Object, Date: FakeDate, console: { log() {}, warn() {}, error() {} } });
  new vm.Script(`((register, {${names.join(',')}}) => {${body}\n})`, { filename: path }).runInContext(context)(register, dependencies);
  return registered;
}
const common = { FORMATS, ...schema, buildChunk, ...usage, ...reasoning, ...finish, fallbackToolCallId, encodeDataUri };
const toChat = await legs('open-sse/translator/response/gemini-to-openai.js', { ...common, storeGeminiThoughtSignature() {} });
const toResponses = (await legs('open-sse/translator/response/openai-responses.js', common))[`${FORMATS.OPENAI}:${FORMATS.OPENAI_RESPONSES}`];
const toMessages = (await legs('open-sse/translator/response/openai-to-claude.js', common))[`${FORMATS.OPENAI}:${FORMATS.CLAUDE}`];

// The pin's own translateResponse and initState (translator/index.js:191-251,
// 259-310), over a registry holding the legs above. Tool names are not renamed.
const index = source('open-sse/translator/index.js');
const fn = name => index.match(new RegExp(`export function ${name}\\([\\s\\S]*?\\n}\\n`))[0].replace(/^export /, '');
const responseRegistry = new Map(Object.entries({ ...toChat, [`${FORMATS.OPENAI}:${FORMATS.OPENAI_RESPONSES}`]: toResponses, [`${FORMATS.OPENAI}:${FORMATS.CLAUDE}`]: toMessages }));
const translator = new vm.Script(`(({FORMATS, responseRegistry}) => {
const ensureInitialized = () => {}, restoreToolNames = value => value, decloakStreamChunk = value => value;
${fn('translateResponse')}
${fn('initState')}
return { translateResponse, initState };
})`).runInContext(vm.createContext({ Date: FakeDate, Math, Map, Set, Array }))({ FORMATS, responseRegistry });

// utils/stream.js:393 translates each upstream chunk, and :123 flushes with null.
function stream(target, client, chunks) {
  const state = { ...translator.initState(client), toolNameMap: null };
  const sent = [];
  for (const chunk of [...chunks, null]) sent.push(...translator.translateResponse(target, client, chunk, state).filter(Boolean));
  return sent;
}

const nonStreaming = await load('open-sse/handlers/chatCore/nonStreamingHandler.js', {
  FORMATS, ROLE, RESPONSES_ITEM: blocks.RESPONSES_ITEM, needsTranslation: () => true, openAICompletionToClaudeMessage: body => body, ollamaBodyToOpenAI: body => body,
  addBufferToUsage: value => value, filterUsageForFormat: value => value, createErrorResult() {}, upstreamResponseHeaders: () => ({}), HTTP_STATUS: {},
  parseSSEToOpenAIResponse() {}, unwrapClineEnvelope: body => body, buildRequestDetail() {}, extractRequestConfig() {}, extractUsageFromResponse() {},
  saveUsageStats() {}, formatDoneLine() {}, appendRequestLog() {}, saveRequestDetail() {}, decloakToolNames: body => body, restoreToolNames: body => body,
  rewriteResponsesCustomToolOutput() {}, PROVIDERS: {},
});

const candidate = parts => ({ candidates: [{ content: { role: 'model', parts }, finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 7, candidatesTokenCount: 3, totalTokenCount: 10 }, responseId: 'r1', modelVersion: 'gemini-2.5-flash-image' });
const answerBodies = {
  'text and image': candidate([{ text: 'a cat' }, { inlineData: { mimeType: 'image/png', data: 'QUJD' } }]),
  'image only, no mime type': candidate([{ inline_data: { data: 'QUJD' } }]),
  'two images around text': candidate([{ inlineData: { mimeType: 'image/jpeg', data: 'AAAA' } }, { text: 'and' }, { inlineData: { mimeType: 'image/webp', data: 'BBBB' } }]),
};
const targets = [FORMATS.GEMINI, FORMATS.GEMINI_CLI, FORMATS.VERTEX, FORMATS.ANTIGRAVITY];
const clients = [FORMATS.OPENAI, FORMATS.OPENAI_RESPONSES, FORMATS.CLAUDE];
// The two Cloud Code wires wrap the answer in `response` (v1internal).
const wrap = (target, body) => (target === FORMATS.GEMINI_CLI || target === FORMATS.ANTIGRAVITY ? { response: body } : body);
const answers = [];
for (const [name, raw] of Object.entries(answerBodies)) {
  for (const target of targets) {
    const body = wrap(target, raw);
    for (const client of clients) {
      answers.push({
        name, target, client, body,
        buffered: nonStreaming.translateNonStreamingResponse(structuredClone(body), target, client),
        streamed: stream(target, client, [structuredClone(body)]),
      });
    }
  }
}

writeFileSync(out, JSON.stringify({ pin, now: NOW, answers }, null, 1) + '\n');
console.log(`wrote ${answers.length} answers`);
