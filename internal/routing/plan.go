// Package routing turns a resolved model into a bounded dispatch plan.
// PR 7 ships the single-provider plan needed by the OpenAI Chat native path;
// multi-account strategies, fallback chains and Combo arrive in Sprint 3.
package routing

import (
	"errors"
	"strings"
)

// ProviderSettings is the allowlisted, non-secret projection of sealed provider data.
type ProviderSettings struct {
	ProfileARN, AuthMethod                                              string
	BaseURL, AzureEndpoint, APIVersion, Deployment, Organization        string
	Region, WorkspaceID, DeviceID, AgentID                              string
	Email, UserID, SystemID, OrganizationID, DefaultOrganizationID, UID string
	OrgID                                                               string // Kilo Code organization (stored as orgId)
	ProjectID                                                           string // Vertex partner GCP project (stored as projectId)
}

// ConnectionMetadata contains immutable account configuration needed by provider wires.
type ConnectionMetadata struct {
	NodeID, NodeKind, NodeName, NodePrefix           string
	Name, AuthKind, Identity, AccountID, ProxyPoolID string
	Settings                                         ProviderSettings
}

// ProviderRef identifies one configured provider node and connection.
type ProviderRef struct {
	Connection    ConnectionMetadata `json:"-"`
	ProviderID    string
	Protocol      string
	BaseURL       string
	APIToken      string
	ConnectionID  string
	UpstreamModel string
	Endpoint      string
}

// Plan is the immutable route decision for one request attempt.
type Plan struct {
	SourceProtocol string
	TargetProtocol string
	Provider       ProviderRef
}

// NativePath reports whether source and target share a wire protocol, so no
// canonical translation is required (SPEC 9.1/9.2).
func (p Plan) NativePath() bool { return p.SourceProtocol == p.TargetProtocol }

// PlanOptions are the request-scoped inputs required to build one plan.
type PlanOptions struct {
	SourceProtocol string
	TargetProtocol string
	Provider       ProviderRef
}

// BuildPlan validates the inputs and returns one bounded attempt.
func BuildPlan(options PlanOptions) (Plan, error) {
	if strings.TrimSpace(options.SourceProtocol) == "" {
		return Plan{}, errors.New("source protocol is required")
	}
	if strings.TrimSpace(options.Provider.ProviderID) == "" {
		return Plan{}, errors.New("provider is required")
	}
	if strings.TrimSpace(options.Provider.BaseURL) == "" {
		return Plan{}, errors.New("provider base URL is required")
	}
	target := options.TargetProtocol
	if target == "" {
		target = options.Provider.Protocol
	}
	return Plan{
		SourceProtocol: options.SourceProtocol,
		TargetProtocol: target,
		Provider:       options.Provider,
	}, nil
}
