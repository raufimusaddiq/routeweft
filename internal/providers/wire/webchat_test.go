package wire

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

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

func TestGrokWebStreamCompletesWhatTokensDidNotCarry(t *testing.T) {
	for name, input := range map[string]string{
		"whole message only": `{"result":{"response":{"modelResponse":{"message":"All of it"}}}}`,
		"message extends":    `{"result":{"response":{"token":"All "}}}` + "\n" + `{"result":{"response":{"modelResponse":{"message":"All of it"}}}}`,
	} {
		chunks, done, err := streamChunks(t, func(w io.Writer) error {
			return GrokWebStreamToChat(context.Background(), strings.NewReader(input), w, "grok-4")
		})
		if err != nil || !done || contents(chunks, "content") != "All of it" {
			t.Errorf("%s: content=%q done=%v err=%v", name, contents(chunks, "content"), done, err)
		}
	}
}

func TestGrokWebFailuresAreNotAnswers(t *testing.T) {
	for name, input := range map[string]string{
		"error event":  `{"result":{"response":{"token":"par"}}}` + "\n" + `{"error":{"message":"rate limited","code":8}}`,
		"error code":   `{"error":{"code":8}}`,
		"empty answer": `{"result":{"response":{"llmInfo":{"modelHash":"h"}}}}`,
		"empty body":   ``,
		"oversized":    strings.Repeat("x", webMaxLine+1),
	} {
		chunks, done, err := streamChunks(t, func(w io.Writer) error {
			return GrokWebStreamToChat(context.Background(), strings.NewReader(input), w, "grok-4")
		})
		if err == nil || done {
			t.Errorf("%s streamed as a success: done=%v err=%v", name, done, err)
		}
		if strings.Contains(contents(chunks, "content"), "[Error") {
			t.Errorf("%s: error text was delivered as content", name)
		}
		if _, err := GrokWebResponseToChat([]byte(input), "grok-4"); err == nil {
			t.Errorf("%s: buffered answer succeeded", name)
		}
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
	if _, ok := got["usage"]; ok {
		t.Fatal("usage was invented; the site reports none")
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

func pplxParams(t *testing.T, chat map[string]any, model string) (string, map[string]any) {
	t.Helper()
	body, err := PerplexityWebRequest(chat, model)
	if err != nil {
		t.Fatal(err)
	}
	got := webObject(t, body)
	params := got["params"].(map[string]any)
	if got["query_str"] != params["query_str"] {
		t.Fatal("query_str differs between envelope and params")
	}
	return got["query_str"].(string), params
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
	want := `{"history":[{"content":"first <q> & more","role":"user"},{"content":"premier","role":"assistant"}],` +
		`"instructions":["answer in French","Available tools (reference only, cannot invoke):\n- lookup: Find a thing.\n- unnamed: ","You have built-in web search. Answer questions directly using search results."],"query":"second"}`
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
	if _, err := PerplexityWebRequest(map[string]any{"messages": []any{}}, "pplx-auto"); err == nil {
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

func TestPerplexityWebStreamToChat(t *testing.T) {
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return PerplexityWebStreamToChat(context.Background(), strings.NewReader(perplexityStream), w, "pplx-sonar")
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
	body, err := PerplexityWebResponseToChat([]byte(perplexityStream), "pplx-sonar")
	if err != nil {
		t.Fatal(err)
	}
	got := webObject(t, body)
	message := got["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "It is sunny. Bring a hat" || !strings.HasPrefix(message["reasoning_content"].(string), "Searching: weather\nReading: https://a") {
		t.Fatalf("message = %v", message)
	}
	if _, ok := got["usage"]; ok {
		t.Fatal("usage was invented; the site reports none")
	}
}

func TestPerplexityWebStreamCompletesAWholeAnswer(t *testing.T) {
	input := pplxBlocks(markdown("DONE", `["Whole ","answer[2]"]`))
	chunks, done, err := streamChunks(t, func(w io.Writer) error {
		return PerplexityWebStreamToChat(context.Background(), strings.NewReader(input), w, "pplx-sonar")
	})
	if err != nil || !done || contents(chunks, "content") != "Whole answer" {
		t.Fatalf("content=%q done=%v err=%v", contents(chunks, "content"), done, err)
	}
	// An answer carried as plain text is read the same way (:279-287).
	text := "data: {\"text\":\"Plain\"}\n\ndata: {\"text\":\"Plain text\"}\n\n"
	chunks, _, err = streamChunks(t, func(w io.Writer) error {
		return PerplexityWebStreamToChat(context.Background(), strings.NewReader(text), w, "pplx-sonar")
	})
	if err != nil || contents(chunks, "content") != "Plain text" {
		t.Fatalf("text content=%q err=%v", contents(chunks, "content"), err)
	}
}

func TestPerplexityWebFailuresAreNotAnswers(t *testing.T) {
	for name, input := range map[string]string{
		"error message": pplxBlocks(markdown("IN_PROGRESS", `["par"]`)) + "data: {\"error_message\":\"blocked\"}\n\n",
		"error code":    "data: {\"error_code\":\"RATE_LIMIT\"}\n\n",
		"numeric code":  "data: {\"error_code\":429}\n\n",
		"no answer":     "data: {\"backend_uuid\":\"b\"}\n\n",
		"thinking only": "data: {\"blocks\":[{\"intended_usage\":\"plan\",\"plan_block\":{\"goals\":[{\"description\":\"g\"}]}}]}\n\n",
		"empty":         "",
	} {
		chunks, done, err := streamChunks(t, func(w io.Writer) error {
			return PerplexityWebStreamToChat(context.Background(), strings.NewReader(input), w, "pplx-sonar")
		})
		if err == nil || done {
			t.Errorf("%s streamed as a success: done=%v err=%v", name, done, err)
		}
		if strings.Contains(contents(chunks, "content"), "[Error") {
			t.Errorf("%s: error text was delivered as content", name)
		}
		if _, err := PerplexityWebResponseToChat([]byte(input), "pplx-sonar"); err == nil {
			t.Errorf("%s: buffered answer succeeded", name)
		}
	}
}

func TestPerplexityWebStreamStopsWhenTheClientDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := PerplexityWebStreamToChat(ctx, strings.NewReader(perplexityStream), io.Discard, "pplx-sonar")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestPerplexityWebEndOfStreamEventEndsTheAnswer(t *testing.T) {
	input := pplxBlocks(markdown("IN_PROGRESS", `["Done"]`)) + "event: end_of_stream\ndata: {\"blocks\":" + markdown("IN_PROGRESS", `["late"]`) + "}\n\n"
	body, err := PerplexityWebResponseToChat([]byte(input), "pplx-sonar")
	if err != nil || !strings.Contains(string(body), `"content":"Done"`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
}
