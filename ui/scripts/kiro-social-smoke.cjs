// Browser fixture for Kiro's Google and GitHub sign-in (#219): the two methods
// are real choices beside the AWS SSO device code, each fixed to its provider
// (no field to retype it), through a cancel, a denial, a paste from another
// attempt and a success; Reauthenticate is offered only on a connection a social
// sign-in made and renews it with that provider. The control API is stubbed, so
// no provider is contacted and no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');

const METHODS = [
  { mode: 'device', authKind: 'oauth', resolvesIdentity: false, metaKeys: ['region', 'startUrl', 'authMethod'] },
  { mode: 'browser', authKind: 'oauth', resolvesIdentity: false, variant: 'google', completion: 'redirect-url' },
  { mode: 'browser', authKind: 'oauth', resolvesIdentity: false, variant: 'github', completion: 'redirect-url' },
  { mode: 'import', authKind: 'api-key', resolvesIdentity: false },
];
const SENTINELS = ['kiro-code-sentinel'];
const LOGIN = 'https://prod.us-east-1.auth.desktop.kiro.dev/login';
const paste = (state, extra = 'code=kiro-code-sentinel') => `kiro://kiro.kiroAgent/authenticate-success?${extra}&state=${state}`;

(async () => {
  const chrome = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || '/root/.cache/ms-playwright/chromium-1148/chrome-linux/chrome', args: ['--no-sandbox'] });
  const page = await chrome.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  page.on('console', message => { if (message.type() === 'error' && !message.text().startsWith('Failed to load resource')) errors.push('console: ' + message.text()); });
  const artifacts = process.env.ROUTEWEFT_SMOKE_ARTIFACTS || '/tmp/routeweft-ui-evidence';
  mkdirSync(artifacts, { recursive: true });
  const report = {};

  let states = 0;
  const callbackModes = [];
  const starts = [];
  const callbacks = [];
  const cancels = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const fail = (route, status, code, message) => json(route, status, { error: { code, message } });
  const providerItems = [{ id: 'kiro', transports: ['kiro'], auth: 'oauth', authModes: ['oauth', 'api-key'], modelCatalog: 'dynamic', passthroughModels: false, reportsUsage: false, flowType: 'device_code', signInModes: METHODS.map(m => m.mode), signInMethods: METHODS, configuredNodes: 1, enabledAccounts: 3, readiness: { status: 'ready', served: true } }];
  const row = (id, name, signInMethod) => ({ id, nodeId: 'node-kiro', providerId: 'kiro', name, authKind: 'oauth', identity: `${id}@example.test`, enabled: true, priority: 0, credentialConfigured: true, signInMethod });
  const connections = [row('conn-github', 'GitHub account', 'github'), row('conn-builder', 'Builder account', 'builder-id'), row('conn-imported', 'Imported account', 'imported')];

  await page.context().route('https://prod.us-east-1.auth.desktop.kiro.dev/**', route => route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>kiro stub</title>' }));
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 1, nodes: 1, enabledConnections: 3 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
    if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
    if (path === '/providers') return json(route, 200, { items: providerItems, page: 1, pageSize: 100, total: providerItems.length });
    if (path === '/connections' && method === 'GET') return json(route, 200, { items: connections, page: 1, pageSize: 100, total: connections.length });
    const match = path.match(/^\/oauth\/kiro\/(start|callback|cancel)$/);
    if (match && method === 'POST') {
      const body = route.request().postDataJSON();
      if (match[1] === 'start') {
        starts.push(body);
        states += 1;
        const state = `kiro-state-${states}`;
        const idp = body.meta.authMethod === 'github' ? 'Github' : 'Google';
        return json(route, 200, { state, authorizeUrl: `${LOGIN}?idp=${idp}&state=${state}`, completion: 'redirect-url', flowType: 'authorization_code_pkce', expiresAt: '2026-10-27T12:10:00Z' });
      }
      if (match[1] === 'cancel') { cancels.push(body); return json(route, 200, { status: 'cancelled' }); }
      callbacks.push(body);
      const mode = callbackModes.shift();
      if (mode === 'mismatch') return fail(route, 400, 'oauth_state_mismatch', 'this address is from a different sign-in attempt; paste the one from this attempt');
      if (mode === 'denied') return fail(route, 400, 'oauth_denied', 'the provider did not approve the sign-in; start again');
      const start = starts.at(-1);
      if (start.connectionId) return json(route, 200, { ...connections[0], outcome: 'reauthenticated' });
      return json(route, 200, { ...row('conn-new', start.identity, start.meta.authMethod), identity: start.identity, outcome: 'added' });
    }
    if (method === 'GET') return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    return fail(route, 404, 'not_found', 'not stubbed');
  });
  await page.clock.setFixedTime(new Date('2026-10-27T12:00:00Z'));
  await page.goto('http://127.0.0.1:4173/#providers');
  await page.waitForSelector('.provider-option');
  await page.evaluate(() => { window.open = () => null; });
  await page.fill('#provider-filter', 'kiro');
  await page.locator('.provider-option', { hasText: 'kiro' }).first().click();

  const form = page.locator('.provider-form');
  const result = form.locator('[aria-label="Sign-in result"]');
  const banner = page.locator('.providers-content > .auth-error');
  const pasteField = form.getByLabel('Address from the browser', { exact: true });
  async function open(label) {
    await page.getByRole('button', { name: 'Add connection', exact: true }).click();
    await form.waitFor();
    await form.getByRole('radio', { name: label, exact: true }).check();
    return form.getByRole('button', { name: label, exact: true });
  }
  async function close() {
    await form.getByRole('button', { name: 'Close', exact: true }).click();
    await form.waitFor({ state: 'detached' });
  }

  // The choices are the server's methods: device code, Google, GitHub, import.
  await page.getByRole('button', { name: 'Add connection', exact: true }).click();
  report.methods = await form.getByRole('radio').evaluateAll(nodes => nodes.map(node => node.parentElement.textContent));
  for (const label of ['Sign in with Kiro (device code)', 'Sign in with Google', 'Sign in with GitHub']) assert.ok(report.methods.includes(label), `${label} offered: ${report.methods}`);
  await close();

  for (const [variant, label, idp] of [['google', 'Sign in with Google', 'Google'], ['github', 'Sign in with GitHub', 'Github']]) {
    const seen = {};
    let start = await open(label);
    // Fixed to its provider: no AWS SSO method select, and the account is asked.
    assert.equal(await form.getByLabel('AWS SSO method').count(), 0, `${variant}: no method field`);
    seen.before = await form.locator('.journey-notes').textContent();
    assert.match(seen.before, new RegExp(`signs you in with ${variant === 'github' ? 'GitHub' : 'Google'}.*kiro://kiro\\.kiroAgent/authenticate-success.*nothing on this server receives it`, 's'));
    assert.equal(await start.isDisabled(), true, `${variant}: waits for the account`);
    await form.getByLabel('Stable account identity').fill(`named-${variant}@example.test`);
    await form.screenshot({ path: join(artifacts, `kiro-${variant}-before.png`) });

    // Cancel.
    await start.click();
    await form.getByText('Pending', { exact: true }).waitFor();
    assert.deepEqual(starts.at(-1), { identity: `named-${variant}@example.test`, meta: { authMethod: variant } });
    seen.hint = await form.locator('.oauth-panel p').first().textContent();
    assert.match(seen.hint, /kiro:\/\/kiro\.kiroAgent\/authenticate-success\?code=…&state=…/);
    assert.equal(await pasteField.getAttribute('type'), 'text');
    assert.equal(await form.getByRole('link', { name: 'Open Kiro sign-in page' }).getAttribute('href'), `${LOGIN}?idp=${idp}&state=kiro-state-${states}`);
    await form.getByRole('button', { name: 'Cancel sign-in', exact: true }).click();
    await result.getByText('Cancelled', { exact: true }).waitFor();
    assert.deepEqual(cancels.at(-1), { state: `kiro-state-${states}` });

    // Denial ends it; nothing is saved.
    await form.getByLabel('Stable account identity').fill(`named-${variant}@example.test`);
    callbackModes.push('denied');
    await start.click();
    await pasteField.fill(paste(`kiro-state-${states}`, 'error=access_denied'));
    await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
    await result.getByText('Failed', { exact: true }).waitFor();
    assert.match(await result.textContent(), /did not approve the sign-in/);

    // A paste from another attempt keeps it; the right one connects.
    await form.getByLabel('Stable account identity').fill(`named-${variant}@example.test`);
    callbackModes.push('mismatch');
    await start.click();
    await pasteField.fill(paste('kiro-state-other'));
    await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
    await banner.filter({ hasText: 'different sign-in attempt' }).waitFor();
    assert.equal(await pasteField.count(), 1, `${variant}: a wrong-state paste keeps the attempt`);
    await pasteField.fill(paste(`kiro-state-${states}`));
    await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
    await result.getByText('Connected', { exact: true }).waitFor();
    assert.deepEqual(callbacks.at(-1), { state: `kiro-state-${states}`, input: paste(`kiro-state-${states}`) });
    assert.match(await form.locator('[aria-label="Signed-in account"]').textContent(), new RegExp(`named-${variant}@example\\.test`));
    seen.connected = await result.textContent();
    await form.getByRole('button', { name: 'Done', exact: true }).click();
    await form.waitFor({ state: 'detached' });
    report[variant] = seen;
  }

  // Reauthenticate: only the GitHub connection offers it, and it renews with GitHub.
  const rowFor = name => page.locator('.connection-row', { hasText: name });
  assert.equal(await rowFor('Builder account').getByRole('button', { name: 'Reauthenticate' }).count(), 0, 'no Reauthenticate on a Builder ID connection');
  assert.equal(await rowFor('Imported account').getByRole('button', { name: 'Reauthenticate' }).count(), 0, 'no Reauthenticate on an imported connection');
  await rowFor('GitHub account').getByRole('button', { name: 'Reauthenticate', exact: true }).click();
  await form.getByRole('heading', { name: 'Reauthenticate “GitHub account”' }).waitFor();
  assert.equal(await form.getByRole('radio', { name: 'Sign in with GitHub', exact: true }).isChecked(), true);
  await form.getByRole('button', { name: 'Reauthenticate', exact: true }).click();
  await form.getByText('Pending', { exact: true }).waitFor();
  assert.deepEqual(starts.at(-1), { connectionId: 'conn-github', meta: { authMethod: 'github' } });
  await pasteField.fill(paste(`kiro-state-${states}`));
  await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
  await result.getByText('Connected', { exact: true }).waitFor();
  assert.match(await result.textContent(), /Reauthenticated “GitHub account”/);
  await form.getByRole('button', { name: 'Done', exact: true }).click();
  await form.waitFor({ state: 'detached' });

  const html = await page.content();
  for (const sentinel of SENTINELS) assert.equal(html.includes(sentinel), false, `${sentinel} stayed in the DOM`);
  assert.deepEqual(errors, []);
  report.starts = starts.length;
  report.callbacks = callbacks.length;
  report.cancels = cancels.length;
  writeFileSync(join(artifacts, 'kiro-social-report.json'), JSON.stringify(report, null, 2) + '\n');
  console.log(`kiro social: ${starts.length} starts, ${callbacks.length} callbacks, ${cancels.length} cancels`);
  await chrome.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
