// Browser fixture for the Usage page (#275, U05): estimated costs, the request-detail viewer, exact
// filters, paging, the period selector and the provider topology/live activity. The admin API is
// stubbed with the shapes SPEC §21 defines, including cost, so no credential or upstream is used.
// Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', error => errors.push('pageerror: ' + error.message));
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

  await page.addInitScript(() => {
    const Native = window.EventSource;
    window.__usageStreams = [];
    window.EventSource = class extends Native {
      constructor(url, options) {
        const target = String(url);
        if (target.includes('/admin/v1/usage/stream') || target.endsWith('/admin/v1/events')) {
          const stream = new EventTarget();
          Object.assign(stream, { url: target, readyState: 1, withCredentials: false, onopen: null, onerror: null, onmessage: null, close() { this.readyState = 2 } });
          window.__usageStreams.push(stream);
          setTimeout(() => stream.onopen && stream.onopen(new Event('open')), 0);
          return stream;
        }
        super(url, options);
      }
    };
  });

  const days = ['2026-10-26', '2026-10-27', '2026-10-28', '2026-10-29', '2026-10-30', '2026-10-31', '2026-11-01'];
  const series = days.map((day, index) => ({ day, requests: 10 + index, inputTokens: 1000, outputTokens: 200, cacheReadTokens: 100, cacheWriteTokens: 10, cost: 0.1 + index / 100 }));
  const event = (index, overrides = {}) => ({
    id: 100 - index, requestId: `req-${index}`, status: 200, inputTokens: 30000, outputTokens: 9000, cacheReadTokens: 1200, cacheWriteTokens: 0,
    durationMs: 900, ttftMs: 300, cost: 0.0075, createdAt: `2026-11-01T10:${String(index % 60).padStart(2, '0')}:00Z`,
    providerId: index === 1 ? 'anthropic' : 'openai', modelId: index === 1 ? 'claude-sonnet' : 'gpt-5', connectionId: 'conn-openai', apiKeyId: 'key-ci', ...overrides,
  });
  const items = Array.from({ length: 30 }, (_, index) => event(index));
  let lastListURL = '';
  let lastSummaryURL = '';
  let activeReads = 0;
  const active = {
    activeRequests: [{ model: 'gpt-5', provider: 'openai', account: 'work', count: 2 }, { model: 'claude-sonnet', provider: 'anthropic', account: 'home', count: 1 }],
    recentRequests: [{ timestamp: '2026-11-01T10:59:00Z', model: 'gpt-5', provider: 'openai', promptTokens: 100, completionTokens: 20, status: 'success' }],
    errorProvider: 'anthropic',
  };

  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace('/admin/v1', '');
    const method = route.request().method();
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/usage/summary') {
      lastSummaryURL = url.toString();
      const period = url.searchParams.get('period');
      return json(route, 200, {
        period, since: '2026-10-26T00:00:00Z',
        totals: { requests: 112, inputTokens: 7000, outputTokens: 1400, cacheReadTokens: 700, cacheWriteTokens: 70, errors: 2, avgDurationMs: 900, avgTtftMs: 300, cost: 1.23 },
        series,
        providers: [{ key: 'openai', requests: 90, inputTokens: 6000, outputTokens: 1200, errors: 1, cost: 1.0 }, { key: 'anthropic', requests: 22, inputTokens: 1000, outputTokens: 200, errors: 1, cost: 0.23 }],
        models: [{ key: 'openai/gpt-5', requests: 90, inputTokens: 6000, outputTokens: 1200, errors: 1, cost: 1.0 }],
        statuses: [{ key: '200', requests: 110, inputTokens: 0, outputTokens: 0, errors: 0 }, { key: '500', requests: 2, inputTokens: 0, outputTokens: 0, errors: 2 }],
      });
    }
    if (path === '/usage/active') { activeReads++; return json(route, 200, active); }
    if (path === '/usage/requests/req-0') {
      return json(route, 200, { requestId: 'req-0', events: [items[0]], detail: { routeMode: 'native', detail: { provider: 'openai', model: 'gpt-5', tokens: { input: 30000, output: 9000 } }, createdAt: '2026-11-01T10:00:00Z' } });
    }
    if (path.startsWith('/usage/requests/')) return json(route, 404, { error: { code: 'request_not_found', message: 'not stored' } });
    if (path === '/usage' && method === 'GET') {
      lastListURL = url.toString();
      const pageNumber = Number(url.searchParams.get('page') || '1');
      const provider = url.searchParams.get('providerId');
      const filtered = provider ? items.filter(item => item.providerId === provider) : items;
      return json(route, 200, { items: filtered.slice((pageNumber - 1) * 25, pageNumber * 25), page: pageNumber, pageSize: 25, total: filtered.length });
    }
    return json(route, 404, { error: { code: 'not_found', message: 'not stubbed' } });
  });

  await page.goto('http://127.0.0.1:4173/#usage');
  await page.waitForSelector('.usage-content');
  await page.waitForSelector('.usage-topology-figure');

  // Costs: the period total, the provider rows and the model line carry the API's cost.
  await page.locator('.ed-facts').filter({ hasText: 'Cost' }).waitFor();
  const facts = await page.locator('.ed-facts').first().textContent();
  assert.match(facts, /Cost\s*~\$1\.23\s*est\./);
  assert.match(await page.locator('.prov-table').textContent(), /openai\s*90\s*\$1\.00/);
  assert.match(await page.locator('.ed-models').textContent(), /\$1\.00/);
  // The chart's cost trend uses the same priced series.
  assert.match(await page.locator('.ed-mini').filter({ hasText: 'Cost per day' }).textContent(), /\$1\.23 est\./);

  // Topology and live activity from /admin/v1/usage/active.
  assert.match(await page.locator('.usage-topology-core').textContent(), /Router\s*3 in flight/);
  const nodes = await page.locator('.usage-topology-node').allTextContents();
  assert.equal(nodes.length, 2);
  assert.match(nodes[0], /openai\s*2 in flight\s*gpt-5/);
  assert.match(nodes[1], /anthropic\s*1 in flight/);
  assert.ok(await page.locator('.usage-recent').textContent().then(text => /gpt-5 on openai, 100 in \/ 20 out/.test(text)));

  // Period selection re-reads the summary with the chosen window.
  await page.selectOption('#usage-period', '30d');
  await page.waitForFunction(() => document.querySelector('.ed-headline')?.textContent.includes('tokens'));
  assert.match(lastSummaryURL, /period=30d/);

  // Filters are exact and applied on submit; paging reads the next page from the server.
  await page.click('#usage-tab-logs');
  await page.waitForSelector('.log-table');
  await page.locator('.log-filter').filter({ hasText: 'Provider' }).locator('input').fill('anthropic');
  await page.getByRole('button', { name: 'Apply filters' }).click();
  await page.waitForFunction(() => document.querySelectorAll('.log-table tbody tr').length === 1);
  assert.match(lastListURL, /providerId=anthropic/);
  assert.match(await page.locator('.log-table tbody tr').textContent(), /anthropic \/ claude-sonnet/);
  // The priced row keeps a sub-cent cost visible.
  assert.match(await page.locator('.log-table .log-cost').textContent(), /\$0\.0075/);
  await page.getByRole('button', { name: 'Clear' }).click();
  await page.waitForFunction(() => document.querySelectorAll('.log-table tbody tr').length === 25);
  assert.match(await page.locator('.log-pagination .count-label').textContent(), /Page 1 of 2/);
  await page.getByRole('button', { name: 'Next' }).click();
  await page.waitForFunction(() => document.querySelector('.log-pagination .count-label').textContent.includes('Page 2 of 2'));
  assert.equal(await page.locator('.log-table tbody tr').count(), 5);

  // Live: a completed frame from the usage stream prepends the request on page 1.
  await page.getByRole('button', { name: 'Previous' }).click();
  await page.waitForFunction(() => document.querySelectorAll('.log-table tbody tr').length === 25);
  await page.evaluate(() => {
    const stream = window.__usageStreams.find(candidate => candidate.url.includes('/usage/stream?'));
    stream.dispatchEvent(new MessageEvent('request.completed', { data: JSON.stringify({ requestId: 'req-live', providerId: 'openai', modelId: 'gpt-5', connectionId: 'conn-openai', apiKeyId: 'key-ci', status: 200, durationMs: 10, inputTokens: 5, outputTokens: 2, createdAt: '2026-11-01T11:00:00Z' }) }));
  });
  await page.waitForFunction(() => document.body.textContent.includes('req-live'));
  assert.match(await page.locator('.live-bar .status').textContent(), /Live/);

  // The request-detail viewer opens from the log and shows the priced event and the redacted detail.
  await page.locator('.log-table .link-button').filter({ hasText: 'req-0' }).click();
  await page.waitForSelector('.request-event');
  assert.match(await page.locator('.request-facts').textContent(), /Cost\s*\$0\.0075/);
  assert.match(await page.locator('.request-detail-json').textContent(), /"provider": "openai"/);

  // 320px: nothing scrolls sideways.
  await page.setViewportSize({ width: 320, height: 900 });
  await page.waitForTimeout(200);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  assert.equal(overflow, 0, 'the Usage page does not overflow at 320px');

  assert.ok(activeReads >= 1);
  assert.deepEqual(errors, []);
  await browser.close();
  console.log('usage page smoke ok');
})().catch(error => { console.error(error); process.exit(1); });
