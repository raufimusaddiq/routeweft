import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { EmptyState, ErrorNotice, SkeletonRows } from './kit'
import { Caption, Fact, Main, Margin, Row, Status } from './editorial'
import { compactOrDash, numOrDash, num, statusTone, type RequestRecord, type UsageEvent } from './format'

type Props = { requestId: string; onSelect: (requestId: string) => void; onUnauthorized: () => void }

const dash = '—'

function EventFacts({ event }: { event: UsageEvent }) {
  return <dl className="ed-facts request-facts">
    <Fact label="Status" value={<Status tone={statusTone(event.status)}>{event.status}</Status>} note={`${new Date(event.createdAt).toLocaleString()}${event.termination ? ` · ${event.termination.replace('_', ' ')}` : ''}`} />
    <Fact label="Provider" value={event.providerId || dash} note={event.modelId ? `model ${event.modelId}` : 'no model recorded'} />
    <Fact label="Connection" value={<code>{event.connectionId || dash}</code>} />
    <Fact label="Key" value={<code>{event.apiKeyId || dash}</code>} note={event.apiKeyId ? undefined : 'no client key was recorded for this request'} />
    <Fact label="Tokens" value={<>{compactOrDash(event.inputTokens, event.outputTokens)} <small>in + out</small></>} note={event.usageUnobserved ? 'the upstream reported no usage for this request' : `input ${numOrDash(event.inputTokens)}, output ${numOrDash(event.outputTokens)}`} />
    <Fact label="Cache" value={<>{compactOrDash(event.cacheReadTokens, event.cacheWriteTokens)} <small>tokens</small></>} note={event.usageUnobserved ? undefined : `read ${numOrDash(event.cacheReadTokens)}, write ${numOrDash(event.cacheWriteTokens)}`} />
    <Fact label="Duration" value={event.durationMs !== undefined ? <>{num(event.durationMs)} <small>ms</small></> : dash} />
    <Fact label="Time to first token" value={event.ttftMs !== undefined ? <>{num(event.ttftMs)} <small>ms</small></> : dash} note={event.ttftMs === undefined ? 'not recorded for this request' : undefined} />
  </dl>
}

// One request, looked up by id: its usage events and, if the diagnostic store kept one, the redacted detail.
export function RequestDetails({ requestId, onSelect, onUnauthorized }: Props) {
  const [draft, setDraft] = useState(requestId)
  const [record, setRecord] = useState<RequestRecord | null>(null)
  const [missing, setMissing] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)

  useEffect(() => { setDraft(requestId) }, [requestId])

  const load = useCallback(async () => {
    controller.current?.abort()
    setRecord(null)
    setMissing(false)
    setError('')
    if (!requestId) { setLoading(false); return }
    const abort = new AbortController()
    controller.current = abort
    const { signal } = abort
    setLoading(true)
    try {
      const response = await fetch(`/admin/v1/usage/requests/${encodeURIComponent(requestId)}`, { credentials: 'same-origin', signal })
      if (signal.aborted) return
      if (response.status === 401) { onUnauthorized(); return }
      if (response.status === 404) { setMissing(true); return }
      if (!response.ok) throw new Error('Could not load this request.')
      const body = await response.json() as RequestRecord
      if (signal.aborted) return
      setRecord(body)
    } catch (cause) {
      if (signal.aborted || (cause instanceof DOMException && cause.name === 'AbortError')) return
      setError(cause instanceof Error ? cause.message : 'Could not load this request.')
    } finally { if (!signal.aborted) setLoading(false) }
  }, [requestId, onUnauthorized])

  useEffect(() => { void load(); return () => controller.current?.abort() }, [load])

  const submit = (event: FormEvent) => { event.preventDefault(); const id = draft.trim(); if (id) onSelect(id) }
  const events = record?.events ?? []

  return <>
    <Row gap="s" label="Find a request">
      <div className="ed-main">
        <form className="log-filters" onSubmit={submit} aria-label="Find a request by id">
          <label className="log-filter log-filter-wide"><span className="ed-label">Request id</span>
            <input type="text" value={draft} onChange={event => setDraft(event.target.value)} autoComplete="off" spellCheck={false} /></label>
          <div className="log-filter-actions"><button className="button-primary" type="submit" disabled={!draft.trim()}>Show request</button></div>
        </form>
      </div>
    </Row>

    <Row gap="s" label="Request">
      <Main title="Request">
        {!requestId && <EmptyState reason="Enter a request id above, or open one from the Requests tab. The id is also returned to API clients in the response." />}
        {loading && <div role="status" aria-label="Loading request"><SkeletonRows rows={4} columns={2} /></div>}
        {error && <ErrorNotice message={error} busy={loading} onRetry={() => void load()} />}
        {missing && <EmptyState reason={`No stored request has the id ${requestId}.`} />}
        {record && <>
          <p className="ed-subline"><code>{record.requestId}</code>{events.length > 1 ? `, ${events.length} usage events` : ''}</p>
          {events.length === 0
            ? <EmptyState reason="No usage event is stored for this request; only a diagnostic detail is." />
            : events.map((event, index) => <figure className="ed-figure request-event" key={event.id}>
              <EventFacts event={event} />
              <figcaption><Caption kicker={events.length > 1 ? `Event ${index + 1} of ${events.length}` : 'Usage event'}>Recorded when the request finished. Credentials are never stored here, only the connection and key ids.</Caption></figcaption>
            </figure>)}
        </>}
      </Main>
      {record && <Margin>
        <h3 className="ed-h3">Diagnostic detail</h3>
        {record.detail
          ? <figure className="ed-figure">
            <dl className="ed-facts"><Fact label="Route mode" value={record.detail.routeMode || dash} note={new Date(record.detail.createdAt).toLocaleString()} /></dl>
            <pre className="request-detail-json" tabIndex={0} aria-label="Redacted diagnostic detail">{JSON.stringify(record.detail.detail, null, 2)}</pre>
            <figcaption><Caption kicker="Detail">Redacted before storage and kept for 7 days.</Caption></figcaption>
          </figure>
          : <p className="ed-now-note">No diagnostic detail is stored for this request. Details are bounded, kept for 7 days, and most requests have none; the usage event on the left is the full record.</p>}
      </Margin>}
    </Row>
  </>
}
