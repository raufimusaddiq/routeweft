import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { git, load, pin, source } from './oracle.mjs';

const text = readFileSync('docs/PARITY.md', 'utf8');
const files = git('ls-tree', '-r', '--name-only', pin).trim().split('\n');
const paths = files.filter(p => /^src\/app\/api\/.*\/route\.(js|ts)$/.test(p));
const routeRows = [...text.matchAll(/^\| (\d+) \| `(\/api\/[^`]+)` \| ([^|]+) \| ([^|]+) \| ([^|]+) \| ([^|]+) \| ([^|]+) \| ([^|]+) \|$/gm)];
assert.equal(routeRows.length, 97);
assert.deepEqual(routeRows.map(r => `src/app${r[2]}/route.js`).sort(), paths.sort());
const guard = await load('src/dashboardGuard.js', {
  NextResponse: { next: () => new Response(null, { status: 204 }), json: (body, options) => Response.json(body, options), redirect: url => Response.redirect(url) },
  getSettings: async () => ({ requireLogin: true }), validateApiKey: async key => key === 'synthetic-client',
  getConsistentMachineId: async () => 'synthetic-cli', verifyDashboardAuthToken: async token => token === 'synthetic-session',
  hasTrustedPeerHeaders: () => false,
});
let operations = 0;
for (const row of routeRows) {
  const path = `src/app${row[2]}/route.js`;
  const code = source(path);
  const methods = new Set([...code.matchAll(/export\s+(?:(?:async\s+)?function|const)\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b/g)].map(m => m[1]));
  for (const declaration of code.matchAll(/export\s*\{([^}]+)\}/g)) {
    for (const method of declaration[1].match(/\b(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b/g) || []) methods.add(method);
  }
  assert.deepEqual([...methods].sort(), row[3].trim().split(/,\s*/).sort(), `${row[2]} methods drifted`);
  assert.ok(row[5].trim(), `${row[2]} missing shape contract`);
  for (const method of methods) {
    const url = new URL(`https://remote.invalid${row[2]}`);
    const request = { method, url: url.href, nextUrl: url, headers: new Headers(), cookies: { get: () => undefined } };
    const expected = { PUB: 204, D: 401, S: 401, L: 403, 'L + S': 403, DB: 401, K: 401 }[row[4].trim()];
    assert.ok(expected, `${row[2]} unknown auth classification`);
    assert.equal((await guard.proxy(request)).status, expected, `${row[2]} ${method} anonymous auth drifted`);
    if (row[4].trim() === 'K') {
      for (const header of ['Authorization', 'x-api-key', 'x-goog-api-key', 'query']) {
        request.headers = new Headers();
        url.search = '';
        if (header === 'query') url.searchParams.set('key', 'synthetic-client');
        else request.headers.set(header, header === 'Authorization' ? 'Bearer synthetic-client' : 'synthetic-client');
        assert.equal((await guard.proxy(request)).status, 204, `${row[2]} ${header} key form`);
      }
    }
    operations++;
  }
}

const providerSection = text.split('### Hidden providers and non-provider assets')[0].split('| Provider | v1 auth |')[1];
const providers = [...providerSection.matchAll(/^\| `([a-z0-9-]+)` \|/gm)].map(m => m[1]).sort();
// The parity doc's table and the oracle's active imports must agree, and the
// count is derived rather than pinned: a new provider is a documentation update,
// not a silent test failure.
const activeImports = [...source('open-sse/providers/registry/index.js').matchAll(/^import \w+ from "\.\/(.+)\.js";/gm)].map(m => m[1]).sort();
assert.deepEqual(providers, activeImports);
const pageSection = text.split('## 4. Dashboard and supporting pages (16/16)')[1].split('## 5.')[0];
const pages = [...pageSection.matchAll(/^\| `([^`]+page\.js)` \|/gm)].map(m => m[1].replace(/^V\//, 'src/app/(dashboard)/dashboard/')).sort();
assert.equal(pages.length, 16);
assert.deepEqual(pages, files.filter(p => /^src\/app\/(?:.*\/)?page\.js$/.test(p)).sort());

const gaps = text.split('\n').filter(line => /^\|/.test(line) && !line.startsWith('| Inventory |') && /\| (?:MISSING|PARTIAL)\b[^|]*\|/.test(line));
for (const row of gaps) {
  assert.match(row, /R11: (?:product decision|privileged host operation|no SPEC basis): [^|]{20,}/, `unannotated gap: ${row}`);
}
assert.ok(gaps.length > 0, 'inventory silently removed all gaps');
console.log(`${pin}: ${paths.length} routes, ${operations} operations, ${providers.length} providers, ${pages.length} pages; ${gaps.length} gaps dispositioned. Shapes are inventoried; only the differential cases replay wire behavior.`);
