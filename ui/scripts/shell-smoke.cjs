
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || '/root/.cache/ms-playwright/chromium-1148/chrome-linux/chrome', args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], { origin: 'http://127.0.0.1:4173' });
  const errors = [];
  const report = {};
  const artifacts = process.env.ROUTEWEFT_SMOKE_ARTIFACTS || '/tmp/routeweft-ui-evidence';
  mkdirSync(artifacts, { recursive: true });
  report.visualChecks = [];
  async function captureVisuals(view) {
    const savedTheme = await page.locator('#theme-select').inputValue();
    for (const theme of ['light', 'dark', 'system']) {
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.selectOption('#theme-select', theme);
      if (theme === 'system') await page.emulateMedia({ colorScheme: 'dark' });
      for (const width of theme === 'system' ? [1440] : [1440, 390]) {
        await page.setViewportSize({ width, height: 900 });
        await page.mouse.move(width - 1, 1);
        await page.evaluate(() => document.fonts.ready);
        await page.waitForTimeout(260); // let hover/theme transitions and the 120ms entry settle
        const checks = await page.evaluate(() => {
          const canvas = document.createElement('canvas');
          canvas.width = canvas.height = 1;
          const context = canvas.getContext('2d', { willReadFrequently: true });
          const rgba = value => {
            context.clearRect(0, 0, 1, 1);
            context.fillStyle = value;
            context.fillRect(0, 0, 1, 1);
            return [...context.getImageData(0, 0, 1, 1).data];
          };
          const luminance = rgb => rgb.slice(0, 3).map(value => {
            const c = value / 255;
            return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
          }).reduce((sum, c, index) => sum + c * [0.2126, 0.7152, 0.0722][index], 0);
          const ratio = (a, b) => {
            const x = luminance(a), y = luminance(b);
            return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
          };
          const background = element => {
            const layers = [];
            for (let node = element; node; node = node.parentElement) layers.push(rgba(getComputedStyle(node).backgroundColor));
            return layers.reverse().reduce((base, color) => color.slice(0, 3).map((c, i) => c * color[3] / 255 + base[i] * (1 - color[3] / 255)), [255, 255, 255]);
          };
          const elements = [...document.querySelectorAll('.app-shell, .app-shell *')];
          const visible = element => element.getClientRects().length && getComputedStyle(element).visibility !== 'hidden';
          const lowContrast = elements.filter(visible).filter(element => !element.disabled && [...element.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim())).flatMap(element => {
            const value = ratio(rgba(getComputedStyle(element).color), background(element));
            return value < 4.5 ? [{ element: element.className, ratio: value }] : [];
          });
          const radii = elements.flatMap(element => ['', '::before', '::after'].flatMap(pseudo => {
            const style = getComputedStyle(element, pseudo || null);
            return ['borderTopLeftRadius', 'borderTopRightRadius', 'borderBottomLeftRadius', 'borderBottomRightRadius'].filter(key => style[key].split(' ').some(value => parseFloat(value) > 2)).map(key => `${element.className}${pseudo}: ${key}=${style[key]}`);
          }));
          const states = ['--danger', '--success', '--warning', '--info'].map(token => ({ token, ratio: ratio(rgba(getComputedStyle(document.documentElement).getPropertyValue(token)), background(document.body)) }));
          const sizes = [...new Set(elements.filter(visible).filter(element => [...element.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim())).map(element => getComputedStyle(element).fontSize))].sort((a, b) => parseFloat(a) - parseFloat(b));
          const eleven = elements.filter(visible).filter(element => [...element.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim()) && getComputedStyle(element).fontSize === '11px' && !element.closest('svg')).map(element => element.tagName + '.' + element.className);
          const accents = [...document.querySelectorAll('.accent-surface')].filter(visible).length;
          const active = document.querySelector('.nav-link-active');
          const marker = getComputedStyle(active, '::before');
          return {
            resolvedTheme: document.documentElement.dataset.theme,
            overflow: document.documentElement.scrollWidth - innerWidth,
            lowContrast, radii, states, sizes, eleven, accents,
            active: { width: marker.width, color: marker.backgroundColor, weight: getComputedStyle(active).fontWeight, inactiveWeight: getComputedStyle(document.querySelector('.nav-link:not(.nav-link-active)')).fontWeight },
          };
        });
        assert.equal(checks.resolvedTheme, theme === 'system' ? 'dark' : theme);
        assert.equal(checks.overflow, 0, `${view}/${theme}/${width}: horizontal overflow`);
        assert.deepEqual(checks.radii, [], `${view}/${theme}/${width}: rounded shell`);
        assert.deepEqual(checks.lowContrast, [], `${view}/${theme}/${width}: low contrast`);
        assert.ok(checks.states.every(state => state.ratio >= 4.5), JSON.stringify(checks.states));
        const allowedSizes = width < 700 ? ['11px', '12px', '16px', '24px', '28px'] : ['11px', '12px', '16px', '28px'];
        assert.ok(checks.sizes.every(size => allowedSizes.includes(size)), `${view}/${theme}/${width}: type sizes ${checks.sizes}`);
        assert.ok(checks.sizes.includes('12px') && checks.sizes.includes('28px'), `${view}/${theme}/${width}: scale ${checks.sizes}`);
        assert.deepEqual(checks.eleven, [], `${view}/${theme}/${width}: 11px is reserved for chart axis text`);
        assert.equal(checks.accents, 1, `${view}/${theme}/${width}: exactly one accent surface`);
        assert.equal(checks.active.width, '2px');
        assert.notEqual(checks.active.color, 'rgba(0, 0, 0, 0)');
        assert.ok(Number(checks.active.weight) > Number(checks.active.inactiveWeight));
        if (width === 1440) {
          await page.locator('.brand').focus();
          await page.keyboard.press('Tab');
          checks.focus = await page.locator('.nav-link').first().evaluate(element => ({ focused: document.activeElement === element, visible: element.matches(':focus-visible'), style: getComputedStyle(element).outlineStyle, width: getComputedStyle(element).outlineWidth, color: getComputedStyle(element).outlineColor, offset: getComputedStyle(element).outlineOffset, token: getComputedStyle(document.documentElement).getPropertyValue('--focus').trim() }));
          assert.equal(checks.focus.offset, '2px');
          assert.notEqual(checks.focus.color, 'rgb(32, 36, 43)', 'focus ring is the accent colour, not the ink');
          assert.equal(checks.focus.focused, true);
          assert.equal(checks.focus.visible, true);
          assert.equal(checks.focus.style, 'solid');
          assert.equal(checks.focus.width, '2px');
          assert.notEqual(checks.focus.color, 'rgba(0, 0, 0, 0)');
          await page.locator('.nav-link').first().evaluate(element => element.blur());
        }
        const screenshot = `${view}-${theme}-${width}.png`;
        await page.screenshot({ path: join(artifacts, screenshot), fullPage: true });
        report.visualChecks.push({ view, theme, width, screenshot, ...checks });
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.selectOption('#theme-select', savedTheme);
    await page.emulateMedia({ colorScheme: 'light' });
  }
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  // Stub the control API so the smoke run needs no server or credentials.
  const overviewPayload = {
    runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 7, snapshotVersion: 7 },
    providers: { catalog: 80, nodes: 3, enabledConnections: 2 },
    apiKeys: { active: 1 },
    traffic: { active: 4, requests: 1200, errors: 3, lastRequestAt: '2026-09-25T20:00:00Z' },
    telemetry: { enabled: true, written: 1180, lost: 0, lostDiagnostics: 2, degraded: true },
  };
  const signInFixtureProviders = [
    { id: 'codex', transports: ['openai-responses'], auth: 'oauth', authModes: [], modelCatalog: 'static', passthroughModels: false, reportsUsage: true, flowType: 'authorization_code_pkce', signInModes: ['browser', 'import'], signInMethods: [{ mode: 'browser', authKind: 'oauth', resolvesIdentity: true, completion: 'redirect-url', loopbackRedirect: 'http://localhost:1455/auth/callback' }, { mode: 'import', authKind: 'oauth', resolvesIdentity: true }], configuredNodes: 0, enabledAccounts: 0, readiness: { status: 'ready', served: true } },
    { id: 'kimchi', transports: ['openai-chat'], auth: 'api-key', authModes: ['api-key', 'oauth'], modelCatalog: 'dynamic', passthroughModels: true, reportsUsage: false, flowType: 'browser_token', signInModes: ['browser'], signInMethods: [{ mode: 'browser', authKind: 'oauth', resolvesIdentity: false, completion: 'token' }], configuredNodes: 0, enabledAccounts: 0, readiness: { status: 'ready', served: true } },
  ];
  let sessionCalls = 0;
  let loginCalls = 0;
  let overviewCalls = 0;
  let settingsRequireApiKey = 'true';
  let keyListCalls = 0;
  let createCalls = 0;
  let deleteCalls = 0;
  let patchKeyCalls = 0;
  let accessPatchCalls = 0;
  let keyAccess = { restricted: false, allow: [] };
  let createAccessBody = null;
  let lastAccessBody = null;
  let providerPatches = 0;
  let modelProbeStatus = 200;
  let comboCreated = false;
  let comboPutCalls = 0;
  let comboDeleteCalls = 0;
  let comboPayload = null;
  let adapterEnabled = false;
  let systemOneModelAdded = false;
  // Sign-in fixtures (#214): served only once the main provider checks have run,
  // so the provider counts above stay as they were.
  let signInProviders = false;
  let signInExpiresAt = '';
  const signInStarts = [];
  const signInCancels = [];
  const signInCallbacks = [];
  const signInPatches = [];
  let usageSummaryCalls = 0;
  let lastUsageURL = '';
  // Quota mock (#213): 130 accounts so paging must reach past 100, with one row
  // per limit state. `remaining` is a fraction in [0,1], as the read model sends.
  const quotaURLs = [];
  const quotaAccounts = [
    { connectionId: 'conn-1', providerId: 'openai', name: 'primary', enabled: true, status: 'available', plan: 'plus', remaining: 0.42, resetAt: '2026-09-26T01:00:00Z', nextResetAt: '2026-09-26T01:00:00Z', observedAt: '2026-09-25T23:30:00Z', stale: false, windows: [{ name: 'session', remaining: 0.42, resetAt: '2026-09-26T01:00:00Z', unlimited: false }, { name: 'weekly', remaining: 0.85, resetAt: '2026-09-30T00:00:00Z', unlimited: false }] },
    { connectionId: 'conn-2', providerId: 'anthropic', name: 'claude', enabled: false, status: 'exhausted', remaining: 0, resetAt: '2026-09-26T02:00:00Z', nextResetAt: '2026-09-26T02:00:00Z', observedAt: '2026-09-25T23:30:00Z', stale: false, windows: [{ name: 'session', remaining: 0, resetAt: '2026-09-26T02:00:00Z', unlimited: false }] },
    { connectionId: 'conn-3', providerId: 'deepseek', name: 'balance', enabled: true, status: 'unknown', plan: 'DeepSeek', observedAt: '2026-09-25T23:30:00Z', stale: false, creditAvailable: true, windows: [{ name: 'Balance (USD)', unlimited: false, credit: { currency: 'USD', state: 'positive', total: 12.5 } }, { name: 'Balance (CNY)', unlimited: false, credit: { currency: 'CNY', state: 'zero', total: 0 } }] },
    { connectionId: 'conn-4', providerId: 'groq', name: 'free tier', enabled: true, status: 'unlimited', plan: 'free', observedAt: '2026-09-25T23:30:00Z', stale: false, windows: [{ name: 'daily', unlimited: true }] },
    { connectionId: 'conn-5', providerId: 'openai', name: 'fresh', enabled: true, status: 'not-reported' },
    { connectionId: 'conn-6', providerId: 'kimi', name: 'flaky', enabled: true, status: 'error', error: true, observedAt: '2026-09-25T20:00:00Z', windowsObservedAt: '2026-09-25T18:00:00Z', stale: true, windows: [{ name: 'weekly', remaining: 0.5, unlimited: false }] },
    ...Array.from({ length: 124 }, (_, i) => ({ connectionId: `fill-${i}`, providerId: 'openai', name: `bulk-${String(i).padStart(3, '0')}`, enabled: true, status: 'available', remaining: 1, observedAt: '2026-09-25T23:30:00Z', stale: false })),
  ];
  let usageGate = null;
  // History length of the usage fixture: 2 days by default (the original fixture); a steady 100 a day with the latest 7 days at 140 otherwise.
  let historyDays = 2;
  const windowedHistory = () => { const all = historySeries(); return all && (usagePeriod === '7d' ? all.slice(-7) : usagePeriod === '24h' ? all.slice(-1) : all); };
  const historySeries = () => historyDays === 2 ? null : Array.from({ length: historyDays }, (_, i) => { const date = new Date(Date.UTC(2026, 8, 25 - (historyDays - 1 - i))).toISOString().slice(0, 10); return { day: date, requests: i >= historyDays - 7 ? 140 : 100, inputTokens: 1000, outputTokens: 300, cacheReadTokens: 0, cacheWriteTokens: 0 }; });
  let usageMode = 'ok';
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
  let streamURLs = [];
  let streamMode = 'ok';
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
  const readerKey = comboCreated ? [{ id: 'k-reader', name: 'combo-reader', prefix: 'rw_rdr', enabled: true, paused: false, createdAt: '2026-09-25T20:05:00Z', access: { restricted: true, allow: ['daily'] } }] : [];
  const items = deleteCalls > 0 ? readerKey : [{ id: createdKeyID, name: 'ci-pipeline', prefix: 'rw_smoke', enabled: true, paused: patchKeyCalls % 2 === 1, createdAt: '2026-09-25T20:00:00Z', access: keyAccess }, ...readerKey];
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items, page: 1, pageSize: 50, total: items.length }) });
  }
  if (url.pathname === '/admin/v1/keys' && method === 'POST') {
    createCalls += 1;
    createAccessBody = route.request().postDataJSON().access;
    return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ key: { id: createdKeyID, name: route.request().postDataJSON().name, prefix: 'rw_smoke', enabled: true, paused: false, access: keyAccess }, secret: createdSecret }) });
  }
  if (url.pathname.startsWith('/admin/v1/keys/') && method === 'PATCH') {
    const patch = route.request().postDataJSON();
    if (patch.access) {
      // Mirror the server: normalize (sort) and clear the list when unrestricted.
      accessPatchCalls += 1;
      lastAccessBody = patch.access;
      keyAccess = patch.access.restricted === true ? { restricted: true, allow: [...patch.access.allow].sort() } : { restricted: false, allow: [] };
    } else {
      patchKeyCalls += 1;
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ key: { id: createdKeyID, name: 'ci-pipeline', prefix: 'rw_smoke', enabled: true, paused: patchKeyCalls % 2 === 1, access: keyAccess }, configRevision: 9 }) });
  }
  if (url.pathname.startsWith('/admin/v1/keys/') && method === 'DELETE') {
    deleteCalls += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: createdKeyID, revoked: true, configRevision: 10 }) });
  }
  if (url.pathname === '/admin/v1/providers' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [...(signInProviders ? signInFixtureProviders : []), { id: 'openai', transports: ['openai-chat','openai-responses'], auth: 'api-key', authModes: ['api-key'], defaultBaseURL: 'https://api.openai.com/v1', modelCatalog: 'static', passthroughModels: false, reportsUsage: false, configuredNodes: 1, enabledAccounts: 1, readiness: { status: 'ready', served: true } }, { id: 'openrouter', transports: ['openai-chat'], auth: 'api-key', authModes: ['api-key'], modelCatalog: 'dynamic', passthroughModels: true, reportsUsage: false, configuredNodes: 0, enabledAccounts: 0, readiness: { status: 'unavailable', served: false, reason: 'smoke fixture: wire not bound' } }], page: 1, pageSize: 100, total: signInProviders ? 4 : 2 }) });
  }
  if (url.pathname === '/admin/v1/oauth/codex/start' && method === 'POST') {
    signInStarts.push(route.request().postDataJSON());
    const state = `smoke-state-${signInStarts.length}`;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ state, authorizeUrl: 'https://auth.openai.com/oauth/authorize?state=' + state, completion: 'redirect-url', flowType: 'authorization_code_pkce', expiresAt: signInExpiresAt || '2026-09-25T12:10:00Z' }) });
  }
  if (url.pathname === '/admin/v1/oauth/codex/cancel' && method === 'POST') {
    signInCancels.push(route.request().postDataJSON());
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'cancelled' }) });
  }
  if (url.pathname === '/admin/v1/oauth/codex/callback' && method === 'POST') {
    signInCallbacks.push(route.request().postDataJSON());
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'conn-codex', nodeId: 'node-codex', providerId: 'codex', name: 'resolved@example.test', authKind: 'oauth', identity: 'resolved@example.test', enabled: true, priority: 0, credentialConfigured: true }) });
  }
  if (url.pathname === '/admin/v1/connections/conn-codex' && method === 'PATCH') {
    signInPatches.push(route.request().postDataJSON());
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'conn-codex', name: 'Work' }) });
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
    if (usageGate) await usageGate;
    if (usageMode === 'error') return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { message: 'Usage store is unavailable. Check the database volume.' } }) });
    if (usageMode === 'empty') return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] }) });
    usagePeriod = url.searchParams.get('period');
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
      period: usagePeriod, since: '2026-09-18T00:00:00.000Z',
      totals: { ...{ requests: 120, inputTokens: 9000, outputTokens: 3000, cacheReadTokens: 4000, cacheWriteTokens: 500, errors: 3, avgDurationMs: 420, avgTtftMs: 90 }, requests: windowedHistory() ? windowedHistory().reduce((total, entry) => total + entry.requests, 0) : 120 },
      series: windowedHistory() || [{ day: '2026-09-24', requests: 70, inputTokens: 5000, outputTokens: 1800, cacheReadTokens: 2000, cacheWriteTokens: 200 }, { day: '2026-09-25', requests: 50, inputTokens: 4000, outputTokens: 1200, cacheReadTokens: 2000, cacheWriteTokens: 300 }],
      providers: [{ key: 'openai', requests: 100, inputTokens: 8000, outputTokens: 2800, errors: 2 }, { key: 'anthropic', requests: 20, inputTokens: 1000, outputTokens: 200, errors: 1 }],
      models: [{ key: 'openai/gpt-5', requests: 90, inputTokens: 7000, outputTokens: 2500, errors: 1 }],
      statuses: [{ key: '200', requests: 117, inputTokens: 9000, outputTokens: 3000, errors: 0 }, { key: '500', requests: 3, inputTokens: 0, outputTokens: 0, errors: 3 }],
    }) });
  }
  if (url.pathname === '/admin/v1/usage/requests/req-abc' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ requestId: 'req-abc', events: [{ id: 1, requestId: 'req-abc', status: 200, providerId: 'openai', modelId: 'gpt-5', connectionId: 'conn-1', apiKeyId: 'key-a', inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, cacheWriteTokens: 0, durationMs: 300, ttftMs: 80, createdAt: '2026-09-25T20:00:00Z' }], detail: null }) });
  }
  if (url.pathname.startsWith('/admin/v1/usage/requests/') && method === 'GET') {
    return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'request_not_found', message: 'no usage or detail is stored for this request id' } }) });
  }
  if (url.pathname === '/admin/v1/usage/stream' && method === 'GET') {
    streamURLs.push(url.search);
    if (streamMode === 'full') return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'stream_limit', message: 'too many live usage streams' } }) });
    // A finite body: the long retry keeps the browser from reconnecting during the check.
    const live = { requestId: 'req-live-1', providerId: 'openai', modelId: 'gpt-5', status: 200, durationMs: 120, inputTokens: 7, outputTokens: 3, createdAt: new Date().toISOString() };
    return route.fulfill({ status: 200, contentType: 'text/event-stream', body: `retry: 600000\n: connected\n\nid: 1\nevent: request.completed\ndata: ${JSON.stringify(live)}\n\n` });
  }
  if (url.pathname === '/admin/v1/usage' && method === 'GET') {
    lastUsageURL = url.search;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ id: 1, requestId: 'req-abc', status: 200, providerId: 'openai', modelId: 'gpt-5', inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, cacheWriteTokens: 0, durationMs: 300, ttftMs: 80, createdAt: '2026-09-25T20:00:00Z' }], page: 1, pageSize: 50, total: 1 }) });
  }
  if (url.pathname === '/admin/v1/quota' && method === 'GET') {
    quotaURLs.push(url.search);
    const query = url.searchParams;
    const provider = query.get('provider') || '';
    const search = (query.get('q') || '').toLowerCase();
    const matched = quotaAccounts.filter(item => (!provider || item.providerId === provider) && (!search || item.name.toLowerCase().includes(search) || item.providerId.includes(search)));
    const size = Number(query.get('pageSize') || 50);
    const number = Number(query.get('page') || 1);
    const providers = [...new Set(quotaAccounts.map(item => item.providerId))].sort();
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: matched.slice((number - 1) * size, number * size), page: number, pageSize: size, total: matched.length, providers, sort: query.get('sort') || 'default' }) });
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
  if (url.pathname === '/admin/v1/connections/conn-1/test-models' && method === 'POST') {
    if (modelProbeStatus === 0) return;
    const message = modelProbeStatus === 504 ? 'Model testing exceeded the aggregate deadline.' : 'Model testing was canceled.';
    const body = { transport: 'openai-chat', error: modelProbeStatus === 200 ? undefined : { message } };
    if (modelProbeStatus !== 400) body.results = [{ modelId: 'gpt-5', ok: modelProbeStatus !== 408, error: modelProbeStatus === 408 ? message : undefined }];
    return route.fulfill({ status: modelProbeStatus, contentType: 'application/json', body: JSON.stringify(body) });
  }
  if (url.pathname === '/admin/v1/models/disabled' && method === 'GET') {
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], page: 1, pageSize: 100, total: 0 }) });
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
  if (url.pathname.startsWith('/admin/v1/combos/') && method === 'PUT') {
    comboPutCalls += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'combo-1', ...route.request().postDataJSON(), configRevision: 11 }) });
  }
  if (url.pathname.startsWith('/admin/v1/combos/') && method === 'DELETE') {
    comboDeleteCalls += 1;
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'combo-1', deleted: true, configRevision: 12 }) });
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
  // Freeze the page clock: the usage windows are calendar windows ending today, and the fixture's days are 2026-09-24 and 2026-09-25.
  await page.clock.setFixedTime(new Date('2026-09-25T12:00:00Z'));
  await page.goto('http://127.0.0.1:4173');
  await page.waitForSelector('#sign-in-title');
  await page.keyboard.press('Tab');
  report.firstFocus = await page.evaluate(() => document.activeElement.className);
  report.signInPrompt = await page.locator('#sign-in-title').textContent();
  report.passwordMasked = await page.locator('#admin-password').getAttribute('type');
  await page.fill('#admin-username', 'operator');
  await page.fill('#admin-password', 'not-a-real-secret');
  await page.click('button[type="submit"]');
  await page.waitForSelector('.overview-content .ed-headline');
  report.loginCalls = loginCalls;
  report.initialOverviewCalls = overviewCalls;
  report.overviewHeadline = await page.locator('.overview-content .ed-headline').first().innerText();
  report.overviewLive = await page.locator('.ed-now').innerText();
  report.weekendFact = await page.locator('.ed-fact', { hasText: 'Weekends' }).innerText();
  report.busiestFact = await page.locator('.ed-fact', { hasText: 'Busiest day' }).innerText();
  report.overviewAlert = await page.locator('[aria-label="Needs a look"]').innerText();
  report.sessionCalls = sessionCalls;
  report.brandMark = await page.locator('.sidebar .brand-mark').evaluate(el => { const r = el.getBoundingClientRect(); return { w: r.width, h: r.height, svg: !!el.querySelector('svg'), rects: el.querySelectorAll('rect').length, letters: el.textContent.replace('Routeweft', '').trim().length }; });
  report.headLinks = await page.evaluate(() => ({ themeColors: [...document.querySelectorAll('meta[name="theme-color"]')].map(m => m.media), icons: [...document.querySelectorAll('link[rel="icon"], link[rel="apple-touch-icon"]')].map(l => l.getAttribute('href')) }));
  report.signOutLabel = await page.locator('.sign-out').textContent();
  report.monospaceValue = await page.locator('.ed-fact-note').first().evaluate(el => getComputedStyle(el).fontFamily);
  report.overviewScroll = await page.evaluate(() => document.documentElement.scrollWidth + '/' + document.documentElement.clientWidth);
  await captureVisuals('overview');
  await page.click('.refresh-button');
  await page.waitForSelector('.refresh-error');
  report.refreshError = await page.locator('.refresh-error').innerText();
  report.staleHeadlineAfterRefreshError = await page.locator('.overview-content .ed-headline').first().innerText();
  await page.click('.refresh-error button');
  await page.waitForFunction(() => !document.querySelector('.refresh-error'));
  report.overviewCallsAfterRetry = overviewCalls;
  // A 10-day install makes no week-over-week claim; 30 days with the latest week at 140 against 100 a day claims exactly +40.0%.
  const refreshOverview = async () => { const refreshed = page.waitForResponse(response => response.url().includes('usage/summary?period=30d')); await page.click('.refresh-button'); await refreshed; await page.waitForTimeout(400); };
  const readOverviewHistory = () => page.evaluate(() => ({ headline: document.querySelector('.overview-content .ed-headline').innerText, weekFact: [...document.querySelectorAll('.ed-fact')].find(el => el.innerText.startsWith('This week')).innerText, ticks: document.querySelectorAll('.overview-content .chart-ghost').length }));
  historyDays = 10; await refreshOverview(); report.tenDayInstall = await readOverviewHistory();
  historyDays = 30; await refreshOverview(); report.thirtyDayInstall = await readOverviewHistory();
  historyDays = 2; await refreshOverview();
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
  report.createAccessBody = createAccessBody;
  report.accessTagBefore = await page.locator('.key-row .access-tag').textContent();
  // Access editor: explicit restricted mode, reachable picker, stored-state readback.
  const accessButton = page.locator('.key-row').getByRole('button', { name: 'Access', exact: true });
  await accessButton.click();
  await page.waitForSelector('.key-access-editor');
  report.accessExpanded = await accessButton.getAttribute('aria-expanded');
  report.accessControls = await accessButton.getAttribute('aria-controls');
  report.accessEditorId = await page.locator('.key-access-editor').getAttribute('id');
  report.saveDisabledUnchanged = await page.getByRole('button', { name: 'Save access' }).isDisabled();
  await page.locator('.key-access-editor').getByRole('radio', { name: /^Restricted/ }).check();
  await page.waitForSelector('.access-list');
  report.accessChoices = await page.locator('.access-list code').allTextContents();
  await page.locator('.access-list label', { hasText: 'openai/gpt-5' }).locator('input').check();
  // Switching to Unrestricted and back must not lose the selection.
  await page.locator('.key-access-editor').getByRole('radio', { name: /^Unrestricted/ }).check();
  await page.locator('.key-access-editor').getByRole('radio', { name: /^Restricted/ }).check();
  report.selectionKept = await page.locator('.access-list label', { hasText: 'openai/gpt-5' }).locator('input').isChecked();
  // Mobile: no horizontal overflow and a single-column mode picker.
  await page.setViewportSize({ width: 390, height: 844 });
  report.mobileOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  report.mobileModeColumns = await page.evaluate(() => getComputedStyle(document.querySelector('.access-modes')).gridTemplateColumns.split(' ').length);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('button', { name: 'Save access' }).click();
  await page.waitForFunction(() => document.querySelector('.key-row .access-tag').textContent.startsWith('Restricted'));
  report.accessTagAfter = await page.locator('.key-row .access-tag').textContent();
  report.accessBodySent = lastAccessBody;
  report.accessEditorClosed = await page.locator('.key-access-editor').count();
  await page.locator('.key-row').getByRole('button', { name: 'Pause', exact: true }).click();
  await page.waitForFunction(() => [...document.querySelectorAll('.key-row button')].some(button => button.textContent === 'Resume'));
  report.pausedLabel = await page.locator('.key-row').getByRole('button', { name: 'Resume', exact: true }).textContent();
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
  await page.waitForSelector('.overview-content .ed-headline');
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark'); // the media-query change is asynchronous
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
  await page.waitForFunction(() => document.querySelector('#page-title')?.textContent === 'Providers');
  report.pageAfterNav = await page.locator('#page-title').textContent();
  report.ariaCurrent = await page.locator('a[href="#providers"]').getAttribute('aria-current');
  report.focusAfterDesktopNav = await page.evaluate(() => ({ className: document.activeElement.className, visibility: getComputedStyle(document.activeElement).visibility, display: getComputedStyle(document.activeElement).display }));
  await page.waitForSelector('.provider-option');
  report.providerCards = await page.locator('.provider-option').count();
  report.providerPageSummary = await page.locator('.providers-content').innerText().then(text => text.split('\n').filter(line => /providers carry traffic|carry \d+% of the month/.test(line)));
  await page.locator('.provider-option', { hasText: 'openai' }).first().click();
  await page.waitForSelector('.connection-row');
  report.connectionRows = await page.locator('.connection-row').count();
  // Readiness renders as its own tag, apart from the per-account Enabled/Disabled tag.
  assert.equal((await page.locator('.provider-summary .readiness-tag').textContent()).trim(), 'Ready');
  assert.equal(await page.locator('.provider-summary .readiness-note').count(), 0);
  assert.match(await page.locator('.connection-row .state-tag').first().textContent(), /^(Enabled|Disabled)$/);
  assert.equal(await page.locator('.connection-row .readiness-tag').count(), 0);
  report.providerDetail = await page.locator('.provider-summary h2').textContent();
  report.modelRows = await page.locator('.model-row').count();
  report.modelProbeGuidance = await page.locator('.model-test-form .count-label').textContent();
  await captureVisuals('providers');
  report.modelProbeResults = [];
  for (const status of [200, 504, 408, 400]) {
    modelProbeStatus = status;
    await page.fill('#test-model-id', 'gpt-5');
    await page.getByRole('button', { name: 'Run model test', exact: true }).click();
    await page.getByRole('button', { name: 'Run model test', exact: true }).waitFor();
    if (status !== 200) await page.locator('.providers-content > .auth-error').waitFor();
    const notice = await page.locator('.providers-content > .inline-notice').allTextContents();
    const error = await page.locator('.providers-content > .auth-error').allTextContents();
    assert.equal(error.length, status === 200 ? 0 : 1);
    if (status === 400) assert.equal(notice.length, 0);
    else assert.match(notice.join(''), status === 408 ? /gpt-5: Model testing was canceled/ : /gpt-5: OK/);
    report.modelProbeResults.push({ status, notice, error });
  }
  modelProbeStatus = 0;
  const probeAborted = page.waitForEvent('requestfailed', request => request.url().endsWith('/connections/conn-1/test-models') && request.failure()?.errorText === 'net::ERR_ABORTED');
  await page.fill('#test-model-id', 'gpt-5');
  await page.getByRole('button', { name: 'Run model test', exact: true }).click();
  await page.getByRole('button', { name: 'Cancel model test', exact: true }).click();
  await probeAborted;
  await page.getByRole('button', { name: 'Run model test', exact: true }).waitFor();
  report.modelProbeCancelError = await page.locator('.providers-content > .auth-error').textContent();
  assert.equal(report.modelProbeCancelError, 'Model test canceled.');
  assert.match(report.modelProbeGuidance, /10-minute batch limit/);
  await page.fill('#provider-filter', 'openrouter');
  report.filteredCards = await page.locator('.provider-option').count();
  assert.match(await page.locator('.provider-option', { hasText: 'openrouter' }).first().textContent(), /unavailable/);
  report.providerPageSummaryFiltered = await page.locator('.providers-content').innerText().then(text => text.split('\n').filter(line => /providers carry traffic|carry \d+% of the month/.test(line)));
  // An unavailable provider says which requests are not routed, not that every request fails (#229).
  await page.locator('.provider-option', { hasText: 'openrouter' }).first().click();
  await page.waitForSelector('.provider-summary .readiness-note');
  assert.equal((await page.locator('.provider-summary .readiness-tag').textContent()).trim(), 'Unavailable');
  report.unavailableReadinessNote = await page.locator('.provider-summary .readiness-note').textContent();
  assert.match(report.unavailableReadinessNote, /does not route Chat, Responses or Messages requests to this provider yet/);
  assert.match(report.unavailableReadinessNote, /smoke fixture: wire not bound\./);
  assert.doesNotMatch(report.unavailableReadinessNote, /will fail/);
  await page.fill('#provider-filter', '');
  await page.locator('.provider-option', { hasText: 'openai' }).first().click();
  await page.waitForSelector('.connection-row');
  await page.click('.providers-content .provider-details .connection-row .button-secondary');
  await page.waitForFunction(() => document.querySelector('.providers-content .connection-row .state-tag').textContent === 'Disabled');
  report.providerPatches = providerPatches;
  report.providerDisabledLabel = await page.locator('.connection-row .state-tag').textContent();
  // Provider-aware sign-in (#214): methods are separate choices, Codex starts with
  // neither name nor identity, and every attempt ends in an explicit state.
  signInProviders = true;
  await page.click('a[href="#combo-capability-adapter"]');
  await page.waitForSelector('.combos-content');
  await page.click('a[href="#providers"]');
  await page.waitForSelector('.provider-option');
  await page.fill('#provider-filter', 'codex');
  await page.locator('.provider-option', { hasText: 'codex' }).first().click();
  await page.getByRole('button', { name: 'Add connection', exact: true }).click();
  const signInForm = page.locator('.provider-form');
  await signInForm.waitFor();
  report.codexMethods = await signInForm.locator('.access-modes label').allTextContents();
  assert.deepEqual(report.codexMethods, ['Sign in with Codex', 'Import token', 'Paste OAuth tokens']);
  assert.equal(await signInForm.getByLabel('Account name').count(), 0);
  assert.equal(await signInForm.getByLabel('Stable account identity').count(), 0);
  assert.equal(await signInForm.locator('input[type="password"]').count(), 0, 'browser sign-in must not show raw token fields');
  await signInForm.getByRole('button', { name: 'Sign in with Codex', exact: true }).click();
  await signInForm.getByText('Pending').waitFor();
  assert.deepEqual(signInStarts[0], { meta: {} }, 'codex start sends no name or identity');
  report.codexHint = await signInForm.locator('.oauth-panel p').first().textContent();
  assert.match(report.codexHint, /http:\/\/localhost:1455\/auth\/callback/);
  assert.match(report.codexHint, /not on the Routeweft server/);
  assert.equal(await signInForm.locator('.access-picker').evaluate(element => element.disabled), true, 'method choice is locked while an attempt is pending');
  await signInForm.getByRole('button', { name: 'Cancel sign-in', exact: true }).click();
  await signInForm.getByText('Cancelled', { exact: true }).waitFor();
  assert.deepEqual(signInCancels, [{ state: 'smoke-state-1' }]);
  // Expiry is explicit: the attempt ends at the server's expiresAt.
  signInExpiresAt = '2026-09-25T12:00:01Z';
  await signInForm.getByRole('button', { name: 'Sign in with Codex', exact: true }).click();
  await signInForm.getByText('Expired', { exact: true }).waitFor({ timeout: 5000 });
  assert.equal(await signInForm.getByLabel('Address from the browser').count(), 0);
  signInExpiresAt = '';
  await signInForm.getByRole('button', { name: 'Sign in with Codex', exact: true }).click();
  await signInForm.getByLabel('Address from the browser').fill('http://localhost:1455/auth/callback?code=auth-code-smoke&state=smoke-state-3');
  await signInForm.getByRole('button', { name: 'Finish sign-in', exact: true }).click();
  await signInForm.getByText('Connected', { exact: true }).waitFor();
  assert.deepEqual(signInCallbacks, [{ state: 'smoke-state-3', input: 'http://localhost:1455/auth/callback?code=auth-code-smoke&state=smoke-state-3' }]);
  report.codexConnected = await signInForm.locator('[aria-label="Sign-in result"] p').textContent();
  assert.match(report.codexConnected, /Signed in as resolved@example\.test/);
  assert.equal(await page.content().then(html => html.includes('auth-code-smoke')), false, 'pasted callback must not stay in the DOM');
  await signInForm.getByLabel('Nickname (optional)').fill('Work');
  await signInForm.getByRole('button', { name: 'Save nickname', exact: true }).click();
  await signInForm.waitFor({ state: 'detached' });
  assert.deepEqual(signInPatches, [{ name: 'Work' }]);
  // Dual auth: the primary API key stays the default, and each mode shows only its own fields.
  await page.fill('#provider-filter', 'kimchi');
  await page.locator('.provider-option', { hasText: 'kimchi' }).first().click();
  await page.getByRole('button', { name: 'Add connection', exact: true }).click();
  await signInForm.waitFor();
  report.kimchiMethods = await signInForm.locator('.access-modes label').allTextContents();
  assert.deepEqual(report.kimchiMethods, ['API key', 'Sign in with Kimchi', 'Paste OAuth tokens']);
  assert.equal(await signInForm.getByLabel('Account name').count(), 1);
  assert.equal(await signInForm.getByLabel('Access token / API key').count(), 1);
  await signInForm.getByText('Sign in with Kimchi', { exact: true }).click();
  assert.equal(await signInForm.getByLabel('Account name').count(), 0);
  assert.equal(await signInForm.getByLabel('Access token / API key').count(), 0);
  assert.equal(await signInForm.getByLabel('Stable account identity').count(), 1, 'kimchi cannot always resolve the account, so it asks');
  assert.equal(await signInForm.getByRole('button', { name: 'Sign in with Kimchi', exact: true }).isDisabled(), true);
  await signInForm.getByRole('button', { name: 'Close', exact: true }).click();
  await page.fill('#provider-filter', '');
  signInProviders = false;
  // Combo & Capability Adapter workflow (PRD 11).
  await page.click('a[href="#combo-capability-adapter"]');
  await page.waitForSelector('.combos-content');
  report.comboEmpty = await page.locator('.combo-list-panel .empty-keys').textContent();
  await page.click('.combos-toolbar .button-primary');
  await page.waitForSelector('.combo-form');
  await page.fill('.combo-form-grid input', 'daily');
  await page.selectOption('.combo-form-grid select', 'sticky-round-robin');
  // Typed picker: unknown names are refused with a reason, known ones resolve to provider/model.
  await page.fill('.member-picker input', 'nope/none');
  await page.click('.member-picker .button-secondary');
  report.memberUnknownError = await page.locator('.combo-form .auth-error').textContent();
  report.memberRowsAfterUnknown = await page.locator('.member-row').count();
  report.memberChoices = await page.locator('#combo-member-choices option').evaluateAll(options => options.map(option => option.getAttribute('value')));
  await page.fill('.member-picker input', 'openai/gpt-5');
  await page.click('.member-picker .button-secondary');
  report.memberRows = await page.locator('.member-row').count();
  report.memberLabel = await page.locator('.member-row code').textContent();
  await page.click('.member-row .member-actions .button-secondary:nth-child(3)');
  report.memberDeselected = await page.locator('.member-row .state-tag').first().textContent();
  await page.click('.member-row .member-actions .button-secondary:nth-child(3)');
  await page.click('.combo-form > .button-primary');
  await page.waitForSelector('.combo-row');
  report.comboRowName = await page.locator('.combo-row strong').textContent();
  report.comboCreated = comboCreated;
  report.comboStrategy = comboCreated ? comboPayload.strategy : '';
  // In-use feedback comes from restricted keys that list this Combo by name.
  await page.waitForFunction(() => document.querySelector('.combo-row .combo-inuse'));
  report.comboInUse = await page.locator('.combo-row .combo-inuse').textContent();
  report.comboInUseTitle = await page.locator('.combo-row .combo-inuse').getAttribute('title');
  let deleteMessage = '';
  page.once('dialog', async dialog => { deleteMessage = dialog.message(); await dialog.dismiss(); });
  await page.locator('.combo-row').getByRole('button', { name: 'Delete', exact: true }).click();
  await page.waitForTimeout(150);
  report.deleteWarning = deleteMessage;
  report.deleteCallsAfterCancel = comboDeleteCalls;
  let renameMessage = '';
  page.once('dialog', async dialog => { renameMessage = dialog.message(); await dialog.dismiss(); });
  await page.locator('.combo-row').getByRole('button', { name: 'Edit', exact: true }).click();
  await page.fill('.combo-form-grid input', 'daily-renamed');
  await page.click('.combo-form > .button-primary');
  await page.waitForTimeout(150);
  report.renameWarning = renameMessage;
  report.putCallsAfterCancel = comboPutCalls;
  await page.fill('.combo-form-grid input', 'daily');
  await page.click('.combo-form .button-ghost');
  report.adapterCards = await page.locator('.adapter-section').count();
  report.adapterInitial = await page.locator('.adapter-section').first().locator('small').textContent();
  await page.locator('.adapter-section').first().locator('.adapter-header .button-secondary').click();
  await page.waitForFunction(() => document.querySelector('.adapter-section small').textContent.includes('empty pool is a no-op'));
  report.adapterAfterEnable = await page.locator('.adapter-section').first().locator('small').textContent();
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
  let releaseUsage;
  usageGate = new Promise(resolve => { releaseUsage = resolve; });
  await page.click('a[href="#usage"]');
  await page.waitForSelector('.usage-content .skeleton-page');
  report.skeleton = await page.evaluate(() => ({
    busy: document.querySelector('.usage-content').getAttribute('aria-busy'),
    blocks: document.querySelectorAll('.usage-content .skeleton').length,
    label: document.querySelector('.skeleton-page').getAttribute('aria-label'),
    spinner: document.querySelectorAll('.spinner, [role="progressbar"]').length,
    animation: getComputedStyle(document.querySelector('.usage-content .skeleton')).animationName,
  }));
  await page.screenshot({ path: join(artifacts, 'usage-skeleton-1440.png') });
  usageGate = null;
  releaseUsage();
  await page.waitForSelector('.usage-content');
  await page.waitForSelector('.usage-content .chart');
  report.usageFacts = await page.locator('.usage-content .ed-fact dt, .usage-content .ed-fact dd:not(.ed-fact-note)').allInnerTexts();
  await captureVisuals('usage');
  report.usageBars = await page.locator('.usage-bar').count();
  report.usageProviderRows = await page.locator('.usage-content .prov-table tbody tr').count();
  report.usagePeriod = usagePeriod;
  // Focus, error and empty states (#102).
  await page.locator('#usage-period').focus();
  await page.keyboard.press('Shift+Tab');
  await page.keyboard.press('Tab');
  report.focusRing = await page.evaluate(() => { const el = document.activeElement, style = getComputedStyle(el); return { id: el.id, style: style.outlineStyle, width: style.outlineWidth, offset: style.outlineOffset, color: style.outlineColor, token: getComputedStyle(document.documentElement).getPropertyValue('--focus').trim() }; });
  await page.screenshot({ path: join(artifacts, 'usage-focus-1440.png'), clip: { x: 208, y: 120, width: 1000, height: 160 } });
  usageMode = 'error';
  await page.getByRole('button', { name: 'Refresh' }).click();
  await page.waitForSelector('.usage-content .page-error');
  report.usageError = await page.evaluate(() => { const el = document.querySelector('.usage-content .page-error'); const style = getComputedStyle(el); return { text: el.textContent, leftWidth: style.borderLeftWidth, leftColor: style.borderLeftColor, retry: !!el.querySelector('button') }; });
  await page.waitForTimeout(200);
  await page.screenshot({ path: join(artifacts, 'usage-error-1440.png') });
  usageMode = 'empty';
  await page.locator('.usage-content .page-error button').click();
  await page.waitForSelector('.usage-content .empty-state');
  report.usageEmpty = await page.evaluate(() => ({ boxes: document.querySelectorAll('.usage-content .empty-state').length, action: !!document.querySelector('.usage-content .empty-state button'), border: getComputedStyle(document.querySelector('.empty-state')).borderTopStyle }));
  await page.waitForTimeout(200);
  await page.screenshot({ path: join(artifacts, 'usage-empty-1440.png') });
  usageMode = 'ok';
  await page.getByRole('button', { name: 'Refresh' }).click();
  await page.waitForSelector('.usage-content .chart');
  // A window label must name the window it sums: 'all' on a 2-day history and the 24-hour period never say "latest 7 days".
  const requestsNote = () => page.evaluate(() => [...document.querySelectorAll('.usage-content .ed-fact')].find(el => el.innerText.startsWith('Requests')).innerText);
  const allLoaded = page.waitForResponse(response => response.url().includes('usage/summary?period=all'));
  await page.selectOption('#usage-period', 'all'); await allLoaded; await page.waitForTimeout(300);
  report.usageNoteAll = await requestsNote();
  const dayLoaded = page.waitForResponse(response => response.url().includes('usage/summary?period=24h'));
  await page.selectOption('#usage-period', '24h'); await dayLoaded; await page.waitForTimeout(300);
  report.usageNote24h = await requestsNote();
  await page.waitForFunction(() => document.querySelector('.usage-toolbar .count-label').textContent.includes('since'));
  report.usagePeriodAfterChange = usagePeriod;
  report.usageSummaryCalls = usageSummaryCalls;
  // The two added periods reach the API as themselves.
  for (const added of ['today', '60d']) {
    const loaded = page.waitForResponse(response => response.url().includes(`usage/summary?period=${added}`));
    await page.selectOption('#usage-period', added); await loaded; await page.waitForTimeout(200);
    if (added === 'today') report.todayHeadline = await page.locator('.usage-content .ed-subline').first().innerText();
  }
  // Tabs: Requests lists the log and filters it server-side; a row opens Request details.
  report.tabs = await page.getByRole('tab').allInnerTexts();
  await page.getByRole('tab', { name: 'Requests' }).click();
  await page.waitForSelector('.log-table tbody tr');
  report.usageEventRows = await page.locator('.log-table tbody tr:not(.log-row-live)').count();
  report.logsURL = lastUsageURL;
  // Live view: a request that finishes while the log is open is appended without a refresh, marked live, and scoped by the log's filters.
  await page.waitForSelector('.log-row-live');
  report.liveRow = await page.locator('.log-row-live').first().innerText();
  report.liveStreamURL = streamURLs[0];
  report.liveStatus = await page.locator('.live-bar [role=status]').innerText();
  await page.getByLabel('Provider').fill('openai');
  await page.getByLabel('Status').fill('200');
  const filtered = page.waitForResponse(response => response.url().includes('providerId=openai'));
  await page.getByRole('button', { name: 'Apply filters' }).click(); await filtered;
  report.logsFilteredURL = lastUsageURL;
  for (let waited = 0; !streamURLs.some(query => query.includes('providerId=openai')) && waited < 5000; waited += 25) await page.waitForTimeout(25);
  report.liveFilteredStreamURL = streamURLs.find(query => query.includes('providerId=openai'));
  await page.getByRole('button', { name: 'Pause live view' }).click();
  await page.locator('.live-bar [role=status]', { hasText: 'Paused' }).waitFor();
  report.livePausedStatus = await page.locator('.live-bar [role=status]').innerText();
  const streamCallsWhenPaused = streamURLs.length;
  streamMode = 'full';
  await page.getByRole('button', { name: 'Resume live view' }).click();
  await page.getByRole('button', { name: 'Reconnect' }).waitFor();
  await page.locator('.live-bar [role=status]', { hasText: 'Stream closed' }).waitFor();
  report.liveClosedStatus = await page.locator('.live-bar [role=status]').innerText();
  report.liveReconnectedAfterPause = streamURLs.length - streamCallsWhenPaused;
  streamMode = 'ok';
  await page.getByRole('button', { name: 'Reconnect' }).click();
  await page.waitForSelector('.log-row-live');
  await page.getByRole('button', { name: 'Open details for request req-abc' }).click();
  await page.waitForSelector('.usage-content .ed-facts');
  report.detailTabSelected = await page.getByRole('tab', { name: 'Request details' }).getAttribute('aria-selected');
  report.detailFacts = await page.locator('.usage-content .ed-fact dt').allInnerTexts();
  report.detailNoDetail = await page.locator('.ed-margin .ed-now-note').innerText();
  await page.screenshot({ path: join(artifacts, 'usage-details-1440.png') });
  await page.getByLabel('Request id').fill('nope');
  await page.getByRole('button', { name: 'Show request' }).click();
  await page.waitForSelector('.usage-content .empty-state');
  report.detailMissing = await page.locator('.usage-content .empty-state').innerText();
  // Quota Tracker (PRD-QUOTA-001).
  await page.click('a[href="#quota-tracker"]');
  await page.waitForSelector('.quota-content');
  await page.waitForSelector('.quota-row');
  report.quotaRows = await page.locator('.quota-row').count();
  report.quotaRemaining = await page.locator('.quota-row > .quota-remaining').allTextContents();
  report.quotaRemaining = report.quotaRemaining.slice(0, 6);
  report.quotaStatuses = [];
  for (let i = 0; i < 6; i++) report.quotaStatuses.push(await page.locator('.quota-row').nth(i).locator('> .state-tag').allTextContents());
  report.quotaWindows = await page.locator('.quota-row').first().locator('.quota-windows li').allInnerTexts();
  report.quotaCredit = await page.locator('.quota-row').nth(2).locator('.quota-windows li').allInnerTexts();
  report.quotaStaleNote = await page.locator('.quota-row').nth(5).locator('.quota-window-note').innerText();
  report.quotaResetZone = await page.locator('.quota-row').first().locator('> .quota-reset').innerText();
  report.quotaResetTitle = await page.locator('.quota-row').first().locator('> .quota-reset').getAttribute('title');
  report.quotaPager = await page.locator('.quota-content .pagination span').innerText();
  await captureVisuals('quota');
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.waitForFunction(() => document.querySelector('.quota-content .pagination span')?.textContent?.startsWith('Page 3'));
  report.quotaLastPageRows = await page.locator('.quota-row').count();
  report.quotaLastPageFirst = await page.locator('.quota-row strong').first().innerText();
  await page.selectOption('#quota-provider', 'deepseek');
  await page.waitForFunction(() => document.querySelectorAll('.quota-row').length === 1);
  report.quotaProviderRow = await page.locator('.quota-row strong').innerText();
  await page.selectOption('#quota-provider', '');
  await page.fill('#quota-search', 'bulk-12');
  await page.press('#quota-search', 'Enter');
  await page.waitForFunction(() => document.querySelector('.quota-toolbar .count-label')?.textContent === '4 accounts match');
  report.quotaSearchRows = await page.locator('.quota-row').count();
  await page.selectOption('#quota-sort', 'exhausted');
  await page.waitForTimeout(100);
  report.quotaURLs = quotaURLs.slice();
  await page.fill('#quota-search', '');
  await page.press('#quota-search', 'Enter');
  await page.selectOption('#quota-sort', 'default');
  await page.waitForFunction(() => document.querySelectorAll('.quota-row').length === 50);
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
  await page.waitForFunction(() => document.querySelector('.inline-notice')?.textContent.includes('Maximum queued records saved'));
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
  await page.waitForFunction(() => document.documentElement.dataset.theme === 'light');
  report.systemLight = await page.evaluate(() => document.documentElement.dataset.theme);
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
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
      skipLink.focus();
      const bounds = skipLink.getBoundingClientRect();
      const style = getComputedStyle(skipLink);
      const focused = document.activeElement === skipLink;
      const visible = bounds.top >= 0 && bounds.bottom <= innerHeight && bounds.left >= 0 && bounds.right <= innerWidth;
      const outlined = style.outlineStyle === 'solid' && parseFloat(style.outlineWidth) >= 2;
      skipLink.blur();
      return focused && visible && outlined;
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
  // Every page: three type sizes, one accent surface (#102).
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await page.setViewportSize({ width: 1440, height: 900 });
  report.pageSizes = {};
  for (const href of await page.locator('nav a.nav-link').evaluateAll(links => links.map(link => link.getAttribute('href')))) {
    await page.evaluate(target => { location.hash = target; }, href);
    await page.waitForTimeout(500);
    report.pageSizes[href] = await page.evaluate(() => {
      const visible = element => element.getClientRects().length && getComputedStyle(element).visibility !== 'hidden';
      const sizes = [...new Set([...document.querySelectorAll('.workspace *, .sidebar *')].filter(visible).filter(element => [...element.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim())).map(element => getComputedStyle(element).fontSize))].sort((a, b) => parseFloat(a) - parseFloat(b));
      return { sizes, accents: [...document.querySelectorAll('.accent-surface')].filter(visible).length };
    });
  }
  console.log(JSON.stringify(report, null, 2));
  assert.match(report.overviewHeadline, /requests this week/);
  assert.match(report.busiestFact, /17% above the average over the 2 recorded days/, 'the average starts at the first recorded day: (70+50)/2 = 60, so 70 is 17% above it');
  assert.deepEqual([report.brandMark.w, report.brandMark.h], [20, 20], 'the mark is 20px in the sidebar');
  assert.ok(report.brandMark.svg && report.brandMark.rects >= 6 && report.brandMark.letters === 0, 'the mark is the drawn weave, not a letter');
  assert.deepEqual(report.headLinks.themeColors, ['(prefers-color-scheme: light)', '(prefers-color-scheme: dark)']);
  assert.deepEqual(report.headLinks.icons, ['/favicon.svg', '/favicon-32.png', '/apple-touch-icon.png']);
  assert.doesNotMatch(report.tenDayInstall.headline, /[↑↓]|%/, 'ten recorded days cannot support a week-over-week percentage');
  assert.match(report.tenDayInstall.weekFact, /successful of/);
  assert.equal(report.tenDayInstall.ticks, 0, 'no ghost ticks without a fully observed week before');
  assert.match(report.thirtyDayInstall.headline, /980 requests this week, ↑ 40\.0%/, 'hand arithmetic: 7 x 140 = 980 against 7 x 100 = 700');
  assert.equal(report.thirtyDayInstall.ticks, 7);
  assert.doesNotMatch(report.usageNote24h, /latest 7 days/, 'the 24-hour period must not claim a 7-day sum');
  assert.match(report.usageNote24h, /a day/);
  assert.doesNotMatch(report.usageNoteAll, /latest 7 days/, 'a 2-day history must not claim a 7-day sum');
  assert.match(report.usageNoteAll, /a day/);
  assert.match(report.overviewLive, /Live/);
  assert.match(report.weekendFact, /—/, 'a two-day fixture has no weekend days, so the ratio is a dash');
  assert.doesNotMatch(report.weekendFact, /lighter|heavier|%/);
  assert.match(report.overviewLive, /2 connections enabled, 1 API keys/);
  assert.equal(report.signInPrompt, 'Sign in to Routeweft');
  assert.equal(report.passwordMasked, 'password');
  assert.equal(report.loginCalls, 1);
  assert.equal(report.initialOverviewCalls, 1);
  assert.match(report.refreshError, /Overview is temporarily unavailable/);
  assert.match(report.staleHeadlineAfterRefreshError, /requests this week/);
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
  assert.deepEqual(report.createAccessBody, { restricted: false, allow: [] });
  assert.equal(report.accessTagBefore, 'Unrestricted');
  assert.equal(report.accessExpanded, 'true');
  assert.ok(report.accessEditorId && report.accessControls === report.accessEditorId);
  assert.equal(report.saveDisabledUnchanged, true);
  assert.deepEqual(report.accessChoices, ['openai/gpt-5']);
  assert.equal(report.selectionKept, true);
  assert.ok(report.mobileOverflow <= 0, 'access editor overflows horizontally on mobile: ' + report.mobileOverflow);
  assert.equal(report.mobileModeColumns, 1);
  assert.equal(report.accessTagAfter, 'Restricted · 1');
  assert.deepEqual(report.accessBodySent, { restricted: true, allow: ['openai/gpt-5'] });
  assert.equal(report.accessEditorClosed, 0);
  assert.equal(report.pausedLabel, 'Resume');
  assert.match(report.emptyAfterRevoke, /No API keys yet/);
  assert.equal(report.patchKeyCalls, 1);
  assert.equal(report.deleteCalls, 1);
  assert.equal(report.enforcementAfterToggle, 'Not required');
  assert.match(report.overviewAlert, /3 recorded/);
  assert.match(report.overviewAlert, /Degraded: some accounting may be delayed/);
  assert.equal(report.sessionCalls, 1);
  assert.equal(report.signOutLabel, 'Sign out · operator');
  assert.match(report.monospaceValue, /JetBrains Mono/);
  assert.equal(report.overviewScroll, '1440/1440');
  assert.equal(report.navCount, 10);
  assert.deepEqual(report.groups, ['Operate', 'Route', 'Observe', 'System']);
  assert.equal(report.sidebarWidth, 200);
  assert.equal(report.themeDark, 'dark');
  assert.equal(report.pageAfterNav, 'Providers');
  assert.equal(report.ariaCurrent, 'page');
  assert.equal(report.providerCards, 2);
  assert.equal(report.connectionRows, 1);
  assert.equal(report.providerDetail, 'openai');
  assert.equal(report.modelRows, 1);
  assert.equal(report.filteredCards, 1);
  assert.deepEqual(report.providerPageSummaryFiltered, report.providerPageSummary, 'headline and concentration must not follow the Find box');
  assert.ok(report.providerPageSummary.length >= 1);
  assert.equal(report.providerPatches, 1);
  assert.equal(report.providerDisabledLabel, 'Disabled');
  assert.match(report.comboEmpty, /No Combos yet/);
  assert.equal(report.memberRows, 1);
  assert.equal(report.memberLabel, 'openai / gpt-5');
  assert.equal(report.memberDeselected, 'Deselected');
  assert.equal(report.comboRowName, 'daily');
  assert.match(report.memberUnknownError, /not a known model or alias/);
  assert.equal(report.memberRowsAfterUnknown, 0);
  assert.ok(report.memberChoices.includes('openai/gpt-5'));
  assert.equal(report.comboInUse, 'Allowed by 1 key');
  assert.equal(report.comboInUseTitle, 'combo-reader');
  assert.match(report.deleteWarning, /combo-reader/);
  assert.equal(report.deleteCallsAfterCancel, 0);
  assert.match(report.renameWarning, /combo-reader/);
  assert.equal(report.putCallsAfterCancel, 0);
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
  assert.deepEqual(report.usageFacts.slice(0, 2), ['Requests', '120 req']);
  assert.ok(report.usageFacts.includes('12k in + out') || report.usageFacts.some(text => text.startsWith('12,000') || text.startsWith('12k')));
  assert.ok(report.usageFacts.some(text => text.startsWith('420 ms')));
  assert.equal(report.focusRing.style, 'solid');
  assert.equal(report.focusRing.width, '2px');
  assert.equal(report.focusRing.offset, '2px');
  assert.notEqual(report.focusRing.color, 'rgba(0, 0, 0, 0)');
  assert.match(report.usageError.text, /Usage store is unavailable/);
  assert.equal(report.usageError.leftWidth, '3px');
  assert.equal(report.usageError.retry, true);
  assert.equal(report.usageEmpty.action, true);
  assert.equal(report.usageEmpty.border, 'solid');
  for (const [href, result] of Object.entries(report.pageSizes)) {
    assert.ok(result.sizes.every(size => ['11px', '12px', '16px', '28px'].includes(size)), `${href}: type sizes ${result.sizes}`);
    assert.equal(result.accents, 1, `${href}: one accent surface`);
  }
  assert.equal(report.skeleton.busy, 'true');
  assert.ok(report.skeleton.blocks >= 10, 'skeleton mirrors the content shape');
  assert.equal(report.skeleton.spinner, 0);
  assert.equal(report.skeleton.animation, 'pulse');
  assert.equal(report.usageBars, 7, 'a 7-day period draws 7 calendar days; absent days are zero bars');
  assert.equal(report.usageProviderRows, 2);
  assert.equal(report.usageEventRows, 1);
  assert.deepEqual(report.tabs, ['Overview', 'Requests', 'Request details']);
  assert.match(report.todayHeadline, /so far today \(UTC\)/);
  assert.match(report.logsURL, /startDate=/, 'the selected period bounds the log');
  assert.match(report.logsFilteredURL, /providerId=openai/);
  assert.match(report.logsFilteredURL, /status=200/);
  assert.equal(report.detailTabSelected, 'true');
  assert.ok(['Status', 'Provider', 'Connection', 'Key', 'Duration', 'Time to first token'].every(label => report.detailFacts.includes(label)));
  assert.match(report.detailNoDetail, /No diagnostic detail is stored/);
  assert.match(report.detailMissing, /No stored request has the id nope/);
  assert.equal(report.usagePeriod, '7d');
  assert.equal(report.usagePeriodAfterChange, '24h');
  assert.equal(report.usageSummaryCalls >= 2, true);
  assert.equal(report.quotaRows, 50);
  assert.deepEqual(report.quotaRemaining, ['42% remaining', '0% remaining', 'limit not reported', 'unlimited', 'not reported', 'limit not reported']);
  assert.deepEqual(report.quotaStatuses, [['available'], ['exhausted', 'disabled'], ['limit unknown'], ['unlimited'], ['not reported'], ['read failed', 'stale']]);
  assert.equal(report.quotaWindows.length, 2);
  assert.match(report.quotaWindows[0], /^session\s+42% remaining\s+resets /);
  assert.match(report.quotaWindows[1], /^weekly\s+85% remaining/);
  assert.match(report.quotaCredit[0], /12\.50 credit/);
  assert.match(report.quotaCredit[1], /CN¥|CNY/);
  assert.ok(report.quotaCredit.every(text => !/%|unlimited/.test(text)), report.quotaCredit.join(' | '));
  assert.match(report.quotaStaleNote, /last successful read/);
  assert.match(report.quotaResetZone, /next reset .*(UTC|GMT)/);
  assert.match(report.quotaResetTitle, /UTC/);
  assert.equal(report.quotaPager, 'Page 1 of 3 · 130 accounts');
  assert.equal(report.quotaLastPageRows, 30);
  assert.equal(report.quotaLastPageFirst, 'bulk-094');
  assert.equal(report.quotaProviderRow, 'balance');
  assert.equal(report.quotaSearchRows, 4);
  assert.ok(report.quotaURLs.includes('?page=3&pageSize=50&sort=default'), report.quotaURLs.join(' '));
  assert.ok(report.quotaURLs.includes('?page=1&pageSize=50&sort=default&provider=deepseek'), report.quotaURLs.join(' '));
  assert.ok(report.quotaURLs.includes('?page=1&pageSize=50&sort=exhausted&q=bulk-12'), report.quotaURLs.join(' '));
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
  assert.match(report.liveRow, /req-live-1/);
  assert.match(report.liveRow, /openai \/ gpt-5/);
  assert.match(report.liveRow, /7 \/ 3/);
  assert.doesNotMatch(report.liveStreamURL, /providerId/);
  assert.match(report.liveFilteredStreamURL, /providerId=openai/);
  assert.match(report.liveFilteredStreamURL, /status=200/);
  assert.equal(report.livePausedStatus, 'Paused');
  assert.equal(report.liveClosedStatus, 'Stream closed');
  assert.equal(report.liveReconnectedAfterPause, 1);
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
  writeFileSync(join(artifacts, 'shell-report.json'), JSON.stringify(report, null, 2) + '\n');
  await browser.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
