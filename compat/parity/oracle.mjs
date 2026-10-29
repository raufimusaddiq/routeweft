import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
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
