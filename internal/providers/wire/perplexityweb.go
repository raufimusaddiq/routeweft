package wire

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Perplexity Web is the perplexity.ai chat page's own SSE endpoint, reached with
// the account's next-auth session cookie (E/perplexity-web.js, registry/
// perplexity-web.js:19-23). Like Grok Web the pin converts in its executor, and
// these are that executor's builder, headers and reader. Not routable until the
// owner approves it (registry.notReady, ingress.unwired).
//
// The pin's Bearer branch (:445-446) is not carried: nothing at the pin can put an
// access token on a perplexity-web connection. The call chain, at 1c3b9f47:
// connections are created by POST /api/providers, which stores only `apiKey`
// (src/app/api/providers/route.js:179-189); updated by PUT /api/providers/[id],
// which writes `apiKey` only for authType apikey ([id]/route.js:96-122); and
// accessToken is written by the OAuth routes, which exchange tokens against a
// registered OAuth provider (src/app/api/oauth/[provider]/[action]/route.js) that
// perplexity-web is not (no entry under src/lib/oauth). The executor reads it back
// at src/sse/services/auth.js:198. A Routeweft connection of this provider is
// AuthCookie only (registry builtins), so no producer exists here either.

const (
	perplexityWebEndpoint = "rest/sse/perplexity_ask"
	perplexityAPIVersion  = "2.18"
	// perplexityQueryLimit is the pin's cut of the encoded query (:208).
	perplexityQueryLimit = 96000
)

// perplexityModels is MODEL_MAP (E/perplexity-web.js:10-18): mode, model preference.
var perplexityModels = map[string][2]string{
	"pplx-auto":     {"concise", "pplx_pro"},
	"pplx-sonar":    {"copilot", "experimental"},
	"pplx-gpt":      {"copilot", "gpt54"},
	"pplx-gemini":   {"copilot", "gemini31pro_high"},
	"pplx-sonnet":   {"copilot", "claude46sonnet"},
	"pplx-opus":     {"copilot", "claude46opus"},
	"pplx-nemotron": {"copilot", "nv_nemotron_3_super"},
}

// perplexityThinking is THINKING_MAP (:20-24).
var perplexityThinking = map[string]string{
	"pplx-gpt":    "gpt54_thinking",
	"pplx-sonnet": "claude46sonnetthinking",
	"pplx-opus":   "claude46opusthinking",
}

var perplexityWebHook = Hook{
	RequestHeaders:    perplexityWebHeaders,
	Target:            func(routing.ProviderRef) (Target, error) { return Target{Endpoint: perplexityWebEndpoint}, nil },
	NoOpenAIBearer:    true,
	Codec:             perplexityCodec,
	RequireCredential: true,
	Do:                webDo(perplexityHTTPMessage),
}

// perplexityHTTPMessage is the pin's wording for a refused request (E/perplexity-web.js:469-471).
func perplexityHTTPMessage(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "Perplexity auth failed — session cookie may be expired. Re-paste your __Secure-next-auth.session-token."
	case http.StatusTooManyRequests:
		return "Perplexity rate limited. Wait a moment and retry."
	}
	return fmt.Sprintf("Perplexity returned HTTP %d", status)
}

// perplexityWebHeaders is the browser identity the pin presents (E/perplexity-web.js:435-449):
// a Chrome 130 tab on Linux. Declared, not hidden. The credential is checked as a
// cookie value rather than pasted into the header unexamined (:447-448).
func perplexityWebHeaders(provider routing.ProviderRef, _ []byte, _ string) (map[string]string, error) {
	cookie, err := webCookie("__Secure-next-auth.session-token", provider.APIToken, "__Secure-next-auth.session-token=")
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Content-Type":     "application/json",
		"Accept":           "text/event-stream",
		"Cookie":           cookie,
		"Origin":           "https://www.perplexity.ai",
		"Referer":          "https://www.perplexity.ai/",
		"User-Agent":       "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
		"X-App-ApiClient":  "default",
		"X-App-ApiVersion": perplexityAPIVersion,
	}, nil
}

// perplexitySessionTTL and perplexitySessionCap are the pin's follow-up cache bounds (:34-35).
const (
	perplexitySessionTTL = time.Hour
	perplexitySessionCap = 200
)

// PerplexitySessions is the pin's follow-up cache (:34-75): a finished turn's
// backend_uuid, found again by the transcript that led to it, so the next request
// sends only its new message. The pin keys by a 32-bit FNV hash of the transcript
// alone, in one process-wide map. Here the key is SHA-256 over the connection and
// the length-prefixed transcript, so a thread is continued only on the account that
// produced it (after an account fallback the lookup misses, which is the pin's own
// cache-miss path: the whole transcript is sent) and two transcripts cannot collide
// into one thread. It is process-local and bounded; nothing is persisted.
type PerplexitySessions struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]perplexitySession
	now     func() time.Time
}

type perplexitySession struct {
	backendUUID string
	at          time.Time
}

// NewPerplexitySessions returns an empty cache.
func NewPerplexitySessions() *PerplexitySessions {
	return &PerplexitySessions{entries: map[[sha256.Size]byte]perplexitySession{}, now: time.Now}
}

type pplxTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func sessionKey(account string, turns []pplxTurn) [sha256.Size]byte {
	hash := sha256.New()
	field := func(text string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(text)))
		hash.Write(size[:])
		hash.Write([]byte(text))
	}
	field(account)
	for _, turn := range turns {
		field(turn.Role)
		field(turn.Content)
	}
	var key [sha256.Size]byte
	copy(key[:], hash.Sum(nil))
	return key
}

// lookup is sessionLookup (:50-60). An empty account never continues a thread.
func (s *PerplexitySessions) lookup(account string, history []pplxTurn) string {
	if account == "" || len(history) == 0 {
		return ""
	}
	key := sessionKey(account, history)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return ""
	}
	if s.now().Sub(entry.at) > perplexitySessionTTL {
		delete(s.entries, key)
		return ""
	}
	return entry.backendUUID
}

// store is sessionStore (:62-75): the key is the transcript including this turn.
func (s *PerplexitySessions) store(account string, history []pplxTurn, current, answer, backendUUID string) {
	if account == "" || backendUUID == "" {
		return
	}
	full := append(append([]pplxTurn(nil), history...), pplxTurn{"user", current}, pplxTurn{"assistant", answer})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[sessionKey(account, full)] = perplexitySession{backendUUID, s.now()}
	if len(s.entries) > perplexitySessionCap {
		var oldest [sha256.Size]byte
		var oldestAt time.Time
		first := true
		for key, entry := range s.entries {
			if first || entry.at.Before(oldestAt) {
				oldest, oldestAt, first = key, entry.at, false
			}
		}
		delete(s.entries, oldest)
	}
}

// PerplexityExchange is one request and its answer. Begin builds the envelope; the
// two readers return the answer as Chat and remember the thread for the next turn.
type PerplexityExchange struct {
	Body     []byte
	model    string
	account  string
	history  []pplxTurn
	current  string
	sessions *PerplexitySessions
}

// Begin builds the perplexity_ask envelope from a Chat body (E/perplexity-web.js:396-433,
// 160-209). account is the connection the request is sent as. The transcript, system text
// and tool list travel as one JSON document in query_str, or, when the cache knows the
// thread, only the new message.
func (s *PerplexitySessions) Begin(account string, chat map[string]any, model string) (*PerplexityExchange, error) {
	messages, err := webMessages(chat)
	if err != nil {
		return nil, err
	}
	var system strings.Builder
	var history []pplxTurn
	for _, m := range messages {
		switch m.role {
		case "system":
			system.WriteString(m.text + "\n")
		case "user", "assistant":
			history = append(history, pplxTurn{m.role, m.text})
		}
	}
	current := ""
	if n := len(history); n > 0 && history[n-1].Role == "user" {
		current = history[n-1].Content
		history = history[:n-1]
	}

	mode, preference := "copilot", model
	if mapped, ok := perplexityModels[model]; ok {
		mode, preference = mapped[0], mapped[1]
	}
	if thinking, _ := chat["thinking"].(bool); thinking || reasoningRequested(chat["reasoning_effort"]) {
		if variant, ok := perplexityThinking[model]; ok {
			mode, preference = "copilot", variant
		}
	}

	followUp := s.lookup(account, history)
	query := current
	if followUp == "" {
		query, err = perplexityQuery(system.String(), chat["tools"], history, current)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("query is empty after processing")
	}

	zone := time.Now().Location().String()
	if zone == "Local" {
		zone = "UTC"
	}
	var last any
	if followUp != "" {
		last = followUp
	}
	body, err := json.Marshal(map[string]any{
		"query_str": query,
		"params": map[string]any{
			"query_str": query, "search_focus": "internet", "mode": mode, "model_preference": preference,
			"sources": []string{"web"}, "attachments": []any{},
			"frontend_uuid": uuid.NewString(), "frontend_context_uuid": uuid.NewString(),
			"version": perplexityAPIVersion, "language": "en-US", "timezone": zone,
			"search_recency_filter": nil, "is_incognito": true, "use_schematized_api": true,
			"last_backend_uuid": last,
		},
	})
	if err != nil {
		return nil, err
	}
	return &PerplexityExchange{Body: body, model: model, account: account, history: history, current: current, sessions: s}, nil
}

// perplexityQuery is buildQuery without a thread (:195-209), cut to its last 96000 units.
func perplexityQuery(system string, tools any, history []pplxTurn, current string) (string, error) {
	instructions := []string{}
	if text := strings.TrimSpace(system); text != "" {
		instructions = append(instructions, text)
	}
	if hint := perplexityToolsHint(tools); hint != "" {
		instructions = append(instructions, hint)
	}
	instructions = append(instructions, "You have built-in web search. Answer questions directly using search results.")
	document := struct {
		Instructions []string   `json:"instructions"`
		History      []pplxTurn `json:"history,omitempty"`
		Query        *string    `json:"query,omitempty"`
	}{Instructions: instructions, History: history}
	if current != "" || len(history) == 0 {
		document.Query = &current
	}
	encoded, err := marshalPlain(document)
	if err != nil {
		return "", err
	}
	return lastUTF16(string(encoded), perplexityQueryLimit), nil
}

func reasoningRequested(effort any) bool {
	if effort == nil {
		return false
	}
	text, isText := effort.(string)
	return !isText || text != "none"
}

// perplexityToolsHint is formatToolsHint (:184-193): the tools are named for the
// model's reference only; the site cannot call them.
func perplexityToolsHint(tools any) string {
	list, _ := tools.([]any)
	if len(list) == 0 {
		return ""
	}
	lines := make([]string, 0, len(list))
	for _, item := range list {
		tool, _ := item.(map[string]any)
		if fn, ok := tool["function"].(map[string]any); ok {
			tool = fn
		}
		name, _ := tool["name"].(string)
		if name == "" {
			name = "unnamed"
		}
		description, _ := tool["description"].(string)
		description, _, _ = strings.Cut(description, "\n")
		lines = append(lines, "- "+name+": "+firstUTF16(description, 200))
	}
	return "Available tools (reference only, cannot invoke):\n" + strings.Join(lines, "\n")
}

// lastUTF16 and firstUTF16 cut by UTF-16 units, as the pin's slice does.
func lastUTF16(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[len(units)-n:]))
}

func firstUTF16(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

var (
	pplxCitation = regexp.MustCompile(`\[\d+\]`)
	pplxMarkup   = regexp.MustCompile(`(?s)<grok:[^>]*>.*?</grok:[^>]*>|<grok:[^>]*/>|<\?xml[^?]*\?>|(?i)</?response\b[^>]*>`)
	pplxSpaces   = regexp.MustCompile(` {2,}`)
	pplxNewlines = regexp.MustCompile(`\n{3,}`)
)

// cleanPerplexity is cleanResponse (:77-90): citation markers and stray markup are
// removed; whole answers are also whitespace-normalized.
func cleanPerplexity(text string, whole bool) string {
	text = pplxMarkup.ReplaceAllString(pplxCitation.ReplaceAllString(text, ""), "")
	if whole {
		text = strings.TrimSpace(pplxNewlines.ReplaceAllString(pplxSpaces.ReplaceAllString(text, " "), "\n\n"))
	}
	return text
}

// perplexityReader reads the SSE events (E/perplexity-web.js:92-136, 211-292).
type perplexityReader struct {
	backendUUID string
	data        []string // data: lines of the event being read
	answer      string
	seen        int // runes of answer already delivered
	thinking    map[string]bool
}

type pplxEvent struct {
	BackendUUID  string          `json:"backend_uuid"`
	ErrorCode    json.RawMessage `json:"error_code"`
	ErrorMessage string          `json:"error_message"`
	Text         string          `json:"text"`
	Final        bool            `json:"final"`
	Status       string          `json:"status"`
	Blocks       []struct {
		Usage string `json:"intended_usage"`
		Plan  *struct {
			Goals []struct {
				Description string `json:"description"`
			} `json:"goals"`
			Steps []struct {
				Type string `json:"step_type"`
				Web  struct {
					Queries []struct {
						Query string `json:"query"`
					} `json:"queries"`
				} `json:"search_web_content"`
				Read struct {
					URLs []string `json:"urls"`
				} `json:"read_results_content"`
			} `json:"steps"`
		} `json:"plan_block"`
		Markdown *struct {
			Progress string   `json:"progress"`
			Chunks   []string `json:"chunks"`
		} `json:"markdown_block"`
	} `json:"blocks"`
}

// pplxStep is what one event adds: reasoning lines and the new answer text.
type pplxStep struct {
	thinking []string
	delta    string
	stop     bool
}

// feed takes one SSE line. A blank line closes the event; `event: end_of_stream`
// and `[DONE]` end the stream.
func (p *perplexityReader) feed(line string) (pplxStep, error) {
	switch {
	case line == "event: end_of_stream":
		return pplxStep{stop: true}, nil
	case strings.HasPrefix(line, "data:"):
		p.data = append(p.data, strings.TrimLeft(line[len("data:"):], " \t"))
		return pplxStep{}, nil
	case line != "":
		return pplxStep{}, nil
	}
	return p.flush()
}

func (p *perplexityReader) flush() (pplxStep, error) {
	if len(p.data) == 0 {
		return pplxStep{}, nil
	}
	payload := strings.TrimSpace(strings.Join(p.data, "\n"))
	p.data = nil
	if payload == "" || payload == "[DONE]" {
		return pplxStep{stop: true}, nil
	}
	var event pplxEvent
	if json.Unmarshal([]byte(payload), &event) != nil {
		return pplxStep{}, nil
	}
	return p.event(event)
}

func (p *perplexityReader) event(event pplxEvent) (pplxStep, error) {
	if jsTruthy(event.ErrorCode) || event.ErrorMessage != "" {
		message := event.ErrorMessage
		if message == "" {
			message = "Perplexity error: " + rawText(event.ErrorCode)
		}
		return pplxStep{}, &WebAnswerError{message, "PPLX_ERROR"}
	}
	if event.BackendUUID != "" {
		p.backendUUID = event.BackendUUID
	}
	if p.thinking == nil {
		p.thinking = map[string]bool{}
	}
	var step pplxStep
	note := func(text string) {
		if text != "" && !p.thinking[text] {
			p.thinking[text] = true
			step.thinking = append(step.thinking, text)
		}
	}
	for _, block := range event.Blocks {
		if block.Plan != nil && block.Usage == "pro_search_steps" {
			for _, s := range block.Plan.Steps {
				switch s.Type {
				case "SEARCH_WEB":
					for _, q := range s.Web.Queries {
						note("Searching: " + q.Query)
					}
				case "READ_RESULTS":
					for i, u := range s.Read.URLs {
						if i < 3 {
							note("Reading: " + u)
						}
					}
				}
			}
		}
		if block.Plan != nil && block.Usage == "plan" {
			for _, g := range block.Plan.Goals {
				note(g.Description)
			}
		}
		if !strings.Contains(block.Usage, "markdown") || block.Markdown == nil || len(block.Markdown.Chunks) == 0 {
			continue
		}
		joined := strings.Join(block.Markdown.Chunks, "")
		if block.Markdown.Progress == "DONE" {
			p.answer = joined
			continue
		}
		cumulative := p.answer + joined
		if runes := []rune(cumulative); len(runes) > p.seen {
			step.delta += string(runes[p.seen:])
			p.answer, p.seen = cumulative, len(runes)
		}
	}
	if len(event.Blocks) == 0 {
		if text := strings.TrimSpace(event.Text); len([]rune(text)) > p.seen {
			step.delta += string([]rune(text)[p.seen:])
			p.answer, p.seen = text, len([]rune(text))
		}
	}
	step.stop = event.Final || event.Status == "COMPLETED"
	return step, nil
}

// run feeds every line of src to the reader and calls emit with each step. A
// failure the site reports is returned as *WebAnswerError; so is a failed read.
func (p *perplexityReader) run(ctx context.Context, src io.Reader, emit func(pplxStep) error) error {
	stopped := false
	err := webLines(ctx, src, func(line string) (bool, error) {
		step, err := p.feed(line)
		if err != nil {
			return false, err
		}
		if err := emit(step); err != nil {
			return false, err
		}
		stopped = step.stop
		return step.stop, nil
	})
	if err != nil || stopped {
		return err
	}
	step, err := p.flush()
	if err != nil {
		return err
	}
	return emit(step)
}

// ResponseToChat folds a complete SSE answer into one Chat body (E/perplexity-web.js:357-389).
// The site's own failure is a *WebAnswerError for the caller to answer 502 with.
func (x *PerplexityExchange) ResponseToChat(body []byte) ([]byte, error) {
	var p perplexityReader
	var reasoning []string
	err := p.run(context.Background(), strings.NewReader(string(body)), func(step pplxStep) error {
		reasoning = append(reasoning, step.thinking...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	answer := cleanPerplexity(p.answer, true)
	x.remember(answer, p.backendUUID)
	return newWebChunkWriter(io.Discard, "pplx", x.model).completion(answer, strings.Join(reasoning, "\n"), estimateTokens(x.current))
}

// StreamToChat relays Perplexity's SSE as a Chat stream (E/perplexity-web.js:294-355).
// Answer text is cleaned per delta, as in the pin (:324-327), so a citation marker
// split across two deltas is not removed. A site failure or a broken read ends the
// stream in the pin's frames and is then returned, so the dispatcher can tell the
// attempt failed. As at the pin, the thread is remembered after a site failure too.
func (x *PerplexityExchange) StreamToChat(ctx context.Context, src io.Reader, dst io.Writer) error {
	out := newWebChunkWriter(dst, "pplx", x.model)
	if err := out.chunk(map[string]any{"role": "assistant"}, nil); err != nil {
		return err
	}
	var p perplexityReader
	err := p.run(ctx, src, func(step pplxStep) error {
		for _, line := range step.thinking {
			if err := out.chunk(map[string]any{"reasoning_content": line + "\n"}, nil); err != nil {
				return err
			}
		}
		if text := cleanPerplexity(step.delta, false); text != "" {
			return out.chunk(map[string]any{"content": text}, nil)
		}
		return nil
	})
	var failure *WebAnswerError
	switch {
	case errors.As(err, &failure):
		if err := out.failed(failure.Message); err != nil {
			return err
		}
		x.remember(cleanPerplexity(p.answer, true), p.backendUUID)
		return failure
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		if werr := out.broke(err); werr != nil {
			return werr
		}
		return &WebAnswerError{err.Error(), "PPLX_ERROR"}
	}
	if err := out.done(); err != nil {
		return err
	}
	x.remember(cleanPerplexity(p.answer, true), p.backendUUID)
	return nil
}

func (x *PerplexityExchange) remember(answer, backendUUID string) {
	x.sessions.store(x.account, x.history, x.current, answer, backendUUID)
}
