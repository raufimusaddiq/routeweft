// Browser fixture for the public landing page (#347). Signed out, the bare root is the landing page; any hash is the
// console and its own sign-in; a valid session at the root goes straight to the console. Measured, not asserted by eye,
// at 1440, 390 and 320 in light and dark: no horizontal overflow, no text under 4.5:1, no control boundary under 3:1, no phone
// control under 44px, no blur or gradient, every box-shadow a hard-offset token, every control named and ringed, the title
// and the 6px rule at the size direction D sets, and the language choice honoured. The control API is stubbed, so no credential
// exists. With ROUTEWEFT_SMOKE_ARTIFACTS set, screenshots are written there (default `/tmp/routeweft-ui-evidence`). Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');

const ORIGIN = process.env.ROUTEWEFT_SMOKE_ORIGIN || 'http://127.0.0.1:4173';
const WIDTHS = [1440, 390, 320];
const THEMES = ['light', 'dark'];

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const artifacts = process.env.ROUTEWEFT_SMOKE_ARTIFACTS || '/tmp/routeweft-ui-evidence';
  const label = process.env.LANDING_LABEL || 'after';
  const capture = process.env.LANDING_CAPTURE === '1';
  mkdirSync(artifacts, { recursive: true });
  const errors = [];
  let signedIn = false;
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const overview = { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 1, snapshotVersion: 1 }, providers: { catalog: 2, nodes: 2, enabledConnections: 2 }, apiKeys: { active: 0 }, traffic: { active: 0, requests: 0, errors: 0 }, telemetry: { enabled: true, written: 0, lost: 0, lostDiagnostics: 0, degraded: false } };
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  page.on('pageerror', e => errors.push('pageerror: ' + e.message));
  page.on('console', msg => { if (msg.type() === 'error' && !/Failed to load resource/.test(msg.text())) errors.push('console: ' + msg.text()); });
  let sessionReads = 0;
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/auth/session') { sessionReads += 1; return signedIn ? json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' }) : json(route, 401, { error: { code: 'unauthenticated' } }); }
    if (path === '/auth/login') { signedIn = true; return json(route, 200, { username: 'operator' }); }
    if (path === '/events' || path === '/usage/stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': connected\n\n' });
    if (path === '/overview') return json(route, 200, overview);
    if (path === '/usage/summary') return json(route, 200, { period: url.searchParams.get('period'), since: '', totals: { requests: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, errors: 0 }, series: [], providers: [], models: [], statuses: [] });
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

  // Landing-specific facts, read in the browser.
  const probe = () => page.evaluate(() => {
    const visible = element => { const r = element.getBoundingClientRect(); const s = getComputedStyle(element); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
    const title = document.querySelector('#landing-title');
    const body = document.querySelector('.landing-body');
    const controls = [...document.querySelectorAll('a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter(visible);
    const unnamed = controls.filter(el => !((el.getAttribute('aria-label') || el.textContent || '').trim() || (el.id && document.querySelector(`label[for="${el.id}"]`)) || el.getAttribute('aria-labelledby')));
    const action = document.querySelector('.landing-action');
    return {
      found: Boolean(title),
      titleSize: title ? getComputedStyle(title).fontSize : '', titleWeight: title ? getComputedStyle(title).fontWeight : '',
      rule: body ? getComputedStyle(body).borderTopWidth : '', ruleStyle: body ? getComputedStyle(body).borderTopStyle : '',
      shell: Boolean(document.querySelector('.sidebar')), signInForm: Boolean(document.querySelector('#sign-in-title')),
      actionText: action ? action.textContent.trim() : '', actionHref: action ? action.getAttribute('href') : '', actionHeight: action ? Math.round(action.getBoundingClientRect().height) : 0,
      unnamed: unnamed.map(el => el.tagName + '.' + el.className), lang: document.documentElement.lang, controls: controls.length,
      landmarks: { main: document.querySelectorAll('main').length, h1: document.querySelectorAll('h1').length },
    };
  });
  // Every control takes a visible 2px focus ring when focused from the keyboard (UI_STYLE 8).
  const ringed = async () => {
    const missing = [];
    const count = await page.locator('.landing a[href], .landing button').count();
    await page.evaluate(() => { document.activeElement && document.activeElement.blur && document.activeElement.blur(); window.scrollTo(0, 0); });
    await page.locator('.landing').evaluate(node => node.focus());
    for (let i = 0; i < count; i++) {
      await page.keyboard.press('Tab');
      const ring = await page.evaluate(() => { const node = document.activeElement; const s = getComputedStyle(node); return { name: node.id || node.className || node.tagName, width: s.outlineWidth, style: s.outlineStyle, visible: node.matches(':focus-visible') }; });
      if (!ring.visible || ring.style === 'none' || parseFloat(ring.width) < 2) missing.push(`${ring.name} ${ring.style} ${ring.width} focus-visible=${ring.visible}`);
    }
    return missing;
  };

  const results = [];
  for (const theme of THEMES) {
    await page.goto(`${ORIGIN}/`);
    await page.evaluate(value => { try { localStorage.setItem('routeweft-theme', value); } catch {} }, theme);
    await page.emulateMedia({ colorScheme: theme });
    for (const width of WIDTHS) {
      signedIn = false;
      await page.setViewportSize({ width, height: 900 });
      await page.goto(`${ORIGIN}/`);
      await page.reload();
      await page.waitForSelector('#landing-title');
      await page.waitForLoadState('networkidle');
      await page.waitForTimeout(400);
      const found = await measure();
      const facts = await probe();
      const missingRing = await ringed();
      results.push({ page: 'landing', theme, width, ...found, ...facts, missingRing });
      await page.evaluate(() => window.scrollTo(0, 0));
      if (capture) await page.screenshot({ path: join(artifacts, `${label}-landing-${theme}-${width}.png`), fullPage: true });
    }
  }

  // The journey (light, 390 and 1440): Sign in leads to the existing sign-in; Back returns; a deep link skips the landing;
  // a valid session at the root goes straight to the console.
  const journey = {};
  await page.emulateMedia({ colorScheme: 'light' });
  await page.setViewportSize({ width: 1440, height: 900 });
  signedIn = false;
  await page.goto(`${ORIGIN}/`); await page.reload();
  await page.waitForSelector('#landing-title');
  await page.getByRole('link', { name: 'Sign in' }).click();
  await page.waitForSelector('#sign-in-title');
  journey.afterSignInClick = { hash: await page.evaluate(() => location.hash), landing: await page.locator('#landing-title').count(), form: await page.locator('#admin-username').count(), shell: await page.locator('.sidebar').count() };
  if (capture) await page.screenshot({ path: join(artifacts, `${label}-signin-after-click-light-1440.png`), fullPage: true });
  await page.goBack();
  await page.waitForSelector('#landing-title');
  journey.back = { hash: await page.evaluate(() => location.hash), landing: await page.locator('#landing-title').count() };
  await page.goto(`${ORIGIN}/#providers`); await page.reload();
  await page.waitForSelector('#sign-in-title');
  journey.deepLinkSignedOut = { landing: await page.locator('#landing-title').count(), form: await page.locator('#admin-username').count() };
  // The existing sign-in works through the landing: sign in, then the console renders.
  await page.fill('#admin-username', 'operator'); await page.fill('#admin-password', 'not-a-real-secret'); await page.click('button[type="submit"]');
  await page.waitForSelector('.sidebar', { state: 'attached' });
  journey.signedInDeepLink = { shell: await page.locator('.sidebar').count(), title: await page.locator('#page-title').textContent() };
  // Signed in: the root IS the console. No landing at any moment of the load.
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`${ORIGIN}/`);
    const sawLanding = await page.evaluate(() => new Promise(resolve => { let seen = false; const check = () => { if (document.querySelector('#landing-title')) seen = true; }; new MutationObserver(check).observe(document.documentElement, { childList: true, subtree: true }); check(); setTimeout(() => resolve(seen), 1500); }));
    await page.waitForSelector('.sidebar', { state: 'attached' });
    await page.waitForSelector('.overview-content');
    journey[`signedInRoot${width}`] = { landing: await page.locator('#landing-title').count(), shell: await page.locator('.sidebar').count(), overview: await page.locator('.overview-content').count(), hash: await page.evaluate(() => location.hash), sawLanding };
    if (capture) await page.screenshot({ path: join(artifacts, `${label}-signed-in-root-light-${width}.png`), fullPage: true });
  }

  // Language: the page follows the saved choice like every screen. A text with no catalog entry stays English; the language
  // label (an existing entry) is translated and <html lang> follows.
  const locale = {};
  signedIn = false;
  await context.addCookies([{ name: 'locale', value: 'zh-CN', url: ORIGIN }]);
  for (const width of [1440, 320]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`${ORIGIN}/`); await page.reload();
    await page.waitForSelector('#landing-title');
    await page.waitForFunction(() => document.documentElement.lang === 'zh-CN');
    await page.waitForFunction(() => document.querySelector('#landing-language-label').textContent === '语言');
    locale[width] = { lang: await page.evaluate(() => document.documentElement.lang), label: await page.locator('#landing-language-label').textContent(), action: await page.locator('.landing-action').textContent(), overflow: await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), button: await page.locator('#landing-language-button').count() };
    if (capture) await page.screenshot({ path: join(artifacts, `${label}-landing-zh-CN-light-${width}.png`), fullPage: true });
  }
  await context.clearCookies();
  // The control opens the existing dialog from the landing page and changes the language.
  await page.setViewportSize({ width: 390, height: 900 });
  await page.goto(`${ORIGIN}/`); await page.reload();
  await page.waitForSelector('#landing-title');
  await page.locator('#landing-language-button').click();
  await page.locator('[role="dialog"]').waitFor();
  locale.dialog = { open: await page.locator('[role="dialog"]').count() };
  if (capture) await page.screenshot({ path: join(artifacts, `${label}-landing-language-dialog-light-390.png`) });
  await page.keyboard.press('Escape');
  await page.locator('[role="dialog"]').waitFor({ state: 'detached' });
  locale.dialog.focusBack = await page.evaluate(() => document.activeElement && document.activeElement.id);

  writeFileSync(join(artifacts, `landing-${label}.json`), JSON.stringify({ results, journey, locale, sessionReads }, null, 1));
  const bad = results.flatMap(r => {
    const issues = [];
    if (!r.found) issues.push('landing not rendered');
    if (r.overflowX > 0) issues.push(`horizontal overflow ${r.overflowX}px`);
    if (r.lowContrast.length) issues.push(`text under 4.5:1: ${r.lowContrast.slice(0, 4).join('; ')}`);
    if (r.weakControls.length) issues.push(`control boundary under 3:1: ${r.weakControls.slice(0, 4).join('; ')}`);
    if (r.smallTargets.length) issues.push(`phone controls under 44px: ${r.smallTargets.slice(0, 4).join('; ')}`);
    if (r.blur) issues.push(`${r.blur} blurred element(s)`);
    for (const shadow of r.shadows) if (!/^(10px 6px|5px 3px) blur0 spread0$/.test(shadow)) issues.push(`shadow ${shadow}`);
    if (r.unnamed.length) issues.push(`controls without a name: ${r.unnamed.join(', ')}`);
    if (r.missingRing.length) issues.push(`controls without a focus ring: ${r.missingRing.join(', ')}`);
    const wantTitle = r.width > 700 ? '64px' : '40px';
    if (r.titleSize !== wantTitle || r.titleWeight !== '700') issues.push(`title ${r.titleSize}/${r.titleWeight}, want ${wantTitle}/700`);
    if (r.rule !== '6px' || r.ruleStyle !== 'solid') issues.push(`rule ${r.rule} ${r.ruleStyle}, want 6px solid`);
    if (r.shell || r.signInForm) issues.push('the console or the sign-in form rendered on the landing page');
    if (r.actionText !== 'Sign in' || r.actionHref !== '#overview') issues.push(`action ${r.actionText} -> ${r.actionHref}`);
    if (r.actionHeight < 44) issues.push(`action ${r.actionHeight}px high`);
    if (r.landmarks.main !== 1 || r.landmarks.h1 !== 1) issues.push(`landmarks main=${r.landmarks.main} h1=${r.landmarks.h1}`);
    return issues.map(issue => `${r.page}/${r.theme}/${r.width}: ${issue}`);
  });
  console.log(`landing (${label}): ${results.length} views measured, ${bad.length} problems`);
  if (process.env.LANDING_REPORT === '1') { for (const line of bad) console.log('  ' + line); }
  else assert.deepEqual(bad, [], 'landing page problems');
  assert.deepEqual(journey.afterSignInClick, { hash: '#overview', landing: 0, form: 1, shell: 1 }, 'Sign in leads to the existing sign-in');
  assert.deepEqual(journey.back, { hash: '', landing: 1 }, 'Back returns to the landing page');
  assert.deepEqual(journey.deepLinkSignedOut, { landing: 0, form: 1 }, 'a deep link keeps its sign-in and skips the landing page');
  assert.deepEqual(journey.signedInDeepLink, { shell: 1, title: 'Providers' }, 'sign-in through the landing page reaches the deep link');
  for (const width of [1440, 390]) assert.deepEqual(journey[`signedInRoot${width}`], { landing: 0, shell: 1, overview: 1, hash: '', sawLanding: false }, `signed in at the root renders the console, never the landing page (${width})`);
  for (const width of [1440, 320]) {
    assert.equal(locale[width].lang, 'zh-CN');
    assert.equal(locale[width].label, '语言');
    assert.equal(locale[width].action, 'Sign in', 'a text with no catalog entry stays English');
    assert.equal(locale[width].overflow <= 0, true, `no horizontal overflow in zh-CN at ${width}`);
  }
  assert.equal(locale.dialog.open, 1);
  assert.equal(locale.dialog.focusBack, 'landing-language-button', 'focus returns to the language button');
  assert.deepEqual(errors, [], 'page errors');
  await browser.close();
})().catch(e => { console.error('FAIL', e); process.exit(1); });
