package wire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
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
// Two things the pin does are deliberately not carried:
//   - the process-wide follow-up cache (:34-75, 421-423) maps a transcript hash to
//     a backend_uuid with no account in the key, so after an account fallback it
//     would continue account A's thread with account B's cookie. Every request
//     sends the whole transcript instead (last_backend_uuid is null);
//   - the Bearer branch (:445-446): nothing in the pin stores an access token for
//     this provider, whose only credential is the pasted cookie.

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
	RequireCredential: true,
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

// PerplexityWebRequest builds the perplexity_ask envelope from a Chat body
// (E/perplexity-web.js:396-433, 160-209). The transcript, system text and tool
// list travel as one JSON document in query_str.
func PerplexityWebRequest(chat map[string]any, model string) ([]byte, error) {
	messages, err := webMessages(chat)
	if err != nil {
		return nil, err
	}
	var system strings.Builder
	var history []map[string]string
	for _, m := range messages {
		switch m.role {
		case "system":
			system.WriteString(m.text + "\n")
		case "user", "assistant":
			history = append(history, map[string]string{"role": m.role, "content": m.text})
		}
	}
	current := ""
	if n := len(history); n > 0 && history[n-1]["role"] == "user" {
		current = history[n-1]["content"]
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

	instructions := []string{}
	if text := strings.TrimSpace(system.String()); text != "" {
		instructions = append(instructions, text)
	}
	if hint := perplexityToolsHint(chat["tools"]); hint != "" {
		instructions = append(instructions, hint)
	}
	instructions = append(instructions, "You have built-in web search. Answer questions directly using search results.")
	document := map[string]any{"instructions": instructions}
	if len(history) > 0 {
		document["history"] = history
	}
	if current != "" || len(history) == 0 {
		document["query"] = current
	}
	encoded, err := marshalPlain(document)
	if err != nil {
		return nil, err
	}
	query := lastUTF16(string(encoded), perplexityQueryLimit)
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("query is empty after processing")
	}

	zone := time.Now().Location().String()
	if zone == "Local" {
		zone = "UTC"
	}
	return json.Marshal(map[string]any{
		"query_str": query,
		"params": map[string]any{
			"query_str": query, "search_focus": "internet", "mode": mode, "model_preference": preference,
			"sources": []string{"web"}, "attachments": []any{},
			"frontend_uuid": uuid.NewString(), "frontend_context_uuid": uuid.NewString(),
			"version": perplexityAPIVersion, "language": "en-US", "timezone": zone,
			"search_recency_filter": nil, "is_incognito": true, "use_schematized_api": true,
			"last_backend_uuid": nil,
		},
	})
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
	data     []string // data: lines of the event being read
	answer   string
	seen     int // runes of answer already delivered
	thinking map[string]bool
}

type pplxEvent struct {
	ErrorCode    any    `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	Text         string `json:"text"`
	Final        bool   `json:"final"`
	Status       string `json:"status"`
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
	if event.ErrorMessage != "" {
		return pplxStep{}, errors.New("perplexity error: " + event.ErrorMessage)
	}
	if event.ErrorCode != nil && event.ErrorCode != "" && event.ErrorCode != float64(0) {
		return pplxStep{}, errors.New("perplexity error: " + stringify(event.ErrorCode))
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

func stringify(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

var errPerplexityNoAnswer = errors.New("perplexity returned no answer")

// run feeds every line of src to the reader and calls emit with each step.
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

// PerplexityWebResponseToChat folds a complete SSE answer into one Chat body.
func PerplexityWebResponseToChat(body []byte, model string) ([]byte, error) {
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
	if answer == "" {
		return nil, errPerplexityNoAnswer
	}
	return newWebChunkWriter(io.Discard, "pplx", model).completion(answer, strings.Join(reasoning, "\n"))
}

// PerplexityWebStreamToChat relays Perplexity's SSE as a Chat stream. Answer text
// is cleaned per delta, as in the pin (:324-327), so a citation marker split
// across two deltas is not removed. An answer the site sent whole is completed at
// the end so that a client never reads an empty reply for a non-empty one.
func PerplexityWebStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, model string) error {
	out := newWebChunkWriter(dst, "pplx", model)
	if err := out.chunk(map[string]any{"role": "assistant"}, nil); err != nil {
		return err
	}
	var p perplexityReader
	sent := false
	emit := func(step pplxStep) error {
		for _, line := range step.thinking {
			if err := out.chunk(map[string]any{"reasoning_content": line + "\n"}, nil); err != nil {
				return err
			}
		}
		if text := cleanPerplexity(step.delta, false); text != "" {
			sent = true
			return out.chunk(map[string]any{"content": text}, nil)
		}
		return nil
	}
	if err := p.run(ctx, src, emit); err != nil {
		return err
	}
	if runes := []rune(p.answer); len(runes) > p.seen {
		if err := emit(pplxStep{delta: string(runes[p.seen:])}); err != nil {
			return err
		}
	}
	if !sent {
		return errPerplexityNoAnswer
	}
	return out.done()
}
