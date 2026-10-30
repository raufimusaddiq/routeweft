// Browser fixture for the dashboard's one live channel (#277, U08). Only the events transport is replaced:
// /admin/v1/events gets a controllable EventSource so the test can open it, push events, skip ids, and drop it
// (the backend's own framing is covered by the Go tests); everything else is the real page and the real code.
// Checks: one EventSource serves every page and survives moving between them; the Console Log shows new lines
// live (filtered, deduplicated, bounded), pauses, clears from either side, and counts what it did not show; a
// skipped id and a restored stream each re-read; while the stream is down pages re-read on a timer and say so in
// words; the Overview reloads for the events that change it, at most once per burst, and for no others; 320px.
// The control API is stubbed, so no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

const ORIGIN = 'http://127.0.0.1:4173';

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const errors = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

  // ---- shared state the stubs read and the test changes ------------------------------------
  const counts = { overview: 0, logs: 0, deletes: 0 };
  let requests = 10;
  const levelOf = id => (id % 5 === 0 ? 'ERROR' : 'INFO');
  const row = id => ({ id, time: new Date(Date.UTC(2026, 9, 30, 12, 0, id % 60)).toISOString(), level: levelOf(id), message: `line ${id}` });
  const seedLogs = () => Array.from({ length: 60 }, (_, i) => row(i + 1));
  let logRows = seedLogs();

  async function newPage(viewport = { width: 1280, height: 900 }, { clock = false } = {}) {
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    page.on('pageerror', e => errors.push('pageerror: ' + e.message));
    page.on('console', msg => { if (msg.type() === 'error' && !/Failed to load resource/.test(msg.text())) errors.push('console: ' + msg.text()); });
    await page.addInitScript(() => {
      const Native = window.EventSource;
      const live = { instances: [] };
      window.__live = live;
      class Fake extends EventTarget {
        constructor(url) { super(); this.url = url; this.readyState = 0; this.onopen = null; this.onerror = null; this.closed = false; live.instances.push(this); }
        close() { this.readyState = 2; this.closed = true; }
      }
      window.EventSource = class extends Native {
        constructor(url, options) {
          if (String(url).endsWith('/admin/v1/events')) return new Fake(url);
          super(url, options);
        }
      };
      const current = () => live.instances[live.instances.length - 1];
      live.open = () => { const source = current(); source.readyState = 1; source.onopen && source.onopen(new Event('open')); };
      live.emit = (type, id, data) => current().dispatchEvent(new MessageEvent(type, { data: JSON.stringify({ id, type, createdAt: new Date().toISOString(), data }) }));
      live.drop = closed => { const source = current(); source.readyState = closed ? 2 : 0; source.onerror && source.onerror(new Event('error')); };
    });
    if (clock) await page.clock.install();
    await page.route('**/admin/v1/**', route => {
      const url = new URL(route.request().url());
      const method = route.request().method();
      const path = url.pathname.replace('/admin/v1', '');
      if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
      if (path === '/overview') { counts.overview++; return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 2, nodes: 2, enabledConnections: 2 }, apiKeys: { active: 0 }, traffic: { active: 0, requests, errors: 0, lastRequestAt: '' }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } }); }
      if (path === '/logs' && method === 'DELETE') { counts.deletes++; const cleared = logRows.length; logRows = []; return json(route, 200, { cleared }); }
      if (path === '/logs') {
        counts.logs++;
        const level = (url.searchParams.get('level') || '').toLowerCase();
        const query = (url.searchParams.get('query') || '').toLowerCase();
        const matched = logRows.filter(entry => (!level || entry.level.toLowerCase() === level) && (!query || entry.message.toLowerCase().includes(query))).sort((a, b) => b.id - a.id);
        const size = Number(url.searchParams.get('pageSize') || 50);
        const number = Number(url.searchParams.get('page') || 1);
        return json(route, 200, { items: matched.slice((number - 1) * size, number * size), page: number, pageSize: size, total: matched.length });
      }
      if (path === '/usage/summary') return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'x' } }) });
      return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    });
    return { context, page };
  }
  // A line the server announces is also in its buffer, so a re-read shows it (as the real server does).
  const pushLines = (page, eventId, ids, extra = {}) => {
    for (const id of ids) if (!logRows.some(entry => entry.id === id)) logRows.push(row(id));
    return live(page, 'emit', 'console.log', eventId, { lines: ids.map(row), ...extra });
  };
  const live = (page, fn, ...args) => page.evaluate(({ fn, args }) => window.__live[fn](...args), { fn, args });
  const instances = page => page.evaluate(() => window.__live.instances.map(source => ({ url: source.url, closed: source.closed })));
  const state = page => page.locator('[data-live-state]').first().getAttribute('data-live-state');
  const consoleTop = page => page.locator('.console-log-row p').first().innerText();
  const consoleCount = page => page.locator('.console-log-row').count();
  const totalLabel = page => page.locator('.console-log-toolbar .count-label').first().innerText();
  const goConsole = async page => { await page.goto(`${ORIGIN}/#console-log`); await page.waitForSelector('.console-log-row'); };
  const goOverview = async page => { await page.goto(`${ORIGIN}/#overview`); await page.waitForSelector('.overview-content'); await page.waitForFunction(() => document.querySelector('.overview-content')?.getAttribute('aria-busy') === 'false'); };

  // =============== one channel for every page =================================================
  {
    const { context, page } = await newPage();
    await goOverview(page);
    assert.deepEqual((await instances(page)).map(source => source.url), ['/admin/v1/events'], 'one EventSource, on the shared events URL');
    assert.equal(await state(page), 'connecting');
    assert.match(await page.locator('.live-state').innerText(), /Connecting/);
    await live(page, 'open');
    await page.waitForFunction(() => document.querySelector('.live-state')?.getAttribute('data-live-state') === 'live');
    assert.match(await page.locator('.live-state').innerText(), /Live/);
    assert.equal(await page.locator('.live-state').getAttribute('aria-hidden'), null, 'the state is text a screen reader reads');

    // Moving to another live page keeps that same connection.
    await page.click('a[href="#console-log"]');
    await page.waitForSelector('.console-log-row');
    assert.equal((await instances(page)).length, 1, 'the Console Log shares the Overview\'s connection');
    assert.equal((await instances(page))[0].closed, false);
    assert.equal(await state(page), 'live', 'and starts out live without reconnecting');
    await page.click('a[href="#overview"]');
    await page.waitForSelector('.overview-content');
    assert.equal((await instances(page)).length, 1, 'and back again');
    // A page that does not listen lets it go once nobody is left.
    await page.click('a[href="#providers"]');
    await page.waitForFunction(() => window.__live.instances[0].closed === true);
    await page.click('a[href="#console-log"]');
    await page.waitForSelector('.console-log-row');
    assert.equal((await instances(page)).length, 2, 'a page that listens again opens a fresh one');
    await context.close();
  }

  // =============== Console Log: live lines, pause, clear ======================================
  {
    const { context, page } = await newPage();
    await goConsole(page);
    await live(page, 'open');
    await page.waitForFunction(() => document.querySelector('.console-log-live')?.getAttribute('data-live-state') === 'live');
    assert.equal(await page.locator('.console-log-live').innerText(), 'Live');
    assert.equal(await page.locator('.console-log-live').getAttribute('role'), 'status');
    assert.equal(await consoleTop(page), 'line 60');
    assert.equal(await consoleCount(page), 50, 'a page is 50 lines');

    // New lines go on top, newest first, once each.
    await pushLines(page, 100, [61, 62]);
    await page.waitForFunction(() => document.querySelector('.console-log-row p')?.textContent === 'line 62');
    assert.equal(await consoleCount(page), 50, 'bounded to the page size');
    assert.match(await totalLabel(page), /^62 logs$/);
    await live(page, 'emit', 'console.log', 101, { lines: [row(61), row(62)] });
    assert.match(await totalLabel(page), /^62 logs$/, 'a replayed batch adds nothing');
    assert.equal(await state(page), 'live');

    // The filter in force applies to live lines too.
    await page.selectOption('#console-log-level', 'ERROR');
    await page.locator('.console-log-toolbar button[type="submit"]').click();
    await page.waitForFunction(() => document.querySelector('.console-log-toolbar .count-label')?.textContent === '12 logs');
    await pushLines(page, 102, [63]);
    assert.equal(await consoleTop(page), 'line 60', 'an INFO line is not shown under an ERROR filter');
    assert.match(await totalLabel(page), /^12 logs$/);
    await pushLines(page, 103, [65]);
    await page.waitForFunction(() => document.querySelector('.console-log-row p')?.textContent === 'line 65');
    assert.match(await totalLabel(page), /^13 logs$/);
    await page.selectOption('#console-log-level', '');
    await page.locator('.console-log-toolbar button[type="submit"]').click();
    await page.waitForFunction(() => document.querySelector('.console-log-toolbar .count-label')?.textContent === '64 logs');

    // A batch the server cut short, or a skipped id, means "read it again".
    let before = counts.logs;
    await pushLines(page, 104, [64], { truncated: true });
    await expectMore(() => counts.logs, before, 'a truncated batch re-reads');
    before = counts.logs;
    await live(page, 'emit', 'config.updated', 110, {});
    await expectMore(() => counts.logs, before, 'a skipped id re-reads');

    // Pause: the list stands still, the arrivals are counted, and resuming shows them.
    await page.getByRole('button', { name: 'Pause live view' }).click();
    assert.equal(await page.getByRole('button', { name: 'Resume live view' }).getAttribute('aria-pressed'), 'true');
    assert.equal(await state(page), 'paused');
    const topBefore = await consoleTop(page);
    await pushLines(page, 111, [70]);
    await page.waitForFunction(() => /1 new line waiting; resume to show/.test(document.querySelector('.console-log-live')?.textContent || ''));
    assert.match(await page.locator('.console-log-live').innerText(), /^Live view paused/, 'the pause is stated in words');
    assert.equal(await consoleTop(page), topBefore, 'nothing moved under the reader');
    before = counts.logs;
    await page.getByRole('button', { name: 'Resume live view' }).click();
    await page.waitForFunction(() => document.querySelector('.console-log-row p')?.textContent === 'line 70');
    assert.ok(counts.logs > before);
    assert.equal(await page.locator('.console-log-live').innerText(), 'Live');

    // Another page of the buffer is not disturbed either.
    await page.locator('.console-log-pagination button').last().click();
    await page.waitForFunction(() => document.querySelector('.console-log-pagination .count-label')?.textContent.includes('Page 2'));
    const second = await consoleTop(page);
    await pushLines(page, 112, [71]);
    await page.waitForFunction(() => /1 new line waiting; go to page 1 or Refresh/.test(document.querySelector('.console-log-live')?.textContent || ''));
    assert.equal(await consoleTop(page), second);
    await page.locator('.console-log-pagination button').first().click();
    await page.waitForFunction(() => document.querySelector('.console-log-pagination .count-label')?.textContent.includes('Page 1'));
    assert.equal(await page.locator('.console-log-live').innerText(), 'Live', 'reading page 1 again clears the waiting count');

    // Clear: asks first; declining sends nothing; accepting empties the page and says how many.
    page.once('dialog', dialog => dialog.dismiss());
    await page.getByRole('button', { name: 'Clear logs' }).click();
    assert.equal(counts.deletes, 0, 'declined');
    const total = logRows.length;
    page.once('dialog', dialog => { assert.match(dialog.message(), /Clear every log held in memory/); dialog.accept(); });
    await page.getByRole('button', { name: 'Clear logs' }).click();
    await page.locator('.inline-notice', { hasText: `Cleared ${total} logs.` }).waitFor();
    assert.equal(counts.deletes, 1);
    assert.equal(await consoleCount(page), 0);
    assert.match(await totalLabel(page), /^0 logs$/);
    // A clear made elsewhere reaches this page over the channel.
    logRows = Array.from({ length: 8 }, (_, i) => row(i + 1));
    await page.getByRole('button', { name: 'Refresh' }).click();
    await page.waitForFunction(() => document.querySelectorAll('.console-log-row').length === 8);
    logRows = [];
    await live(page, 'emit', 'console.cleared', 120, { cleared: 8 });
    await page.waitForFunction(() => document.querySelectorAll('.console-log-row').length === 0);
    await page.locator('.inline-notice', { hasText: 'The logs were cleared.' }).waitFor();
    assert.match(await totalLabel(page), /^0 logs$/);
    await context.close();
  }

  // =============== while the stream is down: the timer, said in words =========================
  {
    logRows = seedLogs();
    const { context, page } = await newPage({ width: 1280, height: 900 }, { clock: true });
    await goConsole(page);
    await live(page, 'open');
    await page.waitForFunction(() => document.querySelector('.console-log-live')?.getAttribute('data-live-state') === 'live');
    let before = counts.logs;
    await page.clock.fastForward(60000);
    assert.equal(counts.logs, before, 'a live stream needs no polling');
    await live(page, 'drop', false);
    await page.waitForFunction(() => document.querySelector('.console-log-live')?.getAttribute('data-live-state') === 'polling');
    assert.match(await page.locator('.console-log-live').innerText(), /Live updates unavailable · refreshing every 15 s/, 'stated in words, not by colour');
    logRows.push(row(80));
    await page.clock.fastForward(15000);
    await page.waitForFunction(() => document.querySelector('.console-log-row p')?.textContent === 'line 80');
    before = counts.logs;
    await live(page, 'open');
    await page.waitForFunction(() => document.querySelector('.console-log-live')?.getAttribute('data-live-state') === 'live');
    await expectMore(() => counts.logs, before, 'coming back re-reads once');
    before = counts.logs;
    await page.clock.fastForward(120000);
    assert.equal(counts.logs, before, 'and polling has stopped');
    // A stream the browser gave up on is opened again.
    const opened = (await instances(page)).length;
    await live(page, 'drop', true);
    await page.clock.fastForward(1000);
    await page.waitForFunction(n => window.__live.instances.length > n, opened);
    assert.equal(await state(page), 'polling', 'still on the timer until it opens');
    await context.close();

    // The Overview falls back the same way.
    const overview = await newPage({ width: 1280, height: 900 }, { clock: true });
    await goOverview(overview.page);
    await live(overview.page, 'open');
    await overview.page.waitForFunction(() => document.querySelector('.live-state')?.getAttribute('data-live-state') === 'live');
    await live(overview.page, 'drop', false);
    await overview.page.waitForFunction(() => document.querySelector('.live-state')?.getAttribute('data-live-state') === 'polling');
    assert.match(await overview.page.locator('.live-state').innerText(), /refreshing every 15 s/);
    before = counts.overview;
    await overview.page.clock.fastForward(15000);
    await expectMore(() => counts.overview, before, 'the Overview re-reads on the timer');
    await overview.context.close();
  }

  // =============== Overview: invalidated by what changes it, once per burst ====================
  {
    const { context, page } = await newPage({ width: 1280, height: 900 }, { clock: true });
    await goOverview(page);
    await live(page, 'open');
    await page.waitForFunction(() => document.querySelector('.live-state')?.getAttribute('data-live-state') === 'live');
    await page.clock.fastForward(10000);
    const base = counts.overview;
    // A burst of finished requests is one reload, after the quiet minimum.
    requests = 11;
    for (let id = 200; id < 220; id++) await live(page, 'emit', 'request.completed', id, { status: 200 });
    await page.clock.fastForward(3500);
    await expectMore(() => counts.overview, base, 'a burst of finished requests reloads');
    await page.clock.fastForward(30000);
    // The clock keeps running between emits, so the first event may already have started a reload when the rest
    // arrive: a burst is one reload, or two (that one, then one trailing reload 3 s later), never twenty.
    const burst = counts.overview - base;
    assert.ok(burst >= 1 && burst <= 2, `twenty events, at most two reloads (got ${burst})`);
    const settled = counts.overview;
    // Events that do not change the Overview do not reload it.
    await live(page, 'emit', 'console.log', 220, { lines: [] });
    await live(page, 'emit', 'usage.pending', 221, {});
    await live(page, 'emit', 'quota.refresh', 222, {});
    await page.clock.fastForward(30000);
    assert.equal(counts.overview, settled, 'console lines, pending counts and quota do not reload it');
    // A configuration change does.
    await live(page, 'emit', 'config.updated', 223, { resource: 'settings' });
    await page.clock.fastForward(3500);
    await expectMore(() => counts.overview, settled, 'a configuration change reloads');
    await context.close();
  }

  // =============== 320px ========================================================================
  {
    logRows = seedLogs();
    const { context, page } = await newPage({ width: 320, height: 700 });
    await goConsole(page);
    await live(page, 'open');
    await page.waitForFunction(() => document.querySelector('.console-log-live')?.getAttribute('data-live-state') === 'live');
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), 'no sideways scroll at 320px');
    for (const name of ['Pause live view', 'Clear logs', 'Refresh', 'Filter']) {
      const box = await page.getByRole('button', { name, exact: true }).boundingBox();
      assert.ok(box.height >= 44 && box.width >= 44 && box.x >= 0 && box.x + box.width <= 320, `${name} ${JSON.stringify(box)}`);
    }
    await live(page, 'drop', false);
    await page.waitForFunction(() => document.querySelector('.console-log-live')?.getAttribute('data-live-state') === 'polling');
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), 'and the long fallback sentence fits');
    await context.close();
  }

  assert.deepEqual(errors, []);
  await browser.close();
  console.log('live events smoke ok');
})().catch(error => { console.error(error); process.exit(1); });

// Waits until read() has gone past `than`: for events that make the page start a request, which finishes
// asynchronously. It waits on the condition (up to 5 s), not for a fixed time.
async function expectMore(read, than, message) {
  const deadline = Date.now() + 5000;
  while (read() <= than && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 20));
  assert.ok(read() > than, message);
}
