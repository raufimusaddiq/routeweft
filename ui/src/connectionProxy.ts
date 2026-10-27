// A LiteRouter per-connection proxy (providerSpecificData.connectionProxyUrl),
// as the connections list reports it: its URL without credentials.
export type ConnectionProxy = { url: string; noProxy?: string }

// connectionProxyText says which proxy a connection's own proxy is and when it
// applies: always without a pool, and only when a bound pool has no usable
// member otherwise (the pin's order, connectionProxy.js:92-165).
export function connectionProxyText(connection: { proxyPoolId?: string; connectionProxy?: ConnectionProxy }): string {
  const proxy = connection.connectionProxy
  if (!proxy) return ''
  const bypass = proxy.noProxy ? ` · bypassed for ${proxy.noProxy}` : ''
  const when = connection.proxyPoolId ? ' · used when the pool has no usable member' : ''
  return `Connection proxy ${proxy.url}${bypass}${when}`
}
