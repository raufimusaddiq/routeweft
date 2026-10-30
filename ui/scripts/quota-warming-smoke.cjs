// Browser fixture for the per-account quota warming switch (#297). Warming is
// usage-increasing and off for every account until someone turns it on, so this
// checks: the switch exists only for OAuth Claude and Codex accounts; it starts
// off, in words as well as state; the keyboard turns it on and a request with the
// right body is sent; the choice survives a reload because the server holds it; a
// change made elsewhere is not undone by the next click; a failed save leaves the
// switch as it was and says so; unreadable settings show no switch rather than a
// false "off"; targets are 44px; and a 320px viewport does not scroll sideways.
// The control API is stubbed, so no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

  // The server's copy of the opt-in maps; a PATCH replaces one provider's JSON text.
  const stored = { claudeAutoPing: '{"connections":{}}', codexAutoPing: '{"connections":{}}' };
  const patches = [];
  let settingsStatus = 200;
  let patchMode = 'ok'; // ok | http | abort
  let settingsReads = 0;
  const items = [
    { connectionId: 'claude-oauth', providerId: 'claude', name: 'Claude home', authKind: 'oauth', enabled: true, status: 'available', remaining: 0.8 },
    { connectionId: 'claude-key', providerId: 'claude', name: 'Claude API key', authKind: 'api-key', enabled: true, status: 'available', remaining: 0.5 },
    { connectionId: 'codex-oauth', providerId: 'codex', name: 'Codex work', authKind: 'oauth', enabled: true, status: 'available', remaining: 0.4 },
    { connectionId: 'codex-two', providerId: 'codex', name: 'Codex spare', authKind: 'oauth', enabled: true, status: 'available', remaining: 0.9 },
    { connectionId: 'gemini-oauth', providerId: 'gemini-cli', name: 'Gemini', authKind: 'oauth', enabled: true, status: 'available', remaining: 0.7 },
  ];
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 2, nodes: 2, enabledConnections: 2 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
    if (path === '/quota' && method === 'GET') return json(route, 200, { items, page: 1, pageSize: 50, total: items.length, providers: ['claude', 'codex', 'gemini-cli'], sort: 'default' });
    if (path === '/settings' && method === 'GET') {
      settingsReads++;
      if (settingsStatus !== 200) return json(route, settingsStatus, { error: { code: 'settings_unavailable', message: 'settings are not configured' } });
      return json(route, 200, { settings: { ...stored }, writable: ['claudeAutoPing', 'codexAutoPing'] });
    }
    if (path === '/settings' && method === 'PATCH') {
      const body = route.request().postDataJSON();
      patches.push(body);
      if (patchMode === 'abort') return route.abort('connectionreset');
      if (patchMode === 'http') return json(route, 400, { error: { code: 'invalid_setting', message: 'setting "codexAutoPing" has an invalid value' } });
      Object.assign(stored, body.set);
      return json(route, 200, { revision: patches.length });
    }
    return json(route, 404, { error: { code: 'not_found', message: 'not stubbed' } });
  });

  const rowOf = name => page.locator('.quota-row', { hasText: name });
  const switchOf = name => rowOf(name).getByRole('switch', { name: `Quota warming for ${name}` });
  // goto alone is a fragment navigation when the page is already there and would
  // keep React's state, so a real reload is forced: what shows afterwards came from the server.
  const open = async () => { await page.goto('http://127.0.0.1:4173/#quota-tracker'); await page.reload(); await page.waitForSelector('.quota-row'); };

  await open();

  // Only OAuth Claude and Codex accounts get a switch.
  await switchOf('Claude home').waitFor();
  assert.equal(await page.getByRole('switch').count(), 3, 'claude oauth + two codex oauth');
  assert.equal(await rowOf('Claude API key').getByRole('switch').count(), 0, 'an API-key account has no warming');
  assert.equal(await rowOf('Gemini').getByRole('switch').count(), 0, 'only Claude and Codex warm');

  // Off by default, in words, with the explanation visible (not hover only).
  for (const name of ['Claude home', 'Codex work', 'Codex spare']) {
    assert.equal(await switchOf(name).isChecked(), false, `${name} starts off`);
    assert.match(await rowOf(name).locator('.switch-label').innerText(), /^Warming off$/);
  }
  assert.match(await rowOf('Codex work').locator('.quota-warming small').innerText(), /Consumes a small amount of quota\. Off unless you turn it on\./);
  assert.match(await rowOf('Claude home').locator('.quota-warming small').innerText(), /moment it resets/);
  const described = await switchOf('Codex work').getAttribute('aria-describedby');
  assert.ok(described && await page.locator('#' + described).count() === 1, 'the switch is described by its explanation');

  // 44px targets, whatever the viewport.
  const box = await rowOf('Codex work').locator('.quota-warming-switch').boundingBox();
  assert.ok(box.height >= 44 && box.width >= 44, `target ${box.width}x${box.height}`);

  // The keyboard turns it on: focus, Space. Focus is visible.
  await switchOf('Codex work').focus();
  await page.keyboard.press('Tab'); await page.keyboard.press('Shift+Tab');
  const outline = await switchOf('Codex work').evaluate(el => getComputedStyle(el).outlineStyle);
  assert.notEqual(outline, 'none', 'keyboard focus is visible');
  await page.keyboard.press('Space');
  await page.waitForFunction(() => /Warming on/.test(document.querySelector('.quota-row:has(input[aria-label="Quota warming for Codex work"]) .switch-label')?.textContent || ''));
  assert.equal(patches.length, 1);
  assert.deepEqual(patches[0], { set: { codexAutoPing: '{"connections":{"codex-oauth":true}}' } });
  assert.equal(await switchOf('Codex work').isChecked(), true);
  assert.equal(await switchOf('Codex spare').isChecked(), false, 'only the chosen account changed');
  assert.equal(await switchOf('Claude home').isChecked(), false);
  assert.match(await page.locator('.inline-notice[role="status"]').innerText(), /Quota warming is on for Codex work/);

  // Persistence: a real reload reads it back from the server.
  const readsBeforeReload = settingsReads;
  await open();
  assert.ok(settingsReads > readsBeforeReload, 'the page read the stored setting after the reload');
  await switchOf('Codex work').waitFor();
  assert.equal(await switchOf('Codex work').isChecked(), true, 'the choice survived a reload');
  assert.match(await rowOf('Codex work').locator('.switch-label').innerText(), /^Warming on$/);
  assert.equal(await switchOf('Codex spare').isChecked(), false);

  // A change made elsewhere since the page loaded is not undone: the next click
  // reads the stored map first and changes only its own account.
  stored.codexAutoPing = '{"connections":{"codex-oauth":true,"made-elsewhere":true}}';
  await switchOf('Codex spare').click();
  await page.waitForFunction(() => /Warming on/.test(document.querySelector('.quota-row:has(input[aria-label="Quota warming for Codex spare"]) .switch-label')?.textContent || ''));
  assert.deepEqual(JSON.parse(patches[1].set.codexAutoPing).connections, { 'codex-oauth': true, 'made-elsewhere': true, 'codex-two': true });

  // Turning it off removes the entry and leaves the others.
  await switchOf('Codex work').click();
  await page.waitForFunction(() => /Warming off/.test(document.querySelector('.quota-row:has(input[aria-label="Quota warming for Codex work"]) .switch-label')?.textContent || ''));
  assert.deepEqual(JSON.parse(patches[2].set.codexAutoPing).connections, { 'made-elsewhere': true, 'codex-two': true });
  assert.equal(patches.length, 3);

  // Claude is stored under its own setting, not Codex's.
  await switchOf('Claude home').click();
  await page.waitForFunction(() => /Warming on/.test(document.querySelector('.quota-row:has(input[aria-label="Quota warming for Claude home"]) .switch-label')?.textContent || ''));
  assert.deepEqual(patches[3], { set: { claudeAutoPing: '{"connections":{"claude-oauth":true}}' } });

  // A save the server refuses leaves the switch as it was and says so.
  patchMode = 'http';
  await switchOf('Codex work').click();
  await page.waitForSelector('p.auth-error[role="alert"]:has-text("invalid value")');
  assert.match(await page.locator('p.auth-error[role="alert"]').first().innerText(), /Warming for Codex work was not changed/);
  assert.equal(await switchOf('Codex work').isChecked(), false, 'a refused save does not flip the switch');
  // A request that never completes does the same.
  patchMode = 'abort';
  await switchOf('Codex work').click();
  await page.waitForSelector('p.auth-error[role="alert"]:has-text("was not changed")');
  assert.equal(await switchOf('Codex work').isChecked(), false);
  patchMode = 'ok';
  assert.equal(patches.length, 6, 'six clicks sent six requests: a failed save is never repeated');

  // Unreadable settings show no switch (not a false "off") and say why.
  settingsStatus = 503;
  await open();
  await page.waitForSelector('p.auth-error[role="alert"]:has-text("Warming switches are hidden")');
  assert.equal(await page.getByRole('switch').count(), 0);
  settingsStatus = 200;

  // 320px: nothing scrolls sideways and the targets hold.
  await page.setViewportSize({ width: 320, height: 800 });
  await open();
  await switchOf('Codex spare').waitFor();
  const overflow = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }));
  assert.ok(overflow.scroll <= overflow.client, `horizontal overflow at 320px: ${overflow.scroll} > ${overflow.client}`);
  const narrow = await rowOf('Codex spare').locator('.quota-warming-switch').boundingBox();
  assert.ok(narrow.height >= 44 && narrow.width >= 44, `target at 320px ${narrow.width}x${narrow.height}`);
  const rowBox = await rowOf('Codex spare').boundingBox();
  const textBox = await rowOf('Codex spare').locator('.quota-warming small').boundingBox();
  assert.ok(textBox.x + textBox.width <= rowBox.x + rowBox.width + 1, 'the explanation stays inside its row at 320px');

  assert.deepEqual(errors, []);
  await browser.close();
  console.log('quota warming smoke ok');
})().catch(error => { console.error(error); process.exit(1); });
