import { useCallback, useEffect, useRef, useState } from 'react'
import { EmptyState, ErrorNotice } from './kit'
import { Caption, Dot, Main, Margin, Row, Status } from './editorial'
import { useLiveChannel } from './liveChannel'
import type { Breakdown } from './format'
import { ago } from './format'
import { inFlight, topologyNodes, type UsageActive } from './usageTopology'

type Props = { providers: Breakdown[]; onUnauthorized: () => void }

// Provider topology and activity, the Usage page's live view (#275). It reads the process-memory live
// snapshot (SPEC §21, /admin/v1/usage/active) on open, on every usage.pending event over the shared
// channel, and on a 5 s timer while mounted. Nothing is persisted and no request content is carried.
export function UsageTopology({ providers, onUnauthorized }: Props) {
  const [active, setActive] = useState<UsageActive | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const controller = useRef<AbortController | null>(null)

  const load = useCallback(async () => {
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    const { signal } = abort
    try {
      const response = await fetch('/admin/v1/usage/active', { credentials: 'same-origin', signal })
      if (signal.aborted) return
      if (response.status === 401) { onUnauthorized(); return }
      if (!response.ok) throw new Error('Could not load live activity.')
      const snapshot = await response.json() as UsageActive
      if (signal.aborted) return
      setActive({ activeRequests: snapshot.activeRequests ?? [], recentRequests: snapshot.recentRequests ?? [], errorProvider: snapshot.errorProvider ?? '' })
      setError('')
    } catch (cause) {
      if (signal.aborted || (cause instanceof DOMException && cause.name === 'AbortError')) return
      setError(cause instanceof Error ? cause.message : 'Could not load live activity.')
    } finally { if (!signal.aborted) setLoading(false) }
  }, [onUnauthorized])

  useEffect(() => { void load(); return () => controller.current?.abort() }, [load])
  useEffect(() => {
    const timer = window.setInterval(() => void load(), 5000)
    return () => window.clearInterval(timer)
  }, [load])
  const reload = useCallback(() => void load(), [load])
  useLiveChannel({ types: ['usage.pending'], onEvent: reload, onRefresh: reload })

  const running = active ? inFlight(active.activeRequests) : 0
  const nodes = active ? topologyNodes(active.activeRequests, active.recentRequests, providers, active.errorProvider) : []
  const recent = active?.recentRequests ?? []

  return <Row gap="s" label="Provider topology">
    <Main title="Provider topology">
      {error
        ? <ErrorNotice message={error} busy={loading} onRetry={() => void load()} />
        : nodes.length === 0
          ? <EmptyState reason={loading ? 'Reading live activity…' : 'No provider activity yet. A request in flight or a finished one appears here.'} />
          : <figure className="ed-figure usage-topology-figure">
            <div className="usage-topology-core">
              <Dot tone={running > 0 ? 'live' : 'idle'} />
              <strong>Router</strong>
              <span className="count-label">{running} in flight</span>
            </div>
            <ul className="usage-topology-nodes" aria-label="Providers by activity">
              {nodes.map(node => <li key={node.provider} className="usage-topology-node" data-active={node.active > 0}>
                <Status tone={node.error ? 'bad' : node.active > 0 ? 'live' : 'idle'}>{node.provider}</Status>
                <span className="count-label">{node.active > 0 ? `${node.active} in flight` : 'idle'}</span>
                {node.models.length > 0 && <small>{node.models.slice(0, 3).join(', ')}{node.models.length > 3 ? ` +${node.models.length - 3}` : ''}</small>}
              </li>)}
            </ul>
            <figcaption><Caption kicker="Fig. 3">Live requests in flight, grouped by provider, refreshed every 5 seconds{active?.errorProvider ? `; ${active.errorProvider} failed most recently` : ''}.</Caption></figcaption>
          </figure>}
    </Main>
    <Margin>
      <h3 className="ed-h3">Recent requests</h3>
      {active && recent.length === 0
        ? <p className="ed-now-note">No request has finished since this process started.</p>
        : <ul className="ed-watch usage-recent" aria-label="Recently finished requests">
          {recent.slice(0, 6).map((item, index) => <li key={`${item.timestamp}-${index}`}>
            <Status tone={item.status === 'error' ? 'bad' : 'good'}>{item.status === 'error' ? 'Failed' : 'Finished'}</Status>
            <span>{item.model || 'unknown model'} on {item.provider || 'unknown provider'}, {item.promptTokens} in / {item.completionTokens} out, {ago(item.timestamp)}</span>
          </li>)}
        </ul>}
    </Margin>
  </Row>
}
