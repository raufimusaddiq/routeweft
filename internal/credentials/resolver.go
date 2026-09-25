package credentials

import (
	"context"
	"errors"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// NodeFallback supplies compiled provider identity (protocol, base URL) for a
// connection whose node is a builtin catalog entry rather than an operator node
// record. Generic nodes carry their own base URL/transports, so the callback is
// consulted only when the node has no base URL.
type NodeFallback func(providerID string) (protocol, baseURL string, ok bool)

// Resolver turns a provider/account selection into a dispatchable ProviderRef
// with a currently valid credential. It is the request-path bridge between the
// durable credential store and ingress (SPEC §8, PRD-AUTH-003).
type Resolver struct {
	Store    *Store
	Registry *Registry
	Fallback NodeFallback
}

// ResolveForAccount builds a ProviderRef for one connection, refreshing the
// credential when required. Base URL and protocol come from the node record or
// the compiled fallback.
func (r Resolver) ResolveForAccount(ctx context.Context, connectionID, upstreamModel string) (routing.ProviderRef, error) {
	if r.Store == nil || r.Registry == nil {
		return routing.ProviderRef{}, errors.New("credential resolver is not configured")
	}
	connection, err := r.Store.GetConnection(ctx, connectionID)
	if err != nil {
		return routing.ProviderRef{}, err
	}
	if !connection.Enabled {
		return routing.ProviderRef{}, errors.New("provider connection is disabled")
	}
	node, err := r.node(ctx, connection.NodeID)
	if err != nil {
		return routing.ProviderRef{}, err
	}
	secret, err := r.Registry.Secret(ctx, connectionID)
	if err != nil {
		return routing.ProviderRef{}, err
	}
	token := secret.AccessToken
	if token == "" {
		token = secret.Cookie
	}
	baseURL := strings.TrimSpace(node.BaseURL)
	protocol := strings.TrimSpace(firstTransport(node.Transports))
	if baseURL == "" || protocol == "" {
		if r.Fallback == nil {
			return routing.ProviderRef{}, errors.New("provider node has no resolvable base URL or transport")
		}
		fallbackProtocol, fallbackBaseURL, ok := r.Fallback(node.ProviderID)
		if !ok {
			return routing.ProviderRef{}, errors.New("provider node has no compiled identity")
		}
		if baseURL == "" {
			baseURL = fallbackBaseURL
		}
		if protocol == "" {
			protocol = fallbackProtocol
		}
	}
	if baseURL == "" {
		return routing.ProviderRef{}, errors.New("provider base URL is required")
	}
	return routing.ProviderRef{
		ProviderID:    node.ProviderID,
		Protocol:      protocol,
		BaseURL:       baseURL,
		APIToken:      token,
		ConnectionID:  connection.ID,
		UpstreamModel: upstreamModel,
	}, nil
}

func (r Resolver) node(ctx context.Context, nodeID string) (Node, error) {
	const query = "SELECT id,kind,provider_id,name,COALESCE(prefix,''),COALESCE(base_url,''),COALESCE(transports,'') FROM provider_nodes WHERE id=?"
	row := r.Store.db.QueryRowContext(ctx, query, nodeID)
	var node Node
	var transports string
	if err := row.Scan(&node.ID, &node.Kind, &node.ProviderID, &node.Name, &node.Prefix, &node.BaseURL, &transports); err != nil {
		return Node{}, err
	}
	node.Transports, _ = decodeTransports(transports)
	return node, nil
}

func firstTransport(transports []string) string {
	if len(transports) == 0 {
		return ""
	}
	return transports[0]
}
