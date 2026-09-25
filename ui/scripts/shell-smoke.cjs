
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || '/root/.cache/ms-playwright/chromium-1148/chrome-linux/chrome', args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
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
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
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
