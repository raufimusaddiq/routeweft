// Read-only helper diagnostics for the Token Saver page (#276). The #93 API
// reports a probe outcome and PXPIPE's process-local counters and tail log;
// these helpers only shape that response for display. No React, so unit-tested.
import type { Tone } from './format'

export type HelperHealth = { healthy: boolean; class?: string; message?: string; statusCode?: number; latencyMs: number }
export type HeadroomStatus = { enabled: boolean; url: string; health: HelperHealth }
export type PXPIPEStatus = { enabled: boolean; url: string; health: HelperHealth }
export type PXPIPEStats = { attempts: number; succeeded: number; failed: number; skipped: number }
export type PXPIPELog = {
  step: string
  status: string
  skipped: boolean
  attempted: boolean
  succeeded: boolean
  inputChars: number
  outputChars: number
  durationMs: number
  error?: string
}

export type LogState = { tone: Tone; label: string }

// The API reports healthy true/false plus a failure class. When no URL is
// configured that is a statement about configuration, not a failed probe.
export function helperState(health: HelperHealth): { tone: Tone; label: string } {
  if (health.healthy) return { tone: 'good', label: 'Available' }
  if (health.class === 'not_configured') return { tone: 'idle', label: 'Not configured' }
  return { tone: 'bad', label: 'Unavailable' }
}

// The API's own reason, verbatim; bad_status names its code in a separate field.
export function helperReason(health: HelperHealth): string {
  if (health.healthy) return `Probe answered in ${health.latencyMs} ms.`
  if (!health.message) return 'No reason was reported.'
  return health.statusCode ? `${health.message} (HTTP ${health.statusCode}).` : `${health.message}.`
}

export function logState(entry: PXPIPELog): LogState {
  if (entry.skipped) return { tone: 'idle', label: 'Skipped' }
  if (entry.succeeded) return { tone: 'good', label: 'Succeeded' }
  if (entry.attempted) return { tone: 'bad', label: 'Failed' }
  const label = entry.status ? entry.status[0].toUpperCase() + entry.status.slice(1) : 'Unrecorded'
  return { tone: 'warn', label }
}

// The endpoint returns the tail oldest first; operators read the newest first.
export function newestLogs(logs: PXPIPELog[], limit = 50): PXPIPELog[] {
  return logs.slice(-limit).reverse()
}

export function statsRows(stats: PXPIPEStats): { label: string; value: number }[] {
  return [
    { label: 'Attempts', value: stats.attempts },
    { label: 'Succeeded', value: stats.succeeded },
    { label: 'Failed', value: stats.failed },
    { label: 'Skipped', value: stats.skipped },
  ]
}
