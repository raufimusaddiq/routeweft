// Browser fixture for the Quota page actions (#226). A refresh is followed as a
// job: while queued or running the page says the rows are the previous
// observations, and only a finished job reloads them, marking rows it did not
// update; a failed job reads as a failure. Codex reset credits list soonest
// expiry first, a spend asks first and spends one credit, and a spend whose
// outcome is unknown is reported as unknown and never sent again. The control
// API is stubbed, so no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

const minutesAgo = minutes => new Date(Date.now() - minutes * 60000).toISOString();
const inDays = days => new Date(Date.now() + days * 86400000).toISOString();

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || undefined, args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

  let refreshStarted = '';
  let refreshPolls = [];
  let refreshPosts = 0;
  let quotaReads = 0;
  let spends = [];
  let spendStatus = 502;
  let creditReads = 0;
  // What Codex holds. Each POST takes one, as a spend whose answer was lost may have.
  let balance = 2;
  let creditReadFails = false;
  const items = () => [
    { connectionId: 'codex-1', providerId: 'codex', name: 'Codex work', enabled: true, status: 'available', remaining: 0.4, observedAt: refreshStarted ? new Date(Date.parse(refreshStarted) + 2000).toISOString() : minutesAgo(30), availableResetCredits: 2 },
    { connectionId: 'claude-1', providerId: 'claude', name: 'Claude home', enabled: true, status: 'available', remaining: 0.8, observedAt: minutesAgo(30) },
  ];
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/quota' && method === 'GET') { quotaReads++; const list = items(); return json(route, 200, { items: list, page: 1, pageSize: 50, total: list.length, providers: ['claude', 'codex'], sort: 'default' }) }
    if (path === '/quota/refresh' && method === 'POST') return json(route, 202, { accepted: true, joined: false, job: { id: 'job-' + (++refreshPosts), status: 'queued', requestedAt: new Date().toISOString() } });
    if (path === '/quota/refresh' && method === 'GET') {
      const next = refreshPolls.shift();
      return json(route, 200, { status: next.status, job: next });
    }
    if (path === '/quota/codex-1/codex-reset-credits' && method === 'GET') {
      creditReads++;
      if (creditReadFails) return json(route, 502, { error: { code: 'quota_unavailable', message: 'Codex reset credits could not be read' } });
      return json(route, 200, { availableCount: balance, credits: [
        { status: 'available', grantedAt: minutesAgo(60 * 24 * 20), expiresAt: inDays(20) },
        { status: 'available', grantedAt: minutesAgo(60 * 24 * 5), expiresAt: inDays(3) },
        { status: 'used' },
      ] });
    }
    if (path === '/quota/codex-1/codex-reset-credits' && method === 'POST') {
      spends.push(route.request().postDataJSON());
      balance = Math.max(0, balance - 1);
      if (spendStatus === 'abort') return route.abort('connectionreset');
      if (spendStatus >= 500) return json(route, spendStatus, { error: { code: 'reset_failed', message: 'Codex reset credit outcome is unavailable' } });
      return json(route, 200, { ok: true, noCredit: false, status: 200, code: 'reset', windowsReset: 1 });
    }
    if (method === 'GET') return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    return json(route, 404, { error: { code: 'not_found', message: 'not stubbed' } });
  });

  await page.goto('http://127.0.0.1:4173/#quota-tracker');
  await page.waitForSelector('.quota-row');
  const readsBefore = quotaReads;

  // Queued, then running: the page never claims the rows were updated.
  const start = new Date().toISOString();
  refreshPolls = [
    { id: 'job-1', status: 'running', requestedAt: start, startedAt: start },
    { id: 'job-1', status: 'running', requestedAt: start, startedAt: start },
    { id: 'job-1', status: 'succeeded', requestedAt: start, startedAt: start, finishedAt: new Date().toISOString() },
  ];
  await page.click('.quota-toolbar .button-primary');
  await page.waitForSelector('[data-refresh-status="queued"], [data-refresh-status="running"]');
  const pending = await page.locator('.quota-refresh-state').textContent();
  assert.match(pending, /still show the previous observations/);
  assert.doesNotMatch(pending, /finished/);
  assert.equal(quotaReads, readsBefore, 'rows reloaded before the job ended');
  refreshStarted = start;
  await page.waitForSelector('[data-refresh-status="succeeded"]', { timeout: 10000 });
  await page.waitForFunction(() => document.querySelectorAll('.quota-row').length === 2);
  assert.ok(quotaReads > readsBefore, 'a finished job reloads the rows');
  assert.match(await page.locator('.quota-refresh-state').textContent(), /^Quota refresh finished at .*Rows were reloaded/);
  const claude = await page.locator('.quota-row', { hasText: 'Claude home' }).innerText();
  const codex = await page.locator('.quota-row', { hasText: 'Codex work' }).innerText();
  assert.match(claude, /not updated by the last refresh/);
  assert.doesNotMatch(codex, /not updated by the last refresh/);

  // A failed job reads as a failure, not as an update.
  const again = new Date().toISOString();
  refreshPolls = [{ id: 'job-2', status: 'failed', requestedAt: again, startedAt: again, finishedAt: again, error: 'one or more accounts could not be read' }];
  await page.click('.quota-toolbar .button-primary');
  await page.waitForSelector('[data-refresh-status="failed"]', { timeout: 10000 });
  const failed = page.locator('.quota-refresh-state');
  assert.match(await failed.textContent(), /without reading every account: one or more accounts could not be read/);
  assert.ok((await failed.getAttribute('class')).includes('auth-error'));

  // Credit expiry, soonest first; the claude row has no credit controls.
  assert.equal(await page.locator('.quota-row', { hasText: 'Claude home' }).locator('.quota-actions').count(), 0);
  const codexRow = page.locator('.quota-row', { hasText: 'Codex work' });
  await codexRow.getByRole('button', { name: 'Credit expiry' }).click();
  await page.waitForSelector('.quota-credits li');
  const credits = await codexRow.locator('.quota-credits li').allInnerTexts();
  assert.match(credits[0], /3 reset credits, 2 available/);
  assert.match(credits[1], /expires .*in 2 d|expires .*in 3 d/);
  assert.match(credits[2], /expires .*in 19 d|expires .*in 20 d/);
  assert.match(credits[3], /^used\n[\s\S]*no expiry reported$/);

  // A spend asks first; declining sends nothing.
  const dialogs = [];
  page.once('dialog', dialog => { dialogs.push(dialog.message()); dialog.dismiss() });
  await codexRow.getByRole('button', { name: 'Use 1 reset credit' }).click();
  await page.waitForTimeout(200);
  assert.equal(dialogs[0], 'Use 1 Codex reset credit for Codex work? This cannot be undone. Remaining credits: 2.');
  assert.equal(spends.length, 0, 'a declined confirmation spent a credit');

  // Accepted, every unknown outcome (502, 503, a dropped request) is reported
  // as unknown, sent once and never again, and the open credit list is read
  // again so it never shows the balance from before the spend.
  const spendButton = codexRow.getByRole('button', { name: 'Use 1 reset credit' });
  const unknownCases = [[502, /\(HTTP 502\)/], [503, /\(HTTP 503\)/], ['abort', /\(the request did not complete\)/]];
  balance = 4; // granted more, so every case below has a credit to spend
  for (const [status, how] of unknownCases) {
    spendStatus = status;
    const sentBefore = spends.length;
    const readsBefore = creditReads;
    page.once('dialog', dialog => dialog.accept());
    await spendButton.click();
    await page.waitForFunction(() => document.querySelector('.auth-error[role="alert"]')?.textContent.includes('may or may not'));
    const alert = await page.locator('.auth-error[role="alert"]').first().textContent();
    assert.match(alert, how, String(status));
    assert.match(alert, /request was not sent again\. The credit list below was read again; check it before trying again\./);
    await page.waitForFunction(() => !document.querySelector('.quota-credits[role="status"]'));
    assert.ok(creditReads > readsBefore, `${status}: the credit list was not read again`);
    assert.match((await codexRow.locator('.quota-credits li').allInnerTexts())[0], new RegExp(`, ${balance} available`), `${status}: the list still shows the balance from before the spend`);
    await page.waitForTimeout(1200);
    assert.equal(spends.length, sentBefore + 1, `${status}: an unknown outcome was sent again`);
    assert.deepEqual(spends[spends.length - 1], { confirm: true });
  }
  assert.equal(dialogs.length, 1);

  // If that re-read fails, no spend is offered until a fresh list is read.
  spendStatus = 503;
  creditReadFails = true;
  page.once('dialog', dialog => dialog.accept());
  await spendButton.click();
  await codexRow.locator('.quota-credits.auth-error').waitFor();
  assert.equal(await spendButton.isDisabled(), true, 'a spend was offered with no fresh credit list');
  assert.match(await spendButton.getAttribute('title'), /unknown outcome; read the credit list/);
  creditReadFails = false;
  await codexRow.getByRole('button', { name: 'Credit expiry' }).click(); // closes the failed read
  await codexRow.getByRole('button', { name: 'Credit expiry' }).click(); // reads it fresh
  await codexRow.locator('.quota-credits li').first().waitFor();
  assert.equal(await spendButton.isDisabled(), true, 'the fresh list shows no credit left, so nothing can be spent');
  assert.match(await spendButton.getAttribute('title'), /No reset credit reported/);
  assert.equal(spends.length, 4);

  // A confirmed spend that succeeds says one credit was used and reloads.
  spendStatus = 200;
  balance = 2; // Codex grants credits again: the next read shows two
  await codexRow.getByRole('button', { name: 'Credit expiry' }).click();
  await codexRow.getByRole('button', { name: 'Credit expiry' }).click();
  await page.waitForFunction(() => document.querySelector('.quota-credits li')?.textContent.includes('2 available'));
  const readsBeforeSpend = quotaReads;
  page.once('dialog', dialog => { assert.match(dialog.message(), /Remaining credits: 2\./); dialog.accept() });
  await spendButton.click();
  await page.waitForSelector('.inline-notice[role="status"]:has-text("One Codex reset credit used")');
  assert.equal(spends.length, 5);
  await page.waitForTimeout(300);
  assert.ok(quotaReads > readsBeforeSpend, 'a spend reloads the rows');

  assert.deepEqual(errors, []);
  await browser.close();
  console.log('quota actions smoke ok');
})().catch(error => { console.error(error); process.exit(1); });
