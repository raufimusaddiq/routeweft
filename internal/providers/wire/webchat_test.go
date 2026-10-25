package wire

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Offline fixtures: the shapes are the pin's own (E/grok-web.js:103-133,
// E/perplexity-web.js:92-136, 211-292), with invented text and credentials.

const fixtureCookie = "fixture-session-cookie-value"

func webRef(id, token string) routing.ProviderRef {
	return routing.ProviderRef{ProviderID: id, Protocol: id, APIToken: token, BaseURL: "https://web.example"}
}

func webObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, body)
	}
	return out
}

// streamChunks runs a stream codec and returns the Chat chunk payloads and whether [DONE] closed it.
func streamChunks(t *testing.T, stream func(io.Writer) error) (chunks []map[string]any, done bool, err error) {
	t.Helper()
	var out bytes.Buffer
	err = stream(&out)
	for _, block := range strings.Split(out.String(), "\n\n") {
		payload, ok := strings.CutPrefix(block, "data: ")
		switch {
		case !ok:
		case payload == "[DONE]":
			done = true
		default:
			chunks = append(chunks, webObject(t, []byte(payload)))
		}
	}
	return chunks, done, err
}

func contents(chunks []map[string]any, key string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		delta := chunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if text, ok := delta[key].(string); ok {
			out.WriteString(text)
		}
	}
	return out.String()
}

func TestWebProvidersAreDispatchedWithoutBearer(t *testing.T) {
	for _, id := range []string{"grok-web", "perplexity-web"} {
		if got := OpenAIHeaders(webRef(id, fixtureCookie)).Get("Authorization"); got != "" {
			t.Errorf("%s sent the session cookie as a bearer: %q", id, got)
		}
		if !RequiresCredential(id) {
			t.Errorf("%s would dispatch without a session", id)
		}
	}
	for id, endpoint := range map[string]string{"grok-web": "rest/app-chat/conversations/new", "perplexity-web": "rest/sse/perplexity_ask"} {
		target, err := ResolveTarget(webRef(id, fixtureCookie), "chat/completions")
		if err != nil || target.Endpoint != endpoint || target.BaseURL != "https://web.example" {
			t.Errorf("%s target = %+v, %v", id, target, err)
		}
	}
}

func TestWebSessionRefusalNeverEchoesTheCredential(t *testing.T) {
	for _, id := range []string{"grok-web", "perplexity-web"} {
		for _, token := range []string{"", "  ", "SECRETMARK;x=1", "SECRETMARK b", "SECRETMARK\r\nX-Injected: 1", `SECRETMARK"`, "SECRETMARKé"} {
			_, err := For(id).RequestHeaders(webRef(id, token), nil, "")
			if !errors.Is(err, ErrWebSession) {
				t.Errorf("%s %q: err = %v", id, token, err)
			} else if strings.Contains(err.Error(), "SECRETMARK") {
				t.Errorf("%s: error echoes the credential: %v", id, err)
			}
		}
	}
	if _, err := For("grok-web").RequestHeaders(webRef("grok-web", "sso="), nil, ""); !errors.Is(err, ErrWebSession) {
		t.Fatalf("a bare sso= prefix is a cookie with no value: %v", err)
	}
}

func TestGrokWebHeadersAreTheDeclaredIdentity(t *testing.T) {
	for _, token := range []string{fixtureCookie, "sso=" + fixtureCookie, " sso=" + fixtureCookie + " "} {
		headers, err := For("grok-web").RequestHeaders(webRef("grok-web", token), nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if headers["Cookie"] != "sso="+fixtureCookie {
			t.Fatalf("cookie = %q", headers["Cookie"])
		}
		if _, ok := headers["Accept-Encoding"]; ok {
			t.Fatal("Accept-Encoding would defeat the transport's decoding")
		}
		for name, want := range map[string]string{
			"Origin": "https://grok.com", "Referer": "https://grok.com/", "Sec-Ch-Ua-Platform": `"macOS"`,
			"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
		} {
			if headers[name] != want {
				t.Errorf("%s = %q, want %q", name, headers[name], want)
			}
		}
		statsig, err := base64.StdEncoding.DecodeString(headers["X-Statsig-Id"])
		if err != nil || !regexp.MustCompile(`^e:TypeError: Cannot read properties of (null \(reading 'children\["[a-z0-9]{5}"\]'\)|undefined \(reading '[a-z]{10}'\))$`).Match(statsig) {
			t.Fatalf("statsig id %q: %v", statsig, err)
		}
		if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-00$`).MatchString(headers["Traceparent"]) {
			t.Fatalf("traceparent %q", headers["Traceparent"])
		}
	}
	a, _ := For("grok-web").RequestHeaders(webRef("grok-web", fixtureCookie), nil, "")
	b, _ := For("grok-web").RequestHeaders(webRef("grok-web", fixtureCookie), nil, "")
	if a["X-Xai-Request-Id"] == b["X-Xai-Request-Id"] || a["Traceparent"] == b["Traceparent"] {
		t.Fatal("per-request ids repeat")
	}
}

func TestGrokWebRequestFlattensTheTranscript(t *testing.T) {
	chat := map[string]any{"messages": []any{
		map[string]any{"role": "developer", "content": "be brief"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}, map[string]any{"type": "image_url"}, map[string]any{"type": "text", "text": "there"}}},
		map[string]any{"role": "assistant", "content": "hello"},
		map[string]any{"role": "user", "content": "   "},
		map[string]any{"role": "user", "content": "and now?"},
	}}
	body, err := GrokWebRequest(chat, "grok-4-heavy")
	if err != nil {
		t.Fatal(err)
	}
	got := webObject(t, body)
	if got["message"] != "system: be brief\n\nuser: hi there\n\nassistant: hello\n\nand now?" {
		t.Fatalf("message = %q", got["message"])
	}
	if got["modelName"] != "grok-4" || got["modelMode"] != "MODEL_MODE_HEAVY" || got["temporary"] != true || got["disableMemory"] != true {
		t.Fatalf("envelope = %v", got)
	}
	fallback, err := GrokWebRequest(chat, "unmapped-model")
	if err != nil {
		t.Fatal(err)
	}
	if g := webObject(t, fallback); g["modelName"] != "grok-4-1-thinking-1129" || g["modelMode"] != "MODEL_MODE_FAST" {
		t.Fatalf("unmapped model envelope = %v", g)
	}
	for _, empty := range []map[string]any{{}, {"messages": []any{}}, {"messages": []any{map[string]any{"role": "user", "content": " "}}}} {
		if _, err := GrokWebRequest(empty, "grok-3"); err == nil {
			t.Errorf("%v built an envelope", empty)
		}
	}
}

const grokStream = `{"result":{"response":{"llmInfo":{"modelHash":"hash-1"},"responseId":"r1"}}}
not json
{"result":{"response":{"token":"Hel"}}}
{"result":{"response":{"token":"lo"}}}
{"result":{"response":{"token":""}}}
{"result":{"response":{"modelResponse":{"message":"Hello","metadata":{"llm_info":{"modelHash":"hash-2"}}}}}}
`

func TestGrokWebStreamToChat(t *testing.T) {
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return GrokWebStreamToChat(context.Background(), strings.NewReader(grokStream), w, "grok-4")
	})
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if contents(chunks, "content") != "Hello" || contents(chunks, "role") != "assistant" {
		t.Fatalf("chunks = %v", chunks)
	}
	last := chunks[len(chunks)-1]
	if last["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" || last["system_fingerprint"] != "hash-2" || last["model"] != "grok-4" {
		t.Fatalf("terminal chunk = %v", last)
	}
}

// The pin streams tokens only, and an answer that arrives whole streams as nothing
// (E/grok-web.js:164-169); the buffered answer is the whole message (:204-206).
func TestGrokWebWholeAnswerIsNotInventedOnAStream(t *testing.T) {
	input := `{"result":{"response":{"modelResponse":{"message":"All of it"}}}}`
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return GrokWebStreamToChat(context.Background(), strings.NewReader(input), w, "grok-4")
	})
	if err != nil || !done || contents(chunks, "content") != "" {
		t.Fatalf("content=%q done=%v err=%v", contents(chunks, "content"), done, err)
	}
	body, err := GrokWebResponseToChat([]byte(input), "grok-4")
	if err != nil || !strings.Contains(string(body), `"content":"All of it"`) {
		t.Fatalf("buffered = %s, %v", body, err)
	}
}

func TestGrokWebEmptyAnswerIsEmptyAsAtThePin(t *testing.T) {
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return GrokWebStreamToChat(context.Background(), strings.NewReader(""), w, "grok-4")
	})
	if err != nil || !done || contents(chunks, "content") != "" {
		t.Fatalf("stream content=%q done=%v err=%v", contents(chunks, "content"), done, err)
	}
	body, err := GrokWebResponseToChat(nil, "grok-4")
	if err != nil || !strings.Contains(string(body), `"content":""`) {
		t.Fatalf("buffered = %s, %v", body, err)
	}
}

func TestGrokWebStreamFailureIsThePinsFrameAndIsReported(t *testing.T) {
	for input, message := range map[string]string{
		`{"result":{"response":{"token":"par"}}}` + "\n" + `{"error":{"message":"rate limited","code":8}}`: "rate limited",
		`{"error":{"code":8}}`:  "Grok error: 8",
		`{"error":{}}`:          "Grok error: undefined",
		`{"error":"just text"}`: "Grok error: undefined",
	} {
		chunks, done, err := streamChunks(t, func(w io.Writer) error {
			return GrokWebStreamToChat(context.Background(), strings.NewReader(input), w, "grok-4")
		})
		var failure *WebAnswerError
		if !errors.As(err, &failure) || failure.Message != message || failure.Code != "GROK_ERROR" {
			t.Fatalf("err = %v, want %q", err, message)
		}
		if !done || !strings.Contains(contents(chunks, "content"), "[Error: "+message+"]") {
			t.Fatalf("frames = %v done=%v", chunks, done)
		}
		last := chunks[len(chunks)-1]["choices"].([]any)[0].(map[string]any)
		if last["finish_reason"] != "stop" {
			t.Fatalf("terminal chunk = %v", last)
		}
		_, err = GrokWebResponseToChat([]byte(input), "grok-4")
		if !errors.As(err, &failure) || string(failure.Body()) != `{"error":{"code":"GROK_ERROR","message":`+strconv.Quote(message)+`,"type":"upstream_error"}}` {
			t.Fatalf("buffered failure = %v", err)
		}
	}
}

func TestGrokWebBrokenReadIsThePinsStreamErrorFrame(t *testing.T) {
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return GrokWebStreamToChat(context.Background(), strings.NewReader(`{"result":{"response":{"token":"a"}}}`+"\n"+strings.Repeat("x", webMaxLine+1)), w, "grok-4")
	})
	var failure *WebAnswerError
	if !errors.As(err, &failure) || !done || !strings.Contains(contents(chunks, "content"), "a[Stream error: ") {
		t.Fatalf("err=%v done=%v chunks=%v", err, done, chunks)
	}
}

func TestGrokWebBufferedAnswer(t *testing.T) {
	body, err := GrokWebResponseToChat([]byte(grokStream), "grok-4")
	if err != nil {
		t.Fatal(err)
	}
	got := webObject(t, body)
	choice := got["choices"].([]any)[0].(map[string]any)
	if choice["message"].(map[string]any)["content"] != "Hello" || choice["finish_reason"] != "stop" || got["object"] != "chat.completion" || got["system_fingerprint"] != "hash-2" {
		t.Fatalf("body = %s", body)
	}
	// The pin's estimate: ceil(5/4)=2 for the answer, and the same figure for the prompt (:211-212).
	if usage := got["usage"].(map[string]any); usage["prompt_tokens"] != 2.0 || usage["completion_tokens"] != 2.0 || usage["total_tokens"] != 4.0 {
		t.Fatalf("usage = %v", usage)
	}
}

func TestGrokWebStreamStopsWhenTheClientDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := GrokWebStreamToChat(ctx, strings.NewReader(grokStream), io.Discard, "grok-4")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

type fakeDoer struct{ status int }

func (f fakeDoer) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: f.status, Header: http.Header{"Content-Type": {"text/html"}, "Retry-After": {"7"}}, Body: io.NopCloser(strings.NewReader("<html>challenge SECRETMARK</html>")), Request: r}, nil
}

// A refused request becomes the pin's error document with the status kept, and the
// site's own body (an HTML challenge page) is not relayed.
func TestWebHTTPFailuresArePinsErrorDocuments(t *testing.T) {
	for id, cases := range map[string]map[int]string{
		"grok-web": {401: "Grok auth failed \u2014 SSO cookie may be expired. Re-paste your sso cookie value from grok.com.", 403: "Grok auth failed \u2014 SSO cookie may be expired. Re-paste your sso cookie value from grok.com.",
			429: "Grok rate limited. Wait a moment and retry, or rotate cookies.", 500: "Grok returned HTTP 500"},
		"perplexity-web": {401: "Perplexity auth failed \u2014 session cookie may be expired. Re-paste your __Secure-next-auth.session-token.",
			429: "Perplexity rate limited. Wait a moment and retry.", 503: "Perplexity returned HTTP 503"},
	} {
		for status, message := range cases {
			request, _ := http.NewRequest(http.MethodPost, "https://web.example/x", nil)
			response, err := For(id).Do(webRef(id, fixtureCookie), &http.Client{Transport: fakeDoer{status}}, request)
			if err != nil || response.StatusCode != status || response.Header.Get("Retry-After") != "7" || response.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("%s %d: %v %+v", id, status, err, response)
			}
			body, _ := io.ReadAll(response.Body)
			var document struct {
				Error struct{ Message, Type, Code string }
			}
			if json.Unmarshal(body, &document) != nil || document.Error.Message != strings.ReplaceAll(message, "\\u2014", "\u2014") || document.Error.Type != "upstream_error" || document.Error.Code != "HTTP_"+strconv.Itoa(status) {
				t.Fatalf("%s %d: body %s", id, status, body)
			}
			if strings.Contains(string(body), "SECRETMARK") {
				t.Fatal("site body relayed")
			}
		}
	}
	request, _ := http.NewRequest(http.MethodPost, "https://web.example/x", nil)
	response, err := For("grok-web").Do(webRef("grok-web", fixtureCookie), &http.Client{Transport: fakeDoer{200}}, request)
	if body, _ := io.ReadAll(response.Body); err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "SECRETMARK") {
		t.Fatal("a success was rewritten")
	}
}

func TestPerplexityWebHeadersAreTheDeclaredIdentity(t *testing.T) {
	for _, token := range []string{fixtureCookie, "__Secure-next-auth.session-token=" + fixtureCookie} {
		headers, err := For("perplexity-web").RequestHeaders(webRef("perplexity-web", token), nil, "")
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]string{
			"Cookie": "__Secure-next-auth.session-token=" + fixtureCookie, "Accept": "text/event-stream",
			"Origin": "https://www.perplexity.ai", "Referer": "https://www.perplexity.ai/",
			"X-App-ApiClient": "default", "X-App-ApiVersion": "2.18",
			"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
		} {
			if headers[name] != want {
				t.Errorf("%s = %q, want %q", name, headers[name], want)
			}
		}
	}
}

func pplxBegin(t *testing.T, sessions *PerplexitySessions, account string, chat map[string]any, model string) (*PerplexityExchange, string, map[string]any) {
	t.Helper()
	x, err := sessions.Begin(account, chat, model)
	if err != nil {
		t.Fatal(err)
	}
	got := webObject(t, x.Body)
	params := got["params"].(map[string]any)
	if got["query_str"] != params["query_str"] {
		t.Fatal("query_str differs between envelope and params")
	}
	return x, got["query_str"].(string), params
}

func pplxParams(t *testing.T, chat map[string]any, model string) (string, map[string]any) {
	t.Helper()
	_, query, params := pplxBegin(t, NewPerplexitySessions(), "acct", chat, model)
	return query, params
}

func TestPerplexityWebRequest(t *testing.T) {
	chat := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "answer in French"},
			map[string]any{"role": "user", "content": "first <q> & more"},
			map[string]any{"role": "assistant", "content": "premier"},
			map[string]any{"role": "user", "content": "second"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "description": "Find a thing.\nSecond line"}}, map[string]any{}},
	}
	query, params := pplxParams(t, chat, "pplx-sonnet")
	want := `{"instructions":["answer in French","Available tools (reference only, cannot invoke):\n- lookup: Find a thing.\n- unnamed: ","You have built-in web search. Answer questions directly using search results."],` +
		`"history":[{"role":"user","content":"first <q> & more"},{"role":"assistant","content":"premier"}],"query":"second"}`
	if query != want {
		t.Fatalf("query = %s\nwant    %s", query, want)
	}
	if params["mode"] != "copilot" || params["model_preference"] != "claude46sonnet" || params["last_backend_uuid"] != nil ||
		params["is_incognito"] != true || params["version"] != "2.18" || params["search_focus"] != "internet" || params["timezone"] == "" {
		t.Fatalf("params = %v", params)
	}
	if params["frontend_uuid"] == params["frontend_context_uuid"] {
		t.Fatal("frontend ids are not distinct")
	}

	chat["reasoning_effort"] = "high"
	if _, params := pplxParams(t, chat, "pplx-sonnet"); params["model_preference"] != "claude46sonnetthinking" {
		t.Fatalf("thinking preference = %v", params["model_preference"])
	}
	chat["reasoning_effort"] = "none"
	if _, params := pplxParams(t, chat, "pplx-sonnet"); params["model_preference"] != "claude46sonnet" {
		t.Fatalf("reasoning none changed the model: %v", params["model_preference"])
	}
	chat["thinking"] = true
	if _, params := pplxParams(t, chat, "pplx-auto"); params["mode"] != "concise" || params["model_preference"] != "pplx_pro" {
		t.Fatalf("model without a thinking variant = %v", params)
	}
	if _, params := pplxParams(t, chat, "some-raw-preference"); params["mode"] != "copilot" || params["model_preference"] != "some-raw-preference" {
		t.Fatalf("unmapped model = %v", params)
	}
}

func TestPerplexityWebRequestKeepsTheTailOfALongQuery(t *testing.T) {
	long := strings.Repeat("a", perplexityQueryLimit) + "TAIL"
	query, _ := pplxParams(t, map[string]any{"messages": []any{map[string]any{"role": "user", "content": long}}}, "pplx-auto")
	if len(query) != perplexityQueryLimit || !strings.HasSuffix(query, `TAIL"}`) {
		t.Fatalf("query is %d bytes ending %q", len(query), query[len(query)-8:])
	}
	if _, err := NewPerplexitySessions().Begin("acct", map[string]any{"messages": []any{}}, "pplx-auto"); err == nil {
		t.Fatal("an empty transcript built an envelope")
	}
}

func pplxBlocks(blocks string) string { return "data: {\"blocks\":" + blocks + "}\n\n" }

func markdown(progress, chunks string) string {
	return `[{"intended_usage":"ask_text_0_markdown","markdown_block":{"progress":"` + progress + `","chunks":` + chunks + `}}]`
}

var perplexityStream = "data: {\"backend_uuid\":\"b1\",\"blocks\":[{\"intended_usage\":\"pro_search_steps\",\"plan_block\":{\"steps\":[" +
	"{\"step_type\":\"SEARCH_WEB\",\"search_web_content\":{\"queries\":[{\"query\":\"weather\"}]}}," +
	"{\"step_type\":\"READ_RESULTS\",\"read_results_content\":{\"urls\":[\"https://a\",\"https://b\",\"https://c\",\"https://d\"]}}]}}]}\n\n" +
	"data: {\"blocks\":[{\"intended_usage\":\"plan\",\"plan_block\":{\"goals\":[{\"description\":\"Check the forecast\"}]}}]}\n\n" +
	pplxBlocks(markdown("IN_PROGRESS", `["It is sunny[1]. "]`)) +
	pplxBlocks(markdown("IN_PROGRESS", `["Bring  a hat"]`)) +
	pplxBlocks(markdown("DONE", `["It is sunny[1]. Bring  a hat"]`)) +
	"data: {\"final\":true}\n\n" +
	"data: {\"blocks\":" + markdown("IN_PROGRESS", `["never read"]`) + "}\n\n"

var pplxChat = map[string]any{"messages": []any{map[string]any{"role": "user", "content": "weather?"}}}

func TestPerplexityWebStreamToChat(t *testing.T) {
	x, _, _ := pplxBegin(t, NewPerplexitySessions(), "acct", pplxChat, "pplx-sonar")
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return x.StreamToChat(context.Background(), strings.NewReader(perplexityStream), w)
	})
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if got := contents(chunks, "content"); got != "It is sunny. Bring  a hat" {
		t.Fatalf("content = %q", got)
	}
	if got := contents(chunks, "reasoning_content"); got != "Searching: weather\nReading: https://a\nReading: https://b\nReading: https://c\nCheck the forecast\n" {
		t.Fatalf("reasoning = %q", got)
	}
}

func TestPerplexityWebBufferedAnswer(t *testing.T) {
	x, _, _ := pplxBegin(t, NewPerplexitySessions(), "acct", pplxChat, "pplx-sonar")
	body, err := x.ResponseToChat([]byte(perplexityStream))
	if err != nil {
		t.Fatal(err)
	}
	got := webObject(t, body)
	message := got["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "It is sunny. Bring a hat" || !strings.HasPrefix(message["reasoning_content"].(string), "Searching: weather\nReading: https://a") {
		t.Fatalf("message = %v", message)
	}
	// The pin's estimate: the prompt is the current message (8 -> 2), the completion the answer (24 -> 6).
	if usage := got["usage"].(map[string]any); usage["prompt_tokens"] != 2.0 || usage["completion_tokens"] != 6.0 || usage["total_tokens"] != 8.0 {
		t.Fatalf("usage = %v", usage)
	}
}

// An answer that only arrives as a DONE block is read by the buffered path and
// streams as nothing, as at the pin (:265-267, 323).
func TestPerplexityWebWholeAnswerIsNotInventedOnAStream(t *testing.T) {
	input := pplxBlocks(markdown("DONE", `["Whole ","answer[2]"]`))
	x, _, _ := pplxBegin(t, NewPerplexitySessions(), "acct", pplxChat, "pplx-sonar")
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return x.StreamToChat(context.Background(), strings.NewReader(input), w)
	})
	if err != nil || !done || contents(chunks, "content") != "" {
		t.Fatalf("content=%q done=%v err=%v", contents(chunks, "content"), done, err)
	}
	body, err := x.ResponseToChat([]byte(input))
	if err != nil || !strings.Contains(string(body), `"content":"Whole answer"`) {
		t.Fatalf("buffered = %s, %v", body, err)
	}
	// An answer carried as plain text is read the same way (:279-287).
	text := "data: {\"text\":\"Plain\"}\n\ndata: {\"text\":\"Plain text\"}\n\n"
	chunks, _, err = streamChunks(t, func(w io.Writer) error {
		return x.StreamToChat(context.Background(), strings.NewReader(text), w)
	})
	if err != nil || contents(chunks, "content") != "Plain text" {
		t.Fatalf("text content=%q err=%v", contents(chunks, "content"), err)
	}
}

func TestPerplexityWebEmptyAnswerIsEmptyAsAtThePin(t *testing.T) {
	x, _, _ := pplxBegin(t, NewPerplexitySessions(), "acct", pplxChat, "pplx-sonar")
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return x.StreamToChat(context.Background(), strings.NewReader(""), w)
	})
	if err != nil || !done || contents(chunks, "content") != "" {
		t.Fatalf("stream content=%q done=%v err=%v", contents(chunks, "content"), done, err)
	}
	if body, err := x.ResponseToChat(nil); err != nil || !strings.Contains(string(body), `"content":""`) {
		t.Fatalf("buffered = %s, %v", body, err)
	}
}

func TestPerplexityWebFailureIsThePinsFrameAndIsReported(t *testing.T) {
	for input, message := range map[string]string{
		pplxBlocks(markdown("IN_PROGRESS", `["par"]`)) + "data: {\"error_message\":\"blocked\"}\n\n": "blocked",
		"data: {\"error_code\":\"RATE_LIMIT\"}\n\n":                                                  "Perplexity error: RATE_LIMIT",
		"data: {\"error_code\":429}\n\n":                                                             "Perplexity error: 429",
	} {
		x, _, _ := pplxBegin(t, NewPerplexitySessions(), "acct", pplxChat, "pplx-sonar")
		chunks, done, err := streamChunks(t, func(w io.Writer) error {
			return x.StreamToChat(context.Background(), strings.NewReader(input), w)
		})
		var failure *WebAnswerError
		if !errors.As(err, &failure) || failure.Message != message || failure.Code != "PPLX_ERROR" {
			t.Fatalf("err = %v, want %q", err, message)
		}
		if !done || !strings.Contains(contents(chunks, "content"), "[Error: "+message+"]") {
			t.Fatalf("frames = %v done=%v", chunks, done)
		}
		if _, err := x.ResponseToChat([]byte(input)); !errors.As(err, &failure) || !strings.Contains(string(failure.Body()), `"code":"PPLX_ERROR"`) {
			t.Fatalf("buffered failure = %v", err)
		}
	}
}

func TestPerplexityWebStreamStopsWhenTheClientDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	x, _, _ := pplxBegin(t, NewPerplexitySessions(), "acct", pplxChat, "pplx-sonar")
	if err := x.StreamToChat(ctx, strings.NewReader(perplexityStream), io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestPerplexityWebEndOfStreamEventEndsTheAnswer(t *testing.T) {
	input := pplxBlocks(markdown("IN_PROGRESS", `["Done"]`)) + "event: end_of_stream\ndata: {\"blocks\":" + markdown("IN_PROGRESS", `["late"]`) + "}\n\n"
	x, _, _ := pplxBegin(t, NewPerplexitySessions(), "acct", pplxChat, "pplx-sonar")
	body, err := x.ResponseToChat([]byte(input))
	if err != nil || !strings.Contains(string(body), `"content":"Done"`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

// --- follow-up thread cache: scoped to the account ---

var pplxFollowUp = map[string]any{"messages": []any{
	map[string]any{"role": "user", "content": "weather?"},
	map[string]any{"role": "assistant", "content": "It is sunny. Bring a hat"},
	map[string]any{"role": "user", "content": "and tomorrow?"},
}}

// finish runs the first turn so the cache learns its thread.
func finish(t *testing.T, sessions *PerplexitySessions, account string) {
	t.Helper()
	x, _, _ := pplxBegin(t, sessions, account, pplxChat, "pplx-sonar")
	if _, err := x.ResponseToChat([]byte(perplexityStream)); err != nil {
		t.Fatal(err)
	}
}

func TestPerplexityFollowUpContinuesTheThreadOnTheSameAccountOnly(t *testing.T) {
	sessions := NewPerplexitySessions()
	finish(t, sessions, "account-a")

	_, query, params := pplxBegin(t, sessions, "account-a", pplxFollowUp, "pplx-sonar")
	if query != "and tomorrow?" || params["last_backend_uuid"] != "b1" {
		t.Fatalf("same account: query=%q last=%v", query, params["last_backend_uuid"])
	}
	// After a fallback to another account the thread is not continued: the whole
	// transcript is sent and no backend_uuid of account-a's is presented.
	_, query, params = pplxBegin(t, sessions, "account-b", pplxFollowUp, "pplx-sonar")
	if !strings.HasPrefix(query, `{"instructions"`) || params["last_backend_uuid"] != nil {
		t.Fatalf("other account: query=%q last=%v", query, params["last_backend_uuid"])
	}
	if _, _, params := pplxBegin(t, sessions, "", pplxFollowUp, "pplx-sonar"); params["last_backend_uuid"] != nil {
		t.Fatal("a request with no account continued a thread")
	}
	// A different transcript is a different thread.
	other := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "weather?"},
		map[string]any{"role": "assistant", "content": "Rain"},
		map[string]any{"role": "user", "content": "and tomorrow?"},
	}}
	if _, _, params := pplxBegin(t, sessions, "account-a", other, "pplx-sonar"); params["last_backend_uuid"] != nil {
		t.Fatal("a different transcript continued the thread")
	}
}

func TestPerplexitySessionKeyCannotBeForgedByMovingBoundaries(t *testing.T) {
	a := sessionKey("acct", []pplxTurn{{"user", "a\nassistant:b"}})
	b := sessionKey("acct", []pplxTurn{{"user", "a"}, {"assistant", "b"}})
	c := sessionKey("acct1", []pplxTurn{{"user", "x"}})
	d := sessionKey("acct", []pplxTurn{{"1user", "x"}})
	if a == b || c == d {
		t.Fatal("distinct inputs share a key")
	}
}

func TestPerplexitySessionsExpireAndAreBounded(t *testing.T) {
	sessions := NewPerplexitySessions()
	now := time.Unix(1_000_000, 0)
	sessions.now = func() time.Time { return now }
	finish(t, sessions, "acct")
	now = now.Add(perplexitySessionTTL)
	if got := sessions.lookup("acct", []pplxTurn{{"user", "weather?"}, {"assistant", "It is sunny. Bring a hat"}}); got != "b1" {
		t.Fatalf("an hour-old thread was dropped: %q", got)
	}
	now = now.Add(time.Second)
	if got := sessions.lookup("acct", []pplxTurn{{"user", "weather?"}, {"assistant", "It is sunny. Bring a hat"}}); got != "" {
		t.Fatalf("an expired thread continued: %q", got)
	}
	for i := 0; i < perplexitySessionCap+50; i++ {
		now = now.Add(time.Millisecond)
		sessions.store("acct", nil, "q"+strconv.Itoa(i), "a", "uuid")
	}
	if len(sessions.entries) != perplexitySessionCap {
		t.Fatalf("%d entries, cap %d", len(sessions.entries), perplexitySessionCap)
	}
	// The oldest went first; the newest is still there.
	if sessions.lookup("acct", []pplxTurn{{"user", "q0"}, {"assistant", "a"}}) != "" ||
		sessions.lookup("acct", []pplxTurn{{"user", "q" + strconv.Itoa(perplexitySessionCap+49)}, {"assistant", "a"}}) != "uuid" {
		t.Fatal("eviction did not remove the oldest entry")
	}
}

func TestPerplexitySessionsAreRaceFree(t *testing.T) {
	sessions := NewPerplexitySessions()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(account string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				x, err := sessions.Begin(account, pplxFollowUp, "pplx-sonar")
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = x.ResponseToChat([]byte(perplexityStream))
				sessions.store(account, nil, "q"+strconv.Itoa(i), "a", "uuid")
			}
		}("account-" + strconv.Itoa(g%3))
	}
	wg.Wait()
	if len(sessions.entries) > perplexitySessionCap {
		t.Fatalf("%d entries over the cap", len(sessions.entries))
	}
}

func TestCodecProvidersAreExactlyDeclared(t *testing.T) {
	var got []string
	for _, id := range Registered() {
		if For(id).Codec != nil {
			got = append(got, id)
			if !HasCodec(id) {
				t.Errorf("HasCodec(%q) is false", id)
			}
		}
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"grok-web", "perplexity-web"}) {
		t.Fatalf("codec providers %v", got)
	}
	for _, protocol := range []string{"openai-chat", "commandcode", "", "unknown"} {
		if HasCodec(protocol) {
			t.Errorf("HasCodec(%q)", protocol)
		}
	}
	if _, ok, err := CodecFor(webRef("commandcode", "x"), nil, pplxChat, "m"); ok || err != nil {
		t.Fatalf("a provider without a codec built one: %v %v", ok, err)
	}
}

func TestCodecForBuildsTheWholeConversion(t *testing.T) {
	codec, ok, err := CodecFor(webRef("grok-web", fixtureCookie), nil, pplxChat, "grok-4")
	if !ok || err != nil || codec.Endpoint != "rest/app-chat/conversations/new" || !strings.Contains(string(codec.Body), `"message":"weather?"`) {
		t.Fatalf("grok codec: %+v %v %v", codec, ok, err)
	}
	if answer, err := codec.Response([]byte(grokStream)); err != nil || !strings.Contains(string(answer), `"content":"Hello"`) {
		t.Fatalf("grok response: %s %v", answer, err)
	}

	state := NewCodecState()
	ref := func(connection string) routing.ProviderRef {
		r := webRef("perplexity-web", fixtureCookie)
		r.ConnectionID = connection
		return r
	}
	first, ok, err := CodecFor(ref("a"), state, pplxChat, "pplx-sonar")
	if !ok || err != nil || first.Endpoint != "rest/sse/perplexity_ask" {
		t.Fatalf("perplexity codec: %+v %v %v", first, ok, err)
	}
	if _, err := first.Response([]byte(perplexityStream)); err != nil {
		t.Fatal(err)
	}
	continued := func(connection string) bool {
		c, _, err := CodecFor(ref(connection), state, pplxFollowUp, "pplx-sonar")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(c.Body), `"last_backend_uuid":"b1"`)
	}
	if !continued("a") || continued("b") {
		t.Fatal("the thread must continue on connection a only")
	}
	// No state, no continuation; the request is still built.
	if c, _, err := CodecFor(ref("a"), nil, pplxFollowUp, "pplx-sonar"); err != nil || strings.Contains(string(c.Body), `"last_backend_uuid":"b1"`) {
		t.Fatalf("a request with no state continued a thread: %v", err)
	}
}
