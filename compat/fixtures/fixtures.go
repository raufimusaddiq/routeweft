// Package fixtures loads deterministic protocol wire fixtures.
//
// Fixtures are the compatibility oracle named by SPEC §28.3: adapters and
// ingress tests compare real bytes against these recordings instead of
// hand-maintained struct literals. Only synthetic content belongs here.
package fixtures

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed *.json
var files embed.FS

// Protocol names one public wire protocol (SPEC §10).
type Protocol string

const (
	OpenAIChat      Protocol = "openai-chat"
	OpenAIResponses Protocol = "openai-responses"
	Anthropic       Protocol = "anthropic-messages"
	Gemini          Protocol = "gemini"
	Ollama          Protocol = "ollama"
	SystemOne       Protocol = "systemone"
)

// Kind classifies what an exchange proves.
type Kind string

const (
	KindNonStreaming  Kind = "non-streaming"
	KindStreaming     Kind = "streaming"
	KindCancellation  Kind = "cancellation"
	KindAuthError     Kind = "auth-error"
	KindUpstreamError Kind = "upstream-error"
)

// HTTP is one recorded request or response.
type HTTP struct {
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// Usage is the token accounting asserted by a fixture, when present.
type Usage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	CacheReadTokens   int `json:"cacheReadTokens"`
	CacheCreateTokens int `json:"cacheCreateTokens"`
}

// Exchange is one recorded request/response pair.
type Exchange struct {
	ID          string   `json:"id"`
	Protocol    Protocol `json:"protocol"`
	Kind        Kind     `json:"kind"`
	Description string   `json:"description"`
	Request     HTTP     `json:"request"`
	Response    HTTP     `json:"response"`
	Usage       *Usage   `json:"usage,omitempty"`
	// Terminal is the exact final stream marker when a fixture is streaming.
	Terminal string `json:"terminal,omitempty"`
}

// Load returns every embedded fixture sorted by id.
func Load() ([]Exchange, error) {
	names, err := fs.Glob(files, "*.json")
	if err != nil {
		return nil, err
	}
	loaded := make([]Exchange, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		blob, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read fixture %s: %w", name, err)
		}
		var exchange Exchange
		decoder := json.NewDecoder(strings.NewReader(string(blob)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&exchange); err != nil {
			return nil, fmt.Errorf("decode fixture %s: %w", name, err)
		}
		if exchange.ID == "" {
			return nil, fmt.Errorf("fixture %s: id is required", name)
		}
		if want := strings.TrimSuffix(path.Base(name), ".json"); want != exchange.ID {
			return nil, fmt.Errorf("fixture %s: id %q does not match filename", name, exchange.ID)
		}
		if seen[exchange.ID] {
			return nil, fmt.Errorf("duplicate fixture id %q", exchange.ID)
		}
		seen[exchange.ID] = true
		if err := exchange.validate(name); err != nil {
			return nil, err
		}
		loaded = append(loaded, exchange)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].ID < loaded[j].ID })
	return loaded, nil
}

func (e Exchange) validate(name string) error {
	switch e.Protocol {
	case OpenAIChat, OpenAIResponses, Anthropic, Gemini, Ollama, SystemOne:
	default:
		return fmt.Errorf("fixture %s: unknown protocol %q", name, e.Protocol)
	}
	switch e.Kind {
	case KindNonStreaming, KindStreaming, KindCancellation, KindAuthError, KindUpstreamError:
	default:
		return fmt.Errorf("fixture %s: unknown kind %q", name, e.Kind)
	}
	if e.Description == "" {
		return fmt.Errorf("fixture %s: description is required", name)
	}
	if e.Request.Method == "" || e.Request.Path == "" {
		return fmt.Errorf("fixture %s: request method and path are required", name)
	}
	if e.Response.Status == 0 {
		return fmt.Errorf("fixture %s: response status is required", name)
	}
	if e.Kind == KindStreaming && e.Terminal == "" {
		return fmt.Errorf("fixture %s: streaming fixtures must declare a terminal marker", name)
	}
	if e.Kind != KindStreaming && e.Terminal != "" {
		return fmt.Errorf("fixture %s: terminal marker is only valid for completed streaming fixtures", name)
	}
	if len(e.Response.Body) == 0 {
		return fmt.Errorf("fixture %s: response body is required", name)
	}
	return nil
}
