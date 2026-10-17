import { useCallback, useEffect, useState } from 'react'

export const PAGE_SIZE = 25

type Page<T> = { items: T[]; page: number; total: number }

/** Fetches every page of a bounded admin list (provider catalog, nodes, pools). */
export async function fetchAll<T>(resource: string, onUnauthorized: () => void): Promise<T[] | null> {
  const items: T[] = []
  for (let page = 1; page <= 1000; page += 1) {
    const response = await fetch(`/admin/v1/${resource}?page=${page}&pageSize=100`, { credentials: 'same-origin' })
    if (response.status === 401) { onUnauthorized(); return null }
    if (!response.ok) throw new Error(`Could not load ${resource}.`)
    const body = await response.json() as Page<T>
    items.push(...body.items)
    if (items.length >= body.total || body.items.length === 0) break
  }
  return items
}

/** One provider-scoped, server-paged list. `reload` changes force a refetch. */
export function usePagedList<T>(resource: string, providerId: string, reload: number, onUnauthorized: () => void) {
  const [page, setPage] = useState(1)
  const [items, setItems] = useState<T[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => { setPage(1) }, [providerId])

  const fetchPage = useCallback(async (signal: AbortSignal) => {
    if (!providerId) { setItems([]); setTotal(0); return }
    setLoading(true); setError('')
    try {
      const query = `providerId=${encodeURIComponent(providerId)}&page=${page}&pageSize=${PAGE_SIZE}`
      const response = await fetch(`/admin/v1/${resource}?${query}`, { credentials: 'same-origin', signal })
      if (response.status === 401) { if (!signal.aborted) onUnauthorized(); return }
      if (!response.ok) throw new Error(`Could not load ${resource}.`)
      const body = await response.json() as Page<T>
      if (signal.aborted) return
      const last = Math.max(1, Math.ceil(body.total / PAGE_SIZE))
      if (page > last) { setPage(last); return }
      setItems(body.items); setTotal(body.total)
    } catch (cause) {
      if (!signal.aborted) setError(cause instanceof Error ? cause.message : `Could not load ${resource}.`)
    } finally { if (!signal.aborted) setLoading(false) }
  }, [resource, providerId, page, onUnauthorized])

  useEffect(() => {
    const controller = new AbortController()
    void fetchPage(controller.signal)
    return () => controller.abort()
  }, [fetchPage, reload])

  return { items, total, page, setPage, loading, error }
}

type PagerProps = { label: string; page: number; total: number; loading?: boolean; onPage: (page: number) => void }

export function Pager({ label, page, total, loading, onPage }: PagerProps) {
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  if (total <= PAGE_SIZE) return null
  return <nav className="pagination" aria-label={`${label} pages`}>
    <button className="button-secondary" type="button" disabled={page <= 1 || loading} onClick={() => onPage(page - 1)}>Previous</button>
    <span>Page {page} of {pages} · {total.toLocaleString()} rows</span>
    <button className="button-secondary" type="button" disabled={page >= pages || loading} onClick={() => onPage(page + 1)}>Next</button>
  </nav>
}
