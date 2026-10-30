import assert from 'node:assert/strict'
import { test } from 'node:test'
import { EVENTS_URL, IDLE_CLOSE_MS, KNOWN_EVENTS, LiveChannel, POLL_MS, RECONNECT_MS, channelLabel, parseEvent, type ChannelDeps, type LiveEvent, type RefreshReason } from './liveEvents.ts'

class FakeSource {
  static all: FakeSource[] = []
  readyState = 0
  onopen: ((event: unknown) => void) | null = null
  onerror: ((event: unknown) => void) | null = null
  closed = false
  listeners = new Map<string, Array<(event: { data: unknown }) => void>>()
  url: string
  constructor(url: string) { this.url = url; FakeSource.all.push(this) }
  addEventListener(type: string, listener: (event: { data: unknown }) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]) }
  close() { this.closed = true; this.readyState = 2 }
  open() { this.readyState = 1; this.onopen?.({}) }
  /** The browser is retrying by itself. */
  dropRetrying() { this.readyState = 0; this.onerror?.({}) }
  /** The browser gave up (refused, wrong type, ended). */
  dropClosed() { this.readyState = 2; this.onerror?.({}) }
  emit(id: number, type: string, data: unknown = {}) { for (const listener of this.listeners.get(type) ?? []) listener({ data: JSON.stringify({ id, type, createdAt: '2026-10-30T00:00:00Z', data }) }) }
}

function clock() {
  let now = 0
  let next = 1
  const timers = new Map<number, { at: number; every?: number; callback: () => void }>()
  const deps: ChannelDeps = {
    EventSource: FakeSource,
    setTimeout: (callback, ms) => { const id = next++; timers.set(id, { at: now + ms, callback }); return id },
    clearTimeout: handle => { timers.delete(handle as number) },
    setInterval: (callback, ms) => { const id = next++; timers.set(id, { at: now + ms, every: ms, callback }); return id },
    clearInterval: handle => { timers.delete(handle as number) },
  }
  return {
    deps,
    pending: () => timers.size,
    advance(ms: number) {
      const end = now + ms
      for (;;) {
        const due = [...timers.entries()].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0]
        if (!due) break
        const [id, timer] = due
        now = timer.at
        if (timer.every) timer.at += timer.every
        else timers.delete(id)
        timer.callback()
      }
      now = end
    },
  }
}

function setup() {
  FakeSource.all = []
  const time = clock()
  const channel = new LiveChannel(time.deps)
  return { time, channel }
}
const collect = () => {
  const events: LiveEvent[] = []
  const refreshes: RefreshReason[] = []
  return { events, refreshes, subscription: { types: ['config.updated', 'console.log'] as const, onEvent: (event: LiveEvent) => events.push(event), onRefresh: (reason: RefreshReason) => refreshes.push(reason) } }
}

test('any number of pages share one connection, opened for the first and closed after the last', () => {
  const { channel, time } = setup()
  assert.equal(channel.getState(), 'idle')
  assert.equal(FakeSource.all.length, 0, 'nothing is opened until a page listens')
  const a = channel.subscribe(collect().subscription)
  const b = channel.subscribe(collect().subscription)
  const c = channel.subscribe(collect().subscription)
  assert.equal(FakeSource.all.length, 1, 'one EventSource for three pages')
  assert.equal(FakeSource.all[0].url, EVENTS_URL)
  assert.equal(channel.subscriberCount, 3)
  FakeSource.all[0].open()
  assert.equal(channel.getState(), 'live')
  a(); b()
  assert.equal(FakeSource.all[0].closed, false, 'still open for the last page')
  c()
  assert.equal(FakeSource.all[0].closed, false, 'kept for a moment, in case the next page is about to mount')
  time.advance(IDLE_CLOSE_MS)
  assert.equal(FakeSource.all[0].closed, true)
  assert.equal(channel.getState(), 'idle')
  channel.subscribe(collect().subscription)
  assert.equal(FakeSource.all.length, 2, 'a later page opens a fresh one')
})

test('moving from one page to the next keeps the same connection', () => {
  const { channel, time } = setup()
  const leave = channel.subscribe(collect().subscription)
  FakeSource.all[0].open()
  leave()
  const next = collect()
  channel.subscribe(next.subscription)
  time.advance(IDLE_CLOSE_MS * 3)
  assert.equal(FakeSource.all.length, 1, 'no second connection')
  assert.equal(FakeSource.all[0].closed, false)
  assert.equal(channel.getState(), 'live')
  FakeSource.all[0].emit(1, 'config.updated')
  assert.equal(next.events.length, 1, 'the new page receives events on it')
})

test('every known event type is listened for, whoever subscribed, so ids can be tracked', () => {
  const { channel } = setup()
  channel.subscribe({ types: ['config.updated'] })
  assert.deepEqual([...FakeSource.all[0].listeners.keys()].sort(), [...KNOWN_EVENTS].sort())
})

test('an event reaches only the pages that asked for its type', () => {
  const { channel } = setup()
  const config = collect()
  const consoleOnly = { events: [] as LiveEvent[], subscription: { types: ['console.log'] as const, onEvent: (event: LiveEvent) => consoleOnly.events.push(event) } }
  channel.subscribe(config.subscription)
  channel.subscribe(consoleOnly.subscription)
  const source = FakeSource.all[0]
  source.open()
  source.emit(1, 'config.updated', { resource: 'combo' })
  source.emit(2, 'console.log', { lines: [] })
  source.emit(3, 'request.completed', {})
  assert.deepEqual(config.events.map(event => event.type), ['config.updated', 'console.log'])
  assert.deepEqual(consoleOnly.events.map(event => event.type), ['console.log'])
  assert.deepEqual(config.events[0].data, { resource: 'combo' })
  assert.deepEqual(config.refreshes, [], 'consecutive ids are not a gap')
})

test('a jump in ids means an event was shed, and every page is told to re-read', () => {
  const { channel } = setup()
  const page = collect()
  const other = collect()
  channel.subscribe(page.subscription)
  channel.subscribe(other.subscription)
  const source = FakeSource.all[0]
  source.open()
  source.emit(10, 'config.updated')
  source.emit(11, 'config.updated')
  assert.deepEqual(page.refreshes, [])
  source.emit(14, 'console.log')
  assert.deepEqual(page.refreshes, ['gap'])
  assert.deepEqual(other.refreshes, ['gap'])
  source.emit(15, 'console.log')
  assert.deepEqual(page.refreshes, ['gap'], 'the next consecutive id is fine again')
  source.emit(14, 'console.log')
  assert.deepEqual(page.refreshes, ['gap'], 'a replayed or older id is not a gap')
})

test('while the stream is down pages are asked to refresh on a timer; coming back asks once and stops', () => {
  const { channel, time } = setup()
  const page = collect()
  channel.subscribe(page.subscription)
  const source = FakeSource.all[0]
  source.open()
  time.advance(POLL_MS * 3)
  assert.deepEqual(page.refreshes, [], 'a healthy stream needs no polling')
  source.dropRetrying()
  assert.equal(channel.getState(), 'polling')
  assert.match(channelLabel[channel.getState()], /refreshing every 15 s/, 'the fallback is stated in words')
  time.advance(POLL_MS - 1)
  assert.deepEqual(page.refreshes, [])
  time.advance(1)
  assert.deepEqual(page.refreshes, ['poll'])
  time.advance(POLL_MS)
  assert.deepEqual(page.refreshes, ['poll', 'poll'])
  source.open()
  assert.equal(channel.getState(), 'live')
  assert.deepEqual(page.refreshes, ['poll', 'poll', 'reconnect'], 'one catch-up refresh on recovery')
  time.advance(POLL_MS * 4)
  assert.deepEqual(page.refreshes, ['poll', 'poll', 'reconnect'], 'polling has stopped')
})

test('a stream the browser closed is opened again, waiting longer each time and starting over once it opens', () => {
  const { channel, time } = setup()
  const page = collect()
  channel.subscribe(page.subscription)
  FakeSource.all[0].dropClosed()
  assert.equal(channel.getState(), 'polling')
  assert.equal(FakeSource.all.length, 1)
  time.advance(RECONNECT_MS[0] - 1)
  assert.equal(FakeSource.all.length, 1)
  time.advance(1)
  assert.equal(FakeSource.all.length, 2, 'reopened after the first wait')
  assert.equal(FakeSource.all[0].closed, true)
  FakeSource.all[1].dropClosed()
  time.advance(RECONNECT_MS[1])
  assert.equal(FakeSource.all.length, 3, 'and after the second')
  FakeSource.all[2].open()
  assert.equal(channel.getState(), 'live')
  assert.ok(page.refreshes.includes('reconnect'))
  FakeSource.all[2].dropClosed()
  time.advance(RECONNECT_MS[0])
  assert.equal(FakeSource.all.length, 4, 'the wait starts over after a good connection')
  // The last wait is held, not grown without bound.
  for (let i = 0; i < 8; i++) { FakeSource.all.at(-1)!.dropClosed(); time.advance(RECONNECT_MS.at(-1)!) }
  assert.ok(FakeSource.all.length >= 10)
})

test('leaving while the stream is down stops every timer', () => {
  const { channel, time } = setup()
  const leave = channel.subscribe(collect().subscription)
  FakeSource.all[0].dropClosed()
  assert.ok(time.pending() >= 2, 'a poll timer and a reconnect timer')
  leave()
  time.advance(IDLE_CLOSE_MS)
  assert.equal(time.pending(), 0)
  time.advance(POLL_MS * 5)
  assert.equal(FakeSource.all.length, 1, 'nothing reopens for nobody')
})

test('without EventSource the channel is the polling fallback, said plainly', () => {
  FakeSource.all = []
  const time = clock()
  const channel = new LiveChannel({ ...time.deps, EventSource: undefined })
  const page = collect()
  channel.subscribe(page.subscription)
  assert.equal(channel.getState(), 'polling')
  time.advance(POLL_MS)
  assert.deepEqual(page.refreshes, ['poll'])
})

test('one page failing does not stop the others, and a stale stream cannot speak', () => {
  const { channel } = setup()
  const good = collect()
  channel.subscribe({ types: ['config.updated'], onEvent: () => { throw new Error('boom') }, onRefresh: () => { throw new Error('boom') } })
  channel.subscribe(good.subscription)
  const source = FakeSource.all[0]
  source.open()
  source.emit(1, 'config.updated')
  source.emit(3, 'config.updated')
  assert.equal(good.events.length, 2)
  assert.deepEqual(good.refreshes, ['gap'])
  source.dropClosed()
})

test('frames that are not well-formed events are ignored', () => {
  assert.equal(parseEvent('not json'), null)
  assert.equal(parseEvent(42), null)
  assert.equal(parseEvent('{"type":"x"}'), null)
  assert.equal(parseEvent('{"id":"1","type":"x"}'), null)
  assert.deepEqual(parseEvent('{"id":7,"type":"config.updated","createdAt":"t","data":{"a":1}}'), { id: 7, type: 'config.updated', createdAt: 't', data: { a: 1 } })
  const { channel } = setup()
  const page = collect()
  channel.subscribe(page.subscription)
  const source = FakeSource.all[0]
  source.open()
  for (const listener of source.listeners.get('config.updated') ?? []) listener({ data: 'garbage' })
  assert.deepEqual(page.events, [])
})
