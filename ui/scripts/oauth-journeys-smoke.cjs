// Browser fixture for the provider-aware sign-in journeys (#216): one run per
// browser method beyond Codex, each through a wrong-state paste, an expired
// attempt, a cancel, the identity the provider may or may not report, and a
// Reauthenticate refused for another account and then accepted. The control API
// and every provider page are stubbed, so no provider is contacted and no
// credential exists. Run by scripts/smoke.sh after the shell smoke.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');

const ORIGIN = 'https://routeweft.example.test';
const browser = (completion, extra = {}) => ({ mode: 'browser', authKind: 'oauth', resolvesIdentity: false, completion, ...extra });
// The read model each provider's server sends (TestBrowserMethodsDescribeWhereTheBrowserReturns).
const PROVIDERS = {
  claude: { label: 'Claude', methods: [browser('code')], identity: 'required', paste: s => `claude-code-sentinel#${s}`, pasteLabel: 'Code from Claude', hint: /ends in #state/ },
  xai: { label: 'xAI', methods: [browser('redirect-url', { infersIdentity: true, loopbackRedirect: 'http://127.0.0.1:56121/callback' })], identity: 'optional', paste: s => `http://127.0.0.1:56121/callback?code=xai-code-sentinel&state=${s}`, pasteLabel: 'Address or code from xAI', before: /56121 is on the computer running this browser, not on the Routeweft server, and Routeweft does not listen on it/, hint: /If xAI shows a code instead of redirecting, paste that code/ },
  'gemini-cli': { label: 'Gemini CLI', methods: [browser('code', { infersIdentity: true, redirectUri: `${ORIGIN}/callback` })], identity: 'optional', paste: s => `${ORIGIN}/callback?code=gemini-code-sentinel&state=${s}`, pasteLabel: 'Address or code from Google', before: /Code Assist project, which every request needs/, hint: /returns to https:\/\/routeweft\.example\.test\/callback\?code=…/, project: '' },
  antigravity: { label: 'Antigravity', methods: [browser('code', { infersIdentity: true, redirectUri: `${ORIGIN}/callback` })], identity: 'optional', paste: s => `${ORIGIN}/callback?code=ag-code-sentinel&state=${s}`, pasteLabel: 'Address or code from Google', before: /finishes the account’s Code Assist onboarding in the background/, project: 'fixture-project' },
  gitlab: { label: 'GitLab Duo', methods: [browser('code', { resolvesIdentity: true, metaKeys: ['clientId', 'clientSecret', 'baseUrl'], redirectUri: `${ORIGIN}/callback` }), { mode: 'import', authKind: 'oauth', resolvesIdentity: true, metaKeys: ['baseUrl'] }], identity: 'resolved', paste: s => `${ORIGIN}/callback?code=gitlab-code-sentinel&state=${s}`, pasteLabel: 'Address or code from GitLab', checked: /GitLab named the account from \/api\/v4\/user/, before: /redirect URI https:\/\/routeweft\.example\.test\/callback and the scopes api and read_user.*public application \(Confidential unchecked\) needs only its client id.*confidential application also needs its client secret.*Import personal access token/s, needsClientId: true },
  cline: { label: 'Cline', methods: [browser('code', { infersIdentity: true, redirectUri: `${ORIGIN}/callback` })], identity: 'optional', paste: s => `${ORIGIN}/callback?code=cline-code-sentinel&state=${s}`, pasteLabel: 'Address or code from Cline', before: /WorkOS.*workos: prefix/ },
  clinepass: { label: 'ClinePass', methods: [browser('code', { infersIdentity: true, redirectUri: `${ORIGIN}/callback` })], identity: 'optional', paste: s => `${ORIGIN}/callback?code=clinepass-code-sentinel&state=${s}`, pasteLabel: 'Address or code from Cline', before: /ClinePass uses the same Cline account sign-in/ },
  iflow: { label: 'iFlow', authModes: ['oauth', 'cookie'], methods: [browser('code', { resolvesIdentity: true, redirectUri: `${ORIGIN}/callback` }), { mode: 'import', authKind: 'cookie', resolvesIdentity: true }], identity: 'resolved', paste: s => `${ORIGIN}/callback?code=iflow-code-sentinel&state=${s}`, pasteLabel: 'Address or code from iFlow', before: /reads this account’s iFlow API key/, checked: /iFlow API key was read before the connection was saved/ },
  kimchi: { label: 'Kimchi', auth: 'api-key', authModes: ['api-key', 'oauth'], methods: [browser('token', { infersIdentity: true, redirectUri: `${ORIGIN}/callback` })], identity: 'optional', paste: s => `${ORIGIN}/callback?token=kimchi-token-sentinel&state=${s}`, pasteLabel: 'Address or token from Kimchi', masked: true, before: /checked with the Kimchi gateway before it is stored/, checked: /Kimchi gateway accepted the token/ },
  'xiaomi-mimo': { label: 'Xiaomi MiMo', methods: [browser('callback', { infersIdentity: true, metaKeys: ['keyName'], redirectUri: `${ORIGIN}/` })], identity: 'optional', paste: () => `${ORIGIN}/?u=xiaomi-encrypted-sentinel`, pasteLabel: 'Address from the browser', before: /encrypted to a key this server made for this sign-in alone/, hint: /\?u=…/ },
  zed: { label: 'Zed', methods: [browser('callback', { infersIdentity: true, metaKeys: ['nativeAppPort'] })], identity: 'optional', paste: () => 'http://127.0.0.1:58443/?user_id=7&access_token=zed-encrypted-sentinel', pasteLabel: 'Address from the browser', before: /http:\/\/127\.0\.0\.1:58443\/.*encrypted to a key this server made/s, hint: /user_id=…&access_token=…/ },
};
const SENTINELS = ['claude-code-sentinel', 'xai-code-sentinel', 'gemini-code-sentinel', 'ag-code-sentinel', 'gitlab-code-sentinel', 'cline-code-sentinel', 'clinepass-code-sentinel', 'iflow-code-sentinel', 'kimchi-token-sentinel', 'xiaomi-encrypted-sentinel', 'zed-encrypted-sentinel', 'gitlab-secret-sentinel'];

(async () => {
  const chrome = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || '/root/.cache/ms-playwright/chromium-1148/chrome-linux/chrome', args: ['--no-sandbox'] });
  const page = await chrome.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  // The scripted refusals are logged by the browser as failed loads, and closing
  // a sign-in window that reached the provider is refused because it has no
  // opener (onboarding.ts closeSignInWindow); anything else is a fault.
  const expected = text => text.startsWith('Failed to load resource') || text.startsWith("Unsafe attempt to initiate navigation for frame with origin 'https://auth.example.test'");
  page.on('console', message => { if (message.type() === 'error' && !expected(message.text())) errors.push('console: ' + message.text()); });
  const report = { providers: {} };
  const artifacts = process.env.ROUTEWEFT_SMOKE_ARTIFACTS || '/tmp/routeweft-ui-evidence';
  mkdirSync(artifacts, { recursive: true });

  let states = 0;
  let expired = false;
  // What the next callback answers, in order; an empty queue saves the account.
  const callbackModes = [];
  const starts = [];
  const callbacks = [];
  const cancels = [];
  const patches = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const fail = (route, status, code, message) => json(route, status, { error: { code, message } });
  const providerItems = Object.entries(PROVIDERS).map(([id, p]) => ({ id, transports: ['openai-chat'], auth: p.auth || 'oauth', authModes: p.authModes || [], modelCatalog: 'dynamic', passthroughModels: true, reportsUsage: false, flowType: 'authorization_code', signInModes: p.methods.map(m => m.mode), signInMethods: p.methods, configuredNodes: 1, enabledAccounts: 1, readiness: { status: 'ready', served: true } }));
  const existing = id => ({ id: `conn-${id}`, nodeId: `node-${id}`, providerId: id, name: `Existing ${id}`, authKind: 'oauth', identity: `old@${id}.example.test`, enabled: true, priority: 0, credentialConfigured: true });
  const connections = Object.keys(PROVIDERS).map(existing);
  const saved = (provider, outcome, identity) => ({ ...existing(provider), id: outcome === 'reauthenticated' ? `conn-${provider}` : `conn-${provider}-new`, name: outcome === 'reauthenticated' ? `Existing ${provider}` : identity, identity, outcome, ...(PROVIDERS[provider].project !== undefined ? { project: PROVIDERS[provider].project, projectMissing: PROVIDERS[provider].project === '' } : {}) });

  await page.context().route('https://auth.example.test/**', route => route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>provider stub</title>' }));
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 11, nodes: 11, enabledConnections: 11 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
    if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
    if (path === '/providers') return json(route, 200, { items: providerItems, page: 1, pageSize: 100, total: providerItems.length });
    if (path === '/connections' && method === 'GET') return json(route, 200, { items: connections, page: 1, pageSize: 100, total: connections.length });
    let match = path.match(/^\/oauth\/([^/]+)\/(start|callback|cancel)$/);
    if (match && method === 'POST') {
      const [, provider, step] = match;
      const body = route.request().postDataJSON();
      if (step === 'start') {
        starts.push({ provider, body });
        states += 1;
        const state = `journey-state-${states}`;
        return json(route, 200, { state, authorizeUrl: `https://auth.example.test/${provider}?state=${state}`, completion: PROVIDERS[provider].methods[0].completion, flowType: 'authorization_code', expiresAt: expired ? '2026-10-27T11:00:00Z' : '2026-10-27T12:10:00Z' });
      }
      if (step === 'cancel') { cancels.push({ provider, body }); return json(route, 200, { status: 'cancelled' }); }
      callbacks.push({ provider, body });
      const mode = callbackModes.shift();
      if (mode === 'mismatch') return fail(route, 400, 'oauth_state_mismatch', 'this address is from a different sign-in attempt; paste the one from this attempt');
      if (mode === 'invalid') return fail(route, 400, 'oauth_state_invalid', 'this sign-in attempt expired or was already used; start again');
      if (mode === 'unresolved') return fail(route, 502, 'oauth_identity_unresolved', 'the provider did not report which account signed in; start again and enter its identity');
      if (mode === 'account_mismatch') return fail(route, 409, 'oauth_account_mismatch', 'a different account signed in; nothing was saved. Sign in as the account this connection belongs to, or use Add connection for a new one');
      const start = starts.at(-1).body;
      if (start.connectionId) return json(route, 200, saved(provider, 'reauthenticated', `old@${provider}.example.test`));
      return json(route, 200, saved(provider, 'added', start.identity || `new@${provider}.example.test`));
    }
    match = path.match(/^\/connections\/([^/]+)$/);
    if (match && method === 'PATCH') { patches.push({ id: match[1], body: route.request().postDataJSON() }); return json(route, 200, { id: match[1] }); }
    if (method === 'GET') return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    return fail(route, 404, 'not_found', 'not stubbed');
  });
  await page.clock.setFixedTime(new Date('2026-10-27T12:00:00Z'));
  await page.goto('http://127.0.0.1:4173/#providers');
  await page.waitForSelector('.provider-option').catch(async error => { await page.screenshot({ path: join(artifacts, 'oauth-journeys-boot.png') }); console.error(errors, await page.locator('body').innerText()); throw error; });
  // The sign-in window is refused for most runs: the link and paste-back are the
  // way to finish then, and that path is the same for every provider.
  await page.evaluate(() => { window.routeweftOpen = window.open; window.open = () => null; });

  const form = page.locator('.provider-form');
  const result = form.locator('[aria-label="Sign-in result"]');
  const banner = page.locator('.providers-content > .auth-error');
  async function select(id) {
    await page.fill('#provider-filter', id);
    await page.locator('.provider-option', { hasText: id }).first().click();
    await page.getByRole('heading', { name: new RegExp(`^${id}$`) }).first().waitFor().catch(() => {});
  }
  async function open(p) {
    await page.getByRole('button', { name: 'Add connection', exact: true }).click();
    await form.waitFor();
    const label = `Sign in with ${p.label}`;
    if (await form.locator('.access-modes').count()) await form.getByRole('radio', { name: label, exact: true }).check();
    return form.getByRole('button', { name: label, exact: true });
  }
  async function close() {
    if (await form.count()) await form.getByRole('button', { name: 'Close', exact: true }).click();
    await form.waitFor({ state: 'detached' });
  }
  async function fillMeta(p) {
    if (p.needsClientId) {
      await form.getByLabel('Application client id').fill('fixture-client-id');
      await form.getByLabel('Application client secret').fill('gitlab-secret-sentinel');
    }
  }
  const pasteField = p => form.getByLabel(p.pasteLabel, { exact: true });

  for (const [id, p] of Object.entries(PROVIDERS)) {
    const seen = {};
    await select(id);
    // Before the start: the provider's own notes, and the identity it needs.
    let start = await open(p);
    if (p.before) {
      seen.before = await form.locator('.journey-notes').textContent();
      assert.match(seen.before, p.before, `${id}: notes before sign-in`);
      await form.screenshot({ path: join(artifacts, `journey-${id}-before.png`) });
    }
    if (p.identity === 'required') {
      assert.equal(await form.getByLabel('Stable account identity').count(), 1, `${id}: asks for the account`);
      assert.equal(await start.isDisabled(), true, `${id}: cannot start without an identity`);
      await form.getByLabel('Stable account identity').fill(`named@${id}.example.test`);
    } else if (p.identity === 'optional') {
      assert.equal(await form.getByLabel('Account identity (optional)').count(), 1, `${id}: identity is optional`);
      assert.equal(await form.getByLabel('Stable account identity').count(), 0);
    } else {
      assert.equal(await form.getByLabel(/identity/i).count(), 0, `${id}: the provider names the account`);
    }
    if (p.needsClientId) {
      assert.equal(await start.isDisabled(), true, 'gitlab waits for the application client id');
      await form.getByText('Enter the application client id above to start.').waitFor();
    }
    await fillMeta(p);
    assert.equal(await start.isDisabled(), false, `${id}: can start`);

    // Cancel: the attempt ends only when the server confirms it.
    await start.click();
    await form.getByText('Pending', { exact: true }).waitFor();
    seen.hint = await form.locator('.oauth-panel p').first().textContent();
    if (p.hint) assert.match(seen.hint, p.hint, `${id}: how to finish`);
    assert.equal(await pasteField(p).count(), 1, `${id}: paste field "${p.pasteLabel}"`);
    assert.equal(await pasteField(p).getAttribute('type'), p.masked || p.pasteLabel === 'Code from Claude' ? 'password' : 'text', `${id}: paste field masking`);
    assert.equal(await form.getByRole('link', { name: `Open ${p.label} sign-in page` }).getAttribute('href'), `https://auth.example.test/${id}?state=journey-state-${states}`);
    if (p.identity === 'optional') assert.equal(starts.at(-1).body.identity, undefined, `${id}: a blank optional identity is not sent`);
    await form.getByRole('button', { name: 'Cancel sign-in', exact: true }).click();
    await result.getByText('Cancelled', { exact: true }).waitFor();
    assert.deepEqual(cancels.at(-1), { provider: id, body: { state: `journey-state-${states}` } });

    // Expiry and wrong state: past its time limit the attempt says so but stays
    // until the server answers; a paste from another attempt keeps it; the server
    // no longer holding it reads as closed.
    expired = true;
    if (p.identity === 'required') await form.getByLabel('Stable account identity').fill(`named@${id}.example.test`);
    await fillMeta(p);
    await start.click();
    await form.getByText(/The time limit has passed, so the server will most likely refuse this sign-in/).waitFor();
    expired = false;
    callbackModes.push('mismatch', 'invalid');
    await pasteField(p).fill(p.paste(`journey-state-${states}`));
    await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
    await banner.filter({ hasText: 'different sign-in attempt' }).waitFor();
    assert.equal(await pasteField(p).count(), 1, `${id}: a wrong-state paste keeps the attempt`);
    await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
    await result.getByText('Closed', { exact: true }).waitFor();
    assert.match(await result.textContent(), /no longer open on the server/);

    // Identity: a provider that usually reports the account but did not this time
    // saves nothing and asks for it next time; then the sign-in succeeds.
    if (p.identity === 'optional') {
      callbackModes.push('unresolved');
      await start.click();
      await pasteField(p).fill(p.paste(`journey-state-${states}`));
      await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
      await result.getByText('Failed', { exact: true }).waitFor();
      assert.equal(await form.getByLabel('Stable account identity').count(), 1, `${id}: asks for the account after none was reported`);
      assert.equal(await start.isDisabled(), true);
      await form.getByLabel('Stable account identity').fill(`named@${id}.example.test`);
    } else if (p.identity === 'required') {
      await form.getByLabel('Stable account identity').fill(`named@${id}.example.test`);
    }
    await fillMeta(p);
    await start.click();
    const startBody = starts.at(-1).body;
    if (p.identity !== 'resolved') assert.equal(startBody.identity, `named@${id}.example.test`, `${id}: the named account is sent`);
    if (p.needsClientId) assert.deepEqual(startBody.meta, { clientId: 'fixture-client-id', clientSecret: 'gitlab-secret-sentinel' });
    const pasted = p.paste(`journey-state-${states}`);
    await pasteField(p).fill(pasted);
    await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
    await result.getByText('Connected', { exact: true }).waitFor();
    assert.deepEqual(callbacks.at(-1), { provider: id, body: { state: `journey-state-${states}`, input: pasted } });
    seen.connected = await result.textContent();
    const account = p.identity === 'resolved' ? `new@${id}.example.test` : `named@${id}.example.test`;
    assert.match(await form.locator('[aria-label="Signed-in account"]').textContent(), new RegExp(account.replace(/[.]/g, '\\.')));
    if (p.checked) assert.match(seen.connected, p.checked);
    else assert.match(seen.connected, /Use Check connection to send a test request/);
    assert.equal(await form.locator('input[type="password"]').count(), 0, `${id}: no credential field after login`);
    // Google: the project found is shown; none found asks for one and saves only it.
    if (p.project) assert.match(await form.locator('[aria-label="Signed-in account"]').textContent(), /Code Assist project.*fixture-project/);
    if (p.project === '') {
      const group = form.locator('[aria-label="Code Assist project"]');
      assert.match(await group.textContent(), /no Code Assist project/);
      await group.getByLabel('Project ID').fill('Bad Project');
      await group.getByText(/A project ID is 6 to 30 lowercase letters/).waitFor();
      await form.screenshot({ path: join(artifacts, `journey-${id}-project.png`) });
      assert.equal(await group.getByRole('button', { name: 'Save project' }).isDisabled(), true);
      await group.getByLabel('Project ID').fill('named-project');
      await group.getByRole('button', { name: 'Save project' }).click();
      await form.locator('[aria-label="Signed-in account"]', { hasText: 'named-project' }).waitFor();
      assert.deepEqual(patches.at(-1), { id: `conn-${id}-new`, body: { secret: { providerData: { projectId: 'named-project' } } } });
      assert.equal(await form.locator('[aria-label="Code Assist project"]').count(), 0);
    }
    await form.getByRole('button', { name: 'Done', exact: true }).click();
    await form.waitFor({ state: 'detached' });

    // Duplicate/refresh: Reauthenticate renews one connection; another account is
    // refused and nothing saved, then the right account renews it.
    callbackModes.push('account_mismatch');
    const row = page.locator('.connection-row', { hasText: `Existing ${id}` });
    for (const expect of ['Failed', 'Connected']) {
      await row.getByRole('button', { name: 'Reauthenticate', exact: true }).click();
      await form.getByRole('heading', { name: `Reauthenticate “Existing ${id}”` }).waitFor();
      await fillMeta(p);
      await form.getByRole('button', { name: 'Reauthenticate', exact: true }).click();
      await form.getByText('Pending', { exact: true }).waitFor();
      assert.equal(starts.at(-1).body.connectionId, `conn-${id}`, `${id}: Reauthenticate names its connection`);
      assert.equal(starts.at(-1).body.identity, undefined);
      await pasteField(p).fill(p.paste(`journey-state-${states}`));
      await form.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
      await result.getByText(expect, { exact: true }).waitFor();
      if (expect === 'Failed') { assert.match(await result.textContent(), /different account signed in; nothing was saved/); await close(); }
    }
    assert.match(await result.textContent(), new RegExp(`Reauthenticated “Existing ${id}” as old@${id}`));
    await form.getByRole('button', { name: 'Done', exact: true }).click();
    await form.waitFor({ state: 'detached' });
    report.providers[id] = seen;
  }

  // One run with the window allowed: it opens in the click, has no opener, and is
  // sent to the provider's authorize URL (#215's window, for a #216 provider).
  await page.evaluate(() => { window.open = window.routeweftOpen; });
  await select('xai');
  const start = await open(PROVIDERS.xai);
  const popupOpened = page.waitForEvent('popup');
  await start.click();
  const popup = await popupOpened;
  await popup.waitForURL(/^https:\/\/auth\.example\.test\/xai\?state=journey-state-/);
  assert.equal(await popup.evaluate(() => window.opener), null);
  await form.getByRole('button', { name: 'Cancel sign-in', exact: true }).click();
  await result.getByText('Cancelled', { exact: true }).waitFor();
  await popup.close().catch(() => {});
  await close();

  // GitLab's PAT alternative is its own method with its own note.
  await select('gitlab');
  await page.getByRole('button', { name: 'Add connection', exact: true }).click();
  await form.getByRole('radio', { name: 'Import personal access token', exact: true }).check();
  // #218: the PAT import lists where to create the token and its scopes.
  report.gitlabPAT = await form.locator('[aria-label="Import personal access token"] ol').textContent();
  assert.match(report.gitlabPAT, /personal access token .* with the scopes api, read_user and ai_features/);
  assert.equal(await form.getByLabel('Application client id').count(), 0, 'the PAT does not ask for the OAuth application');
  await close();

  const html = await page.content();
  for (const sentinel of SENTINELS) assert.equal(html.includes(sentinel), false, `${sentinel} stayed in the DOM`);
  report.starts = starts.length;
  report.callbacks = callbacks.length;
  report.cancels = cancels.length;
  assert.deepEqual(errors, []);
  writeFileSync(join(artifacts, 'oauth-journeys-report.json'), JSON.stringify(report, null, 2) + '\n');
  console.log(`oauth journeys: ${Object.keys(report.providers).length} providers, ${starts.length} starts, ${callbacks.length} callbacks, ${cancels.length} cancels`);
  await chrome.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
