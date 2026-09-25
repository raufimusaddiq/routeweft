
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || '/root/.cache/ms-playwright/chromium-1148/chrome-linux/chrome', args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], { origin: 'http://127.0.0.1:4173' });
  const errors = [];
  const report = {};
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  // Stub the control API so the smoke run needs no server or credentials.
  const overviewPayload = {
    runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 7, snapshotVersion: 7 },
    providers: { catalog: 80, nodes: 3, enabledConnections: 2 },
    apiKeys: { active: 1 },
    traffic: { active: 4, requests: 1200, errors: 3, lastRequestAt: '2026-09-25T20:00:00Z' },
    telemetry: { enabled: true, written: 1180, lost: 0, lostDiagnostics: 2, degraded: true },
  };
  let sessionCalls = 0;
  let loginCalls = 0;
  let overviewCalls = 0;
  let settingsRequireApiKey = 'true';
  let keyListCalls = 0;
  let createCalls = 0;
  let deleteCalls = 0;
  let patchKeyCalls = 0;
  let providerPatches = 0;
  let comboCreated = false;
  let comboPayload = null;
  let adapterEnabled = false;
	let systemOneModelAdded = false;
	let systemOneProbeCalls = 0;
	let systemOneProbeTransport = '';
  const createdKeyID = 'k-ci-1';
  const createdSecret = 'rw_smoke_secret_value';
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
  if (url.pathname === '/admin/v1/settings' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ settings: { requireApiKey: settingsRequireApiKey }, writable: ['requireApiKey'] }) });
  }
  if (url.pathname === '/admin/v1/settings' && method === 'PATCH') {
    settingsRequireApiKey = String(route.request().postDataJSON().set.requireApiKey);
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ configRevision: 8, settings: { requireApiKey: settingsRequireApiKey } }) });
  }
  if (url.pathname === '/admin/v1/keys' && method === 'GET') {
    keyListCalls += 1;
  const items = deleteCalls > 0 ? [] : [{ id: createdKeyID, name: 'ci-pipeline', prefix: 'rw_smoke', enabled: true, paused: patchKeyCalls % 2 === 1, createdAt: '2026-09-25T20:00:00Z' }];
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items, page: 1, pageSize: 50, total: items.length }) });
  }
  if (url.pathname === '/admin/v1/keys' && method === 'POST') {
    createCalls += 1;
    return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ key: { id: createdKeyID, name: route.request().postDataJSON().name, prefix: 'rw_smoke', enabled: true, paused: false }, secret: createdSecret }) });
  }
  if (url.pathname.startsWith('/admin/v1/keys/') && method === 'PATCH') {
    patchKeyCalls += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: createdKeyID, paused: true, configRevision: 9 }) });
  }
  if (url.pathname.startsWith('/admin/v1/keys/') && method === 'DELETE') {
    deleteCalls += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: createdKeyID, revoked: true, configRevision: 10 }) });
  }
  if (url.pathname === '/admin/v1/providers' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ id: 'openai', transports: ['openai-chat','openai-responses'], auth: 'api-key', authModes: ['api-key'], defaultBaseURL: 'https://api.openai.com/v1', modelCatalog: 'static', passthroughModels: false, reportsUsage: false, configuredNodes: 1, enabledAccounts: 1 }, { id: 'openrouter', transports: ['openai-chat'], auth: 'api-key', authModes: ['api-key'], modelCatalog: 'dynamic', passthroughModels: true, reportsUsage: false, configuredNodes: 0, enabledAccounts: 0 }], page: 1, pageSize: 100, total: 2 }) });
  }
  if (url.pathname === '/admin/v1/provider-nodes' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ id: 'node-openai', kind: 'builtin', providerId: 'openai', name: 'openai', baseUrl: 'https://api.openai.com/v1', transports: ['openai-chat','openai-responses'] }, { id: 'node-jev', kind: 'builtin', providerId: 'typesafe', name: 'typesafe', baseUrl: 'https://api.typesafe.ai/v1/systemone', transports: ['systemone'] }], page: 1, pageSize: 100, total: 2 }) });
  }
	if (url.pathname === '/admin/v1/connections' && method === 'GET') {
	  return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ id: 'conn-1', nodeId: 'node-openai', providerId: 'openai', name: 'primary', authKind: 'api-key', identity: 'acct-1', enabled: providerPatches === 0, priority: 0, credentialConfigured: true }, { id: 'conn-jev', nodeId: 'node-jev', providerId: 'typesafe', name: 'jev', authKind: 'api-key', identity: 'acct-jev', enabled: true, priority: 0, credentialConfigured: true }], page: 1, pageSize: 100, total: 2 }) });
	}
	if (url.pathname === '/admin/v1/systemone' && method === 'GET') {
	  return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: systemOneModelAdded ? [{ providerId: 'typesafe', id: 'jev', name: 'jev', capabilities: [], disabled: false }, { providerId: 'typesafe', id: 'jev-mini', name: 'jev-mini', capabilities: [], disabled: false }] : [{ providerId: 'typesafe', id: 'jev', name: 'jev', capabilities: [], disabled: false }], page: 1, pageSize: 100, total: 1 }) });
	}
	if (url.pathname === '/admin/v1/models' && method === 'POST') {
	  systemOneModelAdded = true;
	  return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ providerId: 'typesafe', id: 'jev-mini' }) });
	}
	if (url.pathname === '/admin/v1/connections/conn-jev/test-models' && method === 'POST') {
	  systemOneProbeCalls += 1;
	  systemOneProbeTransport = route.request().postDataJSON().transport;
	  return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ connectionId: 'conn-jev', transport: 'systemone', results: [{ modelId: 'jev', ok: true, status: 200 }] }) });
	}
  if (url.pathname === '/admin/v1/models' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ providerId: 'openai', id: 'gpt-5', name: 'GPT-5', contextWindow: 128000, capabilities: [], disabled: false }], page: 1, pageSize: 100, total: 1 }) });
  }
  if ((url.pathname === '/admin/v1/aliases' || url.pathname === '/admin/v1/pricing' || url.pathname === '/admin/v1/proxy-pools') && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], page: 1, pageSize: 100, total: 0 }) });
  }
  if (url.pathname === '/admin/v1/combos' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: comboCreated ? [{ id: 'combo-1', name: 'daily', strategy: 'sticky-round-robin', stickyLimit: 3, judgeModel: '', fusionEnabled: true, members: [{ providerId: 'openai', modelId: 'gpt-5', position: 0, selected: true }] }] : [], page: 1, pageSize: 100, total: comboCreated ? 1 : 0 }) });
  }
  if (url.pathname === '/admin/v1/combos' && method === 'POST') {
    comboCreated = true;
    const payload = route.request().postDataJSON();
    comboPayload = { id: 'combo-1', ...payload };
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(comboPayload) });
  }
  if (url.pathname === '/admin/v1/capability-adapters' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ capability: 'vision', enabled: adapterEnabled, pool: [] }, { capability: 'audio-input', enabled: false, pool: [] }, { capability: 'pdf', enabled: false, pool: [] }, { capability: 'video-input', enabled: false, pool: [] }] }) });
  }
  if (url.pathname === '/admin/v1/capability-adapters/vision' && method === 'PUT') {
    adapterEnabled = route.request().postDataJSON().enabled;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ capability: 'vision', enabled: adapterEnabled, pool: [], configRevision: 11 }) });
  }
  if (url.pathname === '/admin/v1/connections/conn-1' && method === 'PATCH') {
    providerPatches += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'conn-1', enabled: false }) });
  }
  if (url.pathname.endsWith('/order') && method === 'POST') {
    providerPatches += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'conn-1', direction: 'up' }) });
  }
    if (url.pathname === '/admin/v1/auth/session') {
      sessionCalls += 1;
      if (sessionCalls === 1) return route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unauthenticated' } }) });
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ username: 'operator', expiresAt: '2026-09-26T00:00:00Z' }) });
    }
    if (url.pathname === '/admin/v1/auth/login') {
      loginCalls += 1;
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ username: route.request().postDataJSON().username }) });
    }
    if (url.pathname === '/admin/v1/overview') {
      overviewCalls += 1;
      if (overviewCalls === 2) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'read_failed' } }) });
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(overviewPayload) });
    }
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{}' });
  });
  await page.goto('http://127.0.0.1:4173');
  await page.waitForSelector('#sign-in-title');
  await page.keyboard.press('Tab');
  report.firstFocus = await page.evaluate(() => document.activeElement.className);
  report.signInPrompt = await page.locator('#sign-in-title').textContent();
  report.passwordMasked = await page.locator('#admin-password').getAttribute('type');
  await page.fill('#admin-username', 'operator');
  await page.fill('#admin-password', 'not-a-real-secret');
  await page.click('button[type="submit"]');
  await page.waitForSelector('.metrics-grid');
  report.loginCalls = loginCalls;
  report.initialOverviewCalls = overviewCalls;
  report.overviewMetrics = await page.locator('.metric-value').allTextContents();
  report.overviewAlert = await page.locator('.actionable-alert').innerText();
  report.sessionCalls = sessionCalls;
  report.signOutLabel = await page.locator('.sign-out').textContent();
  report.monospaceValue = await page.locator('.technical-value').first().evaluate(el => getComputedStyle(el).fontFamily);
  report.overviewScroll = await page.evaluate(() => document.documentElement.scrollWidth + '/' + document.documentElement.clientWidth);
  await page.click('.refresh-button');
  await page.waitForSelector('.refresh-error');
  report.refreshError = await page.locator('.refresh-error').innerText();
  report.staleMetricAfterRefreshError = await page.locator('.metric-value').first().textContent();
  await page.click('.refresh-error button');
  await page.waitForFunction(() => !document.querySelector('.refresh-error'));
  report.overviewCallsAfterRetry = overviewCalls;
  // Endpoint & Key workflow (PRD 14).
  await page.click('a[href="#endpoint-key"]');
  await page.waitForSelector('.endpoint-content');
  report.endpointURL = await page.locator('.copy-field code').first().textContent();
  await page.locator('.copy-field button').first().click();
  report.copiedEndpoint = await page.evaluate(() => navigator.clipboard.readText());
  report.transportCount = await page.locator('.transport-grid > div').count();
  report.enforcementLabel = await page.locator('.switch-label').textContent();
  report.keyRowCountBefore = await page.locator('.key-row').count();
  await page.fill('#new-key-name', 'smoke-key');
  await page.click('.create-key-form button[type="submit"]');
  await page.waitForSelector('.secret-panel');
  report.secretShown = await page.locator('.secret-panel input').inputValue();
  report.createCalls = createCalls;
  await page.click('.secret-panel .button-primary');
  await page.waitForFunction(() => document.querySelector('.secret-panel .button-primary').textContent === 'Copied');
  report.copiedSecret = await page.evaluate(() => navigator.clipboard.readText());
  await page.click('.secret-panel .button-ghost');
  report.secretCleared = await page.locator('.secret-panel').count();
  await page.click('.key-row .button-secondary');
  await page.waitForFunction(() => document.querySelector('.key-row .button-secondary').textContent === 'Resume');
  report.pausedLabel = await page.locator('.key-row .button-secondary').textContent();
  page.once('dialog', dialog => dialog.accept());
  await page.click('.key-row .button-danger');
  await page.waitForSelector('.empty-keys');
  report.emptyAfterRevoke = await page.locator('.empty-keys').textContent();
  report.patchKeyCalls = patchKeyCalls;
  report.deleteCalls = deleteCalls;
  page.once('dialog', dialog => dialog.accept());
  await page.click('.switch-control input');
  await page.waitForFunction(() => document.querySelector('.switch-label').textContent === 'Not required');
  report.enforcementAfterToggle = await page.locator('.switch-label').textContent();
  await page.click('a[href="#overview"]');
  await page.waitForSelector('.metrics-grid');
  await page.emulateMedia({ colorScheme: 'dark' });
  report.navCount = await page.locator('nav a').count();
  report.groups = await page.locator('.nav-group h2').allTextContents();
  report.sidebarWidth = (await page.locator('.sidebar').boundingBox()).width;
  report.sidebarBg = await page.locator('.sidebar').evaluate(el => getComputedStyle(el).backgroundColor);
  report.themeDark = await page.evaluate(() => document.documentElement.dataset.theme);
  report.bodyBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  report.active = await page.locator('.nav-link-active').textContent();
  report.skipTarget = await page.locator('.skip-link').getAttribute('href');
  report.landmarkFocusable = await page.locator('main#main-content').getAttribute('tabindex');
  await page.click('a[href="#providers"]');
  report.pageAfterNav = await page.locator('#page-title').textContent();
  report.ariaCurrent = await page.locator('a[href="#providers"]').getAttribute('aria-current');
  report.focusAfterDesktopNav = await page.evaluate(() => ({ className: document.activeElement.className, visibility: getComputedStyle(document.activeElement).visibility, display: getComputedStyle(document.activeElement).display }));
  await page.waitForSelector('.provider-card');
  report.providerCards = await page.locator('.provider-card').count();
  await page.locator('.provider-card', { hasText: 'openai' }).first().click();
  await page.waitForSelector('.connection-row');
  report.connectionRows = await page.locator('.connection-row').count();
  report.providerDetail = await page.locator('.provider-summary h2').textContent();
  report.modelRows = await page.locator('.model-row').count();
  await page.fill('#provider-filter', 'openrouter');
  report.filteredCards = await page.locator('.provider-card').count();
  await page.fill('#provider-filter', '');
  await page.click('.providers-content .provider-details .connection-row .button-secondary');
  await page.waitForFunction(() => document.querySelector('.providers-content .connection-row .state-tag').textContent === 'Disabled');
  report.providerPatches = providerPatches;
  report.providerDisabledLabel = await page.locator('.connection-row .state-tag').textContent();
  // Combo & Capability Adapter workflow (PRD 11).
  await page.click('a[href="#combo-capability-adapter"]');
  await page.waitForSelector('.combos-content');
  report.comboEmpty = await page.locator('.combo-list-panel .empty-keys').textContent();
  await page.click('.combos-toolbar .button-primary');
  await page.waitForSelector('.combo-form');
  await page.fill('.combo-form-grid input', 'daily');
  await page.selectOption('.combo-form-grid select', 'sticky-round-robin');
  await page.selectOption('.member-picker select', { label: 'openai / gpt-5' });
  await page.click('.member-picker .button-secondary');
  report.memberRows = await page.locator('.member-row').count();
  report.memberLabel = await page.locator('.member-row code').textContent();
  await page.click('.member-row .member-actions .button-secondary:nth-child(3)');
  report.memberDeselected = await page.locator('.member-row .state-tag').textContent();
  await page.click('.member-row .member-actions .button-secondary:nth-child(3)');
  await page.click('.combo-form > .button-primary');
  await page.waitForSelector('.combo-row');
  report.comboRowName = await page.locator('.combo-row strong').textContent();
  report.comboCreated = comboCreated;
  report.comboStrategy = comboCreated ? comboPayload.strategy : '';
  report.adapterCards = await page.locator('.adapter-card').count();
  report.adapterInitial = await page.locator('.adapter-card').first().locator('small').textContent();
  await page.locator('.adapter-card').first().locator('.adapter-header .button-secondary').click();
  await page.waitForFunction(() => document.querySelector('.adapter-card small').textContent.includes('empty pool is a no-op'));
  report.adapterAfterEnable = await page.locator('.adapter-card').first().locator('small').textContent();
  report.adapterEnabled = adapterEnabled;
	// System One typed request workflow (PRD 11, PRD-API-001).
	await page.click('a[href="#system-one"]');
	await page.waitForSelector('.systemone-content');
	report.systemOneProviders = await page.locator('#systemone-provider option').allTextContents();
	await page.waitForSelector('.systemone-layout');
	report.systemOneModels = await page.locator('.systemone-layout .model-row code').allTextContents();
	await page.fill('.systemone-layout .provider-inline-form input', 'jev-mini');
	await page.click('.systemone-layout .provider-inline-form button');
	await page.waitForFunction(() => document.querySelectorAll('.systemone-layout .model-row').length === 2);
	report.systemOneModelsAfterAdd = await page.locator('.systemone-layout .model-row code').allTextContents();
	report.systemOneModelAdded = systemOneModelAdded;
	await page.selectOption('.systemone-run select', 'jev');
	await page.click('.systemone-run .button-primary');
	await page.waitForSelector('.inline-notice');
	report.systemOneNotice = await page.locator('.inline-notice').textContent();
	report.systemOneProbeCalls = systemOneProbeCalls;
	report.systemOneProbeTransport = systemOneProbeTransport;
  await page.selectOption('#theme-select', 'light');
  report.themeAfterSelect = await page.evaluate(() => document.documentElement.dataset.theme);
  report.stored = await page.evaluate(() => localStorage.getItem('routeweft-theme'));
  await page.reload();
  report.themeAfterReload = await page.evaluate(() => document.documentElement.dataset.theme);
  await page.setViewportSize({ width: 390, height: 780 });
  await page.waitForTimeout(250);
  report.mobileViewport = await page.evaluate(() => ({ width: innerWidth, matches: matchMedia('(max-width: 700px)').matches, visibility: getComputedStyle(document.querySelector('.sidebar')).visibility, transform: getComputedStyle(document.querySelector('.sidebar')).transform, classes: document.querySelector('.sidebar').className }));
  report.sidebarHiddenMobile = !(await page.locator('.sidebar').isVisible());
  await page.click('.mobile-menu');
  await page.waitForTimeout(250);
  report.menuAriaExpanded = await page.locator('.mobile-menu').getAttribute('aria-expanded');
  report.menuLabel = await page.locator('.mobile-menu').getAttribute('aria-label');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(250);
  report.focusAfterEscape = await page.evaluate(() => document.activeElement.className);
  await page.click('.mobile-menu');
  await page.waitForTimeout(250);
  report.drawerOpenVisible = await page.locator('.sidebar').isVisible();
  report.drawerWidth = (await page.locator('.sidebar').boundingBox()).width;
  report.scrollWidthVsClient = await page.evaluate(() => document.documentElement.scrollWidth + '/' + document.documentElement.clientWidth);
  await page.mouse.click(370, 400);
  await page.waitForTimeout(250);
  report.drawerAfterEscape = await page.evaluate(() => ({ visibility: getComputedStyle(document.querySelector('.sidebar')).visibility, classes: document.querySelector('.sidebar').className }));
  report.drawerClosedAfterEscape = !(await page.locator('.sidebar').isVisible());
  report.focusAfterBackdrop = await page.evaluate(() => document.activeElement.className);

  await page.setViewportSize({ width: 1920, height: 1080 });
  report.wide = await page.evaluate(() => ({ sidebar: document.querySelector('.sidebar').getBoundingClientRect().width, workspaceMax: getComputedStyle(document.querySelector('.workspace-inner')).maxWidth, scroll: document.documentElement.scrollWidth + '/' + document.documentElement.clientWidth }));
  await page.selectOption('#theme-select', 'dark');
  report.darkSelectBg = await page.evaluate(() => getComputedStyle(document.querySelector('#theme-select')).backgroundColor);
  await page.selectOption('#theme-select', 'system');
  await page.waitForTimeout(100);
  await page.emulateMedia({ colorScheme: 'light' });
  await page.waitForTimeout(100);
  report.systemLight = await page.evaluate(() => document.documentElement.dataset.theme);
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.waitForTimeout(100);
  report.systemDark = await page.evaluate(() => document.documentElement.dataset.theme);
  await page.setViewportSize({ width: 320, height: 700 });
  report.tiny = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth + '/' + document.documentElement.clientWidth, titleSize: getComputedStyle(document.querySelector('h1')).fontSize }));
  report.errors = errors;
  console.log(JSON.stringify(report, null, 2));
  assert.deepEqual(report.overviewMetrics, ['Ready', '2', '1', '1,200']);
  assert.equal(report.signInPrompt, 'Sign in to Routeweft');
  assert.equal(report.passwordMasked, 'password');
  assert.equal(report.loginCalls, 1);
  assert.equal(report.initialOverviewCalls, 1);
  assert.match(report.refreshError, /Overview is temporarily unavailable/);
  assert.equal(report.staleMetricAfterRefreshError, 'Ready');
  assert.equal(report.overviewCallsAfterRetry, 3);
  assert.match(report.endpointURL, /\/v1$/);
  assert.equal(report.copiedEndpoint, report.endpointURL);
  assert.equal(report.transportCount, 6);
  assert.equal(report.enforcementLabel, 'Required');
  assert.equal(report.keyRowCountBefore, 1);
  assert.equal(report.secretShown, 'rw_smoke_secret_value');
  assert.equal(report.copiedSecret, 'rw_smoke_secret_value');
  assert.equal(report.createCalls, 1);
  assert.equal(report.secretCleared, 0);
  assert.equal(report.pausedLabel, 'Resume');
  assert.match(report.emptyAfterRevoke, /No API keys yet/);
  assert.equal(report.patchKeyCalls, 1);
  assert.equal(report.deleteCalls, 1);
  assert.equal(report.enforcementAfterToggle, 'Not required');
  assert.match(report.overviewAlert, /3 failed requests/);
  assert.match(report.overviewAlert, /telemetry is degraded/);
  assert.equal(report.sessionCalls, 1);
  assert.equal(report.signOutLabel, 'Sign out · operator');
  assert.match(report.monospaceValue, /JetBrains Mono/);
  assert.equal(report.overviewScroll, '1440/1440');
  assert.equal(report.navCount, 10);
  assert.deepEqual(report.groups, ['Operate', 'Route', 'Observe', 'System']);
  assert.equal(report.sidebarWidth, 248);
  assert.equal(report.themeDark, 'dark');
  assert.equal(report.pageAfterNav, 'Providers');
  assert.equal(report.ariaCurrent, 'page');
  assert.equal(report.providerCards, 2);
  assert.equal(report.connectionRows, 1);
  assert.equal(report.providerDetail, 'openai');
  assert.equal(report.modelRows, 1);
  assert.equal(report.filteredCards, 1);
  assert.equal(report.providerPatches, 1);
  assert.equal(report.providerDisabledLabel, 'Disabled');
  assert.match(report.comboEmpty, /No Combos yet/);
  assert.equal(report.memberRows, 1);
  assert.equal(report.memberLabel, 'openai / gpt-5');
  assert.equal(report.memberDeselected, 'Deselected');
  assert.equal(report.comboRowName, 'daily');
  assert.equal(report.comboCreated, true);
  assert.equal(report.comboStrategy, 'sticky-round-robin');
  assert.equal(report.adapterCards, 2);
  assert.match(report.adapterInitial, /Disabled/);
  assert.match(report.adapterAfterEnable, /empty pool is a no-op/);
	assert.equal(report.adapterEnabled, true);
	assert.deepEqual(report.systemOneProviders, ['typesafe']);
	assert.deepEqual(report.systemOneModels, ['jev']);
	assert.deepEqual(report.systemOneModelsAfterAdd, ['jev', 'jev-mini']);
	assert.equal(report.systemOneModelAdded, true);
	assert.match(report.systemOneNotice, /Typed request succeeded for jev \(HTTP 200\)/);
	assert.equal(report.systemOneProbeCalls, 1);
	assert.equal(report.systemOneProbeTransport, 'systemone');
  assert.equal(report.focusAfterDesktopNav.className, 'nav-link nav-link-active');
  assert.notEqual(report.focusAfterDesktopNav.visibility, 'hidden');
  assert.notEqual(report.focusAfterDesktopNav.display, 'none');
  assert.equal(report.firstFocus, 'skip-link');
  assert.equal(report.themeAfterReload, 'light');
  assert.equal(report.sidebarHiddenMobile, true);
  assert.equal(report.menuAriaExpanded, 'true');
  assert.equal(report.focusAfterEscape, 'mobile-menu icon-button');
  assert.equal(report.drawerClosedAfterEscape, true);
  assert.equal(report.focusAfterBackdrop, 'mobile-menu icon-button');
  assert.equal(report.scrollWidthVsClient, '390/390');
  assert.equal(report.wide.scroll, '1920/1920');
  assert.equal(report.systemLight, 'light');
  assert.equal(report.systemDark, 'dark');
  assert.equal(report.tiny.scroll, '320/320');
  assert.deepEqual(report.errors, []);
  await browser.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
