// Browser fixture for the provider-aware device approval screens (#217): one run
// per device provider through its notes and preconditions, the code and link
// with copy, a slow_down that lengthens the interval, a cancel that stops
// polling, transient failures then an approval of an already connected account,
// an expired code, and a server that never answers past the deadline. The
// control API is stubbed and the clock is fake, so no provider is contacted and
// no credential exists. Run by scripts/smoke.sh after the OAuth journeys smoke.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');

const device = (extra = {}) => ({ mode: 'device', authKind: 'oauth', resolvesIdentity: false, ...extra });
// The read model each provider's server sends (signInMethods for device flows).
const PROVIDERS = {
  github: { label: 'GitHub Copilot', methods: [device()], identity: 'required', code: 'GHUB-1234', before: /mints the Copilot token requests use/, copilotMissing: true },
  'grok-cli': { label: 'Grok CLI', methods: [device()], identity: 'required', code: 'GROK-1234', before: /Grok CLI device sign-in on auth\.x\.ai/ },
  kimi: { label: 'Kimi', methods: [device()], identity: 'required', code: 'KIMI-1234', before: /Kimi does not report which account approved/ },
  kilocode: { label: 'Kilo Code', methods: [device({ infersIdentity: true })], identity: 'optional', code: 'KILO-1234', before: /email and organization.*free models are listed live/s },
  'codebuddy-cn': { label: 'CodeBuddy (CN)', methods: [device()], identity: 'required', code: '', before: /CodeBuddy China \(copilot\.tencent\.com\).*CodeBuddy \(Intl\) instead/s },
  'codebuddy-intl': { label: 'CodeBuddy (Intl)', methods: [device()], identity: 'required', code: '', before: /CodeBuddy international \(www\.codebuddy\.ai\).*CodeBuddy \(CN\) instead/s },
  kiro: { label: 'Kiro', methods: [device({ metaKeys: ['region', 'startUrl', 'authMethod'] })], identity: 'required', code: 'KIRO-1234', before: /AWS Builder ID/, idc: true },
  qoder: { label: 'Qoder', methods: [device()], identity: 'required', code: 'QODER123', before: /does not make Qoder served/, readiness: { status: 'unavailable', served: false, reason: 'fixture' } },
};

(async () => {
  const chrome = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || '/root/.cache/ms-playwright/chromium-1148/chrome-linux/chrome', args: ['--no-sandbox'] });
  const context = await chrome.newContext({ viewport: { width: 1440, height: 900 } });
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: 'http://127.0.0.1:4173' });
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  // The scripted refusals are logged by the browser as failed loads.
  page.on('console', message => { if (message.type() === 'error' && !message.text().startsWith('Failed to load resource')) errors.push('console: ' + message.text()); });
  const report = { providers: {} };
  const artifacts = process.env.ROUTEWEFT_SMOKE_ARTIFACTS || '/tmp/routeweft-ui-evidence';
  mkdirSync(artifacts, { recursive: true });

  const NOW = Date.parse('2026-10-27T12:00:00Z');
  // Far enough ahead that the fake clock, advanced by every run, never reaches it.
  const LATER = '2026-10-28T12:00:00Z';
  let states = 0;
  // What the next polls answer, in order; an empty queue answers pending.
  const pollScript = [];
  // Requests the stub accepts and never answers ({ hang: true } in pollScript,
  // hangStart, hangCancel), released only when the run ends.
  const hung = [];
  let hangStart = false;
  let hangCancel = false;
  let expiresAt = LATER;
  const starts = [];
  const polls = [];
  const cancels = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const fail = (route, status, code, message) => json(route, status, { error: { code, message } });
  const providerItems = Object.entries(PROVIDERS).map(([id, p]) => ({ id, transports: ['openai-chat'], auth: 'oauth', authModes: ['oauth'], modelCatalog: 'dynamic', passthroughModels: true, reportsUsage: false, flowType: 'device_code', signInModes: ['device'], signInMethods: p.methods, metaKeys: p.methods[0].metaKeys, configuredNodes: 1, enabledAccounts: 1, readiness: p.readiness || { status: 'ready', served: true } }));
  const existing = id => ({ id: `conn-${id}`, nodeId: `node-${id}`, providerId: id, name: `Existing ${id}`, authKind: 'oauth', identity: `named@${id}.example.test`, enabled: true, priority: 0, credentialConfigured: true });
  const connections = Object.keys(PROVIDERS).map(existing);

  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 8, nodes: 8, enabledConnections: 8 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
    if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
    if (path === '/providers') return json(route, 200, { items: providerItems, page: 1, pageSize: 100, total: providerItems.length });
    if (path === '/connections' && method === 'GET') return json(route, 200, { items: connections, page: 1, pageSize: 100, total: connections.length });
    const match = path.match(/^\/oauth\/([^/]+)\/(device|poll|cancel)$/);
    if (match && method === 'POST') {
      const [, provider, step] = match;
      const body = route.request().postDataJSON();
      if (step === 'device' && hangStart) { starts.push({ provider, body }); hung.push(route); return new Promise(() => {}); }
      if (step === 'cancel' && hangCancel) { cancels.push({ provider, body }); hung.push(route); return new Promise(() => {}); }
      if (step === 'device') {
        starts.push({ provider, body });
        states += 1;
        const p = PROVIDERS[provider];
        return json(route, 200, { state: `device-state-${states}`, flowType: 'device_code', userCode: p.code, verificationUrl: `https://device.example.test/${provider}?code=${p.code}`, intervalSeconds: 5, expiresAt });
      }
      if (step === 'cancel') { cancels.push({ provider, body }); return json(route, 200, { status: 'cancelled' }); }
      polls.push({ provider, body });
      const next = pollScript.shift() || { status: 'pending', intervalSeconds: 5 };
      if (next.hang) { hung.push(route); return new Promise(() => {}); }
      if (next.error) return fail(route, next.http, next.error, next.message);
      if (next.status === 'connected') {
        const p = PROVIDERS[provider];
        return json(route, 200, { status: 'connected', connection: { ...existing(provider), outcome: 'updated', ...(provider === 'github' ? { copilotMissing: Boolean(p.copilotMissing) } : {}) } });
      }
      return json(route, 200, next);
    }
    if (method === 'GET') return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    return fail(route, 404, 'not_found', 'not stubbed');
  });
  await page.clock.install({ time: new Date(NOW) });
  await page.goto('http://127.0.0.1:4173/#providers');
  await page.waitForSelector('.provider-option').catch(async error => { await page.screenshot({ path: join(artifacts, 'device-approval-boot.png') }); console.error(errors, await page.locator('body').innerText()); throw error; });

  const form = page.locator('.provider-form');
  const result = form.locator('[aria-label="Sign-in result"]');
  const pollsFor = id => polls.filter(p => p.provider === id).length;
  // Advances the fake clock until the provider has been polled n times in all.
  async function pollUntil(id, n, step = 5000) {
    for (let i = 0; i < 40 && pollsFor(id) < n; i++) {
      await page.clock.runFor(step);
      await page.waitForTimeout(20);
    }
    assert.equal(pollsFor(id), n, `${id}: expected ${n} polls`);
  }
  async function select(id) {
    await page.fill('#provider-filter', id);
    await page.locator('.provider-option', { hasText: id }).first().click();
  }
  async function open(id, p) {
    await page.getByRole('button', { name: 'Add connection', exact: true }).click();
    await form.waitFor();
    const start = form.getByRole('button', { name: `Sign in with ${p.label} (device code)`, exact: true });
    await start.waitFor();
    return start;
  }
  async function fill(id, p) {
    if (p.identity === 'required') await form.getByLabel('Stable account identity').fill(`named@${id}.example.test`);
    if (p.idc) {
      await form.getByLabel('AWS SSO method').selectOption('idc');
      await form.getByLabel('AWS SSO start URL').fill('https://fixture.awsapps.com/start');
    }
  }
  async function close() {
    if (await form.count()) await form.getByRole('button', { name: 'Close', exact: true }).click();
    await form.waitFor({ state: 'detached' });
  }

  for (const [id, p] of Object.entries(PROVIDERS)) {
    const seen = {};
    await select(id);
    let start = await open(id, p);
    // Before the start: the provider's notes, the identity it needs, and Kiro's
    // Identity Center start URL.
    seen.before = await form.locator('.journey-notes').textContent();
    assert.match(seen.before, p.before, `${id}: notes before approval`);
    assert.match(seen.before, /already connected renews that connection/, `${id}: duplicate rule stated`);
    if (p.identity === 'required') {
      assert.equal(await start.isDisabled(), true, `${id}: cannot start without an identity`);
      await form.getByLabel('Stable account identity').fill(`named@${id}.example.test`);
    } else {
      assert.equal(await form.getByLabel('Account identity (optional)').count(), 1, `${id}: identity is optional`);
    }
    if (p.idc) {
      await form.getByLabel('AWS SSO method').selectOption('idc');
      await form.getByText('Enter the AWS SSO start URL above to start.').waitFor();
      assert.match(await form.locator('.journey-notes').textContent(), /IAM Identity Center/);
      assert.equal(await start.isDisabled(), true, 'kiro idc waits for the start URL');
      await form.getByLabel('AWS SSO start URL').fill('https://fixture.awsapps.com/start');
    }
    assert.equal(await start.isDisabled(), false, `${id}: can start`);
    await form.screenshot({ path: join(artifacts, `device-${id}-before.png`) });

    // Code, link and copy; slow_down lengthens the interval; cancel stops polling.
    pollScript.push({ status: 'pending', intervalSeconds: 10 });
    await start.click();
    await form.getByText('Pending', { exact: true }).waitFor();
    if (id === 'kiro') assert.deepEqual(starts.at(-1).body.meta, { authMethod: 'idc', startUrl: 'https://fixture.awsapps.com/start' });
    const link = `https://device.example.test/${id}?code=${p.code}`;
    assert.equal(await form.getByLabel('Sign-in page address', { exact: true }).textContent(), link);
    assert.equal(await form.getByRole('link', { name: `Open ${p.label} sign-in page` }).getAttribute('href'), link);
    if (p.code) {
      assert.equal(await form.getByLabel('Device code', { exact: true }).textContent(), p.code);
      await form.getByRole('button', { name: 'Copy code', exact: true }).click();
      await form.getByRole('button', { name: 'Copy code', exact: true }).filter({ hasText: 'Copied' }).waitFor();
      assert.equal(await page.evaluate(() => navigator.clipboard.readText()), p.code, `${id}: code copied`);
    } else {
      assert.equal(await form.getByLabel('Device code', { exact: true }).count(), 0, `${id}: no code to show`);
      assert.match(await form.locator('.oauth-panel p').first().textContent(), /shows no code to confirm/);
    }
    await form.getByRole('button', { name: 'Copy sign-in page', exact: true }).click();
    await form.getByRole('button', { name: 'Copy sign-in page', exact: true }).filter({ hasText: 'Copied' }).waitFor();
    assert.equal(await page.evaluate(() => navigator.clipboard.readText()), link, `${id}: link copied`);
    const before = pollsFor(id);
    await pollUntil(id, before + 1);
    await form.getByText(/asked this dashboard to check less often; it now checks every 10 seconds/).waitFor();
    // The next poll waits the new interval: five seconds is not enough.
    await page.clock.runFor(5000); await page.waitForTimeout(20);
    assert.equal(pollsFor(id), before + 1, `${id}: polled before the lengthened interval`);
    await pollUntil(id, before + 2, 5000);
    await form.screenshot({ path: join(artifacts, `device-${id}-pending.png`) });
    await form.getByRole('button', { name: 'Cancel sign-in', exact: true }).click();
    await result.getByText('Cancelled', { exact: true }).waitFor();
    assert.deepEqual(cancels.at(-1), { provider: id, body: { state: `device-state-${states}` } });
    const afterCancel = pollsFor(id);
    await page.clock.runFor(60000); await page.waitForTimeout(20);
    assert.equal(pollsFor(id), afterCancel, `${id}: polled after cancel`);

    // Transient failures are retried; the approval of an already connected
    // account reports that its connection was renewed.
    await fill(id, p);
    pollScript.push({ error: 'oauth_poll_failed', http: 502, message: 'the provider could not be reached; the sign-in is still open' }, { error: 'oauth_poll_failed', http: 502, message: 'the provider could not be reached; the sign-in is still open' }, { status: 'connected' });
    await start.click();
    await form.getByText('Pending', { exact: true }).waitFor();
    await pollUntil(id, afterCancel + 3);
    await result.getByText('Connected', { exact: true }).waitFor();
    seen.connected = await result.textContent();
    assert.match(seen.connected, /already connected as “Existing/, `${id}: duplicate renewal reported`);
    if (id === 'github') assert.match(seen.connected, /GitHub issued no Copilot token for this account/);
    else assert.doesNotMatch(seen.connected, /Copilot token/);
    await form.screenshot({ path: join(artifacts, `device-${id}-connected.png`) });
    await form.getByRole('button', { name: 'Done', exact: true }).click();
    await form.waitFor({ state: 'detached' });

    // An expired code ends the attempt as expired.
    start = await open(id, p);
    await fill(id, p);
    pollScript.push({ error: 'oauth_expired', http: 400, message: 'the sign-in code expired; start again' });
    const beforeExpiry = pollsFor(id);
    await start.click();
    await pollUntil(id, beforeExpiry + 1);
    await result.getByText('Expired', { exact: true }).waitFor();

    // A start the server accepts and never answers is given up after 30 s; the
    // button is free again and no attempt is shown.
    await fill(id, p);
    hangStart = true;
    await start.click();
    await form.getByRole('button', { name: 'Starting…', exact: true }).waitFor();
    await page.clock.runFor(30000);
    await page.getByText('The server did not answer the sign-in start in time; try again.').waitFor();
    hangStart = false;
    assert.equal(await form.getByText('Pending', { exact: true }).count(), 0, `${id}: a hung start showed an attempt`);
    assert.equal(await start.isDisabled(), false, `${id}: the start button stayed busy`);

    // Polls the server accepts and never answer are aborted after 30 s each and
    // count as no answer: five of them pause polling, which then stays paused.
    const beforeHang = pollsFor(id);
    pollScript.push(...Array.from({ length: 5 }, () => ({ hang: true })));
    await start.click();
    await form.getByText('Pending', { exact: true }).waitFor();
    await pollUntil(id, beforeHang + 5);
    await page.clock.runFor(30000);
    await form.getByText(/The server or the provider could not be reached/).waitFor();
    const paused = pollsFor(id);
    await page.clock.runFor(180000); await page.waitForTimeout(20);
    assert.equal(pollsFor(id), paused, `${id}: kept polling after hung polls`);
    // A cancel the server never answers is no answer either: the attempt stays
    // and the operator can cancel again.
    hangCancel = true;
    await form.getByRole('button', { name: 'Cancel sign-in', exact: true }).click();
    await form.getByRole('button', { name: 'Cancelling…', exact: true }).waitFor();
    await page.clock.runFor(30000);
    await form.getByText(/could not be cancelled \(no answer from the server\)/).waitFor();
    hangCancel = false;
    await form.getByRole('button', { name: 'Cancel sign-in', exact: true }).click();
    await result.getByText('Cancelled', { exact: true }).waitFor();

    // Past the deadline with a poll the server never answers: the poll is
    // aborted, and the browser stops on its own and offers to check again.
    await fill(id, p);
    expiresAt = new Date(NOW - 10 * 60 * 1000).toISOString();
    const beforeDeadline = pollsFor(id);
    pollScript.push({ hang: true });
    await start.click();
    await pollUntil(id, beforeDeadline + 1);
    await page.clock.runFor(30000);
    await page.clock.runFor(5000);
    await form.getByText(/The time limit passed without an answer from the server/).waitFor();
    const stalled = pollsFor(id);
    assert.equal(stalled, beforeDeadline + 1, `${id}: polled again past the deadline`);
    await page.clock.runFor(120000); await page.waitForTimeout(20);
    assert.equal(pollsFor(id), stalled, `${id}: kept polling past the deadline`);
    // Check again asks the server once more; here it says the attempt is gone.
    pollScript.push({ error: 'oauth_state_invalid', http: 400, message: 'this sign-in attempt expired or was already used; start again' });
    await form.getByRole('button', { name: 'Check again', exact: true }).click();
    await pollUntil(id, stalled + 1);
    await result.getByText('Closed', { exact: true }).waitFor();
    expiresAt = LATER;
    await close();
    report.providers[id] = seen;
  }

  // Polls never carry anything but the opaque state.
  for (const poll of polls) assert.deepEqual(Object.keys(poll.body), ['state']);
  report.starts = starts.length;
  report.polls = polls.length;
  report.cancels = cancels.length;
  report.hung = hung.length;
  // Per provider: one start, five polls, one cancel and one past-deadline poll never answered.
  assert.equal(hung.length, Object.keys(PROVIDERS).length * 8);
  assert.deepEqual(errors, []);
  writeFileSync(join(artifacts, 'device-approval-report.json'), JSON.stringify(report, null, 2) + '\n');
  console.log(`device approval: ${Object.keys(report.providers).length} providers, ${starts.length} starts, ${polls.length} polls, ${cancels.length} cancels, ${hung.length} requests never answered`);
  for (const route of hung) await route.abort().catch(() => {});
  await chrome.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
