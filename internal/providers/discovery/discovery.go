// Package discovery fetches a provider's live model catalog over its
// compatible /models endpoint and merges it into the runtime catalog without
// dropping operator-managed models (SPEC §7, PROVIDER_BASELINE §7).
//
// Discovery is advisory: a failure never deletes a previously configured or
// manually added model, and unknown IDs stay routable for passthrough
// providers when the operator policy allows it.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const maxModelsResponseBytes = 4 << 20

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
}

// AuthStyle is a supported discovery credential style.
type AuthStyle string

const (
	// AuthBearer sends "Authorization: Bearer <key>".
	AuthBearer AuthStyle = ""
	// AuthXApiKey sends "x-api-key: <key>" (Anthropic-compatible gateways).
	AuthXApiKey AuthStyle = "x-api-key"
	// AuthNone sends no credential (no-auth/free providers).
	AuthNone AuthStyle = "none"
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
	if strings.TrimSpace(request.ProviderID) == "" {
		return nil, errors.New("provider id is required")
	}
	validateURL := transport.ValidatePublicURL
	if c.AllowPrivateUpstreams {
		validateURL = transport.ValidateTrustedLocalURL
	}
	base, err := validateURL(request.BaseURL)
	if err != nil {
		return nil, err
	}
	path := strings.Trim(strings.TrimSpace(request.Path), "/")
	if path == "" {
		path = "models"
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("discovery path must not traverse paths")
		}
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + path
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
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
		}
	default:
		if request.Credential != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+request.Credential)
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
		return nil, fmt.Errorf("model discovery returned HTTP %d", response.StatusCode)
	}
	return ParseModels(body)
}

// ParseModels extracts model IDs from the supported catalog shapes:
// OpenAI "data":[{"id"}], Gemini "models":[{"name"}], and a bare array of IDs.
func ParseModels(body []byte) ([]string, error) {
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	envelopeErr := json.Unmarshal(body, &envelope)
	// A syntax error can still leave partially populated slices. Never trust a
	// partial decode: a malformed catalog must not replace the durable catalog,
	// so only a clean envelope or a clean bare-array decode is accepted.
	if envelopeErr != nil {
		var bare []string
		if json.Unmarshal(body, &bare) != nil {
			return nil, errors.New("model discovery returned invalid JSON")
		}
		return normalizeAndSort(bare), nil
	}
	seen := make(map[string]bool)
	models := make([]string, 0, len(envelope.Data)+len(envelope.Models))
	add := func(id string) {
		id = strings.TrimSpace(id)
		id = strings.TrimPrefix(id, "models/")
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	for _, entry := range envelope.Data {
		add(entry.ID)
	}
	for _, entry := range envelope.Models {
		add(entry.Name)
	}
	sort.Strings(models)
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
