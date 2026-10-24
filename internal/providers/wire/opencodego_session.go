package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const (
	openCodeGoSessionHeader = "x-opencode-session"
	openCodeGoMaxSession    = 256
	openCodeGoAssistantMin  = 50 // sessionManager.js:89-90
)

var (
	claudeCodeSession       = regexp.MustCompile(`_session_([a-f0-9-]+)$`)
	antigravityConversation = regexp.MustCompile(`(?i)^[a-z]+/([0-9a-f-]{36})/`)
	// sessionHeaderKeys are the client headers that carry a session, in priority
	// order (sessionManager.js:95).
	sessionHeaderKeys = []string{"x-session-id", "session-id", "session_id", "x-amp-thread-id", "x-opencode-session"}
)

// openCodeGoSessionFallback is the header a request with no client of its own
// carries (a model probe): the connection's session (opencode-go.js:168-169).
func openCodeGoSessionFallback(provider routing.ProviderRef, body []byte, _ string) (map[string]string, error) {
	return openCodeGoSession(provider, nil, body), nil
}

// openCodeGoSession is prepareRequestCredentials (executors/opencode-go.js:139-153):
// a valid x-opencode-session the client sent is the session; otherwise the session
// the request resolves to is namespaced by the detected client and digested to an
// opaque ses_ id, so no other agent's session id reaches the relay.
//
// Divergence from the pin: the pin keeps two random ids in process memory (one per
// assistant-text hash, one per connection). Here the id is the hash itself and a
// digest of the connection, so it is the same across restarts. A request with
// neither still gets a fresh id, as in the pin.
func openCodeGoSession(provider routing.ProviderRef, client http.Header, body []byte) map[string]string {
	if native := normalizeSession(client.Get(openCodeGoSessionHeader)); native != "" {
		return map[string]string{openCodeGoSessionHeader: native}
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	session := resolveSession(client, parsed, provider)
	tool := detectClientTool(client, parsed)
	if tool == "" {
		tool = "generic"
	}
	sum := sha256.Sum256([]byte("opencode-go\x00" + tool + "\x00" + session))
	return map[string]string{openCodeGoSessionHeader: "ses_" + hex.EncodeToString(sum[:])[:32]}
}

// normalizeSession trims and length-caps a candidate; empty means unusable.
func normalizeSession(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(utf16.Encode([]rune(value))) > openCodeGoMaxSession {
		return ""
	}
	return value
}

func stringField(m map[string]any, keys ...string) (string, bool) {
	var current any = m
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current = object[key]
	}
	text, ok := current.(string)
	return text, ok
}

// resolveSession is resolveSessionIdentity (utils/sessionManager.js:138-221) for
// scope "opencode-go".
func resolveSession(client http.Header, body map[string]any, provider routing.ProviderRef) string {
	if userID, ok := stringField(body, "metadata", "user_id"); ok {
		if session := claudeSessionFromUserID(userID); session != "" {
			return "claude:" + session
		}
	}
	if session := normalizeSession(client.Get("x-claude-code-session-id")); session != "" {
		return "claude:" + session
	}
	if session := antigravitySession(body); session != "" {
		return "antigravity:" + session
	}
	for _, key := range sessionHeaderKeys {
		if session := normalizeSession(client.Get(key)); session != "" {
			return session
		}
	}
	if session := normalizeSession(client.Get("x-client-request-id")); session != "" {
		return session
	}
	for _, key := range []string{"prompt_cache_key", "session_id", "conversation_id"} {
		if text, _ := stringField(body, key); normalizeSession(text) != "" {
			return normalizeSession(text)
		}
	}
	if text, _ := stringField(body, "metadata", "user_id"); normalizeSession(text) != "" {
		return normalizeSession(text)
	}
	if hash := assistantTextSession("opencode-go:"+provider.ConnectionID, body); hash != "" {
		return hash
	}
	if session := connectionSession(provider); session != "" {
		return session
	}
	return uuid.NewString()
}

func claudeSessionFromUserID(userID string) string {
	if userID == "" {
		return ""
	}
	if m := claudeCodeSession.FindStringSubmatch(userID); m != nil {
		return m[1]
	}
	if userID[0] == '{' {
		var parsed struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(userID), &parsed) == nil {
			return normalizeSession(parsed.SessionID)
		}
	}
	return ""
}

func antigravitySession(body map[string]any) string {
	if request, ok := body["request"].(map[string]any); ok {
		switch id := request["sessionId"].(type) {
		case string:
			if id != "" {
				return normalizeSession(id)
			}
		case float64:
			return normalizeSession(fmt.Sprint(id))
		}
	}
	if id, ok := body["requestId"].(string); ok {
		if m := antigravityConversation.FindStringSubmatch(id); m != nil {
			return normalizeSession(m[1])
		}
	}
	return ""
}

// assistantTextSession keys a conversation on the first assistant text it holds
// (sessionManager.js:160-198): the same text opens the same session.
func assistantTextSession(scope string, body map[string]any) string {
	items, _ := body["messages"].([]any)
	if items == nil {
		items, _ = body["input"].([]any)
	}
	var text []uint16
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["role"] != "assistant" {
			continue
		}
		switch content := item["content"].(type) {
		case string:
			text = append(text, utf16.Encode([]rune(content))...)
		case []any:
			for _, part := range content {
				object, _ := part.(map[string]any)
				piece, _ := object["text"].(string)
				if piece == "" {
					piece, _ = object["output"].(string)
				}
				text = append(text, utf16.Encode([]rune(piece))...)
			}
		}
		if len(text) >= openCodeGoAssistantMin {
			break
		}
	}
	if len(text) < openCodeGoAssistantMin {
		return ""
	}
	sum := sha256.Sum256([]byte(scope + ":" + string(utf16.Decode(text[:openCodeGoAssistantMin]))))
	return hex.EncodeToString(sum[:])[:16]
}

// detectClientTool is utils/clientDetector.js:20-49.
func detectClientTool(client http.Header, body map[string]any) string {
	ua := strings.ToLower(client.Get("User-Agent"))
	app := strings.ToLower(client.Get("x-app"))
	intent := strings.ToLower(client.Get("openai-intent"))
	initiator := strings.ToLower(client.Get("x-initiator"))
	originator := strings.ToLower(client.Get("originator"))
	switch {
	case body["userAgent"] == "antigravity":
		return "antigravity"
	case strings.Contains(ua, "githubcopilotchat") || intent == "conversation-panel" || initiator == "user":
		return "github-copilot"
	case strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude-code") || app == "cli":
		return "claude"
	case strings.Contains(ua, "gemini-cli"):
		return "gemini-cli"
	case strings.Contains(ua, "codex-tui") || strings.Contains(ua, "codex-cli") || strings.Contains(ua, "codex_cli_rs") ||
		strings.Contains(ua, "codex desktop") || strings.HasPrefix(originator, "codex_"):
		return "codex"
	case strings.Contains(ua, "deepseek-tui"):
		return "deepseek-tui"
	}
	return ""
}
