
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
  let usageSummaryCalls = 0;
  let quotaRefreshCalls = 0;
  let usagePeriod = '7d';
  let systemOneProbeCalls = 0;
  let systemOneProbeTransport = '';
  let tokenSaverSettings = { providerStrategy: 'fill-first', stickyRoundRobinLimit: '3', providerStrategies: '{}', comboStrategy: 'fallback', comboStickyRoundRobinLimit: '1', comboStrategies: '{}', quotaVisibility: '{}', enableObservability: 'false', observabilityMaxRecords: '1000', observabilityBatchSize: '20', observabilityFlushIntervalMs: '5000', observabilityMaxJsonSize: '5242880', outboundProxyEnabled: 'false', outboundProxyUrl: '[redacted]', noProxy: '[]', dnsToolEnabled: 'false', providerCompatibility: '{}', rtkEnabled: 'true', headroomEnabled: 'false', headroomUrl: 'http://localhost:8787', headroomCompressUserMessages: 'false', headroomTimeoutMs: '3000', cavemanEnabled: 'false', cavemanLevel: 'full', ponytailEnabled: 'false', ponytailLevel: 'full', pxpipeEnabled: 'false', pxpipeAutoInstall: 'true', pxpipeMinChars: '25000', pxpipeTimeoutMs: '15000' };
  let tokenSaverPatches = 0;
  let passwordAttempts = 0;
  let currentAdminPassword = 'not-a-real-secret';
  let restoreChecks = 0;
  let restoreActivations = 0;
  let logRequests = 0;
  let lastLogURL = '';
  const logs = Array.from({ length: 51 }, (_, index) => {
    const id = 51 - index;
    return { id, time: `2026-09-26T01:${String(index % 60).padStart(2, '0')}:00Z`, level: id === 17 ? 'ERROR' : 'INFO', message: id === 17 ? 'upstream connection timeout' : `service event ${id}`, attributes: id === 17 ? { providerId: 'openai', retry: 1 } : {} };
  });
  const createdKeyID = 'k-ci-1';
  const createdSecret = 'rw_smoke_secret_value';
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
  if (url.pathname === '/admin/v1/settings' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ settings: { requireApiKey: settingsRequireApiKey, ...tokenSaverSettings }, writable: ['requireApiKey', ...Object.keys(tokenSaverSettings)] }) });
  }
  if (url.pathname === '/admin/v1/settings' && method === 'PATCH') {
    const patch = route.request().postDataJSON().set;
    tokenSaverPatches += 1;
    if (patch.requireApiKey !== undefined) settingsRequireApiKey = String(patch.requireApiKey);
    for (const [key, value] of Object.entries(patch)) if (key !== 'requireApiKey') tokenSaverSettings[key] = String(value);
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ configRevision: 8, settings: { requireApiKey: settingsRequireApiKey, ...tokenSaverSettings } }) });
  }
  if (url.pathname === '/admin/v1/auth/password' && method === 'POST') {
    passwordAttempts += 1;
    const body = route.request().postDataJSON();
    if (body.currentPassword !== currentAdminPassword) return route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: { code: 'invalid_credentials', message: 'Current password is incorrect.' } }) });
    currentAdminPassword = body.newPassword;
    return route.fulfill({ status: 204 });
  }
  if (url.pathname === '/admin/v1/backup' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/zip', headers: { 'Content-Disposition': 'attachment; filename="routeweft-backup.zip"' }, body: Buffer.from('sanitized-smoke-backup') });
  }
  if (url.pathname === '/admin/v1/backup/restore/check' && method === 'POST') {
    restoreChecks += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ valid: true, schemaVersion: 1, configRevision: 8 }) });
  }
  if (url.pathname === '/admin/v1/backup/restore' && method === 'POST') {
    restoreActivations += 1;
    return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ accepted: true, activation: 'scheduled', schemaVersion: 1, configRevision: 8 }) });
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
  if (url.pathname === '/admin/v1/usage/summary' && method === 'GET') {
    usageSummaryCalls += 1;
    usagePeriod = url.searchParams.get('period');
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
      period: usagePeriod, since: '2026-09-18T00:00:00.000Z',
      totals: { requests: 120, inputTokens: 9000, outputTokens: 3000, cacheReadTokens: 4000, cacheWriteTokens: 500, errors: 3, avgDurationMs: 420, avgTtftMs: 90 },
      series: [{ day: '2026-09-24', requests: 70, inputTokens: 5000, outputTokens: 1800, cacheReadTokens: 2000, cacheWriteTokens: 200 }, { day: '2026-09-25', requests: 50, inputTokens: 4000, outputTokens: 1200, cacheReadTokens: 2000, cacheWriteTokens: 300 }],
      providers: [{ key: 'openai', requests: 100, inputTokens: 8000, outputTokens: 2800, errors: 2 }, { key: 'anthropic', requests: 20, inputTokens: 1000, outputTokens: 200, errors: 1 }],
      models: [{ key: 'openai/gpt-5', requests: 90, inputTokens: 7000, outputTokens: 2500, errors: 1 }],
      statuses: [{ key: '200', requests: 117, inputTokens: 9000, outputTokens: 3000, errors: 0 }, { key: '500', requests: 3, inputTokens: 0, outputTokens: 0, errors: 3 }],
    }) });
  }
  if (url.pathname === '/admin/v1/usage' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ id: 1, requestId: 'req-abc', status: 200, providerId: 'openai', modelId: 'gpt-5', inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, cacheWriteTokens: 0, durationMs: 300, ttftMs: 80, createdAt: '2026-09-25T20:00:00Z' }], page: 1, pageSize: 50, total: 1 }) });
  }
  if (url.pathname === '/admin/v1/quota' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ connectionId: 'conn-1', providerId: 'openai', name: 'primary', enabled: true, status: 'available', remaining: 42, resetAt: '2026-09-26T01:00:00Z', observedAt: '2026-09-25T23:30:00Z' }, { connectionId: 'conn-2', providerId: 'anthropic', name: 'claude', enabled: false, status: 'exhausted', remaining: 0, resetAt: '2026-09-26T02:00:00Z', observedAt: '2026-09-25T23:30:00Z' }], page: 1, pageSize: 100, total: 2 }) });
  }
  if (url.pathname === '/admin/v1/quota/refresh' && method === 'POST') {
    quotaRefreshCalls += 1;
    return route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify({ accepted: true }) });
  }
  if (url.pathname === '/admin/v1/logs' && method === 'GET') {
    logRequests += 1;
    lastLogURL = url.search;
    const level = (url.searchParams.get('level') || '').toUpperCase();
    const query = (url.searchParams.get('query') || '').toLowerCase();
    const filtered = logs.filter(item => (!level || item.level === level) && (!query || item.message.toLowerCase().includes(query) || JSON.stringify(item.attributes).toLowerCase().includes(query)));
    const pageNumber = Number(url.searchParams.get('page') || 1);
    const size = Number(url.searchParams.get('pageSize') || 50);
    const items = filtered.slice((pageNumber - 1) * size, pageNumber * size);
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items, page: pageNumber, pageSize: size, total: filtered.length }) });
  }
  if (url.pathname === '/admin/v1/models' && method === 'POST') {
    systemOneModelAdded = true;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ providerId: 'typesafe', id: 'jev-mini' }) });
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
  // Usage & request details (PRD 13, PRD-OBS-001/002).
  await page.click('a[href="#usage"]');
  await page.waitForSelector('.usage-content');
  await page.waitForSelector('.usage-chart');
  report.usageMetrics = await page.locator('.usage-metrics .metric-value').allTextContents();
  report.usageBars = await page.locator('.usage-bar').count();
  report.usageProviderRows = await page.locator('.breakdown-row').count();
  report.usageEventRows = await page.locator('.usage-event').count();
  report.usagePeriod = usagePeriod;
  await page.selectOption('#usage-period', '24h');
  await page.waitForFunction(() => document.querySelector('.usage-toolbar .count-label').textContent.includes('since'));
  report.usagePeriodAfterChange = usagePeriod;
  report.usageSummaryCalls = usageSummaryCalls;
  // Quota Tracker (PRD-QUOTA-001).
  await page.click('a[href="#quota-tracker"]');
  await page.waitForSelector('.quota-content');
  await page.waitForSelector('.quota-row');
  report.quotaRows = await page.locator('.quota-row').count();
  report.quotaRemaining = await page.locator('.quota-remaining').allTextContents();
  report.quotaStatuses = await page.locator('.quota-row .state-tag').allTextContents();
  await page.click('.quota-toolbar .button-primary');
  await page.waitForSelector('.inline-notice');
  report.quotaNotice = await page.locator('.inline-notice').textContent();
  report.quotaRefreshCalls = quotaRefreshCalls;
  // Token Saver (PRD-XFORM-001/004/005).
  await page.click('a[href="#token-saver"]');
  await page.waitForSelector('.token-saver-content');
  report.tokenSaverFeatures = await page.locator('.feature-row').count();
  report.tokenSaverRtkLabel = await page.locator('.feature-row').first().locator('.state-tag').textContent();
  await page.selectOption('select[aria-label="Caveman level"]', 'ultra');
  await page.locator('.setting-row', { has: page.locator('select[aria-label="Caveman level"]') }).locator('button').click();
  await page.waitForFunction(() => document.querySelector('.inline-notice') && document.querySelector('.inline-notice').textContent.includes('Caveman level saved'));
  report.cavemanLevel = tokenSaverSettings.cavemanLevel;
  await page.click('.feature-row .button-secondary');
  await page.waitForFunction(() => document.querySelector('.inline-notice')?.textContent.includes('RTK'));
  report.rtkAfterToggle = tokenSaverSettings.rtkEnabled;
  report.tokenSaverPatches = tokenSaverPatches;
  // Console Log (PRD §14).
  await page.click('a[href="#console-log"]');
  await page.waitForSelector('.console-log-content');
  await page.waitForSelector('.console-log-row');
  report.logRowsFirstPage = await page.locator('.console-log-row').count();
  report.logFirstMessage = await page.locator('.console-log-row p').first().textContent();
  report.logPageOne = await page.locator('.console-log-pagination .count-label').textContent();
  await page.locator('.console-log-pagination button').last().click();
  await page.waitForFunction(() => document.querySelector('.console-log-pagination .count-label').textContent.includes('Page 2'));
  report.logRowsSecondPage = await page.locator('.console-log-row').count();
  await page.selectOption('#console-log-level', 'ERROR');
  await page.fill('#console-log-query', 'timeout');
  await page.locator('.console-log-toolbar button[type="submit"]').click();
  await page.waitForFunction(() => document.querySelector('.console-log-pagination .count-label').textContent.includes('Page 1 of 1'));
  report.logFilteredRows = await page.locator('.console-log-row').count();
  report.logFilteredMessage = await page.locator('.console-log-row p').textContent();
  await page.locator('.console-log-row summary').click();
  report.logDetails = await page.locator('.console-log-row pre').textContent();
  await page.locator('.console-log-toolbar button[type="button"]').click();
  await page.fill('#console-log-query', 'not-found');
  await page.locator('.console-log-toolbar button[type="submit"]').click();
  await page.waitForSelector('.console-log-content .empty-keys');
  report.logEmptyState = await page.locator('.console-log-content .empty-keys').textContent();
  report.logRequests = logRequests;
  report.lastLogURL = lastLogURL;
  // Settings (PRD §14-15).
  await page.click('a[href="#settings"]');
  await page.waitForSelector('.settings-content');
  report.dashboardLoginState = await page.locator('.setting-toggle').first().locator('.state-tag').textContent();
  await page.selectOption('#setting-providerStrategy', 'round-robin');
  await page.locator('.settings-field', { has: page.locator('#setting-providerStrategy') }).locator('button').click();
  await page.waitForFunction(() => document.querySelector('.inline-notice')?.textContent.includes('Provider strategy saved'));
  report.providerStrategy = tokenSaverSettings.providerStrategy;
  await page.locator('.setting-toggle').filter({ hasText: 'Require client API key' }).locator('button').click();
  await page.waitForFunction(() => document.querySelector('.inline-notice')?.textContent.includes('Require client API key updated'));
  report.requireApiKeyAfterToggle = settingsRequireApiKey;
  await page.locator('.settings-json-field').filter({ hasText: 'Provider strategy overrides' }).locator('textarea').fill('{"openai":"sticky-round-robin"}');
  await page.locator('.settings-json-field').filter({ hasText: 'Provider strategy overrides' }).locator('button').click();
  await page.waitForFunction(() => document.querySelector('.inline-notice')?.textContent.includes('Provider strategy overrides saved'));
  report.providerStrategies = tokenSaverSettings.providerStrategies;
  await page.fill('#setting-observabilityMaxRecords', '0');
  await page.locator('.settings-field', { has: page.locator('#setting-observabilityMaxRecords') }).locator('button').click();
  await page.waitForFunction(() => document.querySelector('.settings-content .auth-error')?.textContent.includes('at least 1'));
  report.invalidSettingError = await page.locator('.settings-content .auth-error').textContent();
  await page.fill('#setting-observabilityMaxRecords', '1500');
  await page.locator('.settings-field', { has: page.locator('#setting-observabilityMaxRecords') }).locator('button').click();
  await page.waitForFunction(() => document.querySelector('.inline-notice')?.textContent.includes('Maximum retained records saved'));
  report.observabilityMaxRecords = tokenSaverSettings.observabilityMaxRecords;
  const [backupDownload] = await Promise.all([page.waitForEvent('download'), page.click('.settings-download')]);
  report.backupFilename = backupDownload.suggestedFilename();
  await page.locator('#restore-file').setInputFiles({ name: 'routeweft-test.zip', mimeType: 'application/zip', buffer: Buffer.from('sanitized-smoke-backup') });
  await page.click('.settings-restore button');
  await page.waitForSelector('.restore-check-result');
  report.restoreCheckResult = await page.locator('.restore-check-result').innerText();
  page.once('dialog', dialog => dialog.accept());
  await page.click('.restore-check-result .button-danger');
  await page.waitForSelector('.sign-in-panel');
  report.restoreChecks = restoreChecks;
  report.restoreActivations = restoreActivations;
  await page.fill('#admin-username', 'operator');
  await page.fill('#admin-password', 'not-a-real-secret');
  await page.locator('.sign-in-panel button[type="submit"]').click();
  await page.waitForSelector('.settings-content');
  await page.fill('#change-password-current', 'wrong-password');
  await page.fill('#change-password-new', 'rotated-secret');
  await page.fill('#change-password-confirm', 'rotated-secret');
  await page.locator('.settings-password button[type="submit"]').click();
  await page.waitForFunction(() => document.querySelector('.settings-content .auth-error')?.textContent.includes('Current password is incorrect'));
  report.passwordError = await page.locator('.settings-content .auth-error').textContent();
  await page.fill('#change-password-current', 'not-a-real-secret');
  await page.fill('#change-password-new', 'rotated-secret');
  await page.fill('#change-password-confirm', 'rotated-secret');
  await page.locator('.settings-password button[type="submit"]').click();
  await page.waitForSelector('.sign-in-panel');
  report.passwordAttempts = passwordAttempts;
  report.adminPasswordRotated = currentAdminPassword === 'rotated-secret';
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
  // UI_STYLE.md §15 accessibility audit: name/label rules, contrast, heading
  // order, landmark presence, and reduced-motion behavior. Runs on the live
  // stubbed DOM rather than a static source scrape.
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  report.a11y = await page.evaluate(() => {
    const luminance = color => {
      const match = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(color);
      if (!match) return null;
      const channel = value => {
        const scaled = Number(value) / 255;
        return scaled <= 0.03928 ? scaled / 12.92 : Math.pow((scaled + 0.055) / 1.055, 2.4);
      };
      return 0.2126 * channel(match[1]) + 0.7152 * channel(match[2]) + 0.0722 * channel(match[3]);
    };
    const contrast = element => {
      const style = getComputedStyle(element);
      let background = style.backgroundColor;
      let parent = element.parentElement;
      while (parent && (background === 'rgba(0, 0, 0, 0)' || background === 'transparent')) {
        background = getComputedStyle(parent).backgroundColor;
        parent = parent.parentElement;
      }
      const foreground = luminance(style.color);
      const backdrop = luminance(background);
      if (foreground === null || backdrop === null) return null;
      const lighter = Math.max(foreground, backdrop);
      const darker = Math.min(foreground, backdrop);
      return (lighter + 0.05) / (darker + 0.05);
    };
    const invisible = element => element.offsetParent === null && getComputedStyle(element).position !== 'fixed';
    const nameOf = element => {
      const label = element.getAttribute('aria-label');
      if (label && label.trim()) return label.trim();
      const labelledBy = element.getAttribute('aria-labelledby');
      if (labelledBy) {
        const text = labelledBy.split(/\s+/).map(id => document.getElementById(id)?.textContent || '').join(' ').trim();
        if (text) return text;
      }
      const content = (element.textContent || '').trim();
      return content.length > 0 ? content : '';
    };
    const buttons = [...document.querySelectorAll('button')].filter(element => !invisible(element));
    const unlabeledButtons = buttons.filter(element => !nameOf(element)).map(element => element.className || element.outerHTML.slice(0, 60));
    const unlabeledInputs = [...document.querySelectorAll('input:not([type="hidden"]), select, textarea')]
      .filter(element => !invisible(element))
      .filter(element => {
        if (element.getAttribute('aria-label')?.trim()) return false;
        if (element.id && document.querySelector('label[for="' + element.id + '"]')) return false;
        return !(element.closest('label')?.textContent || '').trim();
      })
      .map(element => element.id || element.name || element.outerHTML.slice(0, 60));
    const lowContrast = [...document.querySelectorAll('body *')]
      .filter(element => !invisible(element))
      .filter(element => [...element.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim()))
      .map(element => ({ element, ratio: contrast(element) }))
      .filter(entry => entry.ratio !== null && entry.ratio < 4.5)
      .map(entry => ({ className: entry.element.className, ratio: Math.round(entry.ratio * 100) / 100 }));
    const landmarks = {
      main: document.querySelectorAll('main').length,
      nav: document.querySelectorAll('nav').length,
      h1: document.querySelectorAll('h1').length,
    };
    const headings = [...document.querySelectorAll('h1, h2, h3, h4')]
      .map(element => ({ level: Number(element.tagName.slice(1)), text: (element.textContent || '').trim().slice(0, 40) }));
    const headingSkips = [];
    for (let index = 1; index < headings.length; index += 1) {
      if (headings[index].level - headings[index - 1].level > 1) headingSkips.push([headings[index - 1], headings[index]]);
    }
    const focusable = [...document.querySelectorAll('a[href], button, input, select, textarea')].filter(element => !invisible(element) && !element.disabled);
    const skipLink = document.querySelector('.skip-link');
    const skipLinkVisibleOnFocus = (() => {
      if (!skipLink) return false;
      const before = getComputedStyle(skipLink).transform;
      skipLink.focus();
      skipLink.classList.add('smoke-focus');
      const after = getComputedStyle(skipLink).transform;
      const focused = document.activeElement === skipLink;
      skipLink.blur();
      return focused && (before !== after || getComputedStyle(skipLink).outlineStyle !== 'none');
    })();
    return {
      unlabeledButtons,
      unlabeledInputs,
      lowContrast,
      landmarks,
      headingSkips,
      focusableCount: focusable.length,
      skipLinkVisibleOnFocus,
    };
  });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.waitForTimeout(200);
  report.reducedMotion = await page.evaluate(() => ({
    navDuration: getComputedStyle(document.querySelector('.nav-link')).transitionDuration,
    buttonDuration: getComputedStyle(document.querySelector('.button-secondary')).transitionDuration,
    animation: getComputedStyle(document.querySelector('.usage-bar') || document.body).animationDuration,
  }));
  await page.emulateMedia({ reducedMotion: 'no-preference' });
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
  assert.equal(report.usageMetrics[0], '120');
  assert.equal(report.usageMetrics[1], '12,000');
  assert.equal(report.usageMetrics[2], '4,500');
  assert.equal(report.usageMetrics[3], '420 ms');
  assert.equal(report.usageBars, 2);
  assert.equal(report.usageProviderRows, 5);
  assert.equal(report.usageEventRows, 1);
  assert.equal(report.usagePeriod, '7d');
  assert.equal(report.usagePeriodAfterChange, '24h');
  assert.equal(report.usageSummaryCalls >= 2, true);
  assert.equal(report.quotaRows, 2);
  assert.deepEqual(report.quotaRemaining, ['42 remaining', '0 remaining']);
  assert.deepEqual(report.quotaStatuses, ['available', 'exhausted', 'disabled']);
  assert.match(report.quotaNotice, /never blocks inference/);
  assert.equal(report.quotaRefreshCalls, 1);
  assert.equal(report.tokenSaverFeatures, 7);
  assert.equal(report.tokenSaverRtkLabel, 'Enabled');
  assert.equal(report.cavemanLevel, 'ultra');
  assert.equal(report.rtkAfterToggle, 'false');
  assert.equal(report.tokenSaverPatches, 3);
  assert.equal(report.logRowsFirstPage, 50);
  assert.match(report.logFirstMessage, /service event 51/);
  assert.match(report.logPageOne, /Page 1 of 2/);
  assert.equal(report.logRowsSecondPage, 1);
  assert.equal(report.logFilteredRows, 1);
  assert.equal(report.logFilteredMessage, 'upstream connection timeout');
  assert.match(report.logDetails, /openai/);
  assert.match(report.logEmptyState, /No logs match/);
  assert.match(report.lastLogURL, /query=not-found/);
  assert.equal(report.logRequests, 5);
  assert.equal(report.dashboardLoginState, 'Required');
  assert.equal(report.providerStrategy, 'round-robin');
  assert.equal(report.requireApiKeyAfterToggle, 'true');
  assert.equal(report.providerStrategies, '{"openai":"sticky-round-robin"}');
  assert.match(report.invalidSettingError, /whole number of at least 1/);
  assert.equal(report.observabilityMaxRecords, '1500');
  assert.equal(report.backupFilename, 'routeweft-backup.zip');
  assert.match(report.restoreCheckResult, /Valid · schema 1 · config revision 8/);
  assert.equal(report.restoreChecks, 1);
  assert.equal(report.restoreActivations, 1);
  assert.match(report.passwordError, /Current password is incorrect/);
  assert.equal(report.passwordAttempts, 2);
  assert.equal(report.adminPasswordRotated, true);
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
  assert.deepEqual(report.a11y.unlabeledButtons, []);
  assert.deepEqual(report.a11y.unlabeledInputs, []);
  assert.deepEqual(report.a11y.lowContrast, []);
  assert.equal(report.a11y.landmarks.main >= 1, true);
  assert.equal(report.a11y.landmarks.nav >= 1, true);
  assert.equal(report.a11y.landmarks.h1, 1);
  assert.deepEqual(report.a11y.headingSkips, []);
  assert.equal(report.a11y.skipLinkVisibleOnFocus, true);
  assert.equal(report.a11y.focusableCount > 20, true);
  assert.equal(report.reducedMotion.navDuration.endsWith('s'), true);
  assert.equal(parseFloat(report.reducedMotion.navDuration) <= 0.001, true);
  assert.equal(parseFloat(report.reducedMotion.buttonDuration) <= 0.001, true);
  assert.equal(parseFloat(report.reducedMotion.animation) <= 0.001, true);
  assert.deepEqual(report.errors, []);
  await browser.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
