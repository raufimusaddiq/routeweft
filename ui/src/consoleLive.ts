// The Console Log page's live view (#277, U08). The server pushes the newest lines as they arrive and tells
// every page when the buffer is cleared (console-logs/stream/route.js:28-65 in the oracle: init, line, lines,
// clear). Pure and DOM-free so node can test it.

export type Entry = { id: number; time: string; level: string; message: string; attributes?: Record<string, unknown> }

/** Reads the `data` of a `console.log` event. Anything that is not a well-formed line is dropped, not rendered. */
export function parseConsoleLines(data: unknown): { lines: Entry[]; truncated: boolean } {
  const payload = data && typeof data === 'object' ? data as { lines?: unknown; truncated?: unknown } : {}
  const lines: Entry[] = []
  if (Array.isArray(payload.lines)) {
    for (const raw of payload.lines) {
      if (!raw || typeof raw !== 'object') continue
      const line = raw as Record<string, unknown>
      if (typeof line.id !== 'number' || typeof line.time !== 'string' || typeof line.level !== 'string' || typeof line.message !== 'string') continue
      const attributes = line.attributes && typeof line.attributes === 'object' && !Array.isArray(line.attributes) ? line.attributes as Record<string, unknown> : undefined
      lines.push({ id: line.id, time: line.time, level: line.level, message: line.message, ...(attributes ? { attributes } : {}) })
    }
  }
  return { lines, truncated: payload.truncated === true }
}

/** The server's filter (internal/control/api/logs.go list): level equal ignoring case; query a case-insensitive substring of the message or the attributes. */
export function entryMatches(entry: Entry, level: string, query: string): boolean {
  const wantLevel = level.trim()
  if (wantLevel && entry.level.toLowerCase() !== wantLevel.toLowerCase()) return false
  const needle = query.trim().toLowerCase()
  if (!needle) return true
  if (entry.message.toLowerCase().includes(needle)) return true
  return JSON.stringify(entry.attributes ?? null).toLowerCase().includes(needle)
}

/**
 * Newest first, one row per id, bounded to the page size. `added` counts the lines that match the filter and were not
 * already shown, so the total can follow without a request.
 */
export function mergeLive(items: Entry[], incoming: Entry[], level: string, query: string, pageSize: number): { items: Entry[]; added: number } {
  const known = new Set(items.map(item => item.id))
  const fresh = incoming.filter(line => !known.has(line.id) && entryMatches(line, level, query))
  if (fresh.length === 0) return { items, added: 0 }
  const merged = [...fresh, ...items].sort((a, b) => b.id - a.id).slice(0, pageSize)
  return { items: merged, added: fresh.length }
}
