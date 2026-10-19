// Package proxy owns outbound proxy pools and per-connection proxy binding
// (PRD-ROUTE-005, SPEC §20).
//
// Pools are durable; selection cursors are memory-first in RuntimeState. The
// package never dials by itself — it compiles a transport.ProxyPolicy that the
// shared pooled transport turns into one cached client per material config.
package proxy
