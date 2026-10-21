// Package discovery fetches a provider's live model catalog over its
// compatible /models endpoint and merges it into the runtime catalog without
// dropping operator-managed models (SPEC §7, PROVIDER_BASELINE §7).
//
// Discovery is advisory: a failure never deletes a previously configured or
// manually added model, and unknown IDs stay routable for passthrough
// providers when the operator policy allows it.
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const maxModelsResponseBytes = 4 << 20

type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("model discovery returned HTTP %d", e.Status)
}

// HTTPClient is the minimal client contract used for discovery.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Request describes one discovery call. Path defaults to "models" appended to
// BaseURL, matching the shared OpenAI-compatible convention; providers whose
// catalog lives elsewhere (for example an OpenRouter-style gateway) supply an
// explicit relative Path.
type Request struct {
	ProviderID string
	BaseURL    string
	Path       string
	// Headers carry only non-secret provider identity headers; the credential is
	// injected from Credential and never logged.
	Headers map[string]string
	// AuthStyle selects the credential header. Empty means bearer.
	AuthStyle  AuthStyle
	Credential string

	// Method defaults to GET. A POST listing sends Body as JSON.
	Method string
	Body   []byte
	// URL replaces BaseURL and Path when the provider lists from another host.
	URL string
	// Query is the provider's own fixed query (a client-version gate, a server-side
	// filter). It is never taken from a client request: the reference forwards none.
	Query string
	// Sign returns headers computed over the final URL and body, for providers
	// whose listing is signed. Override then replaces or (empty value) removes
	// headers the signer set.
	Sign     func(rawURL string, body []byte) (map[string]string, error)
	Override map[string]string
	// Shape selects the provider's response reader; empty is the shared catalog.
	Shape wire.Shape
}

// AuthStyle is a supported discovery credential style.
type AuthStyle string

const (
	// AuthBearer sends "Authorization: Bearer <key>".
	AuthBearer AuthStyle = ""
	// AuthXApiKey sends "x-api-key: <key>" (Anthropic-compatible gateways).
	AuthXApiKey      AuthStyle = "x-api-key"
	AuthGoogleAPIKey AuthStyle = "x-goog-api-key"
	AuthCookie       AuthStyle = "cookie"
	// AuthNone sends no credential (no-auth/free providers).
	AuthNone AuthStyle = "none"
	// AuthAnthropicCompat sends the key as x-api-key and as a bearer, which is how
	// the reference lists an Anthropic-compatible node (route.js:566-574): gateways
	// of that kind differ on which one they read.
	AuthAnthropicCompat AuthStyle = "anthropic-compat"
)

// Client fetches provider model catalogs. Results are returned as ordered,
// de-duplicated IDs; the caller owns caching and persistence.
type Client struct {
	HTTP HTTPClient
	// AllowPrivateUpstreams is the explicit trusted-local operator policy; off by
	// default so discovery cannot reach loopback/LAN/metadata.
	AllowPrivateUpstreams bool
}

// Models fetches the live catalog. Errors are advisory: callers must keep the
// last known durable catalog on failure.
func (c *Client) Models(ctx context.Context, request Request) ([]string, error) {
	models, err := c.ModelEntries(ctx, request)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, nil
}

// ModelEntries fetches the live catalog with display metadata preserved.
func (c *Client) ModelEntries(ctx context.Context, request Request) ([]Model, error) {
	if strings.TrimSpace(request.ProviderID) == "" {
		return nil, errors.New("provider id is required")
	}
	validateURL := transport.ValidatePublicURL
	if c.AllowPrivateUpstreams {
		validateURL = transport.ValidateTrustedLocalURL
	}
	target, err := wire.ListingURL(validateURL, request.BaseURL, request.URL, request.Path, request.Query)
	if err != nil {
		return nil, err
	}
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if request.Body != nil {
		requestBody = bytes.NewReader(request.Body)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, target, requestBody)
	if err != nil {
		return nil, err
	}
	if request.Body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "Routeweft")
	for name, value := range request.Headers {
		httpRequest.Header.Set(name, value)
	}
	switch request.AuthStyle {
	case AuthNone:
	case AuthXApiKey:
		if request.Credential != "" {
			httpRequest.Header.Set("x-api-key", request.Credential)
			httpRequest.Header.Set("anthropic-version", "2023-06-01")
		}
	case AuthAnthropicCompat:
		if request.Credential != "" {
			httpRequest.Header.Set("x-api-key", request.Credential)
			httpRequest.Header.Set("anthropic-version", "2023-06-01")
			httpRequest.Header.Set("Authorization", "Bearer "+request.Credential)
		}
	case AuthGoogleAPIKey:
		httpRequest.Header.Set("x-goog-api-key", request.Credential)
	case AuthCookie:
		httpRequest.Header.Set("Cookie", request.Credential)
	default:
		if request.Credential != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+request.Credential)
		}
	}
	if request.Sign != nil {
		signed, err := request.Sign(target, request.Body)
		if err != nil {
			return nil, err
		}
		for name, value := range signed {
			httpRequest.Header.Set(name, value)
		}
	}
	for name, value := range request.Override {
		if value == "" {
			httpRequest.Header.Del(name)
		} else {
			httpRequest.Header.Set(name, value)
		}
	}

	client := c.HTTP
	if client == nil {
		if c.AllowPrivateUpstreams {
			client = transport.NewTrustedLocalSSRFProtectedClient()
		} else {
			client = transport.NewSSRFProtectedClient()
		}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("model discovery request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelsResponseBytes+1))
	if err != nil {
		return nil, errors.New("model discovery response could not be read")
	}
	if len(body) > maxModelsResponseBytes {
		return nil, errors.New("model discovery response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &StatusError{Status: response.StatusCode}
	}
	if request.Shape != wire.ShapeDefault {
		listed, err := wire.ParseListing(request.Shape, body)
		if err != nil {
			return nil, err
		}
		models := make([]Model, 0, len(listed))
		for _, model := range listed {
			models = append(models, Model{ID: model.ID, Name: model.Name, ContextWindow: model.ContextWindow, Capabilities: model.Capabilities})
		}
		return models, nil
	}
	return ParseModelEntries(body)
}

// ParseModels extracts model IDs from the supported catalog shapes:
// OpenAI "data":[{"id"}], Gemini "models":[{"name"}], and a bare array of IDs.
func ParseModels(body []byte) ([]string, error) {
	models, err := ParseModelEntries(body)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, nil
}

// catalogEntry is one model of an OpenAI-, Gemini- or Ollama-style list. The
// reference reads the id from id, name or model (route.js:26-29,
// v1/models/route.js:261) and takes the list from a bare array or the first of
// data, models and results (route.js:26-29).
type catalogEntry struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Model            string          `json:"model"`
	DisplayName      string          `json:"display_name"`
	DisplayNameCamel string          `json:"displayName"`
	ContextWindow    int             `json:"context_window"`
	ContextLength    int             `json:"context_length"`
	MaxInputTokens   int             `json:"max_input_tokens"`
	InputTokenLimit  int             `json:"inputTokenLimit"`
	Capabilities     json.RawMessage `json:"capabilities"`
}

func ParseModelEntries(body []byte) ([]Model, error) {
	var envelope struct {
		Data    []catalogEntry `json:"data"`
		Models  []catalogEntry `json:"models"`
		Results []catalogEntry `json:"results"`
	}
	envelopeErr := json.Unmarshal(body, &envelope)
	var entries []catalogEntry
	switch {
	case envelopeErr == nil:
		entries = append(append(append(entries, envelope.Data...), envelope.Models...), envelope.Results...)
	default:
		// A syntax error can still leave partially populated slices. Never trust a
		// partial decode: a malformed catalog must not replace the durable catalog,
		// so only a clean envelope or a clean bare-array decode is accepted.
		var bare []string
		if json.Unmarshal(body, &bare) == nil {
			for _, id := range normalizeAndSort(bare) {
				entries = append(entries, catalogEntry{ID: id})
			}
			break
		}
		if json.Unmarshal(body, &entries) != nil {
			return nil, errors.New("model discovery returned invalid JSON")
		}
	}
	seen := make(map[string]bool)
	models := make([]Model, 0, len(entries))
	for _, entry := range entries {
		id := entry.ID
		name := ""
		if strings.TrimSpace(id) != "" {
			name = entry.Name
		} else if id = entry.Name; strings.TrimSpace(id) == "" {
			id = entry.Model
		}
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if strings.TrimSpace(name) == "" {
			name = entry.DisplayName
		}
		if strings.TrimSpace(name) == "" {
			name = entry.DisplayNameCamel
		}
		model := Model{ID: id, Name: strings.TrimSpace(name), ContextWindow: entry.ContextWindow}
		for _, window := range []int{entry.ContextLength, entry.MaxInputTokens, entry.InputTokenLimit} {
			if model.ContextWindow == 0 {
				model.ContextWindow = window
			}
		}
		// Provider-specific capability objects are not the runtime's string vocabulary.
		var capabilities []string
		if json.Unmarshal(entry.Capabilities, &capabilities) == nil {
			model.Capabilities = capabilities
		}
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func normalizeAndSort(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	models := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		id = strings.TrimPrefix(id, "models/")
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models
}
