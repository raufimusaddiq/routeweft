import { useCallback, useEffect, useState } from 'react'
import { fetchAll } from './ProviderPaging'

export type Totals = { requests: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number; errors: number; avgDurationMs?: number; avgTtftMs?: number }
export type Day = { day: string; requests: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number }
export type Breakdown = { key: string; requests: number; inputTokens: number; outputTokens: number; errors: number }
export type Summary = { period: string; since: string; totals: Totals; series: Day[]; providers: Breakdown[]; models: Breakdown[]; statuses: Breakdown[] }
export type UsageEvent = { id: number; requestId: string; status: number; inputTokens: number; outputTokens: number; cacheReadTokens: number; cacheWriteTokens: number; createdAt: string; providerId?: string; modelId?: string; connectionId?: string; apiKeyId?: string; durationMs?: number; ttftMs?: number }
export type Overview = {
  runtime: { health: string; version: string; commit: string; configRevision: number; snapshotVersion: number }
  providers: { catalog: number; nodes: number; enabledConnections: number }
  apiKeys: { active: number }
  traffic: { active: number; requests: number; errors: number; lastRequestAt: string }
  telemetry: { enabled: boolean; written: number; lost: number; lostDiagnostics: number; degraded: boolean }
}
export type Provider = { id: string; transports: string[]; auth: string; authModes: string[]; modelCatalog: string; configuredNodes: number; enabledAccounts: number }
export type Connection = { id: string; nodeId: string; providerId: string; name: string; authKind: string; identity: string; enabled: boolean; priority: number; credentialConfigured: boolean }

export type Data = {
  overview: Overview | null
  week: Summary | null
  prevWeek: Totals | null
  month: Summary | null
  day: Summary | null
  events: UsageEvent[]
  providers: Provider[]
  connections: Connection[]
  loading: boolean
  error: string
  reload: () => void
}

async function json<T>(url: string, onUnauthorized: () => void): Promise<T | null> {
  const response = await fetch(url, { credentials: 'same-origin' })
  if (response.status === 401) { onUnauthorized(); return null }
  if (!response.ok) throw new Error('Request failed')
  return await response.json() as T
}

export function useData(onUnauthorized: () => void): Data {
  const [state, setState] = useState<Omit<Data, 'reload'>>({ overview: null, week: null, prevWeek: null, month: null, day: null, events: [], providers: [], connections: [], loading: true, error: '' })
  const load = useCallback(async () => {
    setState(current => ({ ...current, loading: true, error: '' }))
    try {
      const [overview, week, month, day, events, providers, connections] = await Promise.all([
        json<Overview>('/admin/v1/overview', onUnauthorized),
        json<Summary>('/admin/v1/usage/summary?period=7d', onUnauthorized),
        json<Summary>('/admin/v1/usage/summary?period=30d', onUnauthorized),
        json<Summary>('/admin/v1/usage/summary?period=24h', onUnauthorized),
        json<{ items: UsageEvent[] }>('/admin/v1/usage?page=1&pageSize=50', onUnauthorized),
        fetchAll<Provider>('providers', onUnauthorized),
        fetchAll<Connection>('connections', onUnauthorized),
      ])
      setState({ overview, week, prevWeek: priorWeek(month), month, day, events: events?.items ?? [], providers: providers ?? [], connections: connections ?? [], loading: false, error: '' })
    } catch (cause) {
      setState(current => ({ ...current, loading: false, error: cause instanceof Error ? cause.message : 'Could not load data.' }))
    }
  }, [onUnauthorized])
  useEffect(() => { void load() }, [load])
  return { ...state, reload: () => void load() }
}

const nf = new Intl.NumberFormat('en-US')
export const num = (value: number) => nf.format(Math.round(value))
export function compact(value: number) {
  const abs = Math.abs(value)
  if (abs >= 1e9) return `${(value / 1e9).toFixed(abs >= 1e10 ? 0 : 1)}B`
  if (abs >= 1e6) return `${(value / 1e6).toFixed(abs >= 1e7 ? 0 : 1)}M`
  if (abs >= 1e4) return `${(value / 1e3).toFixed(0)}k`
  return nf.format(Math.round(value))
}
export const pct = (value: number, digits = 1) => `${value.toFixed(digits)}%`
export const rate = (errors: number, requests: number) => (requests > 0 ? (errors / requests) * 100 : 0)
export function delta(now: number, before: number | undefined) {
  if (before === undefined || before === 0) return null
  return ((now - before) / before) * 100
}
export function ago(iso: string, now = Date.now()) {
  if (!iso) return 'never'
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  if (s < 90) return `${s} s ago`
  if (s < 5400) return `${Math.round(s / 60)} min ago`
  if (s < 129600) return `${Math.round(s / 3600)} h ago`
  return `${Math.round(s / 86400)} d ago`
}
export function clock(iso: string) {
  const d = new Date(iso)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
}
export function statusClass(status: number): 'good' | 'warn' | 'bad' {
  if (status >= 200 && status < 300) return 'good'
  if (status >= 500) return 'bad'
  return 'warn'
}
export function providerTraffic(summary: Summary | null) {
  const map = new Map<string, Breakdown>()
  summary?.providers.forEach(item => map.set(item.key, item))
  return map
}

// Totals for the 7 days before the latest 7, from the daily series (the summary API has no date-range totals).
function priorWeek(month: Summary | null): Totals | null {
  const series = month?.series ?? []
  if (series.length < 14) return null
  const slice = series.slice(-14, -7)
  const sum = (key: keyof Day) => slice.reduce((total, day) => total + (day[key] as number), 0)
  return { requests: sum('requests'), inputTokens: sum('inputTokens'), outputTokens: sum('outputTokens'), cacheReadTokens: sum('cacheReadTokens'), cacheWriteTokens: sum('cacheWriteTokens'), errors: 0 }
}
