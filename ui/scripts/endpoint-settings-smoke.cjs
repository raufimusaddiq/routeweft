// Browser fixture for the connection settings that choose a host or a name (#228): Azure's endpoint,
// deployment, API version and organization, Kimchi's catalog host, local Ollama's host URL and
// optional key, and Cloudflare's account ID. A new connection posts them (trimmed, defaults filled,
// blanks left out), the edit form shows what is saved and sends only what changed, a required field
// blocks the save, and the credential never reaches the page. The control API is stubbed, so no
// credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

const SECRET = 'endpoint-sentinel-key';
const provider = (id, transports, auth, authModes) => ({ id, transports, auth, authModes, modelCatalog: 'static', passthroughModels: false, reportsUsage: false, signInModes: [], signInMethods: [], configuredNodes: 1, enabledAccounts: 0, readiness: { status: 'ready', served: true } });
const PROVIDERS = [
  provider('azure', ['openai-chat'], 'api-key', ['api-key']),
  provider('cloudflare-ai', ['openai-chat'], 'api-key', ['api-key']),
  provider('kimchi', ['openai-chat'], 'api-key', ['api-key']),
  provider('ollama-local', ['ollama'], 'none', ['none', 'api-key']),
];
const NODES = PROVIDERS.map(p => ({ id: `node-${p.id}`, kind: 'builtin', providerId: p.id, name: p.id, transports: p.transports }));
const base = { authKind: 'api-key', enabled: true, credentialConfigured: true, createdAt: '2026-10-30T00:00:00Z', updatedAt: '2026-10-30T00:00:00Z', testStatus: 'untested', authOrigin: 'manual' };
let connections = [
  { ...base, id: 'conn-azure', nodeId: 'node-azure', providerId: 'azure', name: 'Existing Azure', identity: 'az@example.test', priority: 0, settings: { azureEndpoint: 'https://old.openai.azure.com', deployment: 'dep-a', apiVersion: '2025-01-01' } },
  { ...base, id: 'conn-cf', nodeId: 'node-cloudflare-ai', providerId: 'cloudflare-ai', name: 'Existing Cloudflare', identity: 'cf@example.test', priority: 0, accountId: 'acc123' },
];

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  page.on('console', msg => { if (msg.type() === 'error' && !/Failed to load resource/.test(msg.text())) errors.push('console: ' + msg.text()); });
  const writes = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 4, nodes: 4, enabledConnections: 2 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
    if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
    if (path === '/providers') return json(route, 200, { items: PROVIDERS, page: 1, pageSize: 100, total: PROVIDERS.length });
    if (path === '/provider-nodes') return json(route, 200, { items: NODES, page: 1, pageSize: 100, total: NODES.length });
    if (path === '/connections' && method === 'GET') return json(route, 200, { items: connections, page: 1, pageSize: 100, total: connections.length });
    if (path === '/connections' && method === 'POST') {
      const body = route.request().postDataJSON();
      writes.push({ method, path, body });
      const { secret, accountId, ...rest } = body;
      const saved = { ...base, ...rest, id: `conn-new-${writes.length}`, nodeId: `node-${body.providerId}`, credentialConfigured: true, accountId, settings: secret.providerData };
      connections = [...connections, saved];
      return json(route, 200, saved);
    }
    const patched = path.match(/^\/connections\/([^/]+)$/);
    if (patched && method === 'PATCH') {
      const body = route.request().postDataJSON();
      writes.push({ method, path, body });
      connections = connections.map(c => c.id === patched[1] ? { ...c, name: body.name || c.name, accountId: body.accountId ?? c.accountId, settings: { ...c.settings, ...(body.secret && body.secret.providerData) } } : c);
      return json(route, 200, connections.find(c => c.id === patched[1]));
    }
    if (method === 'GET') return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    return json(route, 404, { error: { code: 'not_found', message: 'not stubbed' } });
  });
  await page.goto('http://127.0.0.1:4173/#providers');
  await page.waitForSelector('.provider-option');
  const open = async id => { await page.fill('#provider-filter', id); await page.locator('.provider-option', { hasText: id }).first().click(); };
  const addForm = () => page.locator('form.provider-form').first();
  const startAdd = async () => { await page.getByRole('button', { name: 'Add connection', exact: true }).click(); };

  // ---- Azure: new connection ---------------------------------------------------------
  await open('azure');
  await startAdd();
  let form = addForm();
  assert.equal(await form.getByLabel('API version').inputValue(), '2024-10-01-preview', 'the API version starts at the default');
  await form.getByLabel('Account name').fill('Prod Azure');
  await form.getByLabel('Account identity').fill('prod@example.test');
  await form.locator('input[type=password]').first().fill(SECRET);
  // A missing endpoint blocks the save: nothing is posted.
  await form.getByRole('button', { name: 'Save connection' }).click();
  await page.waitForTimeout(150);
  assert.equal(writes.length, 0, 'no endpoint, no save');
  await form.getByLabel('Azure endpoint').fill('  https://prod.openai.azure.com  ');
  await form.getByLabel('Deployment name').fill('gpt-4o-prod');
  await form.getByLabel('Organization (optional)').fill('');
  await form.getByRole('button', { name: 'Save connection' }).click();
  await page.getByText('Provider connection saved.').waitFor();
  assert.equal(writes.length, 1);
  const created = writes[0].body;
  assert.equal(created.providerId, 'azure');
  assert.deepEqual(created.secret.providerData, { azureEndpoint: 'https://prod.openai.azure.com', deployment: 'gpt-4o-prod', apiVersion: '2024-10-01-preview' }, 'trimmed, defaults filled, blanks left out');
  assert.equal(created.secret.accessToken, SECRET);

  // ---- Azure: edit shows what is saved and sends only what changed ---------------------
  await page.locator('.connection-row', { hasText: 'Existing Azure' }).getByRole('button', { name: 'Edit', exact: true }).click();
  const edit = page.locator('form[aria-labelledby="connection-edit-title"]');
  await edit.waitFor();
  assert.equal(await edit.getByLabel('Azure endpoint').inputValue(), 'https://old.openai.azure.com');
  assert.equal(await edit.getByLabel('Deployment name').inputValue(), 'dep-a');
  assert.equal(await edit.getByLabel('API version').inputValue(), '2025-01-01');
  await edit.getByLabel('Deployment name').fill('dep-b');
  await edit.getByRole('button', { name: 'Save', exact: true }).click();
  await edit.waitFor({ state: 'detached' });
  assert.deepEqual(writes[1], { method: 'PATCH', path: '/connections/conn-azure', body: { secret: { providerData: { deployment: 'dep-b' } } } });

  // ---- Kimchi, local Ollama ------------------------------------------------------------
  await open('kimchi');
  await startAdd();
  form = addForm();
  await form.getByLabel('Account name').fill('Kimchi');
  await form.getByLabel('Account identity').fill('k@example.test');
  await form.locator('input[type=password]').first().fill(SECRET);
  await form.getByRole('button', { name: 'Save connection' }).click();
  await page.getByText('Provider connection saved.').waitFor();
  assert.equal(writes[2].body.secret.providerData, undefined, 'an empty optional host is not posted');
  await startAdd();
  form = addForm();
  await form.getByLabel('Account name').fill('Kimchi EU');
  await form.getByLabel('Account identity').fill('k2@example.test');
  await form.locator('input[type=password]').first().fill(SECRET);
  await form.getByLabel('Catalog host (optional)').fill('https://kimchi.example.test/');
  await form.getByRole('button', { name: 'Save connection' }).click();
  await page.waitForFunction(() => document.body.innerText.includes('Kimchi EU'));
  assert.deepEqual(writes[3].body.secret.providerData, { kimchiEndpoint: 'https://kimchi.example.test/' });

  await open('ollama-local');
  await startAdd();
  form = addForm();
  assert.ok(await form.getByLabel('Ollama host URL').count() === 1, 'local Ollama asks for its host');
  await form.getByLabel('Ollama host URL').fill('http://192.168.1.20:11434');
  assert.ok((await form.innerText()).includes('trusted-local'), 'the field says a private address needs the operator\'s trusted-local mode');

  // ---- Cloudflare: the account ID is required and is a connection field -----------------
  await open('cloudflare-ai');
  await startAdd();
  form = addForm();
  await form.getByLabel('Account name').fill('CF');
  await form.getByLabel('Account identity').fill('cf2@example.test');
  await form.locator('input[type=password]').first().fill(SECRET);
  const before = writes.length;
  await form.getByRole('button', { name: 'Save connection' }).click();
  await page.waitForTimeout(150);
  assert.equal(writes.length, before, 'no account ID, no save');
  await form.getByLabel('Cloudflare account ID').fill(' acc999 ');
  await form.getByRole('button', { name: 'Save connection' }).click();
  await page.waitForFunction(() => document.body.innerText.includes('acc999') || document.body.innerText.includes('Provider connection saved.'));
  const cf = writes[writes.length - 1].body;
  assert.equal(cf.accountId, 'acc999');
  assert.equal(cf.secret.providerData, undefined, 'the account ID is a connection field, not provider data');
  await page.locator('.connection-row', { hasText: 'Existing Cloudflare' }).getByRole('button', { name: 'Edit', exact: true }).click();
  const cfEdit = page.locator('form[aria-labelledby="connection-edit-title"]');
  assert.equal(await cfEdit.getByLabel('Cloudflare account ID').inputValue(), 'acc123');
  await cfEdit.getByLabel('Cloudflare account ID').fill('acc456');
  await cfEdit.getByRole('button', { name: 'Save', exact: true }).click();
  await cfEdit.waitFor({ state: 'detached' });
  assert.deepEqual(writes[writes.length - 1], { method: 'PATCH', path: '/connections/conn-cf', body: { accountId: 'acc456' } });

  const dom = await page.evaluate(() => document.documentElement.outerHTML + [...document.querySelectorAll('input,textarea')].map(element => element.value).join('\n'));
  assert.ok(!dom.includes(SECRET), 'the credential is gone from the page after saving');
  assert.deepEqual(errors, [], 'page errors');
  await browser.close();
  console.log('endpoint settings: chromium');
})().catch(e => { console.error('FAIL', e); process.exit(1); });
