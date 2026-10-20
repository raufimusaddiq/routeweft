package usage

import (
	"errors"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// Readers returns the usage client for every provider whose quota LiteRouter can
// read, keyed by provider id.
//
// The set is the reference dispatcher's table (open-sse/services/usage.js
// USAGE_HANDLERS), not the registry's `usage: true` advertisement: iflow has a
// handler without the flag and qoder advertises without one, so the handler table
// is the source of truth for what can actually be read. The parity gate compares
// this map against the oracle.
//
// A provider absent here has no usage endpoint; the observer reports
// ErrNotConfigured and routing treats that as "not exhausted", never as healthy.
func Readers(client HTTPClient, now func() time.Time) (map[string]quota.UsageClient, error) {
	// A nil client fails every usage read per call, which looks like a provider
	// outage rather than a wiring mistake. It is refused where it is supplied, with
	// an error: a library must not panic on its caller's wiring mistake.
	if client == nil {
		return nil, ErrNoClient
	}
	shared := Client{HTTP: client, Now: now}
	readers := make(map[string]quota.UsageClient, 25)
	for _, id := range []string{
		"glm", "glm-cn", "minimax", "minimax-cn", "codebuddy-cn", "codebuddy-intl",
		"deepseek", "groq", "kenari", "opencode-go", "commandcode", "kimi",
		"vercel-ai-gateway", "ollama", "qoder", "xiaomi-mimo", "zed",
	} {
		readers[id] = apiKeyReader{Client: shared, Name: id}
	}
	readers["github"] = githubReader{Client: shared}
	readers["gemini-cli"] = geminiReader{Client: shared}
	readers["kiro"] = kiroReader{Client: shared}
	readers["antigravity"] = antigravityReader{Client: shared}
	readers["grok-cli"] = grokCLIReader{Client: shared}
	return readers, nil
}

// ErrNoClient means the caller supplied no HTTP client.
var ErrNoClient = errors.New("an HTTP client is required; without one every quota read fails at request time")
