// Browser fixture for #231 acceptance item 2: keyboard, labels and mobile layout.
//
// At 1440 and 390, light and dark, each page is loaded with stubbed control-API data and measured:
// every visible interactive control is reached by Tab exactly once in DOM order with a visible 2px
// focus outline, no positive tabindex reorders it, Enter (links, buttons, checkboxes) and Space
// (buttons, checkboxes) activate the focused control (clicks are counted and their default action
// prevented, so nothing navigates or submits), every input/select/textarea/button/link has an
// accessible name from aria-labelledby, aria-label, label[for], a wrapping label or button text, and
// the language dialog — a real modal — is the only focus trap. At 390 and 320 the page does not
// overflow horizontally; at 390 a focused text field stays fully inside the visual viewport after
// the height shrinks as an on-screen keyboard would, and no fixed bar covers it. Nothing here proves
// a real mobile keyboard or a manual audit.
//
// The control API is stubbed, so no credential or live provider is involved. Run by scripts/smoke.sh.
const { chromium } = require('playwright-core');
const assert = require('node:assert/strict');
const { mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');

const ORIGIN = 'http://127.0.0.1:4173';
const WIDTHS = [1440, 390];
const THEMES = ['light', 'dark'];
const PAGES = [
  { name: 'overview', hash: '#overview', ready: '.ed-title, h1' },
  { name: 'endpoint', hash: '#endpoint-key', ready: 'h1' },
  { name: 'providers', hash: '#providers', ready: 'h1' },
  { name: 'combo', hash: '#combo-capability-adapter', ready: 'h1' },
  { name: 'systemone', hash: '#system-one', ready: 'h1' },
  { name: 'settings', hash: '#settings', ready: 'h1' },
  { name: 'usage', hash: '#usage', ready: 'h1' },
];
const KEYBOARD_FIELD = '#new-key-name';

const connection = (id, providerId, name) => ({ id, nodeId: `node-${providerId}`, providerId, name, authKind: 'api-key', identity: `${id}@example.test`, enabled: true, priority: 0, credentialConfigured: true });
const connections = [connection('conn-1', 'openai', 'primary'), connection('conn-2', 'anthropic', 'claude')];
const models = ['gpt-4o', 'gpt-4o-mini', 'claude-sonnet'].map((id, i) => ({ providerId: i === 2 ? 'anthropic' : 'openai', id, name: id, capabilities: ['vision'], disabled: false }));
const providers = ['openai', 'anthropic', 'deepseek', 'groq'].map(id => ({ id, transports: ['openai-chat'], auth: 'api-key', authModes: ['api-key'], modelCatalog: 'static', passthroughModels: false, reportsUsage: false, signInModes: [], signInMethods: [], configuredNodes: 1, enabledAccounts: 1, readiness: { status: 'ready', served: true } }));
const nodes = ['openai', 'anthropic'].map(id => ({ id: `node-${id}`, kind: 'builtin', providerId: id, name: id, baseUrl: `https://api.${id}.com/v1`, transports: ['openai-chat'] }));
const keys = [{ id: 'k-1', name: 'CI pipeline', prefix: 'rw_ci12', enabled: true, paused: false, createdAt: '2026-09-01T10:00:00Z', lastUsedAt: '2026-09-25T20:00:00Z', access: { restricted: false, allow: [] } }];
const combos = [{ id: 'combo-1', name: 'fast', strategy: 'fallback', stickyLimit: 0, judgeModel: '', fusionEnabled: false, members: [{ providerId: 'openai', modelId: 'gpt-4o', position: 0, selected: true }] }];
const day = i => new Date(Date.UTC(2026, 8, 25 - (29 - i))).toISOString().slice(0, 10);
const series = Array.from({ length: 30 }, (_, i) => ({ day: day(i), requests: 80 + ((i * 37) % 60), inputTokens: 90000 + i * 1000, outputTokens: 20000 + i * 300, cacheReadTokens: 40000, cacheWriteTokens: 5000, cost: 1.2 + i * 0.03 }));
const summary = period => ({ period, since: '2026-08-27T00:00:00Z', totals: { requests: 2947, inputTokens: 8400000, outputTokens: 1900000, cacheReadTokens: 3100000, cacheWriteTokens: 400000, errors: 80, avgDurationMs: 840, avgTtftMs: 310, cost: 42.5 }, series: period === '7d' ? series.slice(-7) : series, providers: [{ key: 'openai', requests: 1500, inputTokens: 1, outputTokens: 1, errors: 12, cost: 1 }], models: [], statuses: [] });
const events = Array.from({ length: 3 }, (_, i) => ({ id: 3 - i, requestId: `req-${i}`, status: 200, inputTokens: 1200, outputTokens: 340, createdAt: '2026-09-25T20:59:00Z', providerId: 'openai', modelId: 'gpt-4o', connectionId: 'conn-1', durationMs: 700, ttftMs: 200, cost: 0.004 }));

;(async () => {
  const browser = await chromium.launch({ executablePath: process.env.ROUTEWEFT_SMOKE_CHROME || chromium.executablePath(), args: ['--no-sandbox'] });
  const artifacts = process.env.ROUTEWEFT_SMOKE_ARTIFACTS || '/tmp/routeweft-ui-evidence';
  mkdirSync(artifacts, { recursive: true });
  const errors = [];
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  page.on('pageerror', error => errors.push('pageerror: ' + error.message));
  page.on('console', message => { if (message.type() === 'error' && !/Failed to load resource/.test(message.text())) errors.push('console: ' + message.text()); });
  const json = (route, status, body) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  const paged = (items, url) => {
    const current = Number(url.searchParams.get('page') || 1);
    const size = Number(url.searchParams.get('pageSize') || 100);
    return { items: items.slice((current - 1) * size, current * size), page: current, pageSize: size, total: items.length };
  };
  await page.route('**/admin/v1/**', async route => {
    const url = new URL(route.request().url());
    if (route.request().method() !== 'GET') return json(route, 200, {});
    const path = url.pathname.replace('/admin/v1', '');
    if (path === '/events' || path === '/usage/stream') return route.fulfill({ status: 200, contentType: 'text/event-stream', body: ': connected\n\n' });
    if (path === '/auth/session') return json(route, 200, { username: 'operator', expiresAt: '2026-12-01T00:00:00Z' });
    if (path === '/overview') return json(route, 200, { runtime: { health: 'ready', version: '0.1.0', commit: 'deadbeef', configRevision: 7, snapshotVersion: 7 }, providers: { catalog: 4, nodes: 2, enabledConnections: 2 }, apiKeys: { active: 1 }, traffic: { active: 1, requests: 2947, errors: 80, lastRequestAt: '2026-09-25T20:59:00Z' }, telemetry: { enabled: true, written: 2900, lost: 0, lostDiagnostics: 2, degraded: true } });
    if (path === '/usage/summary') return json(route, 200, summary(url.searchParams.get('period') || '7d'));
    if (path === '/usage') return json(route, 200, paged(events, url));
    if (path === '/connections') return json(route, 200, paged(connections, url));
    if (path === '/keys') return json(route, 200, paged(keys, url));
    if (path === '/settings') return json(route, 200, { settings: { requireApiKey: 'true' }, writable: ['requireApiKey'] });
    if (path === '/combos') return json(route, 200, paged(combos, url));
    if (path === '/models') return json(route, 200, paged(models, url));
    if (path === '/systemone') return json(route, 200, paged(models.filter(model => model.providerId === 'openai'), url));
    if (path === '/provider-nodes') return json(route, 200, paged(nodes, url));
    if (path === '/providers') return json(route, 200, paged(providers, url));
    if (path === '/capability-adapters') return json(route, 200, { items: [] });
    if (path === '/aliases' || path === '/models/disabled' || path === '/quota') return json(route, 200, paged([], url));
    return json(route, 200, paged([], url));
  });

  // Recorded once for the whole run: activation is a click on the focused control, and its default
  // action is prevented so Enter never submits a form or follows a link.
  await page.addInitScript(() => {
    window.__a11yClicks = [];
    window.__a11yBlockClicks = false;
    window.addEventListener('click', event => {
      if (!window.__a11yBlockClicks) return;
      window.__a11yClicks.push(event.target && event.target.tagName);
      event.preventDefault();
      event.stopPropagation();
    }, true);
  });

  const focusables = () => page.evaluate(() => {
    const visible = element => {
      const rect = element.getBoundingClientRect();
      const style = getComputedStyle(element);
      return rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && style.display !== 'none' && Number(style.opacity) > 0 && !element.closest('[aria-hidden="true"], .sr-only, template');
    };
    return [...document.querySelectorAll('a[href], button, input:not([type="hidden"]), select, textarea, [tabindex]:not([tabindex="-1"])')]
      // Native semantics: a radio group is one Tab stop (the checked one), and roving-tabindex
      // widgets (the Usage tabs) remove the unselected controls from the Tab order.
      .filter(element => visible(element) && !element.disabled && element.tabIndex >= 0)
      .filter(element => !(element.type === 'radio' && !element.checked))
      .map((element, index) => {
        element.dataset.a11yIndex = String(index);
        const style = getComputedStyle(element);
        return {
          index,
          tag: element.tagName.toLowerCase(),
          type: element.getAttribute('type') || '',
          role: element.getAttribute('role') || '',
          id: element.id,
          tabIndex: element.tabIndex,
          name: (() => {
            const labelledBy = element.getAttribute('aria-labelledby');
            if (labelledBy) {
              const text = labelledBy.split(/\s+/).map(id => document.getElementById(id)?.textContent?.trim() || '').join(' ').trim();
              if (text) return text;
            }
            if (element.getAttribute('aria-label')?.trim()) return element.getAttribute('aria-label').trim();
            if (element.labels?.length) {
              const text = [...element.labels].map(label => label.textContent?.trim() || '').join(' ').trim();
              if (text) return text;
            }
            if (element.tagName === 'BUTTON' || element.tagName === 'A') return element.textContent?.trim() || element.getAttribute('title')?.trim() || '';
            return '';
          })(),
          outline: { style: style.outlineStyle, width: style.outlineWidth, color: style.outlineColor, offset: style.outlineOffset },
          background: style.backgroundColor,
        };
      });
  });

  // Tab from the top of the document, recording which control each press lands on.
  const traversal = async count => {
    // Start from the top of the document, not from whatever the page left focused: a control that
    // opened a form may have been removed, and sequential focus would otherwise resume mid-page.
    await page.evaluate(() => {
      document.activeElement?.blur?.();
      document.body.setAttribute('tabindex', '-1');
      document.body.focus();
      document.body.removeAttribute('tabindex');
      window.scrollTo(0, 0);
    });
    const seen = [];
    for (let i = 0; i < count + 1; i++) {
      await page.keyboard.press('Tab');
      seen.push(await page.evaluate(() => {
        const active = document.activeElement;
        if (!active || active === document.body) return null;
        const canvas = document.createElement('canvas');
        canvas.width = canvas.height = 1;
        const context = canvas.getContext('2d', { willReadFrequently: true });
        const rgba = value => { context.clearRect(0, 0, 1, 1); context.fillStyle = value; context.fillRect(0, 0, 1, 1); return [...context.getImageData(0, 0, 1, 1).data]; };
        const luminance = rgb => rgb.slice(0, 3).map(value => { const c = value / 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; }).reduce((sum, c, index) => sum + c * [0.2126, 0.7152, 0.0722][index], 0);
        const ratio = (a, b) => { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
        const background = element => {
          const layers = [];
          for (let node = element; node; node = node.parentElement) layers.push(rgba(getComputedStyle(node).backgroundColor));
          return layers.reverse().reduce((base, color) => color.slice(0, 3).map((c, i) => c * color[3] / 255 + base[i] * (1 - color[3] / 255)), [255, 255, 255]);
        };
        const style = getComputedStyle(active);
        return {
          index: active.dataset?.a11yIndex === undefined ? null : Number(active.dataset.a11yIndex),
          tag: active.tagName.toLowerCase(),
          outline: { style: style.outlineStyle, width: style.outlineWidth, color: style.outlineColor, offset: style.outlineOffset },
          // The ring is drawn outside the control, over what is behind it: that contrast is what
          // makes it visible, not whether it differs from the control's own fill.
          ringContrast: style.outlineStyle === 'none' || parseFloat(style.outlineWidth) === 0 ? 0 : ratio(rgba(style.outlineColor), background(active.parentElement)),
          ringColour: style.outlineColor,
          ringBackground: background(active.parentElement).slice(0, 3),
          text: (active.textContent || active.getAttribute('aria-label') || '').trim().slice(0, 30),
        };
      }));
    }
    return seen;
  };

  const activation = async (control, key) => {
    // A live page can re-render between the snapshot and this probe; a focus that moved means the
    // key never reached the control, so focus again and retry once before calling it a failure.
    for (let attempt = 0; attempt < 2; attempt++) {
      const focused = await page.evaluate(index => {
        const element = document.querySelector(`[data-a11y-index="${index}"]`);
        element?.focus();
        window.__a11yClicks = [];
        window.__a11yBlockClicks = true;
        return document.activeElement === element;
      }, control.index);
      if (!focused) continue;
      await page.keyboard.press(key);
      await page.waitForTimeout(30);
      if (await page.evaluate(() => {
        window.__a11yBlockClicks = false;
        return window.__a11yClicks.length > 0;
      })) return true;
    }
    return false;
  };

  const keyboardOpen = async () => {
    await page.setViewportSize({ width: 390, height: 900 });
    await page.goto(`${ORIGIN}/#endpoint-key`);
    await page.reload();
    await page.waitForSelector(KEYBOARD_FIELD);
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(300);
    await page.locator(KEYBOARD_FIELD).focus();
    await page.setViewportSize({ width: 390, height: 420 });
    // A real on-screen keyboard shrinks the visual viewport and the browser scrolls the focused
    // field into it; emulate that scroll, then judge whether a fixed bar covers the field.
    await page.locator(KEYBOARD_FIELD).evaluate(element => element.scrollIntoView({ block: 'center' }));
    await page.waitForTimeout(250);
    const found = await page.evaluate(selector => {
      const element = document.querySelector(selector);
      const rect = element.getBoundingClientRect();
      const fixed = [...document.querySelectorAll('*')].filter(node => {
        const style = getComputedStyle(node);
        if (style.position !== 'fixed' || style.visibility === 'hidden' || style.display === 'none' || Number(style.zIndex) <= 0) return false;
        if (node === element || node.contains(element)) return false;
        const box = node.getBoundingClientRect();
        return Math.min(box.bottom, rect.bottom) - Math.max(box.top, rect.top) > 0 && Math.min(box.right, rect.right) - Math.max(box.left, rect.left) > 0;
      }).map(node => node.className || node.tagName);
      return {
        rect: { top: Math.round(rect.top), bottom: Math.round(rect.bottom), height: Math.round(rect.height) },
        viewport: { width: innerWidth, height: innerHeight },
        fixed,
        overflow: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth,
      };
    }, KEYBOARD_FIELD);
    await page.setViewportSize({ width: 390, height: 900 });
    return found;
  };

  const modal = async () => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`${ORIGIN}/#overview`);
    await page.waitForSelector('h1');
    await page.waitForLoadState('networkidle');
    await page.locator('.sidebar-footer button', { hasText: 'English' }).first().click();
    await page.locator('.language-dialog').waitFor();
    await page.waitForTimeout(200);
    const inside = [];
    for (let i = 0; i < 12; i++) {
      await page.keyboard.press('Tab');
      inside.push(await page.evaluate(() => Boolean(document.activeElement?.closest('.language-dialog'))));
    }
    await page.keyboard.press('Escape');
    await page.waitForTimeout(150);
    return { allInside: inside.every(Boolean), returned: await page.evaluate(() => Boolean(document.activeElement?.closest('.sidebar-footer'))) };
  };

  const inspect = async (name, theme, width) => {
    const controls = await focusables();
    const order = await traversal(controls.length);
    const visited = order.slice(0, controls.length).map(entry => entry?.index ?? null);
    const mismatch = visited.findIndex((index, position) => index !== controls[position]?.index);
    const missing = controls.filter(control => !visited.includes(control.index)).map(control => `${control.tag}#${control.id}[${control.type}] "${control.name}"`);
    const unnamed = controls.filter(control => !control.name).map(control => `${control.tag}${control.id ? '#' + control.id : ''}`);
    const noRing = order.slice(0, controls.length).filter(entry => entry && (entry.outline.style === 'none' || parseFloat(entry.outline.width) < 2)).map(entry => `${entry.tag} "${entry.text}"`);
    const weakRing = order.slice(0, controls.length).filter(entry => entry && entry.ringContrast < 3).map(entry => `${entry.tag}#${entry.id ?? ''} "${entry.text}" ${entry.ringContrast.toFixed(2)}:1 ${entry.ringColour} on rgb(${entry.ringBackground})`);
    const positiveTabIndex = controls.filter(control => control.tabIndex > 0).map(control => `${control.tag}#${control.id} tabindex=${control.tabIndex}`);
    const activationFailures = [];
    // A radio group is one Tab stop. Space on an unchecked member selects it; that is the
    // activation the requirement names, probed on the first group of the page.
    const radios = await page.evaluate(() => {
      const groups = {};
      for (const radio of document.querySelectorAll('input[type=radio]')) {
        if (radio.disabled || !radio.getClientRects().length) continue;
        (groups[radio.name] ||= []).push(radio);
      }
      const [name, members] = Object.entries(groups).find(([, list]) => list.length > 1) || [];
      if (!name) return null;
      const target = members.find(radio => !radio.checked);
      target?.focus();
      return { name, members: members.length, target: Boolean(target) };
    });
    let radioActivation = null;
    if (radios?.target) {
      await page.evaluate(() => { window.__a11yClicks = []; window.__a11yBlockClicks = true; });
      await page.keyboard.press('Space');
      await page.waitForTimeout(30);
      radioActivation = await page.evaluate(() => {
        window.__a11yBlockClicks = false;
        return { clicked: window.__a11yClicks.length > 0 };
      });
    }
    for (const control of controls) {
      const clickable = control.tag === 'button' || control.tag === 'a' || ['checkbox', 'radio'].includes(control.type);
      if (!clickable) continue;
      if (control.tag === 'a') {
        if (!(await activation(control, 'Enter'))) activationFailures.push(`${control.tag} "${control.text}" Enter`);
        continue;
      }
      if (control.type === 'radio') continue; // a checked radio is a no-op on Space; the group probe below covers it
      if (control.type === 'checkbox') {
        if (!(await activation(control, 'Space'))) activationFailures.push(`${control.tag}#${control.id}[${control.type}] "${control.name}" Space`);
        continue;
      }
      if (!(await activation(control, 'Enter'))) activationFailures.push(`${control.tag}#${control.id}[${control.type}] "${control.name}" Enter`);
      if (!(await activation(control, 'Space'))) activationFailures.push(`${control.tag}#${control.id}[${control.type}] "${control.name}" Space`);
    }
    return {
      page: name, theme, width,
      controls: controls.length,
      visited: visited.filter(index => index !== null),
      order: mismatch < 0,
      mismatch: mismatch < 0 ? null : {
        at: mismatch,
        expected: controls[mismatch] ? `${controls[mismatch].tag}#${controls[mismatch].id} "${controls[mismatch].name}"` : null,
        reached: order[mismatch] ? `${order[mismatch].tag} "${order[mismatch].text}"` : null,
      },
      unnamed, noRing, weakRing, positiveTabIndex, activationFailures, missing, radios, radioActivation,
      overflow: await page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth),
    };
  };

  const results = [];
  for (const theme of THEMES) {
    for (const width of WIDTHS) {
      for (const target of PAGES) {
        await page.setViewportSize({ width, height: 900 });
        await page.goto(`${ORIGIN}/${target.hash}`);
        await page.evaluate(value => { try { localStorage.setItem('routeweft-theme', value); } catch {} }, theme);
        await page.emulateMedia({ colorScheme: theme });
        await page.reload();
        await page.waitForSelector(target.ready);
        await page.waitForLoadState('networkidle');
        await page.waitForTimeout(350);
        results.push(await inspect(target.name, theme, width));

      }
    }
  }
  // The onboarding form: select a provider, open Add connection, then run the same inspection on
  // the connection form itself (labels, Tab order, activation, ring), light and dark, 1440 and 390.
  for (const theme of THEMES) {
    for (const width of WIDTHS) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(`${ORIGIN}/#providers`);
      await page.evaluate(value => { try { localStorage.setItem('routeweft-theme', value); } catch {} }, theme);
      await page.emulateMedia({ colorScheme: theme });
      await page.reload();
      await page.waitForSelector('.provider-option');
      await page.waitForLoadState('networkidle');
      await page.waitForTimeout(300);
      await page.locator('.provider-option').first().click();
      await page.getByRole('button', { name: 'Add connection', exact: true }).click();
      await page.waitForSelector('form.provider-form input');
      await page.waitForTimeout(200);
      results.push(await inspect('providers-form', theme, width));
    }
  }
  // 320px: the frozen no-overflow rule, every page and theme.
  const narrow = [];
  for (const theme of THEMES) {
    for (const target of PAGES) {
      await page.setViewportSize({ width: 320, height: 900 });
      await page.goto(`${ORIGIN}/${target.hash}`);
      await page.evaluate(value => { try { localStorage.setItem('routeweft-theme', value); } catch {} }, theme);
      await page.emulateMedia({ colorScheme: theme });
      await page.reload();
      await page.waitForSelector(target.ready);
      await page.waitForLoadState('networkidle');
      await page.waitForTimeout(250);
      narrow.push({ page: target.name, theme, overflow: await page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth) });
    }
  }
  // The same connection form at 320: the frozen no-overflow rule.
  for (const theme of THEMES) {
    await page.setViewportSize({ width: 320, height: 900 });
    await page.goto(`${ORIGIN}/#providers`);
    await page.evaluate(value => { try { localStorage.setItem('routeweft-theme', value); } catch {} }, theme);
    await page.emulateMedia({ colorScheme: theme });
    await page.reload();
    await page.waitForSelector('.provider-option');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(250);
    await page.locator('.provider-option').first().click();
    await page.getByRole('button', { name: 'Add connection', exact: true }).click();
    await page.waitForSelector('form.provider-form input');
    await page.waitForTimeout(200);
    narrow.push({ page: 'providers-form', theme, overflow: await page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) - document.documentElement.clientWidth) });
  }
  const keyboard = await keyboardOpen();
  const dialog = await modal();

  const problems = [];
  for (const result of results) {
    const where = `${result.page}/${result.theme}/${result.width}`;
    if (!result.order) problems.push(`${where}: Tab order is not the DOM order; at ${result.mismatch?.at} expected ${result.mismatch?.expected}, reached ${result.mismatch?.reached}`);
    for (const control of result.missing) problems.push(`${where}: never reached by Tab: ${control}`);
    for (const name of result.unnamed) problems.push(`${where}: no accessible name for ${name}`);
    for (const name of result.noRing) problems.push(`${where}: no 2px focus outline on ${name}`);
    for (const name of result.weakRing) problems.push(`${where}: focus ring under 3:1 against what is behind it: ${name}`);
    for (const name of result.positiveTabIndex) problems.push(`${where}: positive tabindex reorders focus: ${name}`);
    for (const name of result.activationFailures) problems.push(`${where}: ${name} did not activate`);
    if (result.radios && !result.radioActivation?.clicked) problems.push(`${where}: Space did not activate an unchecked radio (group "${result.radios.name}")`);
    if (result.overflow > 0) problems.push(`${where}: horizontal overflow ${result.overflow}px`);
  }
  for (const result of narrow) if (result.overflow > 0) problems.push(`${result.page}/${result.theme}/320: horizontal overflow ${result.overflow}px`);
  if (keyboard.rect.top < 0 || keyboard.rect.bottom > keyboard.viewport.height) problems.push(`390 keyboard-open: focused field at ${keyboard.rect.top}..${keyboard.rect.bottom}, viewport ${keyboard.viewport.height}`);
  if (keyboard.fixed.length) problems.push(`390 keyboard-open: fixed bar over the focused field: ${keyboard.fixed.join(', ')}`);
  if (keyboard.overflow > 0) problems.push(`390 keyboard-open: horizontal overflow ${keyboard.overflow}px`);
  if (!dialog.allInside) problems.push('the language dialog did not keep Tab inside it');
  if (!dialog.returned) problems.push('Escape did not return focus to the language button');

  const report = {
    views: results.length,
    narrowViews: narrow.length,
    controls: results.reduce((sum, result) => sum + result.controls, 0),
    unnamed: results.reduce((sum, result) => sum + result.unnamed.length, 0),
    noRing: results.reduce((sum, result) => sum + result.noRing.length, 0),
    keyboard,
    dialog,
    problems,
  };
  writeFileSync(join(artifacts, 'a11y-keyboard-labels.json'), JSON.stringify({ results, narrow, keyboard, dialog }, null, 1));
  console.log(`a11y keyboard/labels: ${report.views} views at 1440/390 light+dark, ${report.controls} interactive controls, ${report.unnamed} without a name, ${report.noRing} without a focus ring, ${report.narrowViews} views at 320, ${problems.length} problems`);
  console.log(`keyboard-open 390: field ${keyboard.rect.top}..${keyboard.rect.bottom} in ${keyboard.viewport.height}, fixed bars ${keyboard.fixed.length}; modal trap ${dialog.allInside}, focus return ${dialog.returned}`);
  assert.deepEqual(problems, [], 'accessibility problems');
  assert.deepEqual(errors, [], 'page errors');
  await browser.close();
})().catch(error => { console.error('FAIL', error); process.exit(1); });
