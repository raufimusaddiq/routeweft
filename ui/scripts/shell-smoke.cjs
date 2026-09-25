
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
