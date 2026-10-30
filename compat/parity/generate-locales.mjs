// Records what the oracle serves for the display language (#279, U10): the supported
// locale list and its display names and flags, the oracle's own normalisation of a
// locale value, and the per-locale literal catalogs the dashboard's runtime translator
// reads. The catalogs are copied byte for byte to ui/public/i18n/literals/ (Vite
// serves them from the UI root, as the oracle serves public/i18n/literals/); the rest
// is written to ui/src/localePin.json, which ui/src/locale.test.ts replays against
// ui/src/locale.ts. Nothing here contacts a provider or runs the oracle's server.
//
//   eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"
//   node compat/parity/generate-locales.mjs           # rewrite the pin and the catalogs
//   node compat/parity/generate-locales.mjs --check   # fail if the oracle now differs
//
// --check ignores the oracle commit recorded in the pin, so LiteRouter moving does
// not force a regeneration unless the locale list, a name, a flag, a normalisation
// result or a catalog byte actually changed.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { git, loadTree, pin, source } from './oracle.mjs';

const root = new URL('../../', import.meta.url);
const pinPath = new URL('ui/src/localePin.json', root);
const catalogDir = new URL('ui/public/i18n/literals/', root);
const check = process.argv.includes('--check');

const loaded = await loadTree('src/i18n/config.js');
// loadTree runs the module in its own VM realm; arrays from it are copied so deepEqual compares like with like.
const config = { ...loaded, LOCALES: [...loaded.LOCALES] };
const flags = (await loadTree('src/shared/constants/locales.js')).LOCALE_FLAGS;

// Display names live inside LanguageSwitcher's getLocaleInfo table: `"vi": { name: "Tiếng Việt", flag: "🇻🇳" }`.
const switcher = source('src/shared/components/LanguageSwitcher.js');
const info = {};
for (const [, code, name, flag] of switcher.matchAll(/^\s*"([A-Za-z-]+)":\s*\{\s*name:\s*"([^"]+)",\s*flag:\s*"([^"]+)"\s*\}/gm)) info[code] = { name, flag };
// The grid iterates config.LOCALES (LanguageSwitcher.js:160), so that order is the one shown; the
// display table is keyed, and lists th before km where config lists km first.
assert.deepEqual(Object.keys(info).sort(), [...config.LOCALES].sort(), 'the switcher names exactly the supported locales');
for (const code of config.LOCALES) assert.equal(info[code].flag, flags[code], `${code}: the two flag tables agree`);

// The oracle's own answers for normalizeLocale / isSupportedLocale, over every
// supported value and the shapes a stored cookie or a request body can hold.
const probes = [...config.LOCALES, 'zh', 'ZH', 'en-US', 'pt', 'xx', '', ' en', 'EN', null, undefined, 'zh-cn'];
const normalize = probes.map(value => ({ input: value === undefined ? { undefined: true } : value, normalized: config.normalizeLocale(value), supported: config.isSupportedLocale(value) }));

// Catalogs: every file under public/i18n/literals, with the digest the copy must keep.
const catalogs = {};
const files = git('ls-tree', '-r', '--name-only', pin, 'public/i18n/literals').split('\n').filter(Boolean);
const bytes = {};
for (const path of files) {
  const text = source(path);
  const code = path.replace(/^.*\//, '').replace(/\.json$/, '');
  const map = JSON.parse(text);
  assert.ok(map && typeof map === 'object' && !Array.isArray(map), `${path}: an object`);
  for (const [key, value] of Object.entries(map)) assert.equal(typeof value, 'string', `${path}: ${key} is a string`);
  bytes[code] = text;
  catalogs[code] = { sha256: createHash('sha256').update(text).digest('hex'), entries: Object.keys(map).length };
}
// English has no catalog (runtime.js:23-27); every other supported locale has one.
assert.deepEqual(Object.keys(catalogs).sort(), config.LOCALES.filter(code => code !== config.DEFAULT_LOCALE).sort(), 'a catalog for every locale but English');

const generated = {
  about: 'Pinned from LiteRouter by compat/parity/generate-locales.mjs (#279). src/i18n/config.js, src/shared/components/LanguageSwitcher.js (names), src/shared/constants/locales.js (flags), public/i18n/literals/*.json. Do not edit.',
  oracle: pin,
  defaultLocale: config.DEFAULT_LOCALE,
  cookie: config.LOCALE_COOKIE,
  locales: config.LOCALES,
  info: Object.fromEntries(config.LOCALES.map(code => [code, info[code]])),
  normalize,
  catalogs,
};

const comparable = value => JSON.stringify({ ...value, oracle: undefined }, null, 2) + '\n';
if (check) {
  const current = JSON.parse(readFileSync(pinPath, 'utf8'));
  assert.equal(comparable(current), comparable(generated), 'ui/src/localePin.json is stale: run node compat/parity/generate-locales.mjs');
  const onDisk = readdirSync(catalogDir).filter(name => name.endsWith('.json')).map(name => name.replace(/\.json$/, '')).sort();
  assert.deepEqual(onDisk, Object.keys(bytes).sort(), 'the catalog set differs from the oracle');
  for (const [code, text] of Object.entries(bytes)) assert.equal(readFileSync(new URL(`${code}.json`, catalogDir), 'utf8'), text, `${code}.json differs from the oracle`);
  console.log(`locales ok: ${config.LOCALES.length} locales, ${files.length} catalogs, oracle ${pin}`);
} else {
  mkdirSync(catalogDir, { recursive: true });
  for (const name of readdirSync(catalogDir)) if (name.endsWith('.json')) rmSync(new URL(name, catalogDir));
  for (const [code, text] of Object.entries(bytes)) writeFileSync(new URL(`${code}.json`, catalogDir), text);
  writeFileSync(pinPath, JSON.stringify(generated, null, 2) + '\n');
  console.log(`wrote ${files.length} catalogs and ui/src/localePin.json`);
}
