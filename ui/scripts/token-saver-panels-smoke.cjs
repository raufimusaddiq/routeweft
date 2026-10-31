// Browser fixture for the Token Saver helper health/log/stats panels (#276, U07).
// The R09 endpoints are stubbed with the exact response shapes the Go handlers
// write (internal/control/api/token_saver_helpers.go): probes, PXPIPE counters
// and its bounded tail log. The panels are read-only; this run asserts they
// render those answers, keep no lifecycle control, and never scroll sideways at
// 320px. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on('pageerror', error => errors.push('pageerror: ' + error.message));
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

  let logReads = 0;
  let healthReads = 0;
  const stats = { attempts: 12, succeeded: 9, failed: 2, skipped: 1 };
  // 60 diagnostics, oldest first, exactly as GET /admin/v1/pxpipe/logs answers.
  const logs = Array.from({ length: 60 }, (_, index) => ({
    step: 'pxpipe', status: 'ok', skipped: false, attempted: true, succeeded: true,
    inputChars: 30000 + index, outputChars: 9000, durationMs: 100 + index,
    ...(index === 58 ? { status: 'skipped', skipped: true, attempted: false, succeeded: false, inputChars: 1200, outputChars: 0, durationMs: 0 } : {}),
    ...(index === 59 ? { status: 'failed', attempted: true, succeeded: false, error: 'helper returned 502 after 30 s' } : {}),
  }));
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/settings' && method === 'GET') {
      return json(route, 200, {
        settings: { rtkEnabled: 'true', headroomEnabled: 'true', cavemanEnabled: 'false', ponytailEnabled: 'false', pxpipeEnabled: 'true', cavemanLevel: 'full', ponytailLevel: 'full', headroomUrl: 'http://headroom.internal:8787', headroomTimeoutMs: '3000', headroomCompressUserMessages: 'false', pxpipeMinChars: '25000', pxpipeTimeoutMs: '15000', pxpipeAutoInstall: 'false' },
        writable: ['headroomEnabled', 'pxpipeEnabled', 'headroomUrl', 'headroomTimeoutMs', 'pxpipeMinChars', 'pxpipeTimeoutMs'],
      });
    }
    if (path === '/headroom/status') {
      healthReads += 1;
      return json(route, 200, { enabled: true, url: 'http://headroom.internal:8787', timeoutMs: '3000', compressUserMessages: false, health: { healthy: true, latencyMs: 4 } });
    }
    if (path === '/pxpipe/status') {
      return json(route, 200, { enabled: true, url: 'http://pxpipe.internal:8080', autoInstall: false, minChars: '25000', timeoutMs: '15000', health: { healthy: false, class: 'bad_status', message: 'helper answered its health endpoint with an error status', statusCode: 503, latencyMs: 12 }, stats });
    }
    if (path === '/pxpipe/stats') return json(route, 200, { stats, recent: [] });
    if (path === '/pxpipe/logs') {
      logReads += 1;
      return json(route, 200, { logs });
    }
    return json(route, 404, { error: { code: 'not_found', message: 'not stubbed' } });
  });

  await page.goto('http://127.0.0.1:4173/#token-saver');
  await page.waitForSelector('.token-saver-content');
  await page.locator('.helper-row .status').first().waitFor();

  // Health: the API's word plus the API's reason, each beside a status mark.
  const headroom = page.locator('.helper-row').nth(0);
  const pxpipe = page.locator('.helper-row').nth(1);
  assert.equal(await headroom.locator('.status .dot').count(), 1, 'a status dot sits beside the word');
  assert.match(await headroom.textContent(), /Headroom\s*Enabled · http:\/\/headroom\.internal:8787\s*Available\s*Probe answered in 4 ms/);
  assert.match(await pxpipe.textContent(), /PXPIPE\s*Enabled · http:\/\/pxpipe\.internal:8080\s*Unavailable\s*helper answered its health endpoint with an error status \(HTTP 503\)\./);

  // Stats: exactly the counters the endpoint returned.
  assert.deepEqual(await page.locator('.helper-stat dt').allTextContents(), ['Attempts', 'Succeeded', 'Failed', 'Skipped']);
  assert.deepEqual(await page.locator('.helper-stat dd').allTextContents(), ['12', '9', '2', '1']);

  // Logs: bounded to the newest 50, newest first, with the failure's own text.
  assert.equal(await page.locator('.helper-log-row').count(), 50);
  const firstLog = await page.locator('.helper-log-row').nth(0).textContent();
  const secondLog = await page.locator('.helper-log-row').nth(1).textContent();
  assert.match(firstLog, /Failed\s*30,059 → 9,000\s*159 ms\s*helper returned 502 after 30 s/);
  assert.match(secondLog, /Skipped\s*1,200 → 0\s*0 ms/);

  // Read-only: none of the three new panels offers a lifecycle or config control.
  for (const id of ['helper-health-title', 'pxpipe-stats-title', 'pxpipe-logs-title']) {
    const panel = page.locator(`section[aria-labelledby="${id}"]`);
    assert.equal(await panel.locator('button, a, input, select, textarea, [role="button"]').count(), 0, `${id} is read-only`);
  }

  // Reload re-probes and re-reads the tail, and keeps the same rendering.
  const readsBefore = healthReads;
  const logsBefore = logReads;
  await page.locator('.token-saver-toolbar button', { hasText: 'Reload' }).click();
  await page.waitForFunction(() => document.querySelectorAll('.helper-row .status').length === 2);
  await page.waitForTimeout(300);
  assert.ok(healthReads > readsBefore, 'Reload probes the helpers again');
  assert.ok(logReads > logsBefore, 'Reload re-reads the log tail');

  // 320px: the panels wrap instead of scrolling sideways.
  await page.setViewportSize({ width: 320, height: 900 });
  await page.waitForTimeout(200);
  const overflow = await page.evaluate(() => ({ page: document.documentElement.scrollWidth - document.documentElement.clientWidth, tokenSaver: document.querySelector('.token-saver-content').scrollWidth - document.querySelector('.token-saver-content').clientWidth }));
  assert.equal(overflow.page, 0, 'the page does not overflow at 320px');
  assert.ok(overflow.tokenSaver <= 1, 'the Token Saver content does not scroll sideways (a 1px subpixel rounding is tolerated)');

  assert.deepEqual(errors, []);
  await browser.close();
  console.log('token saver panels smoke ok');
})().catch(error => { console.error(error); process.exit(1); });
