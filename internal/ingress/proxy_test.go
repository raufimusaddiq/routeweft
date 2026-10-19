package ingress

import (
	"net/http"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// TestClientForPrefersConnectionProxyClient proves the request path honors a
// per-connection proxy client when one is configured, and falls back to the
// handler's default client otherwise (PRD-ROUTE-005, SPEC §20).
func TestClientForPrefersConnectionProxyClient(t *testing.T) {
	handler := New(nil, Options{})
	defaultClient := handler.clientFor(routing.ProviderRef{ConnectionID: "conn-1"})
	if defaultClient != handler.client {
		t.Fatal("without ClientFor the default client must be used")
	}
	pooled := &http.Client{}
	handler = New(nil, Options{ClientFor: func(connectionID string) *http.Client {
		if connectionID == "conn-1" {
			return pooled
		}
		return nil
	}})
	if got := handler.clientFor(routing.ProviderRef{ConnectionID: "conn-1"}); got != pooled {
		t.Fatal("bound connection did not use its pooled proxy client")
	}
	if got := handler.clientFor(routing.ProviderRef{ConnectionID: "conn-2"}); got != handler.client {
		t.Fatal("unbound connection must fall back to the default client")
	}
	if got := handler.clientFor(routing.ProviderRef{}); got != handler.client {
		t.Fatal("connectionless ref must use the default client")
	}
}
