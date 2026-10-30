import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import crypto from 'node:crypto';
import os from 'node:os';
import vm from 'node:vm';

// The run's oracle commit. compat/parity/resolve-oracle.mjs resolves LiteRouter
// refs/heads/main once, fetches that commit into ORACLE_DIR and exports it as
// ORACLE_SHA; every reader below uses that one commit, never a branch or HEAD, and
// there is no default to fall back to (#289).
export const pin = process.env.ORACLE_SHA;
assert.ok(process.env.ORACLE_DIR, 'ORACLE_DIR must contain the oracle; no working files are read');
assert.match(pin ?? '', /^[0-9a-f]{40}$/, 'ORACLE_SHA must be the 40-hex commit of this run (run compat/parity/resolve-oracle.mjs)');
export const git = (...args) => execFileSync('git', ['-C', process.env.ORACLE_DIR, ...args], { encoding: 'utf8', maxBuffer: 8 << 20 });
assert.equal(git('rev-parse', `${pin}^{commit}`).trim(), pin);
export const source = path => git('show', `${pin}:${path}`);

export async function load(path, dependencies, globals = {}) {
  const context = vm.createContext({ Request, Response, Headers, URL, ReadableStream, AbortController, DOMException,
    Set, console: { log() {}, warn() {}, error() {} }, process: { env: {} }, ...globals });
  const code = source(path);
  const names = [...code.matchAll(/^export\s+(?:(?:async\s+)?function|const)\s+(\w+)/gm)].map(m => m[1]);
  // Replace module declarations only; imports cannot reach v1 state or host services.
  const body = code.replace(/^import\s[\s\S]*?;\s*$/gm, '').replace(/^export\s+/gm, '');
  assert.ok(names.length, `${path}: no named exports`);
  assert.doesNotMatch(body, /^\s*(?:import|export)\b/m, `${path}: unsupported module syntax`);
  context.dependencies = dependencies;
  return new vm.Script(`(({${Object.keys(dependencies).join(',')}}) => {${body}\nreturn {${names.join(',')}};})(dependencies)`, { filename: path })
    .runInContext(context, { timeout: 5000 });
}

// loadTree loads an oracle module together with the relative modules it imports,
// each executed in its own context from the pinned commit. Host modules (node:*,
// packages) are not available: using one throws, and `stubs` replaces a module by
// path (a leading "/" is the repository root). Used where the oracle's own helpers
// call its translators, so the recorded behavior is the oracle's, not a restatement.
export async function loadTree(entry, { stubs = {}, globals = {} } = {}) {
  const cache = new Map();
  const importPattern = /^import\s*(?:\{([\s\S]*?)\}|\*\s+as\s+(\w+)|(\w+))\s*from\s*["'](.+?)["'];?\s*$/gm;
  const reexportPattern = /^export\s*\{([\s\S]*?)\}\s*from\s*["'](.+?)["'];?\s*$/gm;
  const resolve = (from, specifier) => posixJoin(from, specifier);
  const posixJoin = (from, specifier) => new URL(specifier, `file://${from}`).pathname;
  const host = () => new Proxy({}, { get: () => () => { throw new Error('host module not available'); } });
  const names = list => list.split(',').map(x => x.trim()).filter(Boolean).map(x => x.split(/\s+as\s+/));
  async function module(file) {
    if (stubs[file]) return stubs[file];
    if (cache.has(file)) return cache.get(file);
    const code = source(file.slice(1));
    const dependencies = {};
    for (const m of code.matchAll(importPattern)) {
      const hostModules = { os, 'node:os': os, crypto, 'node:crypto': crypto };
      // "@/x" is the oracle's alias for src/x; any other bare specifier is a host module.
      const imported = stubs[m[4]] ?? (m[4].startsWith('.') ? await module(resolve(file, m[4]))
        : m[4].startsWith('@/') ? await module(`/src/${m[4].slice(2)}`) : hostModules[m[4]] ?? host());
      if (m[1]) for (const [a, b] of names(m[1])) dependencies[b || a] = imported[a];
      else if (m[2]) dependencies[m[2]] = imported;
      else dependencies[m[3]] = imported.default;
    }
    const reexports = {};
    for (const m of code.matchAll(reexportPattern)) {
      const imported = await module(resolve(file, m[2]));
      for (const [a, b] of names(m[1])) reexports[b || a] = imported[a];
    }
    const local = code.replace(reexportPattern, '');
    const exported = new Set([...local.matchAll(/^export\s+(?:(?:async\s+)?function|const|let|class)\s+(\w+)/gm)].map(m => m[1]));
    for (const m of local.matchAll(/^export\s*\{([^}]*)\}/gm)) for (const [a, b] of names(m[1])) exported.add(b || a);
    const body = local.replace(importPattern, '').replace(/^export\s*\{[^}]*\};?\s*$/gm, '').replace(/^export\s+default\s+/gm, 'const __default = ').replace(/^export\s+/gm, '');
    const hasDefault = /^const __default = /m.test(body);
    const context = vm.createContext({ Request, Response, Headers, URL, ReadableStream, AbortController, DOMException, Set, TextEncoder, TextDecoder,
      console: { log() {}, warn() {}, error() {} }, process: { env: {} }, ...globals });
    context.dependencies = dependencies;
    const result = new vm.Script(`(({${Object.keys(dependencies).join(',')}}) => {${body}\nreturn {${[...exported, ...(hasDefault ? ['default: __default'] : [])].join(',')}};})(dependencies)`, { filename: file }).runInContext(context, { timeout: 5000 });
    Object.assign(result, reexports);
    cache.set(file, result);
    return result;
  }
  return module(`/${entry}`);
}

if (process.argv[2] === 'systemone') {
  const cases = JSON.parse(readFileSync(0, 'utf8'));
  const results = [];
  for (const test of cases) {
    let outbound = null;
    let aborted = false;
    const abort = new AbortController();
    const handler = await load('src/sse/handlers/systemOne.js', {
      getSettings: async () => ({ requireApiKey: true }),
      getProviderNodeById: async () => null, getProviderNodes: async () => [],
      getProviderCredentials: async () => ({ apiKey: 'synthetic-upstream', connectionId: 'synthetic-account' }),
      markAccountUnavailable: async () => ({ shouldFallback: false }), clearAccountError: async () => {},
      extractApiKey: request => request.headers.get('authorization')?.replace(/^Bearer /, ''),
      isValidApiKey: async key => key === 'synthetic-client',
      PROVIDERS: { typesafe: { format: 'systemone', baseUrl: 'https://synthetic.invalid/typed' } },
      getModelsByProviderId: () => [{ id: 'jev-latest' }],
      getKeyAccessContext: async () => ({}), enforceKeyAccess: async () => null, enforceKeyAccessResolved: async () => null,
      extractUsageFromResponse: () => ({}), saveUsageStats() {}, trackPendingRequest() {},
      proxyAwareFetch: async (_url, options) => {
        outbound = JSON.parse(options.body);
        options.signal.addEventListener('abort', () => { aborted = true; }, { once: true });
        return new Response(test.upstreamBody, { status: test.upstreamStatus, headers: { 'content-type': test.contentType } });
      },
    });
    const response = await handler.handleSystemOne(new Request('https://synthetic.invalid/v1/systemone', {
      method: 'POST', body: test.body, signal: abort.signal,
      headers: test.authorized ? { authorization: 'Bearer synthetic-client' } : {},
    }));
    if (test.cancel) abort.abort();
    results.push({ status: response.status, body: await response.text(), outbound, aborted });
  }
  process.stdout.write(JSON.stringify(results));
}
