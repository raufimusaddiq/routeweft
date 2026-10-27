package transport

import (
	"context"
	"net/http"
)

type connectionKey struct{}

// WithConnection marks outbound work as made on behalf of one provider
// connection, so a ConnectionRouter sends it through that connection's proxy.
// LiteRouter resolves the connection proxy for every such call, not only
// inference: usage, reset credits, auto-ping, token refresh and the connection
// test (src/lib/network/connectionProxy.js:66 and its callers).
func WithConnection(ctx context.Context, connectionID string) context.Context {
	if connectionID == "" {
		return ctx
	}
	return context.WithValue(ctx, connectionKey{}, connectionID)
}

// ConnectionFrom returns the connection WithConnection recorded, or "".
func ConnectionFrom(ctx context.Context) string {
	id, _ := ctx.Value(connectionKey{}).(string)
	return id
}

// ConnectionRouter sends a request that carries a connection (WithConnection)
// through the transport Route returns for it, and everything else, or a
// connection Route has nothing for, through Base.
type ConnectionRouter struct {
	Base  http.RoundTripper
	Route func(connectionID string) http.RoundTripper
}

func (r ConnectionRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	if r.Route != nil {
		if id := ConnectionFrom(request.Context()); id != "" {
			if next := r.Route(id); next != nil {
				return next.RoundTrip(request)
			}
		}
	}
	return r.Base.RoundTrip(request)
}

// CloseIdleConnections reaches Base; routed transports belong to their pool.
func (r ConnectionRouter) CloseIdleConnections() {
	if closer, ok := r.Base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// RouteByConnection returns a copy of client whose requests made on behalf of
// a connection leave through route's transport for it.
func RouteByConnection(client *http.Client, route func(connectionID string) http.RoundTripper) *http.Client {
	routed := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	routed.Transport = ConnectionRouter{Base: base, Route: route}
	return &routed
}
