// Records the host side of the oracle's PXPIPE (open-sse/rtk/pxpipe.js
// compressWithPxpipe): its gates, what it hands the transform, how it reads the
// transform's answer, the savings estimate it reports, and every way it fails open.
// The transform itself (pxpipe-proxy's transformAnthropicMessages, which renders the
// images) is an installed npm package, not oracle source, so it is scripted here, as
// the oracle's own tests script it. internal/transforms/pxpipe replays the file
// (#271). Run:
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-pxpipe.mjs           # rewrite the pin
//   node compat/parity/generate-pxpipe.mjs --check   # fail if the oracle now differs
//
// No helper is contacted and no provider is called.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { loadTree, pin } from './oracle.mjs';

const oracle = await loadTree('open-sse/rtk/pxpipe.js', { globals: { setTimeout, clearTimeout, Date } });

const text = (unit, times) => ({ unit, times });
const build = (spec, extra = {}) => ({ model: 'claude-fable-5', max_tokens: 100, messages: [{ role: 'user', content: spec.unit.repeat(spec.times) }], ...extra });
const applied = (info, cache = { ownsCacheControl: true, markerCount: 1 }) => ({ applied: true, reason: 'applied', body: { model: 'claude-fable-5', messages: [{ role: 'user', content: [{ type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'AAAA' } }] }] }, info, cache });
const cases = [
  { name: 'disabled', options: { enabled: false, format: 'claude' }, spec: text('x', 30000), script: 'applied' },
  { name: 'transform unavailable (not installed)', options: { enabled: true, format: 'claude' }, spec: text('x', 30000), script: 'none' },
  { name: 'only Claude-format bodies', options: { enabled: true, format: 'openai' }, spec: text('x', 30000), script: 'applied', result: applied({}) },
  { name: 'below the default threshold', options: { enabled: true, format: 'claude' }, spec: text('x', 200), script: 'applied', result: applied({}) },
  { name: 'below a configured threshold', options: { enabled: true, format: 'claude', minChars: 40000 }, spec: text('x', 30000), script: 'applied', result: applied({}) },
  { name: 'a threshold of zero or less means the default', options: { enabled: true, format: 'claude', minChars: 0 }, spec: text('x', 30000), script: 'applied', result: applied({ compressedChars: 25000, imageCount: 2, imageBytes: 5000, imagePixels: 1500000 }) },
  { name: 'applied: pixels give the image tokens', options: { enabled: true, format: 'claude', minChars: 1000, model: 'claude-fable-5' }, spec: text('x', 30000), script: 'applied', result: applied({ compressedChars: 25000, imageCount: 2, imageBytes: 5000, imagePixels: 1500000 }) },
  { name: 'applied: reported image tokens and baseline win', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'applied', result: applied({ compressedChars: 25000, imageCount: 3, imageBytes: 9000, imageTokens: 1234, baselineTokens: 9999 }) },
  { name: 'applied: no pixels or tokens, 4761 per image', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'applied', result: applied({ compressedChars: 20000, imageCount: 2 }) },
  { name: 'applied: no info at all', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'applied', result: applied(undefined, undefined) },
  { name: 'applied: the helper does not own cache control', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'applied', result: applied({ compressedChars: 25000, imageCount: 2, imagePixels: 1500000 }, { ownsCacheControl: false }) },
  { name: 'applied: savings cannot be negative', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'applied', result: applied({ compressedChars: 100, imageCount: 40, imagePixels: 90000000 }) },
  { name: 'applied: a percentage that needs rounding', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30001), script: 'applied', result: applied({ compressedChars: 17777, imageCount: 1, imagePixels: 777777 }) },
  { name: 'the threshold counts UTF-16 units, not characters or bytes', options: { enabled: true, format: 'claude' }, spec: text('\u{1F600}', 13000), script: 'applied', result: applied({ compressedChars: 20000, imageCount: 2, imagePixels: 1000000 }) },
  { name: 'the helper declines: not profitable', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'applied', result: { applied: false, reason: 'not_profitable', body: {}, info: {} } },
  { name: 'the helper declines without a reason', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'applied', result: { applied: false, detail: 'no images', body: {}, info: {} } },
  { name: 'the helper throws', options: { enabled: true, format: 'claude', minChars: 1000 }, spec: text('x', 30000), script: 'throws', error: 'boom' },
  { name: 'the helper never answers', options: { enabled: true, format: 'claude', minChars: 1000, timeoutMs: 50 }, spec: text('x', 30000), script: 'never' },
];

const run = async test => {
  const body = build(test.spec);
  const seen = [];
  const transform = test.script === 'none' ? undefined : async input => {
    // the body is the one the case built; what matters is that it arrives whole
    seen.push({ bodyChars: JSON.stringify(JSON.parse(new TextDecoder().decode(input.body))).length, model: input.model, options: input.options });
    if (test.script === 'throws') throw new Error(test.error);
    if (test.script === 'never') return new Promise(() => {});
    const result = test.result;
    return { ...result, body: new TextEncoder().encode(JSON.stringify(result.body)) };
  };
  const answer = await oracle.compressWithPxpipe(body, { ...test.options, transform });
  const { durationMs, ...summary } = answer.summary;
  return {
    name: test.name, spec: test.spec, options: test.options, script: test.script, result: test.result, error: test.error,
    calls: seen, body: answer.body, summary, hasDuration: durationMs !== undefined, log: oracle.formatPxpipeLog({ ...answer.summary, durationMs: 7 }),
  };
};

const results = [];
for (const test of cases) results.push(await run(test));
const pinned = JSON.stringify({ oracle: 'raufimusaddiq/literouter', sha: pin, cases: results }, null, 1) + '\n';
const file = new URL('../../internal/transforms/pxpipe/testdata/host_pin.json', import.meta.url);
if (process.argv.includes('--check')) {
  const committed = JSON.parse(readFileSync(file, 'utf8'));
  assert.deepEqual({ ...JSON.parse(pinned), sha: null }, { ...committed, sha: null }, 'oracle PXPIPE host behavior differs from the pin; regenerate');
  console.log(`${pin}: PXPIPE host behavior matches the pin (${results.length} cases)`);
} else {
  writeFileSync(file, pinned);
  console.log(`${pin}: ${results.length} PXPIPE cases`);
}
