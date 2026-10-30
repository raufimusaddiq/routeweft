// Browser fixture for the display language (#279, U10). The oracle lets an operator pick
// one of 35 languages, keeps the choice in a one-year `locale` cookie and translates the
// page's text from a per-locale catalog; a text with no entry stays English. This checks
// the same in the real page: the choice, the cookie, the translation and its reversal, the
// sign-in page, a catalog that will not load, and the accessibility of the control (keyboard,
// focus return, state in words, 44px targets, no sideways scroll at 320px). The control API
// is stubbed, so no credential exists. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');

const ORIGIN = 'http://127.0.0.1:4173';

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const errors = [];
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  let signedIn = true;
  const failing = new Set();
  let catalogReads = [];

  async function newPage(viewport = { width: 1280, height: 900 }, cookies = []) {
    const context = await browser.newContext({ viewport });
    if (cookies.length) await context.addCookies(cookies.map(value => ({ name: 'locale', value, url: ORIGIN })));
    const page = await context.newPage();
    page.on('pageerror', e => errors.push('pageerror: ' + e.message));
    page.on('console', msg => { if (msg.type() === 'error' && !/Failed to load resource/.test(msg.text())) errors.push('console: ' + msg.text()); });
    await page.route('**/i18n/literals/*.json', route => {
      const code = new URL(route.request().url()).pathname.split('/').pop().replace('.json', '');
      catalogReads.push(code);
      if (failing.has(code)) return route.fulfill({ status: 404, contentType: 'text/plain', body: 'not found' });
      return route.continue();
    });
    await page.route('**/admin/v1/**', route => {
      const url = new URL(route.request().url());
      const path = url.pathname.replace('/admin/v1', '');
      if (path === '/auth/session') return signedIn ? json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' }) : json(route, 401, { error: { code: 'unauthenticated', message: 'admin session is required' } });
      if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 2, nodes: 2, enabledConnections: 2 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } });
      if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
      if (path === '/settings') return json(route, 200, { settings: {}, writable: [] });
      return json(route, 200, { items: [], page: 1, pageSize: 100, total: 0 });
    });
    return { context, page };
  }
  const navText = (page, href) => page.locator(`.nav-link[href="${href}"]`).innerText();
  const cookieOf = async context => (await context.cookies(ORIGIN)).find(cookie => cookie.name === 'locale');
  const openDialog = async page => { await page.locator('#language-button').click(); await page.locator('[role="dialog"]').waitFor(); };
  const choose = async (page, name) => { await page.locator('.language-option', { hasText: name }).click(); await page.locator('[role="dialog"]').waitFor({ state: 'detached' }); };
  const noScroll = async page => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);

  // ---- English by default, and the control --------------------------------------------
  {
    const { context, page } = await newPage();
    await page.goto(`${ORIGIN}/#overview`);
    await page.waitForSelector('.nav-link');
    assert.equal(await page.evaluate(() => document.documentElement.lang), 'en');
    assert.equal(await navText(page, '#settings'), 'Settings');
    assert.equal(catalogReads.length, 0, 'English reads no catalog');
    const button = page.locator('#language-button');
    assert.equal((await button.innerText()).trim().replace(/\s+/g, ' '), '🇺🇸 English', 'the control says which language is shown, in words');
    assert.match(await button.getAttribute('aria-labelledby'), /language-label language-button/);
    assert.equal(await page.getByRole('button', { name: 'Language English' }).count(), 1, 'the control is named by its label and the language it shows')
    const box = await button.boundingBox();
    assert.ok(box.height >= 44 && box.width >= 44, `the control is ${box.width}x${box.height}`);

    // The dialog: named, every locale, the current one in words, keyboard operable.
    await openDialog(page);
    const dialog = page.locator('[role="dialog"]');
    assert.equal(await dialog.getAttribute('aria-modal'), 'true');
    assert.equal(await page.locator('#language-title').innerText(), 'Select Language');
    assert.equal(await dialog.getAttribute('aria-labelledby'), 'language-title');
    assert.equal(await page.locator('.language-option').count(), 35, 'every supported locale is offered');
    const current = page.locator('.language-option[aria-current="true"]');
    assert.equal(await current.count(), 1);
    assert.match(await current.innerText(), /English/);
    assert.match(await current.locator('.sr-only').textContent(), /current language/, 'the current language is stated in text, not colour alone');
    assert.equal(await page.evaluate(() => document.activeElement.getAttribute('aria-current')), 'true', 'focus starts on the current language');
    const first = await page.locator('.language-option').first().boundingBox();
    assert.ok(first.height >= 44, `a language option is ${first.height}px tall`);
    // Tab stays inside the dialog, both ways.
    for (let i = 0; i < 40; i++) { await page.keyboard.press('Tab'); assert.ok(await page.evaluate(() => !!document.activeElement.closest('[role="dialog"]')), `Tab ${i} left the dialog`); }
    for (let i = 0; i < 40; i++) { await page.keyboard.press('Shift+Tab'); assert.ok(await page.evaluate(() => !!document.activeElement.closest('[role="dialog"]')), `Shift+Tab ${i} left the dialog`); }
    // Escape closes it and focus goes back to the control.
    await page.keyboard.press('Escape');
    await dialog.waitFor({ state: 'detached' });
    assert.equal(await page.evaluate(() => document.activeElement.id), 'language-button', 'focus returns to the control');
    // Clicking the backdrop closes it too, and choosing the current language changes nothing.
    await openDialog(page);
    await page.mouse.click(5, 5);
    await dialog.waitFor({ state: 'detached' });
    await openDialog(page);
    await choose(page, 'English');
    assert.equal(catalogReads.length, 0);
    assert.equal(await cookieOf(context), undefined, 'choosing the language already shown saves nothing');

    // ---- Vietnamese: translated, saved for a year, kept across a reload ------------------
    await openDialog(page);
    await choose(page, 'Tiếng Việt');
    await page.waitForFunction(() => document.documentElement.lang === 'vi');
    assert.deepEqual(catalogReads, ['vi']);
    assert.equal(await navText(page, '#settings'), 'Cài đặt');
    assert.equal(await navText(page, '#usage'), 'Thống kê');
    assert.equal(await navText(page, '#console-log'), 'Nhật ký Console');
    assert.equal(await page.locator('#language-label').textContent(), 'Ngôn ngữ');
    const english = await page.locator('.page-description').innerText();
    assert.equal(english, 'Routeweft gateway operations, in one place.', 'a text with no catalog entry stays English');
    assert.equal((await button.innerText()).trim().replace(/\s+/g, ' '), '🇻🇳 Tiếng Việt', 'the language names are not translated');
    const saved = await cookieOf(context);
    assert.equal(saved.value, 'vi');
    assert.equal(saved.path, '/');
    const days = (saved.expires - Date.now() / 1000) / 86400;
    assert.ok(days > 364 && days < 366, `the cookie lasts ${days.toFixed(1)} days, a year`);
    assert.equal(await page.evaluate(() => document.documentElement.dir), '', 'direction is not changed (the oracle sets none)');

    // Text the app changes after it was translated is translated afresh, never put back to the old text.
    const afterUpdate = await page.evaluate(async () => {
      const holder = document.createElement('div');
      holder.textContent = 'Settings';
      document.body.appendChild(holder);
      await new Promise(resolve => setTimeout(resolve, 50));
      const first = holder.firstChild.nodeValue;
      holder.firstChild.nodeValue = 'Usage';
      await new Promise(resolve => setTimeout(resolve, 50));
      const second = holder.firstChild.nodeValue;
      holder.firstChild.nodeValue = 'Text no catalog has';
      await new Promise(resolve => setTimeout(resolve, 50));
      const third = holder.firstChild.nodeValue;
      holder.remove();
      return [first, second, third];
    });
    assert.deepEqual(afterUpdate, ['Cài đặt', 'Thống kê', 'Text no catalog has']);

    // A page the saved language was not chosen on is translated too: the Settings page.
    await page.click('.nav-link[href="#settings"]');
    await page.waitForSelector('#settings-language-title');
    await page.waitForFunction(() => document.querySelector('#settings-language-title').textContent === 'Ngôn ngữ');

    await page.reload();
    await page.waitForSelector('.nav-link');
    await page.waitForFunction(() => document.documentElement.lang === 'vi');
    await page.waitForFunction(() => document.querySelector('.nav-link[href="#settings"] span:last-child').textContent === 'Cài đặt');
    assert.equal((await page.locator('#language-button').innerText()).trim().replace(/\s+/g, ' '), '🇻🇳 Tiếng Việt', 'the choice survives a reload');

    // ---- A large catalog, a right-to-left language, then back to English ----------------
    await openDialog(page);
    await choose(page, 'فارسی');
    await page.waitForFunction(() => document.documentElement.lang === 'fa');
    assert.equal(await navText(page, '#settings'), 'تنظیمات');
    assert.equal(await navText(page, '#overview'), 'بررسی کلی');
    assert.ok(await noScroll(page), 'no sideways scroll in a right-to-left language');
    await openDialog(page);
    await choose(page, 'English');
    await page.waitForFunction(() => document.documentElement.lang === 'en');
    assert.equal(await navText(page, '#settings'), 'Settings', 'English restores the original text');
    assert.equal(await navText(page, '#overview'), 'Overview');
    assert.equal(await page.locator('#language-label').textContent(), 'Language');
    assert.equal((await cookieOf(context)).value, 'en');

    // ---- A catalog that will not load leaves the language as it was, and says so ---------
    failing.add('de');
    await openDialog(page);
    await page.locator('.language-option', { hasText: 'Deutsch' }).click();
    const alert = page.locator('[role="dialog"] [role="alert"]');
    await alert.waitFor();
    assert.match(await alert.innerText(), /Could not load the Deutsch translations\. The language was not changed\./);
    assert.equal((await cookieOf(context)).value, 'en', 'nothing was saved');
    assert.equal(await page.evaluate(() => document.documentElement.lang), 'en');
    assert.equal(await page.locator('.language-option:disabled').count(), 0, 'the options can be used again');
    failing.delete('de');
    await choose(page, 'Deutsch');
    await page.waitForFunction(() => document.documentElement.lang === 'de');
    await context.close();
  }

  // ---- The sign-in page is translated from the saved choice ---------------------------
  {
    signedIn = false;
    const { context, page } = await newPage({ width: 1280, height: 900 }, ['zh-CN']);
    await page.goto(`${ORIGIN}/#overview`);
    await page.waitForSelector('#admin-password');
    await page.waitForFunction(() => document.querySelector('label[for="admin-password"]').textContent === '密码');
    assert.equal(await page.evaluate(() => document.documentElement.lang), 'zh-CN');
    assert.equal(await page.locator('label[for="admin-username"]').innerText(), 'Username', 'a text with no entry stays English');
    await context.close();
    signedIn = true;
  }

  // ---- Cookie values: "zh" is Simplified; anything unknown is English ----------------
  {
    let ctx = await newPage({ width: 1280, height: 900 }, ['zh']);
    await ctx.page.goto(`${ORIGIN}/#overview`);
    await ctx.page.waitForFunction(() => document.documentElement.lang === 'zh-CN');
    await ctx.page.waitForFunction(() => document.querySelector('.nav-link[href="#settings"] span:last-child').textContent === '设置');
    await ctx.context.close();
    catalogReads = [];
    ctx = await newPage({ width: 1280, height: 900 }, ['klingon']);
    await ctx.page.goto(`${ORIGIN}/#overview`);
    await ctx.page.waitForSelector('.nav-link');
    assert.equal(await ctx.page.evaluate(() => document.documentElement.lang), 'en');
    assert.equal(await navText(ctx.page, '#settings'), 'Settings');
    assert.deepEqual(catalogReads, [], 'an unknown value reads no catalog');
    await ctx.context.close();
  }

  // ---- 320px: the drawer, the control and the dialog fit and keep their targets --------
  {
    const { context, page } = await newPage({ width: 320, height: 700 }, ['pt-BR']);
    await page.goto(`${ORIGIN}/#overview`);
    await page.waitForFunction(() => document.querySelector('.nav-link[href="#settings"] span:last-child').textContent === 'Configurações');
    await page.click('.mobile-menu');
    const box = await page.locator('#language-button').boundingBox();
    assert.ok(box.height >= 44 && box.width >= 44, `the control at 320px is ${box.width}x${box.height}`);
    await openDialog(page);
    assert.ok(await noScroll(page), 'no sideways scroll with the dialog open at 320px');
    const dialog = await page.locator('.language-dialog').boundingBox();
    assert.ok(dialog.x >= 0 && dialog.x + dialog.width <= 320, `the dialog is inside 320px (${dialog.x}..${dialog.x + dialog.width})`);
    for (const option of await page.locator('.language-option').all()) {
      const optionBox = await option.boundingBox();
      assert.ok(optionBox.height >= 44, `an option is ${optionBox.height}px tall at 320px`);
      const inner = await option.evaluate(el => ({ over: el.scrollWidth - el.clientWidth }));
      assert.ok(inner.over <= 0, 'a language name does not overflow its option');
    }
    await context.close();
  }

  assert.deepEqual(errors, []);
  await browser.close();
  console.log('locale smoke ok');
})().catch(error => { console.error(error); process.exit(1); });
