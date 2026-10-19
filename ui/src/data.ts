import { useCallback, useEffect, useRef, useState } from 'react'
import { fetchAll } from './ProviderPaging'
import type { Summary, UsageEvent } from './format'

export type Overview = {
  runtime: { health: string; version: string; commit: string; configRevision: number; snapshotVersion: number }
  providers: { catalog: number; nodes: number; enabledConnections: number }
  apiKeys: { active: number }
  traffic: { active: number; requests: number; errors: number; lastRequestAt: string }
  telemetry: { enabled: boolean; written: number; lost: number; lostDiagnostics: number; degraded: boolean }
}
export type Connection = { id: string; providerId: string; name: string; enabled: boolean; priority: number }

export type OverviewData = {
  overview: Overview | null
  week: Summary | null
  month: Summary | null
  events: UsageEvent[]
  connections: Connection[]
  loading: boolean
  error: string
  updatedAt: string
  reload: () => void
}

// The overview itself is required. Everything that only enriches it (usage history, the request log,
// the connection list) is fail-open: if one of those fails the page still renders what it has.
async function optional<T>(url: string, signal: AbortSignal, onUnauthorized: () => void): Promise<T | null> {
  try {
    const response = await fetch(url, { credentials: 'same-origin', signal })
    if (response.status === 401) { onUnauthorized(); return null }
    return response.ok ? await response.json() as T : null
  } catch { return null }
}

export function useOverviewData(onUnauthorized: () => void): OverviewData {
  const [state, setState] = useState<Omit<OverviewData, 'reload'>>({ overview: null, week: null, month: null, events: [], connections: [], loading: true, error: '', updatedAt: '' })
  const controller = useRef<AbortController | null>(null)

  const load = useCallback(async () => {
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    const { signal } = abort
    setState(current => ({ ...current, loading: true, error: '' }))
    try {
      const response = await fetch('/admin/v1/overview', { credentials: 'same-origin', signal })
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error('Overview is temporarily unavailable. Try again.')
      const overview = await response.json() as Overview
      setState(current => ({ ...current, overview, updatedAt: new Date().toLocaleTimeString() }))
      const [week, month, events, connections] = await Promise.all([
        optional<Summary>('/admin/v1/usage/summary?period=7d', signal, onUnauthorized),
        optional<Summary>('/admin/v1/usage/summary?period=30d', signal, onUnauthorized),
        optional<{ items: UsageEvent[] }>('/admin/v1/usage?page=1&pageSize=50', signal, onUnauthorized),
        fetchAll<Connection>('connections', onUnauthorized, '', signal).catch(() => null),
      ])
      if (signal.aborted) return
      setState(current => ({ ...current, week: week ?? current.week, month: month ?? current.month, events: events?.items ?? current.events, connections: connections ?? current.connections }))
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return
      setState(current => ({ ...current, error: cause instanceof Error ? cause.message : 'Overview is temporarily unavailable. Try again.' }))
    } finally {
      if (!signal.aborted) setState(current => ({ ...current, loading: false }))
    }
  }, [onUnauthorized])

  useEffect(() => { void load(); return () => controller.current?.abort() }, [load])
  return { ...state, reload: () => void load() }
}
