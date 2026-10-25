package wire

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Grok Web is the grok.com chat page's own endpoint, reached with the account's
// `sso` session cookie (E/grok-web.js, registry/grok-web.js:20-23). The pin
// converts in its executor, not in a translator leg, so these are the executor's
// request builder, headers and NDJSON reader. They are not routable until the
// owner approves it (registry.notReady, ingress.unwired).

const (
	grokWebEndpoint = "rest/app-chat/conversations/new"
	// grokWebDefaultModel is the pin's fallback for an id it does not map (:236-237).
	grokWebDefaultModel = "grok-4.1-fast"
)

// grokWebModel is MODEL_MAP (E/grok-web.js:9-24). The pin's isThinking column is
// only read by a branch that can never run (thinkOpened is never set, :106,121),
// so it is not carried.
type grokWebModel struct{ name, mode string }

var grokWebModels = map[string]grokWebModel{
	"grok-3":            {"grok-3", "MODEL_MODE_GROK_3"},
	"grok-3-mini":       {"grok-3", "MODEL_MODE_GROK_3_MINI_THINKING"},
	"grok-3-thinking":   {"grok-3", "MODEL_MODE_GROK_3_THINKING"},
	"grok-4":            {"grok-4", "MODEL_MODE_GROK_4"},
	"grok-4-mini":       {"grok-4-mini", "MODEL_MODE_GROK_4_MINI_THINKING"},
	"grok-4-thinking":   {"grok-4", "MODEL_MODE_GROK_4_THINKING"},
	"grok-4-heavy":      {"grok-4", "MODEL_MODE_HEAVY"},
	"grok-4.1-mini":     {"grok-4-1-thinking-1129", "MODEL_MODE_GROK_4_1_MINI_THINKING"},
	"grok-4.1-fast":     {"grok-4-1-thinking-1129", "MODEL_MODE_FAST"},
	"grok-4.1-expert":   {"grok-4-1-thinking-1129", "MODEL_MODE_EXPERT"},
	"grok-4.1-thinking": {"grok-4-1-thinking-1129", "MODEL_MODE_GROK_4_1_THINKING"},
	"grok-4.2":          {"grok-420", "MODEL_MODE_GROK_420"},
	"grok-4.20":         {"grok-420", "MODEL_MODE_GROK_420"},
	"grok-4.20-beta":    {"grok-420", "MODEL_MODE_GROK_420"},
}

var grokWebHook = Hook{
	RequestHeaders:    grokWebHeaders,
	Target:            func(routing.ProviderRef) (Target, error) { return Target{Endpoint: grokWebEndpoint}, nil },
	NoOpenAIBearer:    true,
	Codec:             grokCodec,
	RequireCredential: true,
	Do:                webDo(grokHTTPMessage),
}

// grokHTTPMessage is the pin's wording for a refused request (E/grok-web.js:309-311).
func grokHTTPMessage(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "Grok auth failed — SSO cookie may be expired. Re-paste your sso cookie value from grok.com."
	case http.StatusTooManyRequests:
		return "Grok rate limited. Wait a moment and retry, or rotate cookies."
	}
	return fmt.Sprintf("Grok returned HTTP %d", status)
}

// grokWebHeaders is the browser identity the pin presents (E/grok-web.js:263-290).
// It is declared, not hidden: Routeweft is a server posing as a Chrome 136 tab on
// macOS, because that is what the site's chat endpoint answers. Differences from
// the pin: Accept-Encoding is left to the Go transport (the pin's br/zstd value,
// :265, would arrive undecoded here), and the credential is checked as a cookie
// value rather than pasted into the header unexamined (:285-290).
func grokWebHeaders(provider routing.ProviderRef, _ []byte, _ string) (map[string]string, error) {
	cookie, err := webCookie("sso", provider.APIToken, "sso=")
	if err != nil {
		return nil, err
	}
	trace, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	span, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	statsig, err := grokStatsigID()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Accept":             "*/*",
		"Accept-Language":    "en-US,en;q=0.9",
		"Baggage":            "sentry-environment=production,sentry-release=d6add6fb0460641fd482d767a335ef72b9b6abb8,sentry-public_key=b311e0f2690c81f25e2c4cf6d4f7ce1c",
		"Cache-Control":      "no-cache",
		"Content-Type":       "application/json",
		"Cookie":             cookie,
		"Origin":             "https://grok.com",
		"Pragma":             "no-cache",
		"Referer":            "https://grok.com/",
		"Sec-Ch-Ua":          `"Google Chrome";v="136", "Chromium";v="136", "Not(A:Brand";v="24"`,
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"macOS"`,
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "same-origin",
		"User-Agent":         "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
		"X-Statsig-Id":       statsig,
		"X-Xai-Request-Id":   uuid.NewString(),
		"Traceparent":        "00-" + trace + "-" + span + "-00",
	}, nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// grokStatsigID is generateStatsigId (E/grok-web.js:33-38): not a measurement of
// anything but a base64 string shaped like a browser TypeError message, which is
// what the pin sends in place of the page's real anti-bot value. Whether the live
// site still accepts it is not provable offline; a refusal surfaces as the 401/403
// the site returns.
func grokStatsigID() (string, error) {
	pick := func(n int, digits bool) (string, error) {
		alphabet := "abcdefghijklmnopqrstuvwxyz"
		if digits {
			alphabet += "0123456789"
		}
		out := make([]byte, n)
		for i := range out {
			j, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			out[i] = alphabet[j.Int64()]
		}
		return string(out), nil
	}
	coin, err := rand.Int(rand.Reader, big.NewInt(2))
	if err != nil {
		return "", err
	}
	var message string
	if coin.Int64() == 0 {
		name, err := pick(5, true)
		if err != nil {
			return "", err
		}
		message = fmt.Sprintf(`e:TypeError: Cannot read properties of null (reading 'children["%s"]')`, name)
	} else {
		name, err := pick(10, false)
		if err != nil {
			return "", err
		}
		message = fmt.Sprintf("e:TypeError: Cannot read properties of undefined (reading '%s')", name)
	}
	return base64.StdEncoding.EncodeToString([]byte(message)), nil
}

// GrokWebRequest builds the conversation envelope from a Chat body
// (E/grok-web.js:226-259). The transcript is flattened into one message: every
// turn but the last user turn is prefixed with its role (:61-72).
func GrokWebRequest(chat map[string]any, model string) ([]byte, error) {
	messages, err := webMessages(chat)
	if err != nil {
		return nil, err
	}
	last := -1
	for i, m := range messages {
		if m.role == "user" {
			last = i
		}
	}
	parts := make([]string, len(messages))
	for i, m := range messages {
		parts[i] = m.role + ": " + m.text
		if i == last {
			parts[i] = m.text
		}
	}
	text := strings.Join(parts, "\n\n")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("query is empty after processing")
	}
	mapped, ok := grokWebModels[model]
	if !ok {
		mapped = grokWebModels[grokWebDefaultModel]
	}
	return json.Marshal(map[string]any{
		"temporary": true, "modelName": mapped.name, "modelMode": mapped.mode, "message": text,
		"fileAttachments": []any{}, "imageAttachments": []any{},
		"disableSearch": false, "enableImageGeneration": false, "returnImageBytes": false,
		"returnRawGrokInXaiRequest": false, "enableImageStreaming": false, "imageGenerationCount": 0,
		"forceConcise": false, "toolOverrides": map[string]any{}, "enableSideBySide": true, "sendFinalMetadata": true,
		"isReasoning": false, "disableTextFollowUps": false, "disableMemory": true,
		"forceSideBySide": false, "isAsyncChat": false, "disableSelfHarmShortCircuit": false,
		"deviceEnvInfo": map[string]any{
			"darkModeEnabled": false, "devicePixelRatio": 2,
			"screenWidth": 2056, "screenHeight": 1329, "viewportWidth": 2056, "viewportHeight": 1083,
		},
	})
}

// grokReader reads Grok's NDJSON (E/grok-web.js:74-133). A line that is not JSON is
// skipped, as the pin does.
type grokReader struct {
	fingerprint string
	streamed    strings.Builder // tokens seen so far
	full        string          // modelResponse.message, the whole answer when sent
}

// read handles one line and returns the token it carries. An `error` event is the
// site's failure (:109-112): its message, else "Grok error: <code>".
func (g *grokReader) read(line string) (token string, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	var event struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			Response *struct {
				LLMInfo       struct{ ModelHash string } `json:"llmInfo"`
				ModelResponse *struct {
					Message  string `json:"message"`
					Metadata struct {
						LLMInfo struct {
							ModelHash string `json:"modelHash"`
						} `json:"llm_info"`
					} `json:"metadata"`
				} `json:"modelResponse"`
				Token *string `json:"token"`
			} `json:"response"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return "", nil
	}
	if jsTruthy(event.Error) {
		var failure map[string]json.RawMessage
		_ = json.Unmarshal(event.Error, &failure)
		var message string
		_ = json.Unmarshal(failure["message"], &message)
		if message != "" {
			return "", &WebAnswerError{message, "GROK_ERROR"}
		}
		code := "undefined"
		if value, ok := failure["code"]; ok && string(value) != "null" {
			code = rawText(value)
		}
		return "", &WebAnswerError{"Grok error: " + code, "GROK_ERROR"}
	}
	resp := event.Result.Response
	if resp == nil {
		return "", nil
	}
	if resp.LLMInfo.ModelHash != "" && g.fingerprint == "" {
		g.fingerprint = resp.LLMInfo.ModelHash
	}
	if mr := resp.ModelResponse; mr != nil {
		if mr.Message != "" {
			g.full = mr.Message
		}
		if hash := mr.Metadata.LLMInfo.ModelHash; hash != "" {
			g.fingerprint = hash
		}
		return "", nil
	}
	if resp.Token != nil {
		g.streamed.WriteString(*resp.Token)
		return *resp.Token, nil
	}
	return "", nil
}

// rawText is a JSON value as a JavaScript template literal prints it.
func rawText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return strings.TrimSpace(string(raw))
}

// answer is the buffered answer: the whole message when the site sent one, else
// the tokens (:204-206).
func (g *grokReader) answer() string {
	if g.full != "" {
		return g.full
	}
	return g.streamed.String()
}

// GrokWebResponseToChat folds a complete NDJSON answer into one Chat body
// (E/grok-web.js:190-219). The site's own failure is a *WebAnswerError for the
// caller to answer 502 with; an empty answer is an empty answer, as at the pin.
func GrokWebResponseToChat(body []byte, model string) ([]byte, error) {
	var g grokReader
	err := webLines(context.Background(), strings.NewReader(string(body)), func(line string) (bool, error) {
		_, err := g.read(line)
		return false, err
	})
	if err != nil {
		return nil, err
	}
	out := newWebChunkWriter(io.Discard, "grok", model)
	out.fingerprint = g.fingerprint
	answer := g.answer()
	// The pin's prompt estimate is the answer's length too (:211-212).
	return out.completion(answer, "", estimateTokens(answer))
}

// GrokWebStreamToChat relays Grok's NDJSON as a Chat stream (E/grok-web.js:135-188).
// Only tokens are streamed. A site failure or a broken read ends the stream in the
// pin's frames and is then returned, so the dispatcher can tell the attempt failed.
func GrokWebStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, model string) error {
	out := newWebChunkWriter(dst, "grok", model)
	if err := out.chunk(map[string]any{"role": "assistant"}, nil); err != nil {
		return err
	}
	var g grokReader
	var failure *WebAnswerError
	err := webLines(ctx, src, func(line string) (bool, error) {
		token, err := g.read(line)
		if errors.As(err, &failure) {
			return true, nil
		}
		if err != nil || token == "" {
			return false, err
		}
		out.fingerprint = g.fingerprint
		return false, out.chunk(map[string]any{"content": token}, nil)
	})
	out.fingerprint = g.fingerprint
	switch {
	case failure != nil:
		if err := out.failed(failure.Message); err != nil {
			return err
		}
		return failure
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		if werr := out.broke(err); werr != nil {
			return werr
		}
		return &WebAnswerError{err.Error(), "GROK_ERROR"}
	}
	return out.done()
}
