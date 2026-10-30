import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { DEFAULT_LOCALE, LOCALES, LOCALE_COOKIE, LOCALE_INFO, LOCALE_MAX_AGE_SECONDS, SKIPPED_TAGS, isSupportedLocale, localeCookie, normalizeLocale, readLocaleCookie, translateText } from './locale.ts'

type Pin = {
  defaultLocale: string; cookie: string; locales: string[]
  info: Record<string, { name: string; flag: string }>
  normalize: Array<{ input: unknown; normalized: string; supported: boolean }>
  catalogs: Record<string, { sha256: string; entries: number }>
}
const pin = JSON.parse(readFileSync(new URL('./localePin.json', import.meta.url), 'utf8')) as Pin
const catalogUrl = (code: string) => new URL(`../public/i18n/literals/${code}.json`, import.meta.url)

test('the supported locales, their order, names and flags are the oracle\'s (U10)', () => {
  assert.deepEqual([...LOCALES], pin.locales)
  assert.equal(LOCALES.length, 35)
  assert.deepEqual(LOCALE_INFO, pin.info)
  assert.equal(DEFAULT_LOCALE, pin.defaultLocale)
  assert.equal(LOCALE_COOKIE, pin.cookie)
  assert.equal(new Set(Object.values(LOCALE_INFO).map(info => info.name)).size, 35, 'each language has its own name')
})

test('normalizeLocale and isSupportedLocale answer as the oracle does for every probed value', () => {
  for (const probe of pin.normalize) {
    const input = typeof probe.input === 'object' && probe.input !== null ? undefined : probe.input
    assert.equal(normalizeLocale(input), probe.normalized, `normalizeLocale(${JSON.stringify(probe.input)})`)
    assert.equal(isSupportedLocale(input), probe.supported, `isSupportedLocale(${JSON.stringify(probe.input)})`)
  }
  // The shapes that matter, spelled out: "zh" is Simplified but not itself a supported value; case matters.
  assert.equal(normalizeLocale('zh'), 'zh-CN')
  assert.equal(isSupportedLocale('zh'), false)
  assert.equal(normalizeLocale('EN'), 'en')
  assert.equal(normalizeLocale('pt'), 'en')
})

test('every locale but English has a catalog, byte-identical to the pinned one', () => {
  for (const code of LOCALES) {
    if (code === DEFAULT_LOCALE) { assert.equal(pin.catalogs[code], undefined, 'English has no catalog'); continue }
    const text = readFileSync(catalogUrl(code), 'utf8')
    const catalog = JSON.parse(text) as Record<string, unknown>
    assert.equal(createHash('sha256').update(text).digest('hex'), pin.catalogs[code].sha256, `${code}.json changed`)
    assert.equal(Object.keys(catalog).length, pin.catalogs[code].entries, `${code}: entries`)
    assert.ok(Object.values(catalog).every(value => typeof value === 'string' && value !== ''), `${code}: every translation is a non-empty string`)
  }
})

test('the saved choice is read back from the cookie the way the oracle reads it', () => {
  assert.equal(readLocaleCookie(''), 'en')
  assert.equal(readLocaleCookie('theme=dark; locale=vi; other=1'), 'vi')
  assert.equal(readLocaleCookie('locale=zh-TW'), 'zh-TW')
  assert.equal(readLocaleCookie('locale=zh'), 'zh-CN')
  assert.equal(readLocaleCookie('locale=pt-BR%20'), 'en', 'an unknown value is English')
  assert.equal(readLocaleCookie('locale=%E0%A4%A'), 'en', 'a malformed escape is English, not an error')
  assert.equal(readLocaleCookie('xlocale=vi'), 'en', 'only the locale cookie counts')
  assert.equal(readLocaleCookie('  locale=ja  '), 'ja')
})

test('the cookie is the oracle\'s: name locale, path /, one year; Lax, and Secure only over https', () => {
  assert.equal(LOCALE_MAX_AGE_SECONDS, 31536000)
  assert.equal(localeCookie('vi', false), 'locale=vi; Path=/; Max-Age=31536000; SameSite=Lax')
  assert.equal(localeCookie('pt-BR', true), 'locale=pt-BR; Path=/; Max-Age=31536000; SameSite=Lax; Secure')
  for (const code of LOCALES) assert.equal(readLocaleCookie(localeCookie(code, false).split(';')[0]), code, `${code} round-trips`)
})

test('a text is translated by its trimmed English; no entry leaves it as it is', () => {
  const catalog = { Settings: 'Cài đặt', accounts: 'tài khoản' }
  assert.equal(translateText('Settings', catalog), 'Cài đặt')
  assert.equal(translateText('  Settings\n', catalog), '  Cài đặt\n', 'the surrounding whitespace is kept')
  assert.equal(translateText(' accounts', catalog), ' tài khoản', 'a count next to its noun does not run together')
  assert.equal(translateText('Not in the catalog', catalog), 'Not in the catalog')
  assert.equal(translateText('settings', catalog), 'settings', 'matching is exact, including case')
  assert.equal(translateText('   ', catalog), '   ')
  assert.equal(translateText('', catalog), '')
})

test('a text that names a property of Object is not looked up there', () => {
  const catalog = { Settings: 'Cài đặt' }
  for (const word of ['constructor', 'toString', 'hasOwnProperty', '__proto__', 'valueOf']) assert.equal(translateText(word, catalog), word)
})

test('the elements whose text is never translated are the oracle\'s (runtime.js:83-89)', () => {
  assert.deepEqual([...SKIPPED_TAGS].sort(), ['code', 'colgroup', 'datalist', 'optgroup', 'pre', 'script', 'select', 'style', 'table', 'tbody', 'tfoot', 'thead', 'tr'])
})
