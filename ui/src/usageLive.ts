import type { UsageEvent } from './format'

// A request that finished while the log was open. It is the stream's metadata-only projection of a UsageEvent:
// no id of its own (the database id is assigned later), so identity is the request id.
export type LiveRow = Pick<UsageEvent, 'requestId' | 'status' | 'inputTokens' | 'outputTokens' | 'createdAt'> &
  Partial<Pick<UsageEvent, 'providerId' | 'modelId' | 'connectionId' | 'apiKeyId' | 'durationMs'>>

export const LIVE_CAP = 25

const text = (value: unknown) => (typeof value === 'string' && value ? value : undefined)
const count = (value: unknown) => (typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0)

// Reads one SSE data payload. Anything that is not a well-formed completed request is dropped rather than rendered.
export function parseLiveRow(data: string): LiveRow | null {
  let raw: Record<string, unknown>
  try { raw = JSON.parse(data) as Record<string, unknown> } catch { return null }
  if (!raw || typeof raw !== 'object') return null
  const requestId = text(raw.requestId)
  const createdAt = text(raw.createdAt)
  if (!requestId || !createdAt || typeof raw.status !== 'number' || Number.isNaN(Date.parse(createdAt))) return null
  return {
    requestId, createdAt, status: raw.status, inputTokens: count(raw.inputTokens), outputTokens: count(raw.outputTokens),
    providerId: text(raw.providerId), modelId: text(raw.modelId), connectionId: text(raw.connectionId), apiKeyId: text(raw.apiKeyId),
    durationMs: typeof raw.durationMs === 'number' ? raw.durationMs : undefined,
  }
}

// Newest first, one row per request id (a replayed or duplicated frame replaces rather than adds), bounded.
export function addLive(rows: LiveRow[], row: LiveRow, cap = LIVE_CAP): LiveRow[] {
  return [row, ...rows.filter(existing => existing.requestId !== row.requestId)].slice(0, cap)
}

// Live rows the loaded history does not already contain. After a reconnect or Refresh the same request is in both;
// the recorded row wins, so nothing is shown twice.
export function freshLive(rows: LiveRow[], recorded: { requestId: string }[]): LiveRow[] {
  const known = new Set(recorded.map(item => item.requestId))
  return rows.filter(row => !known.has(row.requestId))
}

export type Connection = 'connecting' | 'live' | 'reconnecting' | 'paused' | 'closed' | 'unsupported' | 'elsewhere'

export const connectionLabel: Record<Connection, string> = {
  connecting: 'Connecting', live: 'Live', reconnecting: 'Reconnecting', paused: 'Paused', closed: 'Stream closed',
  unsupported: 'Live updates unavailable', elsewhere: 'Live on page 1 only',
}
