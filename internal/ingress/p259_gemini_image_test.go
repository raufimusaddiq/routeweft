package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// gemini_image_answers_pin.json is written by
// compat/parity/generate-gemini-image-answers.mjs from the pin's own buffered
// answer path (nonStreamingHandler.js translateNonStreamingResponse) and its
// stream legs (translator/index.js translateResponse over gemini-to-openai.js,
// openai-responses.js and openai-to-claude.js), for each Gemini-family wire.
type geminiImagePinAnswer struct {
	Name     string           `json:"name"`
	Target   string           `json:"target"`
	Client   string           `json:"client"`
	Body     json.RawMessage  `json:"body"`
	Buffered map[string]any   `json:"buffered"`
	Streamed []map[string]any `json:"streamed"`
}

func geminiImagePinAnswers(t *testing.T) []geminiImagePinAnswer {
	t.Helper()
	raw, err := os.ReadFile("testdata/gemini_image_answers_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Pin     string                 `json:"pin"`
		Answers []geminiImagePinAnswer `json:"answers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(file.Pin, "1c3b9f47") || len(file.Answers) != 36 {
		t.Fatalf("fixture pin %q with %d answers", file.Pin, len(file.Answers))
	}
	// The file is indented; an upstream sends each body on one line.
	for i := range file.Answers {
		var compact bytes.Buffer
		if err := json.Compact(&compact, file.Answers[i].Body); err != nil {
			t.Fatal(err)
		}
		file.Answers[i].Body = compact.Bytes()
	}
	return file.Answers
}

// pinBufferedText is the answer text the pin's buffered path gives the client:
// a Responses output_text, or the chat.completion content it sends a Chat or a
// Messages client (nonStreamingHandler.js:167-175,191,218-223).
func pinBufferedText(answer geminiImagePinAnswer) string {
	if answer.Client == "openai-responses" {
		var text strings.Builder
		for _, item := range answer.Buffered["output"].([]any) {
			for _, part := range item.(map[string]any)["content"].([]any) {
				text.WriteString(part.(map[string]any)["text"].(string))
			}
		}
		return text.String()
	}
	message := answer.Buffered["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	content, _ := message["content"].(string)
	return content
}

// pinStreamed is what the pin's stream gives the client: its text, and the
// image URLs of a Chat client's delta.images.
func pinStreamed(answer geminiImagePinAnswer) (string, []string) {
	var text strings.Builder
	var images []string
	for _, event := range answer.Streamed {
		switch answer.Client {
		case "openai-responses":
			if data, _ := event["data"].(map[string]any); data["type"] == "response.output_text.delta" {
				text.WriteString(data["delta"].(string))
			}
		case "claude":
			if delta, _ := event["delta"].(map[string]any); event["type"] == "content_block_delta" && delta["type"] == "text_delta" {
				text.WriteString(delta["text"].(string))
			}
		default:
			for _, choice := range event["choices"].([]any) {
				delta, _ := choice.(map[string]any)["delta"].(map[string]any)
				content, _ := delta["content"].(string)
				text.WriteString(content)
				for _, image := range asItems(delta["images"]) {
					images = append(images, image.(map[string]any)["image_url"].(map[string]any)["url"].(string))
				}
			}
		}
	}
	return text.String(), images
}

func asItems(value any) []any {
	items, _ := value.([]any)
	return items
}

// geminiImageTranslation is the attempt Routeweft builds for a client of the
// recorded format on the recorded wire, with a model that is not an image model
// by name, so only the client's wire decides how the picture is carried.
func geminiImageTranslation(t *testing.T, client, target string, stream bool) ChatTranslation {
	t.Helper()
	var translation ChatTranslation
	var err error
	switch client {
	case openai.ResponsesProtocol:
		var request *openai.ResponsesRequest
		request, err = openai.ParseResponsesRequest([]byte(fmt.Sprintf(`{"model":"gemini-2.5-flash","input":"draw a cat","stream":%v}`, stream)), false)
		if err == nil {
			request.Scope, request.Project = "fixture-scope", "fixture-project"
			translation, err = TranslateResponses(request, target)
		}
	case anthropic.MessagesProtocol:
		var request *anthropic.MessagesRequest
		request, err = anthropic.ParseMessagesRequest([]byte(fmt.Sprintf(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"draw a cat"}],"stream":%v}`, stream)))
		if err == nil {
			request.Scope, request.Project = "fixture-scope", "fixture-project"
			translation, err = TranslateMessages(request, target)
		}
	default:
		var request *openai.ChatRequest
		request, err = openai.ParseChatRequest([]byte(fmt.Sprintf(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"draw a cat"}],"stream":%v}`, stream)))
		if err == nil {
			request.Scope, request.Project = "fixture-scope", "fixture-project"
			translation, err = TranslateChat(request, target)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return translation
}

// bufferedClientText reads the answer text and Chat images out of a buffered
// client body: Chat content and images, Responses output_text, Messages text.
func bufferedClientText(t *testing.T, client string, body []byte) (string, []string) {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("client body %s: %v", body, err)
	}
	var text strings.Builder
	var images []string
	switch client {
	case openai.ResponsesProtocol:
		for _, item := range asItems(answer["output"]) {
			for _, part := range asItems(item.(map[string]any)["content"]) {
				if part.(map[string]any)["type"] == "output_text" {
					text.WriteString(part.(map[string]any)["text"].(string))
				}
			}
		}
	case anthropic.MessagesProtocol:
		for _, block := range asItems(answer["content"]) {
			if block.(map[string]any)["type"] == "text" {
				text.WriteString(block.(map[string]any)["text"].(string))
			}
		}
	default:
		message := answer["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
		content, _ := message["content"].(string)
		text.WriteString(content)
		for _, image := range asItems(message["images"]) {
			images = append(images, image.(map[string]any)["image_url"].(map[string]any)["url"].(string))
		}
	}
	return text.String(), images
}

// streamedChatImages are the delta.images URLs of a Chat client's stream.
func streamedChatImages(t *testing.T, body string) []string {
	t.Helper()
	var images []string
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatal(err)
		}
		for _, choice := range asItems(event["choices"]) {
			delta, _ := choice.(map[string]any)["delta"].(map[string]any)
			for _, image := range asItems(delta["images"]) {
				images = append(images, image.(map[string]any)["image_url"].(map[string]any)["url"].(string))
			}
		}
	}
	return images
}

// #259: a Responses or Messages client of any Gemini-family wire gets the
// picture as the markdown image the pin's buffered path writes
// (nonStreamingHandler.js:218-223), as output_text for Responses (:167-175), in
// both modes. The pin's stream drops it for those two clients
// (openai-responses.js:115, openai-to-claude.js:161 read only content); here the
// stream carries the same text the buffered answer does (PARITY 6.27). A Chat
// client keeps its images entry in both modes (PARITY 6.7 G10), which is the
// pin's stream form.
func TestGeminiFamilyImageAnswerIsThePinsForEveryClient(t *testing.T) {
	for _, answer := range geminiImagePinAnswers(t) {
		t.Run(answer.Target+"/"+answer.Client+"/"+answer.Name, func(t *testing.T) {
			pinText := pinBufferedText(answer)
			pinStreamText, pinImages := pinStreamed(answer)
			if !strings.Contains(pinText, "![image](data:") || strings.Contains(pinStreamText, "![image]") {
				t.Fatalf("fixture: buffered %q, streamed %q", pinText, pinStreamText)
			}
			if (answer.Client == "openai") != (len(pinImages) > 0) {
				t.Fatalf("fixture: %s stream images %v", answer.Client, pinImages)
			}

			client := map[string]string{"openai": openai.ChatProtocol, "openai-responses": openai.ResponsesProtocol, "claude": anthropic.MessagesProtocol}[answer.Client]
			buffered := geminiImageTranslation(t, client, answer.Target, false)
			body, err := buffered.Response(answer.Body)
			if err != nil {
				t.Fatal(err)
			}
			text, images := bufferedClientText(t, client, body)

			streamed := geminiImageTranslation(t, client, answer.Target, true)
			var out bytes.Buffer
			if err := streamed.Stream(context.Background(), strings.NewReader("data: "+string(answer.Body)+"\n\n"), &out); err != nil {
				t.Fatalf("stream: %v\n%s", err, out.String())
			}
			streamText := clientText(t, out.String())

			if answer.Client == "openai" {
				// Chat is unchanged: the text without the picture, and the picture as
				// the images entry the pin's stream sends.
				if text != pinStreamText || streamText != pinStreamText {
					t.Errorf("chat text: buffered %q, streamed %q, pin stream %q", text, streamText, pinStreamText)
				}
				if fmt.Sprint(images) != fmt.Sprint(pinImages) || fmt.Sprint(streamedChatImages(t, out.String())) != fmt.Sprint(pinImages) {
					t.Errorf("chat images: buffered %v, streamed %v, pin %v", images, streamedChatImages(t, out.String()), pinImages)
				}
				return
			}
			if text != pinText {
				t.Errorf("buffered text %q, pin %q", text, pinText)
			}
			if streamText != pinText {
				t.Errorf("streamed text %q, pin's buffered %q", streamText, pinText)
			}
		})
	}
}

const geminiImageAnswer = `{"candidates":[{"content":{"role":"model","parts":[{"text":"a cat"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1290,"totalTokenCount":1297},"modelVersion":"gemini-2.5-flash-image"}`

// Every Gemini-family provider, served over HTTP to a Responses or Messages
// client, streamed or not, answers with the picture in the text and records the
// upstream's usage; a Chat client still gets the images entry. The Antigravity
// image model takes its own image_gen path (PARITY 6.26) and arrives at the
// same text.
func TestGeminiFamilyImageReachesResponsesAndMessagesClients(t *testing.T) {
	const want = "a cat\n![image](data:image/png;base64,QUJD)\n"
	cases := []struct {
		id, model, token string
		cloud, whole     bool
	}{
		{"gemini", "gemini-2.5-flash-image", "AIza-fixture", false, false},
		{"vertex", "gemini-2.5-flash-image", "AIza-fixture", false, false},
		{"gemini-cli", "gemini-2.5-flash-image", "ya29.fixture", true, false},
		{"antigravity", "gemini-3-flash", "ya29.fixture", true, false},
		{"antigravity", "gemini-3.1-flash-image", "ya29.fixture", true, true},
	}
	for _, c := range cases {
		for _, client := range clientWires {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%s/stream=%v", c.id, c.model, client, stream), func(t *testing.T) {
					var mu sync.Mutex
					var path string
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						path = r.URL.Path
						mu.Unlock()
						answer := geminiImageAnswer
						if c.cloud {
							answer = `{"response":` + answer + `,"traceId":"t"}`
						}
						if stream && !c.whole {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: %s\n\n", answer)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, answer)
					}))
					defer upstream.Close()
					s := newServing(t, routing.ProviderRef{ProviderID: c.id, Protocol: c.id, BaseURL: upstream.URL, UpstreamModel: c.model, APIToken: c.token,
						Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{ProjectID: "fixture-project"}}})
					w := s.call(client, stream)
					if w.Code != http.StatusOK {
						t.Fatalf("client got %d: %s", w.Code, w.Body)
					}
					mu.Lock()
					sent := path
					mu.Unlock()
					if wantStream := stream && !c.whole; strings.HasSuffix(sent, ":streamGenerateContent") != wantStream {
						t.Errorf("upstream path %s", sent)
					}
					var text string
					var images []string
					if stream {
						text, images = clientText(t, w.Body.String()), streamedChatImages(t, w.Body.String())
					} else {
						text, images = bufferedClientText(t, client, w.Body.Bytes())
					}
					switch {
					case client == openai.ChatProtocol && !c.whole:
						if text != "a cat" || fmt.Sprint(images) != "[data:image/png;base64,QUJD]" {
							t.Errorf("chat: text %q, images %v", text, images)
						}
					default:
						if text != want || len(images) != 0 {
							t.Errorf("text %q, images %v, want %q", text, images, want)
						}
					}
					outcomes := s.outcomes()
					if len(outcomes) != 1 || outcomes[0].Status != http.StatusOK || outcomes[0].UsageUnobserved || outcomes[0].InputTokens != 7 || outcomes[0].OutputTokens != 1290 {
						t.Errorf("outcomes %+v", outcomes)
					}
				})
			}
		}
	}
}
