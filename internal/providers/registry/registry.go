// Package registry is the declarative provider identity catalog. Identity is
// separate from protocol adapters (BDR-009): a provider entry names its default
// transport, auth kind and base URL, and reuses shared adapters.
package registry

import (
	"errors"
	"strings"
)

// AuthKind is a supported provider credential mode.
type AuthKind string

const (
	AuthAPIKey AuthKind = "api-key"
	AuthOAuth  AuthKind = "oauth"
	AuthCookie AuthKind = "cookie"
	AuthNone   AuthKind = "none"
)

// Protocol names a shared protocol adapter family (docs/PROVIDER_BASELINE.md §2).
type Protocol string

// ModelCatalog classifies how a provider exposes its model list (PROVIDER_BASELINE column).
type ModelCatalog string

// Quirk names a provider-specific behavior requirement from PROVIDER_BASELINE.
type Quirk string

const (
	// QuirkCacheControl marks providers whose cache-control handling differs
	// from the shared adapter default and must be preserved.
	QuirkCacheControl Quirk = "cache-control"
)

const (
	CatalogStatic      ModelCatalog = "static"
	CatalogDynamic     ModelCatalog = "dynamic"
	CatalogPassthrough ModelCatalog = "passthrough"
)

const (
	TransportOpenAIChat      Protocol = "openai-chat"
	TransportOpenAIResponses Protocol = "openai-responses"
	TransportAnthropic       Protocol = "anthropic-messages"
	TransportGemini          Protocol = "gemini"
	TransportOllama          Protocol = "ollama"
	TransportSystemOne       Protocol = "systemone"
	// Provider-specific wire formats from PROVIDER_BASELINE's Specialized class.
	// They own a dedicated adapter rather than reusing a shared protocol family.
	TransportAntigravity   Protocol = "antigravity"
	TransportGeminiCLI     Protocol = "gemini-cli"
	TransportGrokWeb       Protocol = "grok-web"
	TransportPerplexityWeb Protocol = "perplexity-web"
	TransportQoder         Protocol = "qoder"
	TransportKiro          Protocol = "kiro"
	TransportCursor        Protocol = "cursor"
	TransportVertex        Protocol = "vertex"
	TransportCommandCode   Protocol = "commandcode"
)

// Spec is one built-in provider identity.
type Spec struct {
	ID         string
	Transports []Protocol
	Auth       AuthKind
	// AuthModes lists every credential mode the provider accepts, in baseline
	// order, for dual-auth providers (e.g. API key + OAuth). Auth is the default
	// mode and AuthModes[0] is always Auth. Empty means single-mode (Auth only).
	AuthModes      []AuthKind
	DefaultBaseURL string
	// TransportEndpoints maps a native transport protocol to the relative path
	// appended to DefaultBaseURL when that transport serves a different path than
	// the provider origin (for example a Chat path and a separate Messages path).
	// Missing keys fall back to the shared default for that protocol. A path is
	// relative, non-empty, without query/fragment and without "."/".." segments.
	TransportEndpoints map[Protocol]string
	// ModelCatalog is the baseline model catalog class; PassthroughModels marks
	// providers that must forward arbitrary operator-supplied IDs.
	ModelCatalog      ModelCatalog
	PassthroughModels bool
	StaticModels      []string
	// ReportsUsage marks providers whose upstream usage is accounted.
	ReportsUsage bool
	Quirks       []Quirk
}

// Validate rejects incomplete or duplicated provider identities.
func (s Spec) Validate() error {
	if s.ID == "" {
		return errors.New("provider id is required")
	}
	if len(s.Transports) == 0 {
		return errors.New("provider transport is required")
	}
	for _, transport := range s.Transports {
		switch transport {
		case TransportOpenAIChat, TransportOpenAIResponses, TransportAnthropic, TransportGemini, TransportOllama, TransportSystemOne,
			TransportAntigravity, TransportGeminiCLI, TransportGrokWeb, TransportPerplexityWeb, TransportQoder, TransportKiro, TransportCursor, TransportVertex, TransportCommandCode:
		default:
			return errors.New("unsupported provider transport")
		}
	}
	switch s.Auth {
	case AuthAPIKey, AuthOAuth, AuthCookie, AuthNone:
	default:
		return errors.New("unsupported provider auth kind")
	}
	if len(s.AuthModes) > 0 {
		if s.AuthModes[0] != s.Auth {
			return errors.New("auth modes must lead with the default auth kind")
		}
		seen := map[AuthKind]bool{}
		for _, mode := range s.AuthModes {
			switch mode {
			case AuthAPIKey, AuthOAuth, AuthCookie, AuthNone:
			default:
				return errors.New("unsupported provider auth mode")
			}
			if seen[mode] {
				return errors.New("duplicate provider auth mode")
			}
			seen[mode] = true
		}
	}
	switch s.ModelCatalog {
	case CatalogStatic, CatalogDynamic, CatalogPassthrough:
	default:
		return errors.New("unsupported provider model catalog")
	}
	for _, quirk := range s.Quirks {
		if quirk != QuirkCacheControl {
			return errors.New("unsupported provider quirk")
		}
	}
	if s.PassthroughModels && s.ModelCatalog != CatalogDynamic && s.ModelCatalog != CatalogPassthrough {
		return errors.New("passthrough models require a dynamic or passthrough catalog")
	}
	for protocol, path := range s.TransportEndpoints {
		supported := false
		for _, transport := range s.Transports {
			if transport == protocol {
				supported = true
				break
			}
		}
		if !supported {
			return errors.New("transport endpoint declared for an unsupported transport")
		}
		if err := validateEndpointPath(path); err != nil {
			return err
		}
	}
	return nil
}

// validateEndpointPath rejects paths that could escape the provider origin or
// smuggle a query/fragment, matching the dispatch-time endpoint policy.
func validateEndpointPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("transport endpoint path is required")
	}
	if strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return errors.New("transport endpoint must be a relative path without query or fragment")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return errors.New("transport endpoint must not traverse paths")
		}
	}
	return nil
}

// NativeBinding returns the provider transport matching a source protocol, so
// source-matching native routes win over translation (BDR-010).
func (s Spec) NativeBinding(source string) (Protocol, bool) {
	for _, transport := range s.Transports {
		if string(transport) == source {
			return transport, true
		}
	}
	return "", false
}

// EndpointFor returns the relative dispatch path for a native transport. The
// second result reports whether the provider declared a transport-specific
// override; callers fall back to their shared default when it is false.
func (s Spec) EndpointFor(transport Protocol) (string, bool) {
	path, ok := s.TransportEndpoints[transport]
	return path, ok
}

// Catalog is an immutable provider identity index.
type Catalog struct {
	specs map[string]Spec
}

// NewCatalog validates and indexes provider identities.
func NewCatalog(specs []Spec) (*Catalog, error) {
	indexed := make(map[string]Spec, len(specs))
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return nil, err
		}
		if _, exists := indexed[spec.ID]; exists {
			return nil, errors.New("duplicate provider " + spec.ID)
		}
		spec.Transports = append([]Protocol(nil), spec.Transports...)
		spec.Quirks = append([]Quirk(nil), spec.Quirks...)
		spec.StaticModels = append([]string(nil), spec.StaticModels...)
		spec.AuthModes = append([]AuthKind(nil), spec.AuthModes...)
		if spec.TransportEndpoints != nil {
			copied := make(map[Protocol]string, len(spec.TransportEndpoints))
			for protocol, path := range spec.TransportEndpoints {
				copied[protocol] = path
			}
			spec.TransportEndpoints = copied
		}
		indexed[spec.ID] = spec
	}
	return &Catalog{specs: indexed}, nil
}

// Lookup returns one provider identity.
func (c *Catalog) Lookup(id string) (Spec, bool) {
	if c == nil {
		return Spec{}, false
	}
	spec, ok := c.specs[id]
	if ok {
		spec.Transports = append([]Protocol(nil), spec.Transports...)
		spec.Quirks = append([]Quirk(nil), spec.Quirks...)
		spec.StaticModels = append([]string(nil), spec.StaticModels...)
		spec.AuthModes = append([]AuthKind(nil), spec.AuthModes...)
		if spec.TransportEndpoints != nil {
			copied := make(map[Protocol]string, len(spec.TransportEndpoints))
			for protocol, path := range spec.TransportEndpoints {
				copied[protocol] = path
			}
			spec.TransportEndpoints = copied
		}
	}
	return spec, ok
}

// Len reports how many provider identities are indexed.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.specs)
}

// EndpointFor returns the provider-specific relative dispatch path for one
// native transport, so request dispatch can honor per-protocol endpoints
// without importing provider packages (SPEC §11 base endpoint rules).
func (c *Catalog) EndpointFor(providerID, transport string) (string, bool) {
	spec, ok := c.Lookup(providerID)
	if !ok {
		return "", false
	}
	return spec.EndpointFor(Protocol(transport))
}
