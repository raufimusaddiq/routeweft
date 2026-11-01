// Browser fixture for the phase 3b pages of the #263 restyle: Providers, Usage and Quota Tracker. Each page is loaded with seeded data at 1440, 390 and 320 in light and dark, then
// measured (not asserted by eye): no horizontal overflow, no text under 4.5:1, no control boundary under 3:1,
// no phone control under 44px, no blur, and every box-shadow a hard-offset token or none. The long provider
// and account names, the Codex row with reset credits and the long route in the request log are in the fixtures. With
// ROUTEWEFT_SMOKE_ARTIFACTS set, a full-page screenshot of every state is written there (docs/evidence/263/phase3b).
// The control API is stubbed, so no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');

const ORIGIN = 'http://127.0.0.1:4173';
const WIDTHS = [1440, 390, 320];
const THEMES = ['light', 'dark'];
const LONG_ROUTE = 'openrouter/anthropic/claude-3.5-sonnet-20241022:thinking-extended-very-long-model-name';
const LONG_COMBO = 'production-fallback-with-a-really-long-combo-name-for-phones';

const day = i => new Date(Date.now() - (29 - i) * 86400000).toISOString().slice(0, 10);
const series = Array.from({ length: 30 }, (_, i) => ({ day: day(i), requests: 80 + ((i * 37) % 60) + (i >= 23 ? 70 : 0), inputTokens: 90000 + i * 1000, outputTokens: 20000 + i * 300, cacheReadTokens: 40000, cacheWriteTokens: 5000, cost: 1.2 + i * 0.03 }));
const breakdown = (key, requests, errors) => ({ key, requests, inputTokens: requests * 900, outputTokens: requests * 200, errors, cost: requests * 0.002 });
const summary = period => ({ period, since: '2026-08-27T00:00:00Z', totals: { requests: period === '7d' ? 2947 : 9120, inputTokens: 8400000, outputTokens: 1900000, cacheReadTokens: 3100000, cacheWriteTokens: 400000, errors: period === '7d' ? 80 : 310, avgDurationMs: 840, avgTtftMs: 310, cost: 42.5 }, series: period === '7d' ? series.slice(-7) : series, providers: [breakdown('openai', 1500, 12), breakdown('anthropic', 900, 61), breakdown('deepseek', 400, 5), breakdown('groq', 147, 2)], models: [breakdown('gpt-4o', 1200, 9), breakdown('claude-sonnet', 900, 61), breakdown('deepseek-chat', 400, 5)], statuses: [breakdown('200', 2867, 0), breakdown('429', 50, 50), breakdown('500', 30, 30)] });
const events = Array.from({ length: 24 }, (_, i) => ({ id: 100 - i, requestId: `req-${i}`, status: i % 9 === 4 ? 500 : i % 7 === 3 ? 429 : 200, inputTokens: 1200 + i * 11, outputTokens: 340 + i, createdAt: new Date(Date.UTC(2026, 8, 25, 20, 59 - i)).toISOString(), providerId: i % 3 === 0 ? 'openrouter' : 'openai', modelId: i % 3 === 0 ? 'anthropic/claude-3.5-sonnet-20241022:thinking-extended-very-long-model-name' : 'gpt-4o', connectionId: 'conn-1', durationMs: 700 + i * 13, ttftMs: 200 + i, cost: 0.004 }));
const connections = [
  { id: 'conn-1', nodeId: 'node-openai', providerId: 'openai', name: 'primary', authKind: 'api-key', identity: 'a@example.test', enabled: true, priority: 0, credentialConfigured: true },
  { id: 'conn-2', nodeId: 'node-anthropic', providerId: 'anthropic', name: 'claude', authKind: 'api-key', identity: 'b@example.test', enabled: false, priority: 1, credentialConfigured: true },
  { id: 'conn-s1', nodeId: 'node-s1', providerId: 's1-prod', name: 'Prod account', authKind: 'api-key', identity: 'acct-prod', enabled: true, priority: 0, credentialConfigured: true },
  { id: 'conn-s1b', nodeId: 'node-s1', providerId: 's1-prod', name: 'Prod account with a rather long name to wrap on a phone screen', authKind: 'api-key', identity: 'acct-long', enabled: true, priority: 1, credentialConfigured: true },
];
const keys = [
  { id: 'k-1', name: 'CI pipeline', prefix: 'rw_ci12', enabled: true, paused: false, createdAt: '2026-09-01T10:00:00Z', lastUsedAt: '2026-09-25T20:00:00Z', access: { restricted: false, allow: [] } },
  { id: 'k-2', name: 'Contractor key with a long descriptive name that should wrap', prefix: 'rw_ct34', enabled: true, paused: true, createdAt: '2026-09-10T10:00:00Z', access: { restricted: true, allow: [{ kind: 'provider', providerId: 'openai' }] } },
];
const member = (providerId, modelId, position) => ({ providerId, modelId, position, selected: true });
const combos = [
  { id: 'combo-1', name: 'fast', strategy: 'fallback', stickyLimit: 0, judgeModel: '', fusionEnabled: false, members: [member('openai', 'gpt-4o', 0), member('anthropic', 'claude-sonnet', 1)] },
  { id: 'combo-2', name: LONG_COMBO, strategy: 'round-robin', stickyLimit: 3, judgeModel: '', fusionEnabled: true, fusionTuning: { minPanel: 2, stragglerGraceMs: 8000, panelHardTimeoutMs: 90000 }, members: [member('openai', 'gpt-4o', 0), member('anthropic', 'claude-sonnet', 1), member('deepseek', 'deepseek-chat', 2)] },
];
const models = [
  ...['gpt-4o', 'gpt-4o-mini'].map(id => ({ providerId: 'openai', id, name: id, capabilities: ['vision'], disabled: false })),
  ...['claude-sonnet'].map(id => ({ providerId: 'anthropic', id, name: id, capabilities: ['vision', 'pdf'], disabled: false })),
  ...['deepseek-chat'].map(id => ({ providerId: 'deepseek', id, name: id, capabilities: [], disabled: false })),
  ...Array.from({ length: 6 }, (_, i) => ({ providerId: 's1-prod', id: `jev-${i}`, name: `jev-${i}`, capabilities: [], disabled: false })),
];
const nodes = [
  { id: 'node-openai', kind: 'builtin', providerId: 'openai', name: 'openai', baseUrl: 'https://api.openai.com/v1', transports: ['openai-chat'] },
  { id: 'node-anthropic', kind: 'builtin', providerId: 'anthropic', name: 'anthropic', baseUrl: 'https://api.anthropic.com', transports: ['anthropic'] },
  { id: 'node-s1', kind: 'generic', providerId: 's1-prod', name: 'S1 Prod', prefix: 's1-prod', baseUrl: 'https://s1.example.test/v1/systemone', transports: ['systemone'] },
  { id: 'node-cf', kind: 'builtin', providerId: 'cloudflare-ai', name: 'cloudflare-ai', baseUrl: 'https://api.cloudflare.com/client/v4/accounts', transports: ['systemone', 'openai-chat'] },
];
const providers = ['openai', 'anthropic', 'deepseek', 'groq', 's1-prod', 'cloudflare-ai'].map(id => ({ id, transports: ['openai-chat'], auth: 'api-key', authModes: ['api-key'], modelCatalog: 'static', passthroughModels: false, reportsUsage: false, signInModes: [], signInMethods: [], configuredNodes: 1, enabledAccounts: 1, readiness: { status: 'ready', served: true } }));

const quotaItems = [
  { connectionId: 'codex-1', providerId: 'codex', name: 'Codex work account with a rather long display name to wrap on a phone', enabled: true, status: 'available', remaining: 0.4, nextResetAt: '2026-09-26T10:00:00Z', observedAt: '2026-09-25T20:30:00Z', plan: 'pro', availableResetCredits: 2, windows: [{ name: '5h', displayName: '5-hour window', remaining: 0.4, resetAt: '2026-09-25T23:00:00Z', unlimited: false }, { name: 'weekly', remaining: 0.72, resetAt: '2026-09-29T00:00:00Z', unlimited: false }] },
  { connectionId: 'claude-1', providerId: 'claude', name: 'Claude home', enabled: true, status: 'exhausted', remaining: 0, observedAt: '2026-09-25T20:00:00Z', stale: true, windows: [{ name: 'credits', credit: { currency: 'USD', state: 'positive', total: 12.5 }, unlimited: false }] },
  { connectionId: 'groq-1', providerId: 'groq', name: 'Groq shared', enabled: false, status: 'unlimited', observedAt: '2026-09-25T19:00:00Z' },
];
const requestDetail = { id: 'req-1', requestId: 'req-1', status: 200, providerId: 'openai', modelId: 'gpt-4o', createdAt: '2026-09-25T20:59:00Z', inputTokens: 1200, outputTokens: 340, durationMs: 700 };
const PAGES = [
  { name: 'providers', hash: '#providers', ready: 'h1' },
  { name: 'usage', hash: '#usage', ready: 'h1' },
  { name: 'quota', hash: '#quota-tracker', ready: 'h1' },
];

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const artifacts = process.env.ROUTEWEFT_SMOKE_ARTIFACTS || '/tmp/routeweft-ui-evidence';
  const label = process.env.PHASE3B_LABEL || 'after';
  const capture = process.env.PHASE3B_CAPTURE === '1';
  mkdirSync(artifacts, { recursive: true });
  const errors = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const paged = (items, url) => {
    const page = Number(url.searchParams.get('page') || 1);
    const size = Number(url.searchParams.get('pageSize') || 100);
    return { items: items.slice((page - 1) * size, page * size), page, pageSize: size, total: items.length };
  };
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  page.on('console', msg => { if (msg.type() === 'error' && !/Failed to load resource/.test(msg.text())) errors.push('console: ' + msg.text()); });
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (method !== 'GET') return json(route, 200, {});
    if (path === '/events' || path === '/usage/stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': connected\n\n' });
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/quota') return json(route, 200, { items: quotaItems, page: 1, pageSize: 50, total: quotaItems.length, providers: ['claude', 'codex', 'groq'], sort: 'default' });
    if (path === '/usage/active') return json(route, 200, { items: [], total: 0 });
    if (path.startsWith('/usage/requests/')) return json(route, 200, requestDetail);
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 7, snapshotVersion: 7 }, providers: { catalog: 80, nodes: 4, enabledConnections: 3 }, apiKeys: { active: 2 }, traffic: { active: 4, requests: 2947, errors: 80, lastRequestAt: '2026-09-25T20:59:00Z' }, telemetry: { enabled: true, written: 2900, lost: 0, lostDiagnostics: 2, degraded: true } });
    if (path === '/usage/summary') return json(route, 200, summary(url.searchParams.get('period') || '7d'));
    if (path === '/usage') return json(route, 200, paged(events, url));
    if (path === '/connections') return json(route, 200, paged(connections, url));
    if (path === '/keys') return json(route, 200, paged(keys, url));
    if (path === '/settings') return json(route, 200, { settings: { requireApiKey: 'true' }, writable: ['requireApiKey'] });
    if (path === '/combos') return json(route, 200, paged(combos, url));
    if (path === '/models') return json(route, 200, paged(models, url));
    if (path === '/systemone') return json(route, 200, paged(models.filter(m => m.providerId === 's1-prod'), url));
    if (path === '/provider-nodes') return json(route, 200, paged(nodes, url));
    if (path === '/providers') return json(route, 200, paged(providers, url));
    if (path === '/capability-adapters') return json(route, 200, { items: [{ capability: 'vision', enabled: true, roundRobin: false, pool: [{ providerId: 'openai', modelId: 'gpt-4o' }] }, { capability: 'audio-input', enabled: false, pool: [] }] });
    if (path === '/aliases' || path === '/models/disabled') return json(route, 200, paged([], url));
    return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
  });

  const measure = () => page.evaluate(() => {
    const canvas = document.createElement('canvas');
    canvas.width = canvas.height = 1;
    const context = canvas.getContext('2d', { willReadFrequently: true });
    const rgba = value => { context.clearRect(0, 0, 1, 1); context.fillStyle = value; context.fillRect(0, 0, 1, 1); return [...context.getImageData(0, 0, 1, 1).data]; };
    const luminance = rgb => rgb.slice(0, 3).map(v => { const c = v / 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; }).reduce((s, c, i) => s + c * [0.2126, 0.7152, 0.0722][i], 0);
    const ratio = (a, b) => { const [x, y] = [luminance(a), luminance(b)].sort((m, n) => n - m); return (x + 0.05) / (y + 0.05); };
    const over = (top, below) => { const a = top[3] / 255; return [0, 1, 2].map(i => Math.round(top[i] * a + below[i] * (1 - a))).concat(255); };
    const backdrop = element => {
      let layers = [];
      for (let node = element; node; node = node.parentElement) {
        const colour = rgba(getComputedStyle(node).backgroundColor);
        if (colour[3] > 0) { layers.push(colour); if (colour[3] === 255) break; }
      }
      let result = rgba(getComputedStyle(document.body).backgroundColor);
      if (result[3] < 255) result = [255, 255, 255, 255];
      for (const layer of layers.reverse()) result = over(layer, result);
      return result;
    };
    const visible = element => { const r = element.getBoundingClientRect(); const s = getComputedStyle(element); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' && Number(s.opacity) > 0; };
    const lowContrast = [];
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    const seen = new Set();
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      const element = node.parentElement;
      if (!element || seen.has(element) || !node.textContent.trim() || !visible(element) || element.closest('svg, script, style, [aria-hidden="true"], .sr-only')) continue;
      seen.add(element);
      const style = getComputedStyle(element);
      const text = rgba(style.color);
      const ground = backdrop(element);
      const ink = over(text, ground);
      const value = ratio(ink, ground);
      if (value < 4.5) lowContrast.push(`${element.tagName.toLowerCase()}.${String(element.className).split(' ')[0]} "${node.textContent.trim().slice(0, 24)}" ${value.toFixed(2)}`);
    }
    const weakControls = [];
    const smallTargets = [];
    const menu = document.querySelector('.mobile-menu');
    const header = document.querySelector('.mobile-header');
    // The frameless menu button has no boundary; its bars are judged against the header (UI_STYLE 12.10).
    const menuBars = menu && header && visible(menu) ? ratio(over(rgba(getComputedStyle(menu).color), backdrop(header)), backdrop(header)) : null;
    for (const element of document.querySelectorAll('button, input:not([type=hidden]), select, textarea, a.button-primary, a.button-secondary, [role=button]')) {
      if (!visible(element) || element.closest('.sr-only') || element === menu) continue;
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      if (innerWidth <= 700 && !['checkbox', 'radio'].includes(element.type) && (rect.height < 43.5 || rect.width < 43.5) && !element.closest('table')) smallTargets.push(`${element.tagName.toLowerCase()}.${String(element.className).split(' ')[0]} ${Math.round(rect.width)}x${Math.round(rect.height)} "${(element.textContent || element.ariaLabel || element.id || '').trim().slice(0, 20)}"`);
      const widths = ['Top', 'Right', 'Bottom', 'Left'].map(side => parseFloat(style[`border${side}Width`]));
      const border = rgba(style.borderTopColor);
      const ground = backdrop(element.parentElement || element);
      if (Math.max(...widths) > 0 && border[3] > 0) { if (ratio(over(border, ground), ground) < 3) weakControls.push(`${element.tagName.toLowerCase()}.${String(element.className).split(' ')[0]} border`); }
      else if (!['checkbox', 'radio'].includes(element.type) && element.tagName !== 'A') {
        const fill = rgba(style.backgroundColor);
        const text = rgba(style.color);
        const own = fill[3] > 0 ? ratio(over(fill, ground), ground) : 0;
        if (own < 3 && ratio(over(text, ground), ground) < 4.5) weakControls.push(`${element.tagName.toLowerCase()}.${String(element.className).split(' ')[0]} no boundary`);
      }
    }
    const shadows = new Set();
    let blur = 0;
    for (const element of document.querySelectorAll('*')) {
      const style = getComputedStyle(element);
      if (style.boxShadow !== 'none') { const m = style.boxShadow.match(/(-?\d+)px (-?\d+)px (\d+)px (\d+)px/); shadows.add(m ? `${m[1]}px ${m[2]}px blur${m[3]} spread${m[4]}` : style.boxShadow); }
      if ((style.backdropFilter && style.backdropFilter !== 'none') || (style.filter && style.filter.includes('blur'))) blur += 1;
    }
    const root = document.documentElement;
    return { menuBars, overflowX: Math.max(root.scrollWidth, document.body.scrollWidth) - root.clientWidth, lowContrast, weakControls, smallTargets, shadows: [...shadows], blur, title: document.querySelector('h1')?.textContent || '' };
  });

  const results = [];
  for (const theme of THEMES) {
    await page.goto(`${ORIGIN}/#overview`);
    await page.evaluate(value => { try { localStorage.setItem('routeweft-theme', value); } catch {} }, theme);
    await page.emulateMedia({ colorScheme: theme });
    for (const width of WIDTHS) {
      await page.setViewportSize({ width, height: 900 });
      for (const target of PAGES) {
        await page.goto(`${ORIGIN}/${target.hash}`);
        await page.reload();
        await page.waitForSelector(target.ready);
        await page.waitForLoadState('networkidle');
        await page.waitForTimeout(450);
        const found = await measure();
        const theme_ = await page.evaluate(() => document.documentElement.dataset.theme || '');
        results.push({ page: target.name, theme, width, theme_, ...found });
        if (capture) await page.screenshot({ path: join(artifacts, `${label}-${target.name}-${theme}-${width}.png`), fullPage: true });
      }
    }
  }
  // Interactive states, measured and shot the same way: the drawer, the key access editor with a choice made,
  // the Combo form, and the language dialog. Light and dark, desktop and phone.
  // The Providers empty detail (nothing selected) is a 3px outlined 24px box with the small hard shadow and side
  // padding, not the 6px-ruled panel (UI_STYLE 5, 7). Measured, not assumed: the numbers go to the report.
  const emptyChecks = [];
  const probeEmpty = async (theme, width) => emptyChecks.push({ theme, width, ...(await page.evaluate(() => {
    const element = document.querySelector('.provider-details > .provider-empty');
    if (!element) return { found: false };
    const style = getComputedStyle(element);
    return { found: true, borders: ['Top', 'Right', 'Bottom', 'Left'].map(side => style[`border${side}Width`]), radius: style.borderTopLeftRadius, padLeft: style.paddingLeft, padRight: style.paddingRight, padTop: style.paddingTop, shadow: style.boxShadow.replace(/rgb\([^)]*\)/, '').trim() };
  })) });
  const states = [
    { name: 'provider-empty', width: 1440, hash: '#providers', act: async () => { await page.waitForSelector('.provider-empty'); await probeEmpty(currentTheme, 1440); } },
    { name: 'provider-empty', width: 390, hash: '#providers', act: async () => { await page.waitForSelector('.provider-empty'); await probeEmpty(currentTheme, 390); } },
    { name: 'provider-details', width: 1440, hash: '#providers', act: async () => { await page.locator('.provider-option').first().click(); await page.waitForTimeout(300); } },
    { name: 'provider-details', width: 390, hash: '#providers', act: async () => { await page.locator('.provider-option').first().click(); await page.waitForTimeout(300); } },
    { name: 'provider-connect', width: 1440, hash: '#providers', act: async () => { await page.locator('.provider-option').first().click(); await page.getByRole('button', { name: 'Add connection' }).click(); await page.waitForTimeout(300); } },
    { name: 'provider-connect', width: 390, hash: '#providers', act: async () => { await page.locator('.provider-option').first().click(); await page.getByRole('button', { name: 'Add connection' }).click(); await page.waitForTimeout(300); } },
    { name: 'provider-generic', width: 1440, hash: '#providers', act: async () => { await page.getByRole('button', { name: 'Add Generic Provider' }).click(); await page.waitForTimeout(300); } },
    { name: 'provider-generic', width: 390, hash: '#providers', act: async () => { await page.getByRole('button', { name: 'Add Generic Provider' }).click(); await page.waitForTimeout(300); } },
    { name: 'usage-requests', width: 1440, hash: '#usage', act: async () => { await page.getByRole('tab', { name: /Requests|Logs/ }).click(); await page.waitForTimeout(400); } },
    { name: 'usage-requests', width: 390, hash: '#usage', act: async () => { await page.getByRole('tab', { name: /Requests|Logs/ }).click(); await page.waitForTimeout(400); } },
    { name: 'usage-details', width: 1440, hash: '#usage', act: async () => { await page.getByRole('tab', { name: /details/i }).click(); await page.waitForTimeout(400); } },
    { name: 'usage-details', width: 390, hash: '#usage', act: async () => { await page.getByRole('tab', { name: /details/i }).click(); await page.waitForTimeout(400); } },
    { name: 'quota-credits', width: 1440, hash: '#quota-tracker', act: async () => { await page.getByRole('button', { name: 'Credit expiry' }).click(); await page.waitForTimeout(400); } },
    { name: 'quota-credits', width: 390, hash: '#quota-tracker', act: async () => { await page.getByRole('button', { name: 'Credit expiry' }).click(); await page.waitForTimeout(400); } },
    { name: 'drawer', width: 390, hash: '#providers', act: async () => { await page.locator('.mobile-menu').click(); await page.waitForTimeout(250); } },
  ];
  let currentTheme = '';
  for (const theme of THEMES) {
    currentTheme = theme;
    await page.evaluate(value => { try { localStorage.setItem('routeweft-theme', value); } catch {} }, theme);
    await page.emulateMedia({ colorScheme: theme });
    for (const state of states) {
      await page.setViewportSize({ width: state.width, height: 900 });
      await page.goto(`${ORIGIN}/${state.hash}`);
      await page.reload();
      await page.waitForSelector('h1');
      await page.waitForLoadState('networkidle');
      await page.waitForTimeout(350);
      await state.act();
      const found = await measure();
      results.push({ page: `state:${state.name}`, theme, width: state.width, theme_: await page.evaluate(() => document.documentElement.dataset.theme || ''), ...found });
      // A full-page capture taken from a scrolled page also shows the fixed skip link parked above the viewport; scroll home first.
      await page.evaluate(() => window.scrollTo(0, 0));
      if (capture) await page.screenshot({ path: join(artifacts, `${label}-state-${state.name}-${theme}-${state.width}.png`), fullPage: state.name !== 'drawer' });
    }
  }
  writeFileSync(join(artifacts, `phase3b-${label}.json`), JSON.stringify(results, null, 1));
  const bad = results.flatMap(r => {
    const issues = [];
    if (r.overflowX > 0) issues.push(`horizontal overflow ${r.overflowX}px`);
    if (r.lowContrast.length) issues.push(`text under 4.5:1: ${r.lowContrast.slice(0, 4).join('; ')}`);
    if (r.weakControls.length) issues.push(`control boundary under 3:1: ${r.weakControls.slice(0, 4).join('; ')}`);
    if (r.smallTargets.length) issues.push(`phone controls under 44px: ${r.smallTargets.slice(0, 4).join('; ')}`);
    if (r.menuBars !== null && r.menuBars < 3) issues.push(`menu bars ${r.menuBars.toFixed(2)}:1 against the header`);
    if (r.blur) issues.push(`${r.blur} blurred element(s)`);
    for (const shadow of r.shadows) if (!/^(10px 6px|5px 3px) blur0 spread0$/.test(shadow)) issues.push(`shadow ${shadow}`);
    return issues.map(issue => `${r.page}/${r.theme}/${r.width}: ${issue}`);
  });
  const emptyBad = emptyChecks.flatMap(c => {
    const issues = [];
    if (!c.found) return [`${c.theme}/${c.width}: provider empty detail not rendered`];
    if (c.borders.some(w => w !== '3px')) issues.push(`borders ${c.borders.join('/')}`);
    if (c.radius !== '24px') issues.push(`radius ${c.radius}`);
    if (c.padLeft !== '24px' || c.padRight !== '24px') issues.push(`side padding ${c.padLeft}/${c.padRight}`);
    if (!/^5px 3px 0px 0px$/.test(c.shadow)) issues.push(`shadow ${c.shadow}`);
    return issues.map(i => `${c.theme}/${c.width}: provider empty ${i}`);
  });
  console.log('provider empty state:', JSON.stringify(emptyChecks));
  assert.deepEqual(emptyBad, [], 'provider empty state box');
  const themeFailures = results.filter(r => r.theme_ && r.theme_ !== r.theme);
  console.log(`phase 3b pages (${label}): ${results.length} views measured, ${bad.length} problems`);
  if (process.env.PHASE3B_REPORT === '1') { for (const line of bad) console.log('  ' + line); }
  else assert.deepEqual(bad, [], 'phase 3b page problems');
  assert.deepEqual(themeFailures.map(r => `${r.page}/${r.theme}/${r.width} resolved ${r.theme_}`), [], 'the theme was applied');
  assert.deepEqual(errors, [], 'page errors');
  await browser.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
