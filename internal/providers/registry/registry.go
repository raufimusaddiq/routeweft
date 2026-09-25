// Package registry is the declarative provider identity catalog. Identity is
// separate from protocol adapters (BDR-009): a provider entry names its default
// transport, auth kind and base URL, and reuses shared adapters.
package registry

import "errors"

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

const (
	TransportOpenAIChat      Protocol = "openai-chat"
	TransportOpenAIResponses Protocol = "openai-responses"
	TransportAnthropic       Protocol = "anthropic-messages"
	TransportGemini          Protocol = "gemini"
	TransportOllama          Protocol = "ollama"
	TransportSystemOne       Protocol = "systemone"
)

// Spec is one built-in provider identity.
type Spec struct {
	ID             string
	Transports     []Protocol
	Auth           AuthKind
	DefaultBaseURL string
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
		case TransportOpenAIChat, TransportOpenAIResponses, TransportAnthropic, TransportGemini, TransportOllama, TransportSystemOne:
		default:
			return errors.New("unsupported provider transport")
		}
	}
	switch s.Auth {
	case AuthAPIKey, AuthOAuth, AuthCookie, AuthNone:
	default:
		return errors.New("unsupported provider auth kind")
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
