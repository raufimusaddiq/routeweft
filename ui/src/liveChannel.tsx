import { useEffect, useRef, useSyncExternalStore } from 'react'
import { LiveChannel, type ChannelState, type Subscription } from './liveEvents'

// The one channel every page shares (#277). It opens when the first page listens and closes after the last.
export const liveChannel = new LiveChannel({
  EventSource: typeof EventSource === 'undefined' ? undefined : (EventSource as unknown as ConstructorParameters<typeof LiveChannel>[0]['EventSource']),
  setTimeout: (callback, ms) => window.setTimeout(callback, ms),
  clearTimeout: handle => window.clearTimeout(handle as number),
  setInterval: (callback, ms) => window.setInterval(callback, ms),
  clearInterval: handle => window.clearInterval(handle as number),
})

/**
 * Listen on the shared channel while the calling page is mounted. The handlers may change on every render; the
 * subscription does not. Returns the channel's state so the page can say, in words, whether it is live.
 */
export function useLiveChannel(subscription: Subscription, enabled = true): ChannelState {
  const latest = useRef(subscription)
  latest.current = subscription
  const types = subscription.types?.join('\n') ?? ''
  useEffect(() => {
    if (!enabled) return
    return liveChannel.subscribe({
      types: types ? types.split('\n') : undefined,
      onEvent: event => latest.current.onEvent?.(event),
      onRefresh: reason => latest.current.onRefresh?.(reason),
    })
  }, [types, enabled])
  return useSyncExternalStore(liveChannel.onState, liveChannel.getState)
}
