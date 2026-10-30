// Browser fixture for the System One and Combo pages (#278, U09). System One must read every page
// (a provider, a model or an account past the first 100 is not hidden) and show them a page at a time,
// onboard a custom typed endpoint, its models and an account (with the Cloudflare account ID), and keep
// the API key out of the page. The Combos page must nest another Combo as a member (never offering a
// cycle or a chain deeper than 8), show what a Combo can do (vision, audio, ...) from its members, and
// carry the Fusion timing a Combo already has instead of erasing it on save. The control API is stubbed,
// so no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

const ORIGIN = 'http://127.0.0.1:4173';

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const errors = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const pageOf = (items, url) => {
    const page = Number(url.searchParams.get('page') || 1);
    const size = Number(url.searchParams.get('pageSize') || 100);
    return { items: items.slice((page - 1) * size, page * size), page, pageSize: size, total: items.length };
  };

  // ---- fixtures ---------------------------------------------------------------------
  const typesafeModels = Array.from({ length: 230 }, (_, i) => ({ providerId: 'typesafe', id: `jev-${String(i).padStart(3, '0')}`, name: `jev-${i}`, capabilities: [], disabled: false }));
  const lateModel = { providerId: 's1-late', id: 'late-model', name: 'late-model', capabilities: [], disabled: false };
  // 130 nodes: the only System One node past the first page is s1-late.
  const nodes = [
    ...Array.from({ length: 129 }, (_, i) => ({ id: `node-${i}`, kind: 'builtin', providerId: `filler-${i}`, name: `filler-${i}`, baseUrl: 'https://example.test', transports: ['openai-chat'] })),
    { id: 'node-typesafe', kind: 'builtin', providerId: 'typesafe', name: 'typesafe', baseUrl: 'https://api.typesafe.ai/v1/systemone', transports: ['systemone'] },
    { id: 'node-late', kind: 'generic', providerId: 's1-late', name: 'Late', prefix: 's1-late', baseUrl: 'https://late.example.test/s1', transports: ['systemone'] },
    { id: 'node-cf', kind: 'builtin', providerId: 'cloudflare-ai', name: 'cloudflare-ai', baseUrl: 'https://api.cloudflare.com/client/v4/accounts', transports: ['systemone', 'openai-chat'] },
  ];
  const connections = Array.from({ length: 120 }, (_, i) => ({ id: `conn-${i}`, nodeId: 'node-typesafe', providerId: 'typesafe', name: `account-${String(i).padStart(3, '0')}`, authKind: 'api-key', identity: `identity-${i}`, enabled: i % 7 !== 0, priority: i, credentialConfigured: true }));
  const requests = [];
  const created = { nodes: [], models: [], connections: [] };
  let failNode = false;
  let failModelAt = -1;

  // Combos: base (vision), team (nests base), plus a chain of eight, c1 -> c2 ... -> c8.
  const combos = [
    { id: 'combo-base', name: 'base', strategy: 'fallback', stickyLimit: 0, judgeModel: '', fusionEnabled: false, members: [{ providerId: 'openai', modelId: 'gpt-vision', position: 0, selected: true }] },
    { id: 'combo-team', name: 'team', strategy: 'fallback', stickyLimit: 0, judgeModel: '', fusionEnabled: true, fusionTuning: { minPanel: 3, stragglerGraceMs: 1500, panelHardTimeoutMs: 20000 }, members: [{ providerId: '', modelId: 'base', position: 0, selected: true }, { providerId: 'gemini', modelId: 'flash', position: 1, selected: true }] },
    ...Array.from({ length: 8 }, (_, i) => ({ id: `combo-c${i + 1}`, name: `c${i + 1}`, strategy: 'fallback', stickyLimit: 0, judgeModel: '', fusionEnabled: false, members: i < 7 ? [{ providerId: '', modelId: `c${i + 2}`, position: 0, selected: true }] : [{ providerId: 'openai', modelId: 'gpt', position: 0, selected: true }] })),
  ];
  const comboWrites = [];

  async function newPage(viewport = { width: 1280, height: 900 }) {
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    page.on('pageerror', e => errors.push('pageerror: ' + e.message));
    page.on('console', msg => { if (msg.type() === 'error' && !/Failed to load resource/.test(msg.text())) errors.push('console: ' + msg.text()); });
    await page.route('**/admin/v1/**', async route => {
      const url = new URL(route.request().url());
      const method = route.request().method();
      const path = url.pathname.replace('/admin/v1', '');
      if (method !== 'GET') requests.push({ method, path, body: route.request().postDataJSON?.() });
      if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
      if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 2, nodes: 2, enabledConnections: 2 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
      if (path === '/systemone' && method === 'GET') { requests.push({ method, path: path + '?page=' + url.searchParams.get('page') }); return json(route, 200, pageOf([...typesafeModels, lateModel, ...created.models], url)); }
      if (path === '/provider-nodes' && method === 'GET') { requests.push({ method, path: path + '?page=' + url.searchParams.get('page') }); return json(route, 200, pageOf([...nodes, ...created.nodes], url)); }
      if (path === '/connections' && method === 'GET') { requests.push({ method, path: path + '?page=' + url.searchParams.get('page') }); return json(route, 200, pageOf([...connections, ...created.connections], url)); }
      if (path === '/provider-nodes' && method === 'POST') {
        if (failNode) return json(route, 400, { error: { code: 'provider_save_failed', message: 'That prefix is already in use.' } });
        const body = route.request().postDataJSON();
        const node = { id: `node-new-${created.nodes.length}`, kind: 'generic', providerId: body.prefix, name: body.name, prefix: body.prefix, baseUrl: body.baseUrl, transports: body.transports };
        created.nodes.push(node);
        return json(route, 200, node);
      }
      if (path === '/models' && method === 'POST') {
        const body = route.request().postDataJSON();
        if (failModelAt >= 0 && created.models.length === failModelAt) return json(route, 400, { error: { code: 'invalid_model', message: 'model id is not allowed' } });
        created.models.push({ providerId: body.providerId, id: body.id, name: body.name, capabilities: [], disabled: false });
        return json(route, 200, body);
      }
      if (path === '/connections' && method === 'POST') {
        const body = route.request().postDataJSON();
        const connection = { id: `conn-new-${created.connections.length}`, nodeId: body.nodeId, providerId: body.providerId, name: body.name, authKind: body.authKind, identity: body.identity, accountId: body.accountId, enabled: true, priority: body.priority, credentialConfigured: true };
        created.connections.push(connection);
        return json(route, 200, connection);
      }
      // Combos page
      if (path === '/combos' && method === 'GET') return json(route, 200, pageOf(combos, url));
      if ((path === '/combos' && method === 'POST') || (path.startsWith('/combos/') && method === 'PUT')) { comboWrites.push({ method, path, body: route.request().postDataJSON() }); return json(route, 200, { id: 'combo-x', ...route.request().postDataJSON(), configRevision: 2 }); }
      if (path === '/models' && method === 'GET') return json(route, 200, pageOf([{ providerId: 'openai', id: 'gpt-vision', disabled: false, capabilities: ['vision'] }, { providerId: 'openai', id: 'gpt', disabled: false, capabilities: [] }, { providerId: 'gemini', id: 'flash', disabled: false, capabilities: ['audio-input', 'vision'] }, { providerId: 'claude', id: 'opus', disabled: false, capabilities: ['pdf'] }], url));
      if (path === '/aliases') return json(route, 200, pageOf([], url));
      if (path === '/models/disabled') return json(route, 200, pageOf([], url));
      if (path === '/providers') return json(route, 200, pageOf([{ id: 'openai' }, { id: 'gemini' }, { id: 'claude' }], url));
      if (path === '/keys') return json(route, 200, pageOf([], url));
      if (path === '/capability-adapters') return json(route, 200, { items: [] });
      return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    });
    return { context, page };
  }
  const writes = () => requests.filter(r => r.method !== 'GET');
  const reads = prefix => requests.filter(r => r.method === 'GET' && r.path.startsWith(prefix)).map(r => r.path);

  // =================================== System One ======================================
  {
    const { context, page } = await newPage();
    await page.goto(`${ORIGIN}/#system-one`);
    await page.waitForSelector('.systemone-content[aria-busy="false"]');

    // Every page was read, not just the first 100.
    assert.deepEqual(reads('/systemone'), ['/systemone?page=1', '/systemone?page=2', '/systemone?page=3'], 'all three pages of 230 models');
    assert.deepEqual(reads('/provider-nodes'), ['/provider-nodes?page=1', '/provider-nodes?page=2'], 'both pages of 132 nodes');
    assert.deepEqual(reads('/connections'), ['/connections?page=1', '/connections?page=2'], 'both pages of 120 accounts');
    const providers = await page.locator('#systemone-provider option').allTextContents();
    assert.deepEqual(providers, ['s1-late', 'typesafe', 'cloudflare-ai'].sort(), 'a System One node past the first page is a provider');

    // Models are shown a page at a time, and the last one is reachable.
    await page.selectOption('#systemone-provider', 'typesafe');
    assert.equal(await page.locator('.systemone-layout .model-row').count(), 25);
    const pager = page.getByRole('navigation', { name: 'System One models pages' });
    assert.match(await pager.innerText(), /Page 1 of 10 · 230 rows/);
    for (let i = 0; i < 9; i++) await pager.getByRole('button', { name: 'Next' }).click();
    assert.match(await pager.innerText(), /Page 10 of 10/);
    assert.deepEqual(await page.locator('.systemone-layout .model-row code').last().innerText(), 'jev-229', 'the last model past the first 100 is shown');
    assert.equal(await pager.getByRole('button', { name: 'Next' }).isDisabled(), true);
    // Accounts too.
    const accounts = page.getByRole('navigation', { name: 'System One accounts pages' });
    assert.match(await accounts.innerText(), /Page 1 of 5 · 120 rows/);
    for (let i = 0; i < 4; i++) await accounts.getByRole('button', { name: 'Next' }).click();
    assert.equal(await page.locator('.systemone-account strong').last().innerText(), 'account-119');
    assert.equal(await page.locator('.systemone-account .state-tag').first().innerText().then(text => ['Enabled', 'Disabled'].includes(text)), true, 'state is stated in words');

    // ---- onboarding a custom typed provider ------------------------------------------
    const form = page.getByRole('form', { name: 'Add a System One provider' });
    assert.equal(await form.getByLabel('Endpoint URL').inputValue(), 'https://api.typesafe.ai/v1/systemone', 'starts from the oracle\'s default endpoint');
    await form.getByLabel('Provider name').fill('System One (Prod)');
    await form.getByLabel('Prefix').fill('s1-prod');
    // Too many models: nothing is sent.
    await form.getByLabel(/^Models, one per line/).fill(Array.from({ length: 65 }, (_, i) => `m-${i}`).join('\n'));
    const before = writes().length;
    await form.getByRole('button', { name: 'Add provider' }).click();
    await page.locator('p.auth-error[role="alert"]', { hasText: 'at most 64 models' }).waitFor();
    assert.equal(writes().length, before, 'an over-long model list sends nothing');
    // A refusal from the server is shown and no model is posted.
    failNode = true;
    await form.getByLabel(/^Models, one per line/).fill('jev-latest');
    await form.getByRole('button', { name: 'Add provider' }).click();
    await page.locator('p.auth-error[role="alert"]', { hasText: 'That prefix is already in use.' }).waitFor();
    assert.equal(created.models.length, 0, 'no model is posted when the node is refused');
    failNode = false;
    // Blank lines and repeats are dropped; the node is typed systemone only.
    await form.getByLabel(/^Models, one per line/).fill('jev-latest\n\n  jev-latest  \njev-mini\n');
    await form.getByRole('button', { name: 'Add provider' }).click();
    await page.locator('.inline-notice[role="status"]', { hasText: 'System One provider s1-prod added with 2 models.' }).waitFor();
    const nodeCall = writes().find(r => r.path === '/provider-nodes' && r.body?.prefix === 's1-prod' && r.body?.name === 'System One (Prod)');
    assert.deepEqual(nodeCall.body, { kind: 'generic', name: 'System One (Prod)', prefix: 's1-prod', baseUrl: 'https://api.typesafe.ai/v1/systemone', transports: ['systemone'] });
    assert.deepEqual(created.models.map(model => model.id), ['jev-latest', 'jev-mini']);
    await page.waitForFunction(() => document.querySelector('#systemone-provider').value === 's1-prod' && document.querySelector('.systemone-content').getAttribute('aria-busy') === 'false');
    assert.equal(await form.getByLabel('Provider name').inputValue(), '', 'the form is cleared');

    // A model that fails after the node exists says how far it got.
    failModelAt = created.models.length + 1;
    await form.getByLabel('Provider name').fill('Half');
    await form.getByLabel('Prefix').fill('s1-half');
    await form.getByLabel(/^Models, one per line/).fill('ok-1\nbad-2\nnever-3');
    await form.getByRole('button', { name: 'Add provider' }).click();
    await page.locator('p.auth-error[role="alert"]', { hasText: 'was added, but model bad-2 could not be added (1 of 3 models saved)' }).waitFor();
    failModelAt = -1;

    // ---- onboarding an account: the key never comes back ------------------------------
    await page.selectOption('#systemone-provider', 's1-prod');
    const account = page.getByRole('form', { name: 'Add a System One account' });
    assert.equal(await account.getByLabel('Cloudflare account ID').count(), 0, 'only Cloudflare asks for an account ID');
    assert.equal(await account.getByLabel('API key').getAttribute('type'), 'password');
    await account.getByLabel('Account name').fill('Prod account');
    await account.getByLabel('Account identity').fill('acct-prod');
    await account.getByLabel('API key').fill('sk-never-shown');
    await account.getByRole('button', { name: 'Add account' }).click();
    await page.locator('.inline-notice[role="status"]', { hasText: 'Account Prod account added to s1-prod.' }).waitFor();
    const connCall = writes().find(r => r.path === '/connections' && r.body?.name === 'Prod account');
    assert.deepEqual(connCall.body, { providerId: 's1-prod', nodeId: 'node-new-0', name: 'Prod account', identity: 'acct-prod', authKind: 'api-key', secret: { accessToken: 'sk-never-shown' }, enabled: true, priority: 0 });
    assert.ok(!(await page.content()).includes('sk-never-shown'), 'the key is not in the page once saved');
    assert.equal(await account.getByLabel('API key').inputValue(), '', 'the key field is cleared');
    await page.locator('.systemone-account strong', { hasText: 'Prod account' }).waitFor();

    // Cloudflare needs its account ID.
    await page.selectOption('#systemone-provider', 'cloudflare-ai');
    const cf = page.getByRole('form', { name: 'Add a System One account' });
    await cf.getByLabel('Account name').fill('CF');
    await cf.getByLabel('Account identity').fill('cf-1');
    await cf.getByLabel('API key').fill('cf-key');
    assert.equal(await cf.getByLabel('Cloudflare account ID').getAttribute('required') !== null, true, 'the account ID is required');
    const cfBefore = writes().length;
    await cf.getByRole('button', { name: 'Add account' }).click();
    assert.equal(writes().length, cfBefore, 'no request without the account ID');
    await cf.getByLabel('Cloudflare account ID').fill('acct_123-Prod');
    await cf.getByRole('button', { name: 'Add account' }).click();
    await page.locator('.inline-notice[role="status"]', { hasText: 'Account CF added to cloudflare-ai.' }).waitFor();
    assert.equal(writes().find(r => r.path === '/connections' && r.body?.name === 'CF').body.accountId, 'acct_123-Prod');
    await page.locator('.systemone-account', { hasText: 'account ID acct_123-Prod' }).waitFor();
    await context.close();
  }

  // ---- 320px: the whole page fits and its controls keep their size ----------------------
  {
    const { context, page } = await newPage({ width: 320, height: 700 });
    await page.goto(`${ORIGIN}/#system-one`);
    await page.waitForSelector('.systemone-content[aria-busy="false"]');
    await page.selectOption('#systemone-provider', 'typesafe');
    const fits = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
    assert.ok(fits, 'no sideways scroll at 320px');
    for (const [label, locator] of [['Add provider', page.getByRole('button', { name: 'Add provider' })], ['Add account', page.getByRole('button', { name: 'Add account' })], ['Next', page.getByRole('navigation', { name: 'System One models pages' }).getByRole('button', { name: 'Next' })]]) {
      const box = await locator.boundingBox();
      assert.ok(box.height >= 44 && box.width >= 44, `${label} is ${box.width}x${box.height} at 320px`);
    }
    for (const label of ['Provider name', 'Prefix', 'Endpoint URL', 'Account name', 'Account identity', 'API key']) {
      const box = await page.getByLabel(label, { exact: true }).first().boundingBox();
      assert.ok(box.height >= 44 && box.x >= 0 && box.x + box.width <= 320, `${label} field ${JSON.stringify(box)}`);
    }
    await context.close();
  }

  // =================================== Combos ==========================================
  {
    const { context, page } = await newPage();
    await page.goto(`${ORIGIN}/#combo-capability-adapter`);
    await page.waitForSelector('.combo-row');
    // What a Combo can do, from its members, nested ones included.
    const team = page.locator('.combo-row', { hasText: 'team' }).first();
    assert.deepEqual(await team.locator('.combo-capability').allInnerTexts(), ['vision', 'audio-input'], 'team: its own Gemini member and the nested base');
    assert.match(await team.locator('.combo-main small').innerText(), /2 members · 1 nested/);
    assert.equal(await page.locator('.combo-row', { hasText: 'base' }).first().locator('.combo-capability').count(), 1);
    assert.equal(await team.locator('.state-tag', { hasText: 'Custom timing' }).count(), 1);

    // A Combo that already has Fusion timing keeps it when edited and saved untouched.
    await team.getByRole('button', { name: 'Edit', exact: true }).click();
    await page.waitForSelector('.combo-form');
    const tuning = page.getByRole('group', { name: 'Fusion panel timing (optional)' });
    assert.equal(await tuning.getByLabel('Minimum panel').inputValue(), '3');
    assert.equal(await tuning.getByLabel('Straggler grace (ms)').inputValue(), '1500');
    assert.equal(await tuning.getByLabel('Panel hard timeout (ms)').inputValue(), '20000');
    await Promise.all([page.waitForResponse(response => response.url().includes('/admin/v1/combos/') && response.request().method() === 'PUT'), page.click('.combo-form > .button-primary')]);
    const kept = comboWrites.at(-1);
    assert.equal(kept.method, 'PUT');
    assert.deepEqual(kept.body.fusionTuning, { minPanel: 3, stragglerGraceMs: 1500, panelHardTimeoutMs: 20000 }, 'saving does not erase the timing');
    assert.deepEqual(kept.body.members[0], { providerId: '', modelId: 'base', position: 0, selected: true }, 'the nested member round-trips');

    // base cannot take team: team already contains base.
    await page.locator('.combo-row', { hasText: 'base' }).first().getByRole('button', { name: 'Edit', exact: true }).click();
    await page.waitForSelector('.combo-form');
    const offered = await page.locator('#combo-member-choices option').evaluateAll(options => options.map(option => option.getAttribute('value')));
    assert.ok(!offered.includes('team') && !offered.includes('base'), 'no cycle and no self is offered');
    await page.fill('input[aria-label="Add Combo member"]', 'team');
    await page.click('.member-picker .button-secondary');
    await page.locator('.combo-form p.auth-error[role="alert"]', { hasText: 'would reach itself' }).waitFor();
    await page.click('.combo-form .button-ghost');

    // A new Combo can nest team and base; the chain of eight is refused at the depth limit.
    await page.click('.combos-toolbar .button-primary');
    await page.waitForSelector('.combo-form');
    await page.fill('.combo-form-grid input', 'wrap');
    const options = await page.locator('#combo-member-choices option').evaluateAll(list => list.map(option => option.getAttribute('value')));
    assert.ok(options.includes('team') && options.includes('base') && options.includes('c2') && !options.includes('c1'), `offered ${options.filter(value => !value.includes('/'))}`);
    await page.fill('input[aria-label="Add Combo member"]', 'c1');
    await page.keyboard.press('Enter');
    await page.locator('.combo-form p.auth-error[role="alert"]', { hasText: 'deeper than 8' }).waitFor();
    await page.fill('input[aria-label="Add Combo member"]', 'team');
    await page.keyboard.press('Enter');
    const row = page.locator('.member-row', { hasText: 'team' });
    await row.waitFor();
    assert.match(await row.innerText(), /Nested Combo/);
    assert.equal(await row.locator('code').innerText(), 'team');
    await page.fill('input[aria-label="Add Combo member"]', 'no-such-combo');
    await page.keyboard.press('Enter');
    await page.locator('.combo-form p.auth-error[role="alert"]', { hasText: 'not a known model, alias or Combo' }).waitFor();
    await page.fill('input[aria-label="Add Combo member"]', 'team');
    await page.keyboard.press('Enter');
    await page.locator('.combo-form p.auth-error[role="alert"]', { hasText: 'already in this Combo' }).waitFor();

    // Fusion timing: blank keeps the default; bad values stop the save; good values are sent.
    assert.equal(await page.getByRole('group', { name: 'Fusion panel timing (optional)' }).count(), 0, 'no timing fields until Fusion is on');
    await page.getByLabel('Enable Fusion').check();
    const fusion = page.getByRole('group', { name: 'Fusion panel timing (optional)' });
    assert.equal(await fusion.getByLabel('Minimum panel').getAttribute('placeholder'), '2 (default)');
    assert.equal(await fusion.getByLabel('Straggler grace (ms)').getAttribute('placeholder'), '8000 (default)');
    assert.equal(await fusion.getByLabel('Panel hard timeout (ms)').getAttribute('placeholder'), '90000 (default)');
    await fusion.getByLabel('Panel hard timeout (ms)').fill('0');
    const writesBefore = comboWrites.length;
    await page.click('.combo-form > .button-primary');
    await page.locator('p.auth-error[role="alert"]', { hasText: 'Panel hard timeout must be a whole number of milliseconds from 1 to 600000' }).waitFor();
    assert.equal(comboWrites.length, writesBefore, 'a bad timing sends nothing');
    await fusion.getByLabel('Panel hard timeout (ms)').fill('45000');
    await fusion.getByLabel('Straggler grace (ms)').fill('0');
    await Promise.all([page.waitForResponse(response => response.url().endsWith('/admin/v1/combos') && response.request().method() === 'POST'), page.click('.combo-form > .button-primary')]);
    assert.equal(comboWrites.length, writesBefore + 1);
    const created2 = comboWrites.at(-1);
    assert.equal(created2.method, 'POST');
    assert.deepEqual(created2.body.fusionTuning, { stragglerGraceMs: 0, panelHardTimeoutMs: 45000 }, 'blank minimum stays the default; a zero grace is sent');
    assert.deepEqual(created2.body.members, [{ providerId: '', modelId: 'team', position: 0, selected: true }]);
    assert.equal(created2.body.fusionEnabled, true);
    await context.close();
  }

  // ---- 320px: the Combo form with nested members and timing fits ---------------------
  {
    const { context, page } = await newPage({ width: 320, height: 700 });
    await page.goto(`${ORIGIN}/#combo-capability-adapter`);
    await page.waitForSelector('.combo-row');
    await page.locator('.combo-row', { hasText: 'team' }).first().getByRole('button', { name: 'Edit', exact: true }).click();
    await page.waitForSelector('.combo-form');
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), 'no sideways scroll at 320px');
    for (const label of ['Minimum panel', 'Straggler grace (ms)', 'Panel hard timeout (ms)']) {
      const box = await page.getByLabel(label).boundingBox();
      assert.ok(box.height >= 44 && box.x >= 0 && box.x + box.width <= 320, `${label} ${JSON.stringify(box)}`);
    }
    await context.close();
  }

  assert.deepEqual(errors, []);
  await browser.close();
  console.log('system one and combos smoke ok');
})().catch(error => { console.error(error); process.exit(1); });
