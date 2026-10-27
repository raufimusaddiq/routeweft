// PREVIEW ONLY (#263, phase 1). Captures Overview and Providers with each direction's overlay
// injected over the built app, light and dark, 1440px and 390px, plus the drawer and a provider
// selection, and measures what the contract guards measure. Nothing here ships: the app never
// imports ui/preview. Run against `npx vite preview --port 4173` of a fresh `npm run build`.
// Usage: node preview/263/capture.cjs <outDir> [current|a|b ...]
const { chromium } = require('playwright-core');
const { mkdirSync, readFileSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');

const out = process.argv[2] || '/tmp/routeweft-263-preview';
const wanted = process.argv.slice(3);
const directions = [
  { key: 'current', label: 'Current contract (BDR-015), for comparison', css: '' },
  { key: 'a', label: 'PREVIEW #263 · Direction A "Loom" · not shipped', css: readFileSync(join(__dirname, 'a-loom.css'), 'utf8') },
  { key: 'b', label: 'PREVIEW #263 · Direction B "Patchwork" · not shipped', css: readFileSync(join(__dirname, 'b-patchwork.css'), 'utf8') },
].filter(direction => wanted.length === 0 || wanted.includes(direction.key));

// Sanitized fixture, the same shape as scripts/shell-smoke.cjs; 30 days so Fig. 1 has a real comparison.
const days = Array.from({ length: 30 }, (_, index) => {
  const date = new Date(Date.UTC(2026, 7, 27 + index));
  const weekend = [0, 6].includes(date.getUTCDay());
  const requests = Math.round((weekend ? 60 : 140) + index * 3 + (index % 5) * 9);
  return { day: date.toISOString().slice(0, 10), requests, inputTokens: requests * 80, outputTokens: requests * 25, cacheReadTokens: requests * 30, cacheWriteTokens: requests * 4 };
});
const recent = days.slice(-7).reduce((sum, day) => sum + day.requests, 0);
const summary = period => ({
  period, since: '2026-08-27T00:00:00.000Z',
  totals: { requests: period === '30d' ? days.reduce((sum, day) => sum + day.requests, 0) : recent, inputTokens: 90000, outputTokens: 30000, cacheReadTokens: 40000, cacheWriteTokens: 5000, errors: 41, avgDurationMs: 420, avgTtftMs: 90 },
  series: period === '7d' ? days.slice(-7) : days,
  providers: [{ key: 'openai', requests: 812, inputTokens: 8000, outputTokens: 2800, errors: 9 }, { key: 'anthropic', requests: 344, inputTokens: 1000, outputTokens: 200, errors: 4 }, { key: 'gemini', requests: 197, inputTokens: 900, outputTokens: 150, errors: 22 }, { key: 'deepseek', requests: 61, inputTokens: 300, outputTokens: 90, errors: 1 }],
  models: [{ key: 'openai/gpt-5', requests: 700, inputTokens: 7000, outputTokens: 2500, errors: 6 }],
  statuses: [{ key: '200', requests: recent - 41, inputTokens: 9000, outputTokens: 3000, errors: 0 }, { key: '429', requests: 25, errors: 25 }, { key: '500', requests: 16, errors: 16 }],
});
const provider = (id, transports, served, accounts, extra = {}) => ({ id, transports, auth: 'api-key', authModes: ['api-key'], modelCatalog: 'static', passthroughModels: false, reportsUsage: false, configuredNodes: accounts ? 1 : 0, enabledAccounts: accounts, readiness: served ? { status: 'ready', served: true } : { status: 'unavailable', served: false, reason: 'preview fixture: wire not bound' }, ...extra });
const providers = [provider('openai', ['openai-chat', 'openai-responses'], true, 2, { defaultBaseURL: 'https://api.openai.com/v1' }), provider('anthropic', ['anthropic-messages'], true, 1), provider('gemini', ['openai-chat'], true, 1), provider('deepseek', ['openai-chat'], true, 1), provider('openrouter', ['openai-chat'], false, 0, { modelCatalog: 'dynamic', passthroughModels: true })];
const nodes = ['openai', 'anthropic', 'gemini', 'deepseek'].map(id => ({ id: `node-${id}`, kind: 'builtin', providerId: id, name: id, transports: ['openai-chat'] }));
const connections = [{ id: 'conn-1', nodeId: 'node-openai', providerId: 'openai', name: 'primary', authKind: 'api-key', identity: 'acct-1', enabled: true, priority: 0, credentialConfigured: true }, { id: 'conn-2', nodeId: 'node-openai', providerId: 'openai', name: 'backup', authKind: 'api-key', identity: 'acct-2', enabled: false, priority: 1, credentialConfigured: true }, ...['anthropic', 'gemini', 'deepseek'].map(id => ({ id: `conn-${id}`, nodeId: `node-${id}`, providerId: id, name: 'main', authKind: 'api-key', identity: `acct-${id}`, enabled: true, priority: 0, credentialConfigured: true }))];
const overview = { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 7, snapshotVersion: 7 }, providers: { catalog: 81, nodes: 4, enabledConnections: 4 }, apiKeys: { active: 2 }, traffic: { active: 3, requests: 4120, errors: 41, lastRequestAt: '2026-09-25T11:58:00Z' }, telemetry: { enabled: true, written: 4120, lost: 0, lostDiagnostics: 0, degraded: false } };
const events = [['openai', 'gpt-5', 200, 310], ['anthropic', 'claude-sonnet', 200, 540], ['gemini', 'gemini-2.5-pro', 429, 90], ['openai', 'gpt-5-mini', 200, 180]].map(([providerId, modelId, status, durationMs], index) => ({ id: index + 1, requestId: `req-${index}`, status, providerId, modelId, inputTokens: 100 + index * 40, outputTokens: 20 + index * 9, cacheReadTokens: 10, cacheWriteTokens: 0, durationMs, ttftMs: 80, createdAt: `2026-09-25T11:5${index}:00Z` }));

const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
async function stub(page) {
  await page.route('**/admin/v1/**', route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, { username: 'operator', expiresAt: '2026-09-26T00:00:00Z' });
    if (path === '/overview') return json(route, overview);
    if (path === '/usage/summary') return json(route, summary(url.searchParams.get('period') || '7d'));
    if (path === '/usage') return json(route, { items: events, page: 1, pageSize: 50, total: 4120 });
    if (path === '/usage/stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: 'retry: 600000\n: connected\n\n' });
    if (path === '/providers') return json(route, { items: providers, page: 1, pageSize: 100, total: providers.length });
    if (path === '/provider-nodes') return json(route, { items: nodes, page: 1, pageSize: 100, total: nodes.length });
    if (path === '/connections') return json(route, { items: connections, page: 1, pageSize: 100, total: connections.length });
    if (path === '/models' && url.searchParams.get('providerId') === 'openai') return json(route, { items: [{ providerId: 'openai', id: 'gpt-5', name: 'GPT-5', contextWindow: 128000, capabilities: [], disabled: false }], page: 1, pageSize: 20, total: 1 });
    if (route.request().method() === 'GET') return json(route, { items: [], page: 1, pageSize: 100, total: 0 });
    return json(route, { error: { code: 'preview', message: 'preview is read-only' } }, 405);
  });
}

// The same measures shell-smoke.cjs makes (contrast 4.5:1 for text, overflow, radius) plus
// control boundaries at 3:1, 44px targets on a phone and the box-shadow/blur inventory.
const measure = () => {
  const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1;
  const context = canvas.getContext('2d', { willReadFrequently: true });
  const rgba = value => { context.clearRect(0, 0, 1, 1); context.fillStyle = value; context.fillRect(0, 0, 1, 1); return [...context.getImageData(0, 0, 1, 1).data]; };
  const luminance = rgb => rgb.slice(0, 3).map(value => { const c = value / 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; }).reduce((sum, c, index) => sum + c * [0.2126, 0.7152, 0.0722][index], 0);
  const ratio = (a, b) => { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
  const background = element => { const layers = []; for (let node = element; node; node = node.parentElement) layers.push(rgba(getComputedStyle(node).backgroundColor)); return layers.reverse().reduce((base, color) => color.slice(0, 3).map((c, i) => c * color[3] / 255 + base[i] * (1 - color[3] / 255)), [255, 255, 255]); };
  const elements = [...document.querySelectorAll('.app-shell, .app-shell *')];
  const visible = element => element.getClientRects().length && getComputedStyle(element).visibility !== 'hidden';
  const hasText = element => [...element.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim());
  const lowContrast = elements.filter(visible).filter(element => !element.disabled && hasText(element)).flatMap(element => { const value = ratio(rgba(getComputedStyle(element).color), background(element)); return value < 4.5 ? [{ element: element.className, ratio: +value.toFixed(2) }] : []; });
  const controls = elements.filter(visible).filter(element => element.matches('button:not(.drawer-backdrop), input, select, textarea'));
  const weakBoundaries = controls.flatMap(element => { const style = getComputedStyle(element); if (parseFloat(style.borderTopWidth) === 0) return []; const own = rgba(style.backgroundColor); const behind = background(element.parentElement); const value = Math.max(ratio(rgba(style.borderTopColor), behind), own[3] ? ratio(own, behind) : 0); return value < 3 ? [{ element: element.className || element.tagName, ratio: +value.toFixed(2) }] : []; });
  const smallTargets = innerWidth < 700 ? elements.filter(visible).filter(element => element.matches('button:not(.drawer-backdrop), select, input:not([type=checkbox]):not([type=radio]), .nav-link')).filter(element => element.getBoundingClientRect().height < 44).map(element => element.className || element.tagName) : [];
  const radii = elements.filter(visible).flatMap(element => ['borderTopLeftRadius'].filter(key => parseFloat(getComputedStyle(element)[key]) > 2).map(() => element.className));
  const shadows = [...new Set(elements.map(element => getComputedStyle(element).boxShadow).filter(value => value !== 'none'))];
  const blurred = elements.filter(element => /blur/.test(getComputedStyle(element).filter + getComputedStyle(element).backdropFilter)).length;
  return { overflow: document.documentElement.scrollWidth - innerWidth, lowContrast, weakBoundaries, smallTargets: [...new Set(smallTargets)], roundedElements: radii.length, shadows, blurred, accents: [...document.querySelectorAll('.accent-surface')].filter(visible).length };
};

(async () => {
  mkdirSync(out, { recursive: true });
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME, args: ['--no-sandbox'] });
  const report = [];
  for (const direction of directions) {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await stub(page);
    await page.clock.setFixedTime(new Date('2026-09-25T12:00:00Z'));
    const tag = async () => page.evaluate(({ css, label }) => {
      if (css && !document.getElementById('rw263-overlay')) { const style = document.createElement('style'); style.id = 'rw263-overlay'; style.textContent = css; document.head.append(style); }
      if (!document.getElementById('rw263-tag')) { const note = document.createElement('div'); note.id = 'rw263-tag'; note.setAttribute('aria-hidden', 'true'); note.textContent = label; note.style.cssText = 'position:absolute;z-index:99;top:0;right:0;padding:2px 6px;font:600 11px/16px monospace;color:#fff;background:#b0006a;pointer-events:none'; document.body.append(note); }
    }, direction);
    await page.goto('http://127.0.0.1:4173/#overview');
    await tag();
    for (const [view, hash] of [['overview', 'overview'], ['providers', 'providers']]) {
      for (const theme of ['light', 'dark']) {
        for (const width of [1440, 390, 320]) {
          await page.setViewportSize({ width, height: 900 });
          await page.evaluate(target => { location.hash = target; }, hash);
          if (width < 700) { if (await page.locator('.mobile-menu').getAttribute('aria-expanded') === 'true') await page.locator('.mobile-menu').click(); }
          await page.locator('.workspace-inner, .workspace').first().waitFor();
          if (await page.locator('#theme-select').isVisible()) await page.selectOption('#theme-select', theme);
          else { await page.locator('.mobile-menu').click(); await page.selectOption('#theme-select', theme); await page.keyboard.press('Escape'); }
          await page.mouse.move(width - 1, 1);
          await page.evaluate(() => document.fonts.ready);
          await page.waitForTimeout(700);
          const checks = await page.evaluate(measure);
          const shot = width === 320 ? null : `${direction.key}-${view}-${theme}-${width}.png`;
          if (shot) await page.screenshot({ path: join(out, shot), fullPage: true });
          report.push({ direction: direction.key, view, theme, width, shot, ...checks });
        }
      }
    }
    // Clicked states: the phone drawer (open, then Escape returns focus to the menu button) and a provider selection.
    for (const theme of ['light', 'dark']) {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.evaluate(() => { location.hash = 'providers'; });
      await page.locator('.mobile-menu').click();
      await page.selectOption('#theme-select', theme);
      await page.waitForTimeout(400);
      await page.screenshot({ path: join(out, `${direction.key}-drawer-${theme}-390.png`) });
      await page.keyboard.press('Escape');
      const focusAfterEscape = await page.evaluate(() => document.activeElement.className);
      await page.getByRole('button', { name: /openrouter/ }).first().click().catch(() => {});
      await page.waitForTimeout(400);
      const selected = await page.locator('.provider-option-selected').first().innerText().catch(() => '');
      await page.locator('.provider-option-selected').first().focus().catch(() => {});
      const focusedAfterSelect = await page.evaluate(() => document.activeElement.className);
      await page.evaluate(() => window.scrollTo(0, 0)); // a fixed skip link at top:-100px lands mid-page in a scrolled full-page capture
      await page.mouse.move(389, 843);
      await page.screenshot({ path: join(out, `${direction.key}-providers-selected-${theme}-390.png`), fullPage: true });
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.locator('.brand').focus();
      await page.keyboard.press('Tab');
      await page.waitForTimeout(200);
      await page.screenshot({ path: join(out, `${direction.key}-providers-focus-${theme}-1440.png`), clip: { x: 0, y: 0, width: 1440, height: 520 } });
      report.push({ direction: direction.key, view: 'interactions', theme, focusAfterEscape, focusedAfterSelect, selected: selected.split('\n')[0] });
    }
    report.push({ direction: direction.key, errors });
    await page.close();
  }
  await browser.close();
  writeFileSync(join(out, 'report.json'), JSON.stringify(report, null, 2) + '\n');
  for (const row of report.filter(row => row.width)) console.log([row.direction, row.view, row.theme, row.width, `overflow=${row.overflow}`, `lowText=${row.lowContrast.length}`, `weakControls=${row.weakBoundaries.length}`, `small=${row.smallTargets.length}`, `rounded=${row.roundedElements}`, `shadows=${row.shadows.length}`, `blur=${row.blurred}`, `accents=${row.accents}`].join(' '));
  for (const row of report.filter(row => !row.width)) console.log(JSON.stringify(row));
})().catch(error => { console.error('FAIL', error); process.exit(1); });
