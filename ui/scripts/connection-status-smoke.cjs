// Browser fixture for the connection validation lifecycle (#220). Each row says
// whether the credential is untested, accepted or failed, with the last check,
// model count, expiry, origin and quota kept apart; a failed check shows its
// error. Edit sends only what changed and a replacement credential only when
// one is typed, and the typed credential is gone from the page once saved. The
// control API is stubbed, so no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

const SECRET = 'status-relink-sentinel';
const PROVIDERS = [
  { id: 'openai', transports: ['openai-chat'], auth: 'api-key', authModes: ['api-key'], modelCatalog: 'dynamic', passthroughModels: false, reportsUsage: true, signInModes: [], signInMethods: [], configuredNodes: 1, enabledAccounts: 3, readiness: { status: 'ready', served: true } },
];
const minutesAgo = minutes => new Date(Date.now() - minutes * 60000).toISOString();
const base = { nodeId: 'node-openai', providerId: 'openai', authKind: 'api-key', enabled: true, credentialConfigured: true, createdAt: minutesAgo(90), updatedAt: minutesAgo(5) };
let connections = [
  { ...base, id: 'conn-new', name: 'Fresh', identity: 'fresh@example.test', priority: 0, testStatus: 'untested', authOrigin: 'manual' },
  { ...base, id: 'conn-ok', name: 'Working', identity: 'ok@example.test', priority: 1, testStatus: 'active', lastTestedAt: minutesAgo(3), modelsAvailable: 12, authOrigin: 'manual', plan: 'pro', quotaObservedAt: minutesAgo(10) },
  { ...base, id: 'conn-bad', name: 'Revoked', identity: 'bad@example.test', priority: 2, testStatus: 'failed', lastTestedAt: minutesAgo(1), lastErrorAt: minutesAgo(1), lastError: 'provider rejected credentials', authOrigin: 'manual' },
];

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  const patches = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 1, nodes: 1, enabledConnections: 3 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
    if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
    if (path === '/providers') return json(route, 200, { items: PROVIDERS, page: 1, pageSize: 100, total: PROVIDERS.length });
    if (path === '/provider-nodes') return json(route, 200, { items: [{ id: 'node-openai', kind: 'builtin', providerId: 'openai', name: 'openai', transports: ['openai-chat'] }], page: 1, pageSize: 100, total: 1 });
    if (path === '/connections' && method === 'GET') return json(route, 200, { items: connections, page: 1, pageSize: 100, total: connections.length });
    if (path === '/connections/conn-bad' && method === 'PATCH') {
      const body = route.request().postDataJSON();
      patches.push(body);
      connections = connections.map(c => c.id === 'conn-bad' ? { ...c, name: body.name || c.name, testStatus: 'untested', lastTestedAt: undefined, lastError: undefined, lastErrorAt: undefined } : c);
      const { lastError, ...saved } = connections.find(c => c.id === 'conn-bad');
      return json(route, 200, saved);
    }
    if (method === 'GET') return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    return json(route, 404, { error: { code: 'not_found', message: 'not stubbed' } });
  });
  await page.goto('http://127.0.0.1:4173/#providers');
  await page.waitForSelector('.provider-option');
  await page.fill('#provider-filter', 'openai');
  await page.locator('.provider-option', { hasText: 'openai' }).first().click();
  const row = name => page.locator('.connection-row', { hasText: name });
  await row('Fresh').waitFor();

  // Untested, accepted and failed read differently, and the failed one says why.
  assert.ok((await row('Fresh').innerText()).includes('Untested'));
  assert.ok((await row('Fresh').innerText()).includes('Never checked'));
  const working = await row('Working').innerText();
  assert.ok(working.includes('Credential accepted') && working.includes('12 models listed') && working.includes('Plan pro') && /Quota seen \d+ min ago/.test(working), working);
  const failed = await row('Revoked').innerText();
  assert.ok(failed.includes('Check failed') && failed.includes('Last check failed: provider rejected credentials'), failed);
  assert.ok((await row('Revoked').locator('.state-tag.state-bad').count()) === 1, 'failed status uses the danger tone');

  // Relink: the stored credential is never shown; a typed one is masked, sent
  // alone with the changed name, and cleared from the page once saved.
  await row('Revoked').getByRole('button', { name: 'Edit', exact: true }).click();
  const form = page.locator('form[aria-labelledby="connection-edit-title"]');
  await form.waitFor();
  const field = form.getByLabel('Replace API key');
  assert.equal(await field.inputValue(), '', 'the stored credential is not prefilled');
  assert.equal(await field.evaluate(element => element.type), 'password');
  await form.getByLabel('Name').fill('Relinked');
  await field.fill(SECRET);
  assert.ok(!(await page.evaluate(() => document.body.innerText)).includes(SECRET), 'a typed credential is not in the visible text');
  await form.getByRole('button', { name: 'Save', exact: true }).click();
  await form.waitFor({ state: 'detached' });
  await page.getByText('Credential replaced. Check the connection to confirm it.').waitFor();
  assert.deepEqual(patches, [{ name: 'Relinked', secret: { accessToken: SECRET } }]);
  await row('Relinked').waitFor();
  assert.ok((await row('Relinked').innerText()).includes('Untested'));
  const dom = await page.evaluate(() => document.documentElement.outerHTML + [...document.querySelectorAll('input,textarea')].map(element => element.value).join('\n'));
  assert.ok(!dom.includes(SECRET), 'the credential is gone from the DOM after save');

  // An edit that changes nothing sends nothing.
  await row('Working').getByRole('button', { name: 'Edit', exact: true }).click();
  await form.waitFor();
  await form.getByRole('button', { name: 'Save', exact: true }).click();
  await form.waitFor({ state: 'detached' });
  assert.equal(patches.length, 1, 'an unchanged edit is not sent');
  assert.deepEqual(errors, [], 'page errors');
  await browser.close();
  console.log('connection status: chromium');
})().catch(e => { console.error('FAIL', e); process.exit(1); });
