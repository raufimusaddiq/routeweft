// Browser fixture for credential masking on the specialized imports (#218), run in
// Firefox and Chromium. A pasted or chosen credential file and a bulk paste must
// be masked by every supported browser, not only by a WebKit/Chromium CSS rule, and
// must not appear in the page's visible text until the operator chooses Show. The
// control API is stubbed, so no credential exists. Run by scripts/smoke.sh.
//
// Firefox comes from playwright-core's own build:
//   npx playwright-core install firefox
// or set ROUTEWEFT_SMOKE_FIREFOX to a Firefox binary playwright-core can drive.
const { chromium, firefox } = require('playwright-core');
const assert = require('node:assert/strict');

const SECRET = 'masking-refresh-sentinel';
const BULK_SECRET = 'masking-bulk-sentinel';
const PROVIDERS = [
  { id: 'kiro', transports: ['anthropic'], auth: 'oauth', authModes: ['oauth', 'api-key'], modelCatalog: 'static', passthroughModels: false, reportsUsage: true, flowType: 'device_code', signInModes: ['import'], signInMethods: [{ mode: 'import', authKind: 'api-key', resolvesIdentity: false, metaKeys: ['region'] }], configuredNodes: 0, enabledAccounts: 0, readiness: { status: 'ready', served: true } },
  { id: 'grok-cli', bulkImport: true, transports: ['openai-chat'], auth: 'oauth', authModes: [], modelCatalog: 'static', passthroughModels: false, reportsUsage: true, flowType: 'device_code', signInModes: [], signInMethods: [], configuredNodes: 0, enabledAccounts: 0, readiness: { status: 'ready', served: true } },
];
const EXPORT = JSON.stringify({ auth_method: 'external_idp', access_token: 'masking-access', refresh_token: SECRET, client_id: 'c', profile_arn: 'arn:aws:codewhisperer:us-east-1:0:profile/X', scopes: ['s'], token_endpoint: 'https://login.microsoftonline.com/common/oauth2/v2.0/token' }, null, 2);

async function run(name, launcher, executablePath) {
  const browser = await launcher.launch({ executablePath, ...(name === 'chromium' ? { args: ['--no-sandbox'] } : {}) });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  const imports = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 2, nodes: 0, enabledConnections: 0 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
    if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
    if (path === '/providers') return json(route, 200, { items: PROVIDERS, page: 1, pageSize: 100, total: PROVIDERS.length });
    if (path === '/oauth/kiro/import' && method === 'POST') {
      imports.push(route.request().postDataJSON());
      return json(route, 200, { id: 'conn-kiro', nodeId: 'node-kiro', providerId: 'kiro', name: 'kiro@example.test', authKind: 'oauth', identity: 'kiro@example.test', enabled: true, priority: 0, credentialConfigured: true, outcome: 'added' });
    }
    if (method === 'GET') return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    return json(route, 404, { error: { code: 'not_found', message: 'not stubbed' } });
  });
  await page.goto('http://127.0.0.1:4173/#providers');
  await page.waitForSelector('.provider-option');
  const form = page.locator('.provider-form');
  const visible = async () => form.evaluate(element => element.innerText);

  // A chosen file: masked by the browser's own password field, absent from the
  // visible text, shown only on request, and sent with its line breaks.
  await page.fill('#provider-filter', 'kiro');
  await page.locator('.provider-option', { hasText: 'kiro' }).first().click();
  await page.getByRole('button', { name: 'Add connection', exact: true }).click();
  await form.waitFor();
  await form.getByLabel('CLIProxyAPI export (Microsoft)').check();
  await form.getByLabel('Stable account identity').fill('kiro@example.test');
  await form.locator('input[type="file"]').setInputFiles({ name: 'kiro-cliproxy.json', mimeType: 'application/json', buffer: Buffer.from(EXPORT) });
  await form.getByText(/^Loaded kiro-cliproxy\.json/).waitFor();
  const field = form.getByLabel('CLIProxyAPI auth file (JSON)');
  assert.equal(await field.evaluate(element => element.tagName + ':' + element.type), 'INPUT:password', `${name}: a chosen file is in a password field`);
  assert.ok(!(await visible()).includes(SECRET), `${name}: the masked file is not in the visible text`);
  await form.getByRole('button', { name: 'Show', exact: true }).click();
  assert.equal(await form.getByLabel('CLIProxyAPI auth file (JSON)').evaluate(element => element.tagName), 'TEXTAREA', `${name}: Show reveals the file`);
  assert.ok((await form.getByLabel('CLIProxyAPI auth file (JSON)').inputValue()).includes(SECRET));
  await form.getByRole('button', { name: 'Hide', exact: true }).click();
  assert.equal(await form.getByLabel('CLIProxyAPI auth file (JSON)').evaluate(element => element.type), 'password', `${name}: Hide masks it again`);
  // A paste into the masked field is masked as it is typed.
  await form.getByRole('radio', { name: 'Kiro IDE token file', exact: true }).check();
  await form.getByLabel('Token file (JSON)').fill(`{"refreshToken":"${SECRET}"}`);
  assert.equal(await form.getByLabel('Token file (JSON)').evaluate(element => element.type), 'password', `${name}: a paste is masked`);
  assert.ok(!(await visible()).includes(SECRET), `${name}: the paste is not in the visible text`);
  await form.getByRole('radio', { name: 'CLIProxyAPI export (Microsoft)', exact: true }).check();
  await form.locator('input[type="file"]').setInputFiles({ name: 'kiro-cliproxy.json', mimeType: 'application/json', buffer: Buffer.from(EXPORT) });
  await form.getByRole('button', { name: 'Import credential', exact: true }).click();
  await form.getByText('Connected', { exact: true }).waitFor();
  assert.equal(imports.length, 1);
  assert.equal(imports[0].input, EXPORT, `${name}: the file is sent as chosen, line breaks included`);
  await form.getByRole('button', { name: 'Done', exact: true }).click();
  await form.waitFor({ state: 'detached' });

  // The bulk paste is masked the same way.
  await page.fill('#provider-filter', 'grok-cli');
  await page.locator('.provider-option', { hasText: 'grok-cli' }).first().click();
  await page.getByRole('button', { name: 'Add connection', exact: true }).click();
  await form.waitFor();
  await form.getByRole('radio', { name: 'Bulk import accounts', exact: true }).check();
  await form.getByLabel('Exported accounts (JSON)').fill(`[{"access_token":"${BULK_SECRET}"}]`);
  assert.equal(await form.getByLabel('Exported accounts (JSON)').evaluate(element => element.type), 'password', `${name}: the bulk paste is masked`);
  assert.ok(!(await visible()).includes(BULK_SECRET), `${name}: the bulk paste is not in the visible text`);
  await form.getByRole('button', { name: 'Close', exact: true }).click();
  assert.deepEqual(errors, [], `${name}: page errors`);
  await browser.close();
  return name;
}

(async () => {
  const done = [];
  done.push(await run('firefox', firefox, process.env.ROUTEWEFT_SMOKE_FIREFOX || undefined));
  done.push(await run('chromium', chromium, process.env.ROUTEWEFT_SMOKE_CHROME || '/root/.cache/ms-playwright/chromium-1148/chrome-linux/chrome'));
  console.log(`import masking: ${done.join(', ')}`);
})().catch(e => { console.error('FAIL', e); process.exit(1); });
