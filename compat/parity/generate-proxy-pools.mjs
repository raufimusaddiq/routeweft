// Records what the oracle serves for proxy pools and the Netlify relay (#272): the
// relay bundle and its digests, the digest-deploy route against a scripted Netlify
// API, the pool list/create/update/delete routes, connectionProxy.js (which pool a
// connection gets, relay and strict flags, no-auth rotation) and the relay branch of
// proxyAwareFetch. Everything runs the oracle's own source, loaded from the pinned
// commit; no Netlify, relay or provider is contacted. Routeweft replays the file in
// internal/proxy, internal/proxy/netlify and internal/transport. Run:
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-proxy-pools.mjs           # rewrite the pin
//   node compat/parity/generate-proxy-pools.mjs --check   # fail if the oracle now differs
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { loadTree, pin } from './oracle.mjs';

const b64 = bytes => Buffer.from(bytes).toString('base64');
const digest = bytes => createHash('sha256').update(bytes).digest('hex');

// ---- the relay bundle -------------------------------------------------------------
const relay = await loadTree('src/lib/network/netlifyRelay.js', { globals: { Buffer, Uint8Array } });
const { zip, sha256 } = relay.buildRelayFunctionZip();
const index = relay.buildIndexFile();
const bundle = {
  functionCode: relay.NETLIFY_RELAY_FUNCTION_CODE,
  indexHtml: relay.NETLIFY_INDEX_HTML,
  zipBase64: b64(zip), zipSha256: sha256, indexSha1: index.sha1,
  functionName: relay.NETLIFY_FUNCTION_NAME, functionPath: relay.NETLIFY_FUNCTION_PATH, api: relay.NETLIFY_API,
  relayUrls: ['https://a.netlify.app', 'https://a.netlify.app/', '', undefined].map(site => ({ site: site ?? null, url: relay.buildRelayUrl(site) })),
  headers: relay.netlifyHeaders('TOKEN'),
};

// ---- the digest-deploy route ------------------------------------------------------
const reply = (status, json) => ({ status, json });
const SITE = { id: 'site-1', ssl_url: 'https://relay-a.netlify.app' };
const DEPLOY = { id: 'deploy-1', required: [index.sha1], required_functions: [sha256] };
const ready = reply(200, { state: 'ready' });
const happy = {
  'POST /sites': reply(201, SITE), 'POST /sites/site-1/deploys': reply(200, DEPLOY),
  'PUT /deploys/deploy-1/files/index.html': reply(200, {}), 'PUT /deploys/deploy-1/functions/relay?runtime=js': reply(200, {}),
  'GET /deploys/deploy-1': [reply(200, { state: 'processing' }), ready],
};
const deployCases = [
  { name: 'no token', body: {}, api: {} },
  { name: 'blank token', body: { netlifyToken: '   ' }, api: {} },
  { name: 'full flow, named site', body: { netlifyToken: ' nfp_synthetic ', projectName: 'My Relay_01!!' }, api: happy },
  { name: 'siteName is used when projectName is absent', body: { netlifyToken: 'nfp_synthetic', siteName: 'Edge Relay' }, api: happy },
  { name: 'projectName wins over siteName', body: { netlifyToken: 'nfp_synthetic', projectName: 'one', siteName: 'two' }, api: happy },
  { name: 'unnamed site gets a generated name', body: { netlifyToken: 'nfp_synthetic' }, api: happy },
  { name: 'a name with nothing usable gets a generated name', body: { netlifyToken: 'nfp_synthetic', projectName: '!!!' }, api: happy },
  { name: 'a non-string name gets a generated name', body: { netlifyToken: 'nfp_synthetic', projectName: 42 }, api: happy },
  { name: 'Netlify already has both files', body: { netlifyToken: 'nfp_synthetic', projectName: 'dedupe' }, api: { ...happy, 'POST /sites/site-1/deploys': reply(200, { id: 'deploy-1', required: [], required_functions: [] }) } },
  { name: 'only the function is required', body: { netlifyToken: 'nfp_synthetic', projectName: 'fn-only' }, api: { ...happy, 'POST /sites/site-1/deploys': reply(200, { id: 'deploy-1', required_functions: [sha256] }) } },
  { name: 'site name taken (422)', body: { netlifyToken: 'nfp_synthetic', projectName: 'taken' }, api: { 'POST /sites': reply(422, { message: 'Subdomain not available' }) } },
  { name: 'site create fails with a message', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { 'POST /sites': reply(401, { message: 'Access Denied' }) } },
  { name: 'site create fails with an error field', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { 'POST /sites': reply(403, { error: 'Forbidden' }) } },
  { name: 'site create fails without a body', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { 'POST /sites': reply(500, undefined) } },
  { name: 'site answer without a URL', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { 'POST /sites': reply(201, { id: 'site-1' }) } },
  { name: 'site answer with site_id and url', body: { netlifyToken: 'nfp_synthetic', projectName: 'alt' }, api: { ...happy, 'POST /sites': reply(201, { site_id: 'site-1', url: 'http://relay-a.netlify.app/' }) } },
  { name: 'deploy create fails', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'POST /sites/site-1/deploys': reply(429, { message: 'Too many deploys' }) } },
  { name: 'deploy answer without an id', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'POST /sites/site-1/deploys': reply(200, {}) } },
  { name: 'index.html upload fails', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'PUT /deploys/deploy-1/files/index.html': reply(500, { message: 'disk' }) } },
  { name: 'function upload fails', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'PUT /deploys/deploy-1/functions/relay?runtime=js': reply(400, { error: 'bad zip' }) } },
  { name: 'the deploy ends in error', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'GET /deploys/deploy-1': [reply(200, { state: 'error', error_message: 'Function bundling failed' })] } },
  { name: 'the deploy ends in error without a message', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'GET /deploys/deploy-1': [reply(200, { state: 'error' })] } },
  { name: 'the status call fails', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'GET /deploys/deploy-1': [reply(503, {})] } },
  { name: 'the deploy never becomes ready', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: { ...happy, 'GET /deploys/deploy-1': [reply(200, { state: 'processing' })] }, clockStep: 60000 },
  { name: 'the network fails', body: { netlifyToken: 'nfp_synthetic', projectName: 'x' }, api: {}, throws: 'getaddrinfo ENOTFOUND api.netlify.com' },
];

async function runDeploy(test) {
  const clock = { now: 1700000000000, step: test.clockStep ?? 0 };
  const calls = [];
  const created = [];
  const queues = {};
  const fetchStub = async (url, init = {}) => {
    const path = String(url).replace(bundle.api, '');
    const key = `${init.method ?? 'GET'} ${path}`;
    const body = init.body;
    calls.push({ key, headers: init.headers, body: body === undefined ? null : typeof body === 'string' ? JSON.parse(body) : { bytes: body.length, sha256: digest(body) } });
    if (test.throws) throw new Error(test.throws);
    let answer = test.api[key];
    assert.ok(answer, `${test.name}: unscripted call ${key}`);
    if (Array.isArray(answer)) {
      queues[key] ??= [...answer];
      answer = queues[key].length > 1 ? queues[key].shift() : queues[key][0];
    }
    return new Response(answer.json === undefined ? '' : JSON.stringify(answer.json), { status: answer.status });
  };
  const route = await loadTree('src/app/api/proxy-pools/netlify-deploy/route.js', {
    stubs: {
      'next/server': { NextResponse: { json: (body, init) => ({ status: init?.status ?? 200, body }) } },
      '@/models': { createProxyPool: async data => { created.push(data); return { id: 'pool-1', ...data, createdAt: 'T', updatedAt: 'T' }; } },
    },
    globals: { fetch: fetchStub, Buffer, Uint8Array, setTimeout: fn => { fn(); return 0; }, Date: { now: () => (clock.now += clock.step) } },
  });
  const answer = await route.POST({ json: async () => test.body });
  return { name: test.name, body: test.body, api: test.api, throws: test.throws ?? null, clockStep: test.clockStep ?? 0, calls, created, status: answer.status, answer: answer.body };
}
const deploy = [];
for (const test of deployCases) deploy.push(await runDeploy(test));

// ---- the pool routes --------------------------------------------------------------
const pools = [
  { id: 'a', name: 'A', proxyUrl: 'http://a:1', isActive: true }, { id: 'b', name: 'B', proxyUrl: 'http://b:1', isActive: false },
  { id: 'c', name: 'C', proxyUrl: 'https://c.netlify.app/.netlify/functions/relay', type: 'netlify', isActive: true },
];
const connections = [{ providerSpecificData: { proxyPoolId: 'a' } }, { providerSpecificData: { proxyPoolId: 'a' } }, { providerSpecificData: { proxyPoolId: 'c' } }, { providerSpecificData: {} }, {}];
async function runList(query) {
  const filters = [];
  const route = await loadTree('src/app/api/proxy-pools/route.js', {
    stubs: {
      'next/server': { NextResponse: { json: (body, init) => ({ status: init?.status ?? 200, body }) } },
      '@/models': { createProxyPool: async data => ({ id: 'new', ...data }), getProviderConnections: async () => connections, getProxyPools: async filter => { filters.push(filter); return pools.filter(p => filter.isActive === undefined || p.isActive === Boolean(filter.isActive)); } },
    },
  });
  const answer = await route.GET({ url: `https://x.invalid/api/proxy-pools${query}` });
  return { query, filters, status: answer.status, answer: answer.body };
}
const list = [];
for (const query of ['', '?isActive=true', '?isActive=false', '?isActive=yes', '?isActive=TRUE', '?includeUsage=true', '?includeUsage=1', '?isActive=true&includeUsage=true', '?isActive=false&includeUsage=true']) list.push(await runList(query));

async function runCreate(body) {
  const route = await loadTree('src/app/api/proxy-pools/route.js', {
    stubs: { 'next/server': { NextResponse: { json: (b, init) => ({ status: init?.status ?? 200, body: b }) } }, '@/models': { createProxyPool: async data => ({ id: 'new', ...data }), getProviderConnections: async () => [], getProxyPools: async () => [] } },
  });
  const answer = await route.POST({ json: async () => body });
  return { body, status: answer.status, answer: answer.body };
}
const create = [];
for (const body of [
  { name: ' P ', proxyUrl: ' http://p:1 ' }, { proxyUrl: 'http://p:1' }, { name: 'P' }, { name: 'P', proxyUrl: '  ' }, { name: 'P', proxyUrl: 'http://p:1', noProxy: ' a.com,b.com ' },
  { name: 'P', proxyUrl: 'http://p:1', isActive: false }, { name: 'P', proxyUrl: 'http://p:1', isActive: 'false' }, { name: 'P', proxyUrl: 'http://p:1', isActive: 0 },
  { name: 'P', proxyUrl: 'http://p:1', strictProxy: true }, { name: 'P', proxyUrl: 'http://p:1', strictProxy: 'true' },
  ...['http', 'vercel', 'cloudflare', 'deno', 'netlify', 'socks', 'NETLIFY', 7].map(type => ({ name: 'P', proxyUrl: 'https://r.example/relay', type })),
]) create.push(await runCreate(body));

async function runUpdate(existing, body) {
  const updates = [];
  const route = await loadTree('src/app/api/proxy-pools/[id]/route.js', {
    stubs: { 'next/server': { NextResponse: { json: (b, init) => ({ status: init?.status ?? 200, body: b }) } },
      '@/models': { deleteProxyPool: async () => true, getProviderConnections: async () => [], getProxyPoolById: async id => existing ? { id, ...existing } : null, updateProxyPool: async (id, data) => { updates.push(data); return { id, ...existing, ...data }; } } },
  });
  const answer = await route.PUT({ json: async () => body }, { params: Promise.resolve({ id: 'p1' }) });
  return { existing: Boolean(existing), body, updates, status: answer.status, answer: answer.body };
}
const update = [];
for (const [existing, body] of [
  [null, { name: 'x' }], [{ name: 'P' }, {}], [{ name: 'P' }, { name: '  ' }], [{ name: 'P' }, { name: ' New ' }], [{ name: 'P' }, { proxyUrl: '' }], [{ name: 'P' }, { proxyUrl: ' http://z:2 ' }],
  [{ name: 'P' }, { noProxy: ' a.com ' }], [{ name: 'P' }, { noProxy: 5 }], [{ name: 'P' }, { isActive: false }], [{ name: 'P' }, { isActive: 'true' }],
  [{ name: 'P' }, { strictProxy: true }], [{ name: 'P' }, { strictProxy: 1 }], [{ name: 'P' }, { type: 'deno' }], [{ name: 'P' }, { type: 'nope' }], [{ name: 'P' }, { name: 'N', proxyUrl: 'http://z:2', type: 'netlify', isActive: true, strictProxy: true, noProxy: '' }],
]) update.push(await runUpdate(existing, body));

async function runDelete(existing, connectionList) {
  const deleted = [];
  const route = await loadTree('src/app/api/proxy-pools/[id]/route.js', {
    stubs: { 'next/server': { NextResponse: { json: (b, init) => ({ status: init?.status ?? 200, body: b }) } },
      '@/models': { deleteProxyPool: async id => { deleted.push(id); return true; }, getProviderConnections: async () => connectionList, getProxyPoolById: async id => existing ? { id } : null, updateProxyPool: async () => null } },
  });
  const answer = await route.DELETE({}, { params: Promise.resolve({ id: 'p1' }) });
  return { existing, connections: connectionList.length, deleted, status: answer.status, answer: answer.body };
}
const remove = [];
for (const [existing, list_] of [[false, []], [true, []], [true, [{ providerSpecificData: { proxyPoolId: 'p1' } }]], [true, [{ providerSpecificData: { proxyPoolId: 'p1' } }, { providerSpecificData: { proxyPoolId: 'p1' } }, { providerSpecificData: { proxyPoolId: 'other' } }]], [true, [{ providerSpecificData: { proxyPoolId: 'other' } }]]]) remove.push(await runDelete(existing, list_));

// ---- which pool a connection gets, rotation, strict, relay ----------------------
const table = {
  active: { id: 'active', isActive: true, proxyUrl: ' http://p:8080 ', noProxy: ' a.com ', type: 'http', strictProxy: false },
  strict: { id: 'strict', isActive: true, proxyUrl: 'http://s:1', type: 'http', strictProxy: true },
  inactive: { id: 'inactive', isActive: false, proxyUrl: 'http://i:1', type: 'http', strictProxy: false },
  inactiveStrict: { id: 'inactiveStrict', isActive: false, proxyUrl: 'http://i:1', type: 'http', strictProxy: true },
  empty: { id: 'empty', isActive: true, proxyUrl: '  ', type: 'http', strictProxy: false },
  emptyStrict: { id: 'emptyStrict', isActive: true, proxyUrl: '', type: 'http', strictProxy: true },
  vercel: { id: 'vercel', isActive: true, proxyUrl: 'https://v.example/relay', noProxy: 'x.com', type: 'vercel', strictProxy: false },
  cloudflare: { id: 'cloudflare', isActive: true, proxyUrl: 'https://c.example/', type: 'cloudflare', strictProxy: true },
  deno: { id: 'deno', isActive: true, proxyUrl: 'https://d.example/', type: 'deno' },
  netlify: { id: 'netlify', isActive: true, proxyUrl: 'https://n.netlify.app/.netlify/functions/relay', type: 'netlify', strictProxy: false },
  inactiveRelay: { id: 'inactiveRelay', isActive: false, proxyUrl: 'https://n.example/', type: 'netlify', strictProxy: true },
};
const legacy = { connectionProxyEnabled: true, connectionProxyUrl: ' http://l:1 ', connectionNoProxy: ' l.com ' };
const connectionCases = [
  {}, { proxyPoolId: '' }, { proxyPoolId: '__none__' }, { proxyPoolId: ' active ' }, { proxyPoolId: 'strict' }, { proxyPoolId: 'inactive' }, { proxyPoolId: 'inactiveStrict' },
  { proxyPoolId: 'empty' }, { proxyPoolId: 'emptyStrict' }, { proxyPoolId: 'vercel' }, { proxyPoolId: 'cloudflare' }, { proxyPoolId: 'deno' }, { proxyPoolId: 'netlify' }, { proxyPoolId: 'inactiveRelay' },
  { proxyPoolId: 'gone' }, { proxyPoolId: 'boom' },
  { ...legacy }, { connectionProxyEnabled: true }, { connectionProxyUrl: 'http://l:1' }, { connectionProxyEnabled: 'true', connectionProxyUrl: 'http://l:1' },
  { ...legacy, proxyPoolId: 'active' }, { ...legacy, proxyPoolId: 'inactive' }, { ...legacy, proxyPoolId: 'inactiveStrict' }, { ...legacy, proxyPoolId: 'gone' }, { ...legacy, proxyPoolId: '__none__' }, { ...legacy, proxyPoolId: 'netlify' },
];
let randomValues = [];
const cp = await loadTree('src/lib/network/connectionProxy.js', {
  stubs: { '@/models': { getProxyPoolById: async id => { if (id === 'boom') throw new Error('database is locked'); return table[id] ? structuredClone(table[id]) : null; } } },
  globals: { Math: Object.assign(Object.create(Math), { random: () => randomValues.shift() ?? 0 }) },
});
const resolved = [];
for (const providerSpecificData of connectionCases) {
  let result;
  try { const r = await cp.resolveConnectionProxyConfig(providerSpecificData); result = { ...r, proxyPool: r.proxyPool ? { id: r.proxyPool.id } : null }; } catch (error) { result = { throws: error.message }; }
  resolved.push({ providerSpecificData, result });
}
const rotation = [];
for (const [ids, strategy, provider, random] of [
  [[], 'round-robin', 'p', []], [['a'], 'round-robin', 'single', []], [['a'], 'random', 'single', []],
  [['a', 'b', 'c'], 'round-robin', 'rr', []], [['a', 'b', 'c'], 'round-robin', 'rr', []], [['a', 'b', 'c'], 'round-robin', 'rr', []], [['a', 'b', 'c'], 'round-robin', 'rr', []],
  [['a', 'b', 'c'], 'round-robin', 'other', []], [['a', 'b'], 'round-robin', 'rr', []], [['a', 'b', 'c'], 'random', 'r', [0]], [['a', 'b', 'c'], 'random', 'r', [0.5]], [['a', 'b', 'c'], 'random', 'r', [0.99]],
  [['a', 'b', 'c'], 'none', 'n', []], [['a', 'b', 'c'], 'whatever', 'n', []], [['a', 'b', 'c'], undefined, 'n', []], [null, 'round-robin', 'p', []],
]) {
  randomValues = [...random];
  rotation.push({ ids, strategy: strategy ?? null, provider, random, picked: cp.pickProxyPoolId(ids, strategy, provider) });
}

// ---- the relay branch of proxyAwareFetch ------------------------------------------
async function runFetch(url, options, proxyOptions) {
  const sent = [];
  const pf = await loadTree('open-sse/utils/proxyFetch.js', {
    globals: { fetch: async (target, init) => { sent.push({ target: String(target), method: init?.method ?? null, headers: init?.headers ?? null, body: init?.body ?? null, dispatcher: init?.dispatcher ? true : false }); return new Response('ok'); }, Headers },
  });
  try { await pf.proxyAwareFetch(url, options, proxyOptions); return { url, options, proxyOptions, sent }; } catch (error) { return { url, options, proxyOptions, sent, throws: error.message }; }
}
const relayUrl = 'https://n.netlify.app/.netlify/functions/relay';
const fetches = [];
for (const [url, options, proxyOptions] of [
  ['https://api.openai.com/v1/chat/completions', { method: 'POST', headers: { authorization: 'Bearer synthetic', 'content-type': 'application/json' }, body: '{"a":1}' }, { vercelRelayUrl: relayUrl }],
  ['https://api.anthropic.com:8443/v1/messages?beta=true', { method: 'POST', headers: { 'x-api-key': 'synthetic' }, body: 'b' }, { vercelRelayUrl: ` ${relayUrl} `, strictProxy: true }],
  ['http://upstream.example/path', { method: 'GET' }, { vercelRelayUrl: relayUrl, connectionProxyEnabled: true, connectionProxyUrl: 'http://p:1' }],
  ['https://api.openai.com/v1/models', {}, { vercelRelayUrl: '' }],
  ['https://api.openai.com/v1/models', {}, { strictProxy: true }],
  ['https://api.openai.com/v1/models', {}, { strictProxy: true, proxyPoolId: 'gone' }],
  ['https://api.openai.com/v1/models', {}, { strictProxy: true, enabled: true }],
  ['https://api.openai.com/v1/models', {}, { strictProxy: true, connectionProxyEnabled: true }],
  ['https://api.openai.com/v1/models', {}, { strictProxy: true, connectionProxyUrl: 'http://p:1', connectionProxyEnabled: false }],
  ['https://api.openai.com/v1/models', {}, { strictProxy: false, proxyPoolId: 'gone' }],
]) fetches.push(await runFetch(url, options, proxyOptions));
// A Headers instance is carried over as its entries.
const viaHeaders = await (async () => {
  const sent = [];
  const pf = await loadTree('open-sse/utils/proxyFetch.js', { globals: { fetch: async (t, i) => { sent.push({ target: String(t), headers: i.headers }); return new Response('ok'); }, Headers } });
  await pf.proxyAwareFetch('https://api.openai.com/v1/x', { headers: new Headers({ 'X-Custom': 'v' }) }, { vercelRelayUrl: relayUrl });
  return sent;
})();

// The relay function and the placeholder page, exactly as the oracle ships them.
const goString = text => (/[`\r]/.test(text) ? JSON.stringify(text) : '`' + text + '`');
const bundleGo = execFileSync('gofmt', { input: `// Code generated by compat/parity/generate-proxy-pools.mjs; DO NOT EDIT.
// Source: src/lib/network/netlifyRelay.js at ${pin}.
package netlify

// RelayFunctionCode is the function Netlify runs for a relay pool: a Lambda-style
// exports.handler that forwards the request named by x-relay-target and
// x-relay-path and returns the answer (netlifyRelay.js:29-99).
const RelayFunctionCode = ${goString(bundle.functionCode)}

// IndexHTML is the placeholder page deployed beside it (netlifyRelay.js:100-103).
const IndexHTML = ${goString(bundle.indexHtml)}
`, encoding: 'utf8' });
const bundleFile = new URL('../../internal/proxy/netlify/bundle_gen.go', import.meta.url);

const pinned = JSON.stringify({ oracle: 'raufimusaddiq/literouter', sha: pin, bundle, deploy, list, create, update, remove, resolved, rotation, fetches, viaHeaders }, null, 1) + '\n';
const file = new URL('../../internal/proxy/testdata/proxy_pools_pin.json', import.meta.url);
if (process.argv.includes('--check')) {
  const committed = JSON.parse(readFileSync(file, 'utf8'));
  assert.deepEqual({ ...JSON.parse(pinned), sha: null }, { ...committed, sha: null }, 'oracle proxy-pool behavior differs from the pin; regenerate');
  assert.equal(readFileSync(bundleFile, 'utf8').replace(/at [0-9a-f]{40}/, 'at SHA'), bundleGo.replace(/at [0-9a-f]{40}/, 'at SHA'), 'the relay bundle differs from the oracle; regenerate');
  console.log(`${pin}: proxy pools and Netlify relay match the pin`);
} else {
  writeFileSync(file, pinned);
  writeFileSync(bundleFile, bundleGo);
  console.log(`${pin}: ${deploy.length} deploys, ${list.length + create.length + update.length + remove.length} route cases, ${resolved.length} resolutions, ${rotation.length} rotations, ${fetches.length + 1} relay fetches`);
}
