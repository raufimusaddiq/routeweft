// Regression fixture for the kenari model page whose rows all carry
// `capabilities: null` must render, not blank the console. The control API is
// stubbed with the measured production shape. Screenshots at 1440 and 390
// (light) are written under docs/evidence/kenari-null-capabilities when
// KENARI_EVIDENCE=1.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync } = require('node:fs');
const { join } = require('node:path');

const ORIGIN = process.env.ROUTEWEFT_SMOKE_ORIGIN || 'http://127.0.0.1:4173';
const OUT = process.env.KENARI_EVIDENCE_DIR || join(__dirname, '..', '..', 'docs', 'evidence', 'kenari-null-capabilities');

const providers = [
  { id: 'kenari', transports: ['openai-chat', 'openai-responses'], auth: 'api-key', authModes: ['api-key'], modelCatalog: 'static', passthroughModels: false, staticModels: ['agnes-2-0-flash:free'], discoveredModels: 106, customModels: 0, reportsUsage: false, signInModes: [], signInMethods: [], configuredNodes: 1, enabledAccounts: 0, readiness: { status: 'ready', served: true } },
  { id: 'openai', transports: ['openai-chat'], auth: 'api-key', authModes: ['api-key'], modelCatalog: 'static', passthroughModels: false, discoveredModels: 2, customModels: 0, reportsUsage: true, signInModes: [], signInMethods: [], configuredNodes: 1, enabledAccounts: 1, readiness: { status: 'ready', served: true } },
];
const nodes = [{ id: 'node-kenari', kind: 'builtin', providerId: 'kenari', name: 'kenari', baseUrl: 'https://kenari.id', transports: ['openai-chat'] }];
const connections = [{ id: 'conn-kenari', nodeId: 'node-kenari', providerId: 'kenari', name: 'kenari account', authKind: 'api-key', identity: 'operator', enabled: true, priority: 0, credentialConfigured: true }];
const models = Array.from({ length: 106 }, (_, i) => ({
  providerId: 'kenari',
  id: `model-${i}`,
  name: `Model ${i}`,
  ...(i % 4 === 0 ? { contextWindow: 8192 } : {}),
  capabilities: null,
  disabled: false,
  custom: false,
}));
const summary = { period: '30d', since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] };

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const errors = [];
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
  await context.addInitScript(() => localStorage.setItem('routeweft-theme', 'light'));
  const page = await context.newPage();
  page.on('pageerror', error => errors.push(`pageerror: ${error.message}`));
  page.on('console', message => { if (message.type() === 'error' && !/Failed to load resource/.test(message.text())) errors.push(`console: ${message.text()}`); });
  const json = (route, body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  const paged = (items, url) => {
    const requested = Number(url.searchParams.get('page') || 1);
    const size = Number(url.searchParams.get('pageSize') || 100);
    return { items: items.slice((requested - 1) * size, requested * size), page: requested, pageSize: size, total: items.length };
  };
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/events') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': connected\n\n' });
    if (path === '/auth/session') return json(route, { username: 'operator' });
    if (path === '/providers') return json(route, paged(providers, url));
    if (path === '/provider-nodes') return json(route, paged(nodes, url));
    if (path === '/connections') return json(route, paged(connections, url));
    if (path === '/proxy-pools') return json(route, paged([], url));
    if (path === '/models') return json(route, paged(models.filter(model => !url.searchParams.get('providerId') || model.providerId === url.searchParams.get('providerId')), url));
    if (path === '/models/disabled' || path === '/aliases' || path === '/pricing') return json(route, paged([], url));
    if (path === '/usage/summary') return json(route, summary);
    return json(route, paged([], url));
  });

  await page.goto(`${ORIGIN}/#providers`);
  await page.getByRole('button', { name: /kenari/ }).first().click();
  await page.locator('.model-row').first().waitFor({ state: 'visible', timeout: 5000 });

  assert.equal(await page.locator('.model-row').count() >= 3, true, 'model rows did not render');
  const third = await page.locator('.model-row').nth(2).innerText();
  assert.match(third, /model-2/, 'third model row is missing');
  assert.equal(errors.length, 0, errors.join('\n'));

  if (process.env.KENARI_EVIDENCE === '1') {
    mkdirSync(OUT, { recursive: true });
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.screenshot({ path: join(OUT, 'providers-1440.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: join(OUT, 'providers-390.png'), fullPage: true });
  }
  await browser.close();
  console.log('kenari null-capabilities smoke: ok');
})().catch(error => { console.error(error); process.exit(1); });
