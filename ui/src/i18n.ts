// The dashboard's display language at runtime (#279, U10), after the oracle's
// src/i18n/runtime.js: the page's text nodes are translated in place from a per-locale
// literal catalog (/i18n/literals/<locale>.json), keyed by the exact English text. A text with no
// entry stays English; English needs no catalog. The choice is kept in the `locale` cookie,
// which only this code reads (the oracle's server never does).
//
// Where this differs from the oracle, deliberately:
//  - a node whose text the app changed after it was translated (a React re-render) is taken
//    as new English text. The oracle keeps the first text it saw and would put it back;
//  - the whitespace around a translated text is kept (see translateText);
//  - the choice is saved only once its catalog has loaded, so the saved choice and what is
//    shown never disagree; a failed load leaves the previous language and says so;
//  - <html lang> follows the choice (the oracle leaves it "en").
import { DEFAULT_LOCALE, SKIPPED_TAGS, localeCookie, readLocaleCookie, translateText, type Catalog, type Locale } from './locale'

type NodeState = { original: string; translated: string }

const states = new WeakMap<Text, NodeState>()
const listeners = new Set<() => void>()
let current: Locale = DEFAULT_LOCALE
let catalog: Catalog = {}
let started = false
let attempt = 0

export function getLocale(): Locale { return current }

export function subscribeLocale(listener: () => void): () => void {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

function notify() { for (const listener of [...listeners]) listener() }

async function loadCatalog(locale: Locale): Promise<Catalog> {
  if (locale === DEFAULT_LOCALE) return {}
  const response = await fetch(`/i18n/literals/${locale}.json`, { credentials: 'same-origin' })
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  const body: unknown = await response.json()
  if (!body || typeof body !== 'object' || Array.isArray(body)) throw new Error('not a catalog')
  return body as Catalog
}

function skipped(node: Text): boolean {
  const parent = node.parentElement
  if (!parent) return true
  if (SKIPPED_TAGS.has(parent.tagName.toLowerCase())) return true
  return parent.closest('[data-i18n-skip]') !== null
}

function processTextNode(node: Text) {
  const value = node.nodeValue
  if (value === null || skipped(node)) return
  const state = states.get(node)
  // Our own earlier write, or text the app has set since: the latter is the new English.
  const original = state && value === state.translated ? state.original : value
  if (!original.trim() && !state) return
  const translated = current === DEFAULT_LOCALE ? original : translateText(original, catalog)
  if (translated !== value) node.nodeValue = translated
  if (translated !== original || state) states.set(node, { original, translated })
}

function processElement(root: Node) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  for (let node = walker.nextNode(); node; node = walker.nextNode()) nodes.push(node as Text)
  nodes.forEach(processTextNode)
}

function reprocess() { if (document.body) processElement(document.body) }

/** Start translating the page in the saved language. Safe to call more than once. */
export function initI18n() {
  if (started || typeof document === 'undefined') return
  started = true
  current = readLocaleCookie(document.cookie)
  document.documentElement.lang = current
  const ready = loadCatalog(current).then(loaded => { catalog = loaded }, () => { catalog = {} })
  let loaded = current === DEFAULT_LOCALE
  void ready.then(() => { loaded = true; reprocess(); notify() })
  const observer = new MutationObserver(mutations => {
    if (!loaded) return
    for (const mutation of mutations) {
      if (mutation.type === 'characterData') {
        if (mutation.target.nodeType === Node.TEXT_NODE) processTextNode(mutation.target as Text)
        continue
      }
      mutation.addedNodes.forEach(added => {
        if (added.nodeType === Node.TEXT_NODE) processTextNode(added as Text)
        else if (added.nodeType === Node.ELEMENT_NODE) processElement(added)
      })
    }
  })
  observer.observe(document.body, { childList: true, subtree: true, characterData: true })
  // The oracle re-processes the page after every route change; the routes here are hash routes.
  addEventListener('hashchange', () => requestAnimationFrame(() => requestAnimationFrame(reprocess)))
}

/**
 * Switch the display language. The catalog is loaded first; only then is the choice saved
 * in the cookie, the page translated (or restored to English) and the listeners told.
 * Rejects, changing nothing, when the catalog cannot be loaded.
 */
export async function setLocale(next: Locale): Promise<void> {
  if (next === current) return
  const mine = ++attempt
  const loaded = await loadCatalog(next)
  if (mine !== attempt) return // a later choice superseded this one
  catalog = loaded
  current = next
  document.cookie = localeCookie(next, location.protocol === 'https:')
  document.documentElement.lang = next
  reprocess()
  notify()
}
