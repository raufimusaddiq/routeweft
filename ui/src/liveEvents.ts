// The dashboard's one live channel (#277, U08; PRD-OBS-003, SPEC 22.1).
//
// Every page that wants live updates subscribes here and this opens a single EventSource on
// /admin/v1/events for all of them, closing it when the last page leaves. The server sheds events
// for a subscriber that falls behind rather than slowing anyone down (events.go: "a slow browser
// loses notifications, then refreshes its query state"), so the channel tells subscribers when
// to re-read their data instead of trusting that nothing was missed:
//   - "gap":       an event id jumped, so at least one event was shed;
//   - "reconnect": the stream came back after an error, so anything may have been missed;
//   - "poll":      the stream is down, so subscribers are asked to refresh on a timer.
// Pages react to events for the targeted change and to a refresh by re-reading what they show.
//
// The oracle has no shared channel: its console log and usage pages each open their own
// (console-log/ConsoleLogClient.js:37, UsageStats.js:290) and rely on EventSource's own retry. The
// single channel, the resync and the polling fallback are Routeweft's own contract.
//
// DOM-free apart from the injected EventSource and timers, so node can test it.

export type LiveEvent = { id: number; type: string; createdAt: string; data: unknown }
export type ChannelState = 'idle' | 'connecting' | 'live' | 'polling'
export type RefreshReason = 'poll' | 'gap' | 'reconnect'
export type Subscription = {
  /** Event types that reach onEvent. */
  types?: readonly string[]
  onEvent?: (event: LiveEvent) => void
  /** Re-read what the page shows: the stream cannot be trusted to have delivered everything. */
  onRefresh?: (reason: RefreshReason) => void
}

export const EVENTS_URL = '/admin/v1/events'
/**
 * Every event type the server publishes on the bus (internal/control/events, SPEC 22.1). All of them are
 * listened for whoever subscribed, because gap detection reads the ids of every frame; a type missing here
 * would look like a shed event on every frame after it.
 */
export const KNOWN_EVENTS = ['config.updated', 'quota.refresh', 'usage.pending', 'request.completed', 'console.log', 'console.cleared'] as const
/** How often subscribers are asked to refresh while the stream is down. */
export const POLL_MS = 15000
/**
 * How long the stream is kept after the last page leaves. Moving between pages unmounts one and mounts the
 * next, so without this the connection would close and reopen on every navigation.
 */
export const IDLE_CLOSE_MS = 1000
/** Waits before opening a stream the browser gave up on (a refused or ended connection), then it repeats the last. */
export const RECONNECT_MS = [1000, 2000, 5000, 10000, 30000] as const

type MessageLike = { data: unknown }
type SourceLike = {
  readyState: number
  onopen: ((event: unknown) => void) | null
  onerror: ((event: unknown) => void) | null
  addEventListener(type: string, listener: (event: MessageLike) => void): void
  close(): void
}
export type ChannelDeps = {
  EventSource?: new (url: string) => SourceLike
  setTimeout: (callback: () => void, ms: number) => unknown
  clearTimeout: (handle: unknown) => void
  setInterval: (callback: () => void, ms: number) => unknown
  clearInterval: (handle: unknown) => void
}

const CLOSED = 2

export function parseEvent(data: unknown): LiveEvent | null {
  if (typeof data !== 'string') return null
  let raw: Record<string, unknown>
  try { raw = JSON.parse(data) as Record<string, unknown> } catch { return null }
  if (!raw || typeof raw !== 'object' || typeof raw.type !== 'string' || typeof raw.id !== 'number') return null
  return { id: raw.id, type: raw.type, createdAt: typeof raw.createdAt === 'string' ? raw.createdAt : '', data: raw.data }
}

export class LiveChannel {
  private subscribers = new Set<Subscription>()
  private stateListeners = new Set<() => void>()
  private state: ChannelState = 'idle'
  private source: SourceLike | null = null
  private lastId: number | null = null
  private troubled = false
  private poller: unknown = null
  private retry: unknown = null
  private closing: unknown = null
  private attempt = 0

  private deps: ChannelDeps
  private url: string

  constructor(deps: ChannelDeps, url: string = EVENTS_URL) {
    this.deps = deps
    this.url = url
  }

  getState = (): ChannelState => this.state

  onState = (listener: () => void): (() => void) => {
    this.stateListeners.add(listener)
    return () => { this.stateListeners.delete(listener) }
  }

  subscribe(subscription: Subscription): () => void {
    this.subscribers.add(subscription)
    if (this.closing !== null) {
      // A page replaced the last one within the grace period: the same connection serves it.
      this.deps.clearTimeout(this.closing)
      this.closing = null
    } else if (this.subscribers.size === 1) {
      this.connect()
    }
    return () => {
      this.subscribers.delete(subscription)
      if (this.subscribers.size === 0 && this.closing === null) {
        this.closing = this.deps.setTimeout(() => { this.closing = null; if (this.subscribers.size === 0) this.disconnect() }, IDLE_CLOSE_MS)
      }
    }
  }

  /** How many pages are listening; the channel is open exactly when this is above zero. */
  get subscriberCount(): number { return this.subscribers.size }

  private setState(next: ChannelState) {
    if (next === this.state) return
    this.state = next
    for (const listener of [...this.stateListeners]) listener()
  }

  private refresh(reason: RefreshReason) {
    for (const subscriber of [...this.subscribers]) {
      try { subscriber.onRefresh?.(reason) } catch { /* one page's failure does not stop the others */ }
    }
  }

  private startPolling() {
    if (this.poller !== null) return
    this.poller = this.deps.setInterval(() => this.refresh('poll'), POLL_MS)
  }

  private stopPolling() {
    if (this.poller === null) return
    this.deps.clearInterval(this.poller)
    this.poller = null
  }

  private connect() {
    const Source = this.deps.EventSource
    this.lastId = null
    if (!Source) {
      // No EventSource at all: the channel is the polling fallback.
      this.troubled = true
      this.setState('polling')
      this.startPolling()
      return
    }
    this.setState(this.troubled ? 'polling' : 'connecting')
    const source = new Source(this.url)
    this.source = source
    source.onopen = () => {
      if (this.source !== source) return
      this.attempt = 0
      this.stopPolling()
      this.setState('live')
      if (this.troubled) {
        this.troubled = false
        this.refresh('reconnect')
      }
    }
    source.onerror = () => {
      if (this.source !== source) return
      this.troubled = true
      this.setState('polling')
      this.startPolling()
      // The browser retries a dropped stream itself; one it closed (a refused or ended connection) is opened again here.
      if (source.readyState === CLOSED) this.scheduleReconnect(source)
    }
    for (const type of KNOWN_EVENTS) source.addEventListener(type, message => this.receive(message))
  }

  private scheduleReconnect(closed: SourceLike) {
    if (this.retry !== null) return
    const wait = RECONNECT_MS[Math.min(this.attempt, RECONNECT_MS.length - 1)]
    this.attempt += 1
    this.retry = this.deps.setTimeout(() => {
      this.retry = null
      if (this.subscribers.size === 0 || this.source !== closed) return
      closed.close()
      this.connect()
    }, wait)
  }

  private receive(message: MessageLike) {
    const event = parseEvent(message.data)
    if (!event) return
    // Ids are one sequence over the whole bus and nothing is filtered per subscriber, so a jump means a shed event.
    if (this.lastId !== null && event.id > this.lastId + 1) this.refresh('gap')
    if (this.lastId === null || event.id > this.lastId) this.lastId = event.id
    for (const subscriber of [...this.subscribers]) {
      if (!subscriber.types?.includes(event.type)) continue
      try { subscriber.onEvent?.(event) } catch { /* isolated */ }
    }
  }

  private disconnect() {
    this.stopPolling()
    if (this.retry !== null) { this.deps.clearTimeout(this.retry); this.retry = null }
    this.source?.close()
    this.source = null
    this.lastId = null
    this.troubled = false
    this.attempt = 0
    this.setState('idle')
  }
}

export const channelLabel: Record<ChannelState, string> = {
  idle: '',
  connecting: 'Connecting…',
  live: 'Live',
  polling: `Live updates unavailable · refreshing every ${POLL_MS / 1000} s`,
}
