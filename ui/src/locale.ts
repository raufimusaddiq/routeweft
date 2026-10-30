// The display language (#279, U10). The locale list, its order, the display names and
// flags, and normalizeLocale follow the oracle (LiteRouter src/i18n/config.js:1-152,
// src/shared/components/LanguageSwitcher.js:18-57, src/shared/constants/locales.js);
// locale.test.ts replays them against localePin.json, which
// compat/parity/generate-locales.mjs writes from the oracle. Pure and DOM-free so
// node can test it; i18n.ts is the part that touches the document.

export const DEFAULT_LOCALE = 'en'
/** The cookie that holds the choice (config.js:39); the oracle's server never reads it, only the browser does. */
export const LOCALE_COOKIE = 'locale'
/** One year, as the oracle sets it (api/locale/route.js:20). */
export const LOCALE_MAX_AGE_SECONDS = 60 * 60 * 24 * 365

export const LOCALES = ['en', 'vi', 'zh-CN', 'zh-TW', 'ja', 'pt-BR', 'pt-PT', 'ko', 'es', 'de', 'fr', 'he', 'ar', 'ru', 'pl', 'cs', 'nl', 'tr', 'uk', 'tl', 'id', 'km', 'th', 'hi', 'bn', 'ur', 'ro', 'sv', 'it', 'el', 'hu', 'fi', 'da', 'no', 'fa'] as const
export type Locale = (typeof LOCALES)[number]

export const LOCALE_INFO: Record<Locale, { name: string; flag: string }> = {
  'en': { name: 'English', flag: '🇺🇸' },
  'vi': { name: 'Tiếng Việt', flag: '🇻🇳' },
  'zh-CN': { name: '简体中文', flag: '🇨🇳' },
  'zh-TW': { name: '繁體中文', flag: '🇹🇼' },
  'ja': { name: '日本語', flag: '🇯🇵' },
  'pt-BR': { name: 'Português (Brasil)', flag: '🇧🇷' },
  'pt-PT': { name: 'Português (Portugal)', flag: '🇵🇹' },
  'ko': { name: '한국어', flag: '🇰🇷' },
  'es': { name: 'Español', flag: '🇪🇸' },
  'de': { name: 'Deutsch', flag: '🇩🇪' },
  'fr': { name: 'Français', flag: '🇫🇷' },
  'he': { name: 'עברית', flag: '🇮🇱' },
  'ar': { name: 'العربية', flag: '🇸🇦' },
  'ru': { name: 'Русский', flag: '🇷🇺' },
  'pl': { name: 'Polski', flag: '🇵🇱' },
  'cs': { name: 'Čeština', flag: '🇨🇿' },
  'nl': { name: 'Nederlands', flag: '🇳🇱' },
  'tr': { name: 'Türkçe', flag: '🇹🇷' },
  'uk': { name: 'Українська', flag: '🇺🇦' },
  'tl': { name: 'Tagalog', flag: '🇵🇭' },
  'id': { name: 'Indonesia', flag: '🇮🇩' },
  'km': { name: 'ខ្មែរ', flag: '🇰🇭' },
  'th': { name: 'ไทย', flag: '🇹🇭' },
  'hi': { name: 'हिन्दी', flag: '🇮🇳' },
  'bn': { name: 'বাংলা', flag: '🇧🇩' },
  'ur': { name: 'اردو', flag: '🇵🇰' },
  'ro': { name: 'Română', flag: '🇷🇴' },
  'sv': { name: 'Svenska', flag: '🇸🇪' },
  'it': { name: 'Italiano', flag: '🇮🇹' },
  'el': { name: 'Ελληνικά', flag: '🇬🇷' },
  'hu': { name: 'Magyar', flag: '🇭🇺' },
  'fi': { name: 'Suomi', flag: '🇫🇮' },
  'da': { name: 'Dansk', flag: '🇩🇰' },
  'no': { name: 'Norsk', flag: '🇳🇴' },
  'fa': { name: 'فارسی', flag: '🇮🇷' },
}

export function isSupportedLocale(value: unknown): value is Locale {
  return typeof value === 'string' && (LOCALES as readonly string[]).includes(value)
}

/** config.js:41-148: "zh" is Simplified; a supported value is itself; anything else is English. Case-sensitive. */
export function normalizeLocale(value: unknown): Locale {
  if (value === 'zh') return 'zh-CN'
  return isSupportedLocale(value) ? value : DEFAULT_LOCALE
}

/** runtime.js:12-19: the first `locale=` cookie, percent-decoded, normalised. A malformed value is English. */
export function readLocaleCookie(cookieString: string): Locale {
  const found = cookieString.split(';').map(part => part.trim()).find(part => part.startsWith(`${LOCALE_COOKIE}=`))
  if (!found) return DEFAULT_LOCALE
  try {
    return normalizeLocale(decodeURIComponent(found.slice(LOCALE_COOKIE.length + 1)))
  } catch {
    return DEFAULT_LOCALE
  }
}

/** The cookie the choice is stored in: path /, one year, as the oracle's route sets it; Lax, and Secure on https. */
export function localeCookie(locale: Locale, secure: boolean): string {
  return `${LOCALE_COOKIE}=${encodeURIComponent(locale)}; Path=/; Max-Age=${LOCALE_MAX_AGE_SECONDS}; SameSite=Lax${secure ? '; Secure' : ''}`
}

export type Catalog = Record<string, string>

/**
 * runtime.js:42-48. The catalog is keyed by the exact trimmed English text; a text with no entry
 * is left as it is. Unlike the oracle, the whitespace around the text is kept, so
 * " accounts" next to a count translates to " <translation>" and the words do not run together.
 */
export function translateText(text: string, catalog: Catalog): string {
  const trimmed = text.trim()
  if (!trimmed) return text
  const entry = Object.prototype.hasOwnProperty.call(catalog, trimmed) ? catalog[trimmed] : undefined
  if (!entry) return text
  const start = text.indexOf(trimmed)
  return text.slice(0, start) + entry + text.slice(start + trimmed.length)
}

/** runtime.js:83-89: text under these elements is not translated. */
export const SKIPPED_TAGS: ReadonlySet<string> = new Set([
  'script', 'style', 'code', 'pre', 'colgroup', 'table', 'thead', 'tbody', 'tfoot', 'tr', 'select', 'datalist', 'optgroup',
])
