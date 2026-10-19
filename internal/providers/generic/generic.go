// Package generic implements the unified, operator-configured provider node
// defined by PRD §7 and SPEC §33. Connections remain separate records.
package generic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const maxModelsResponseBytes = 2 << 20

type Transport string

const (
	ChatCompletions Transport = "chat_completions"
	Responses       Transport = "responses"
	Messages        Transport = "messages"
)

// Node is the provider-owned endpoint/prefix/transport capability. Credentials
// belong to independently stored Connection records.
type Node struct {
	ID         string
	Name       string
	Prefix     string
	BaseURL    string
	Transports []Transport
}

// Connection represents one independently rotatable API-key account.
type Connection struct {
	ID     string
	NodeID string
	Name   string
	Key    string
}

func (n Node) Validate(allowPrivate bool) error {
	if strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.Name) == "" || strings.TrimSpace(n.Prefix) == "" {
		return errors.New("generic provider ID, name and prefix are required")
	}
	validateURL := transport.ValidatePublicURL
	if allowPrivate {
		validateURL = transport.ValidateTrustedLocalURL
	}
	if _, err := validateURL(n.BaseURL); err != nil {
		return fmt.Errorf("generic provider base URL: %w", err)
	}
	if len(n.Transports) == 0 {
		return errors.New("generic provider requires at least one transport")
	}
	seen := make(map[Transport]bool, len(n.Transports))
	for _, protocol := range n.Transports {
		switch protocol {
		case ChatCompletions, Responses, Messages:
		default:
			return fmt.Errorf("unsupported generic provider transport %q", protocol)
		}
		if seen[protocol] {
			return fmt.Errorf("duplicate generic provider transport %q", protocol)
		}
		seen[protocol] = true
	}
	return nil
}

// Endpoint returns the provider-native endpoint for sourceProtocol. A missing
// binding means the caller must use its normal translation path.
func (n Node) Endpoint(sourceProtocol string) (string, bool, error) {
	protocol, suffix, ok := endpointFor(sourceProtocol)
	if !ok || !n.advertises(protocol) {
		return "", false, nil
	}
	base, err := parseBaseURL(n.BaseURL)
	if err != nil {
		return "", false, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + suffix
	base.RawPath = ""
	return base.String(), true, nil
}

func (n Node) advertises(protocol Transport) bool {
	for _, candidate := range n.Transports {
		if candidate == protocol {
			return true
		}
	}
	return false
}

func endpointFor(source string) (Transport, string, bool) {
	switch source {
	case "openai-chat":
		return ChatCompletions, "/chat/completions", true
	case "openai-responses":
		return Responses, "/responses", true
	case "anthropic-messages":
		return Messages, "/messages", true
	default:
		return "", "", false
	}
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("generic provider base URL must be absolute HTTP(S) without user info, query or fragment")
	}
	return parsed, nil
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// DiscoverModels queries the compatible /models endpoint. Failure is
// non-fatal to provider setup: callers may retain manually configured models.
func DiscoverModels(ctx context.Context, node Node, connection Connection, allowPrivate bool, client HTTPClient) ([]string, error) {
	validateURL := transport.ValidatePublicURL
	if allowPrivate {
		validateURL = transport.ValidateTrustedLocalURL
	}
	base, err := validateURL(node.BaseURL)
	if err != nil {
		return nil, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/models"
	base.RawPath = ""
	if client == nil {
		if allowPrivate {
			client = transport.NewTrustedLocalSSRFProtectedClient()
		} else {
			client = transport.NewSSRFProtectedClient()
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	if connection.Key != "" {
		request.Header.Set("Authorization", "Bearer "+connection.Key)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("generic provider model discovery failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("generic provider model discovery returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelsResponseBytes+1))
	if err != nil {
		return nil, errors.New("generic provider model response could not be read")
	}
	if len(body) > maxModelsResponseBytes {
		return nil, errors.New("generic provider model response exceeds size limit")
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, errors.New("generic provider returned invalid models JSON")
	}
	models := make([]string, 0, len(catalog.Data))
	seen := make(map[string]bool, len(catalog.Data))
	for _, model := range catalog.Data {
		id := strings.TrimSpace(model.ID)
		if id != "" && !seen[id] {
			models = append(models, id)
			seen[id] = true
		}
	}
	return models, nil
}

// ValidateModel accepts manually configured IDs without requiring discovery.
// Discovery is advisory and may be unavailable for otherwise valid providers.
func ValidateModel(model string, discovered []string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model ID is required")
	}
	if len(discovered) == 0 {
		return nil
	}
	for _, candidate := range discovered {
		if model == candidate {
			return nil
		}
	}
	return errors.New("model ID was not returned by provider discovery")
}
