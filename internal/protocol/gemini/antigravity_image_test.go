package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// antigravityImagePin is what compat/parity/generate-antigravity-image.mjs
// recorded from the pinned executor (PARITY 6.26).
type antigravityImagePin struct {
	Pin    string `json:"pin"`
	Models []struct {
		Model         string         `json:"model"`
		Image         bool           `json:"image"`
		ChatCoreImage bool           `json:"chatCoreImage"`
		StreamURL     string         `json:"streamUrl"`
		PlainURL      string         `json:"plainUrl"`
		ImageConfig   map[string]any `json:"imageConfig"`
		UpstreamModel string         `json:"upstreamModel"`
	} `json:"models"`
	Transforms []struct {
		Name string `json:"name"`
		Body struct {
			Request map[string]json.RawMessage `json:"request"`
		} `json:"body"`
		Sent struct {
			Project     string         `json:"project"`
			Model       string         `json:"model"`
			UserAgent   string         `json:"userAgent"`
			RequestType string         `json:"requestType"`
			Request     map[string]any `json:"request"`
		} `json:"sent"`
	} `json:"transforms"`
}

func loadAntigravityImagePin(t *testing.T) antigravityImagePin {
	t.Helper()
	raw, err := os.ReadFile("testdata/antigravity_image_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin antigravityImagePin
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	if pin.Pin != "1c3b9f47837d42ea64992063309064af755a14a3" || len(pin.Models) == 0 || len(pin.Transforms) == 0 {
		t.Fatalf("fixture is not the pin's: %s %d %d", pin.Pin, len(pin.Models), len(pin.Transforms))
	}
	return pin
}

// Which names are image models, the model sent and the aspect ratio are the
// pin's, case for case; the call is non-streaming exactly for them.
func TestAntigravityImageModelsAreThePins(t *testing.T) {
	for _, c := range loadAntigravityImagePin(t).Models {
		if got := IsAntigravityImageModel(c.Model); got != c.Image || got != c.ChatCoreImage {
			t.Errorf("%q: image %v, pin executor %v, pin chatCore %v", c.Model, got, c.Image, c.ChatCoreImage)
		}
		if c.Image != strings.HasSuffix(c.StreamURL, "v1internal:generateContent") {
			t.Errorf("%q: the pin streams %s", c.Model, c.StreamURL)
		}
		if !c.Image {
			continue
		}
		if got := AntigravityImageModel(c.Model); got != c.UpstreamModel {
			t.Errorf("%q: upstream model %q, pin %q", c.Model, got, c.UpstreamModel)
		}
		config, err := antigravityImageConfig(c.Model)
		if c.Model == "gemini-image-9007199254740993x3" {
			// JavaScript reads the side as 2^53; refused rather than rounded.
			if err == nil {
				t.Errorf("%q: a side above 2^53 is read as %v", c.Model, config)
			}
			continue
		}
		if err != nil || asJSON(config) != asJSON(c.ImageConfig) {
			t.Errorf("%q: image config %v %v, pin %v", c.Model, config, err, c.ImageConfig)
		}
	}
}

// The contents the image request carries are the pin's for every body whose
// parts are text; a body with anything else is refused where the pin drops it.
func TestAntigravityImageContentsAreThePins(t *testing.T) {
	for _, c := range loadAntigravityImagePin(t).Transforms {
		got, err := antigravityImageContents(c.Body.Request["contents"])
		if c.Name == "everything else dropped" {
			if err == nil || !strings.Contains(err.Error(), "inlineData") {
				t.Errorf("%s: an input image is sent as %v (%v)", c.Name, got, err)
			}
			continue
		}
		if err != nil || asJSON(got) != asJSON(c.Sent.Request["contents"]) {
			t.Errorf("%s: contents %s %v, pin %s", c.Name, asJSON(got), err, asJSON(c.Sent.Request["contents"]))
		}
		if c.Sent.RequestType != AntigravityImageRequestType || c.Sent.UserAgent != "antigravity" {
			t.Errorf("%s: pin envelope %+v", c.Name, c.Sent)
		}
	}
}

func pinImageRequest(t *testing.T, name string) map[string]any {
	t.Helper()
	for _, c := range loadAntigravityImagePin(t).Transforms {
		if c.Name == name {
			return c.Sent.Request
		}
	}
	t.Fatalf("no pin transform %q", name)
	return nil
}

// A Chat request to an image model is the pin's image envelope: the model
// without its size suffix, image_gen, text contents, the pin's sampling and
// ratio, and no tools, system instruction or safety settings.
func TestAntigravityImageRequestIsThePins(t *testing.T) {
	want := pinImageRequest(t, "conversation")
	chat := `{"messages":[{"role":"user","content":"draw a cat"},{"role":"assistant","content":"here"},{"role":"user","content":[{"type":"text","text":"bluer"},{"type":"text","text":"please"}]}]}`
	envelope, raw, err := cloudRequestRaw(t, AntigravityProtocol, chat, RequestOptions{Model: "gemini-3.1-flash-image-16x9", Scope: "s"}, CloudCodeOptions{Project: "synthetic-project"})
	if err != nil {
		t.Fatal(err)
	}
	if envelope["model"] != "gemini-3.1-flash-image" || envelope["requestType"] != "image_gen" || envelope["userAgent"] != "antigravity" || envelope["project"] != "synthetic-project" {
		t.Errorf("envelope %s", raw)
	}
	request := at(envelope, "request").(map[string]any)
	if asJSON(request["contents"]) != asJSON(want["contents"]) || asJSON(request["generationConfig"]) != asJSON(want["generationConfig"]) {
		t.Errorf("request %s\npin %s", asJSON(request), asJSON(want))
	}
	for key := range request {
		switch key {
		case "contents", "generationConfig", "sessionId":
		default:
			t.Errorf("the image request carries %q: %s", key, raw)
		}
	}
	if request["sessionId"] == nil {
		t.Errorf("no session: %s", raw)
	}
}

// What the client set of the three controls the image request carries is
// carried; an explicit 0 stays 0. topK and the ratio are the pin's.
func TestAntigravityImageRequestKeepsTheClientsSampling(t *testing.T) {
	envelope, err := cloudRequest(t, AntigravityProtocol, `{"messages":[{"role":"user","content":"x"}],"temperature":0,"top_p":0.5,"max_tokens":300}`, RequestOptions{Model: "imagen-4-1024x768"}, CloudCodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := asJSON(at(envelope, "request.generationConfig")); got != `{"imageConfig":{"aspectRatio":"4:3"},"maxOutputTokens":300,"temperature":0,"topK":40,"topP":0.5}` {
		t.Errorf("generationConfig %s", got)
	}
	if envelope["model"] != "imagen-4" {
		t.Errorf("model %v", envelope["model"])
	}
}

// What the pin drops without a word is refused: the model would not see it.
func TestAntigravityImageRequestRefusesWhatThePinDrops(t *testing.T) {
	user := `{"role":"user","content":"draw a cat"}`
	for name, chat := range map[string]string{
		"system message":    `{"messages":[{"role":"system","content":"be terse"},` + user + `]}`,
		"developer":         `{"messages":[{"role":"developer","content":"be terse"},` + user + `]}`,
		"tools":             `{"messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}}]}`,
		"input image":       `{"messages":[{"role":"user","content":[{"type":"text","text":"edit"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
		"tool call":         `{"messages":[` + user + `,{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"r"}]}`,
		"json response":     `{"messages":[` + user + `],"response_format":{"type":"json_object"}}`,
		"stop":              `{"messages":[` + user + `],"stop":"x"}`,
		"seed":              `{"messages":[` + user + `],"seed":1}`,
		"penalty":           `{"messages":[` + user + `],"presence_penalty":1}`,
		"above the ceiling": `{"messages":[` + user + `],"max_tokens":64001}`,
		"no text":           `{"messages":[{"role":"user","content":[]}]}`,
	} {
		if envelope, err := cloudRequest(t, AntigravityProtocol, chat, RequestOptions{Model: "gemini-3.1-flash-image"}, CloudCodeOptions{}); err == nil {
			t.Errorf("%s: sent %s", name, asJSON(envelope))
		}
	}
	// A text-only request, and a response_format of text, are served.
	if _, err := cloudRequest(t, AntigravityProtocol, `{"messages":[`+user+`],"response_format":{"type":"text"}}`, RequestOptions{Model: "gemini-3.1-flash-image"}, CloudCodeOptions{}); err != nil {
		t.Errorf("text request refused: %v", err)
	}
}

// The model decides the path: a non-image model keeps the agent envelope, with
// no requestType, and a name that only looks like one under Unicode folding is
// not an image model (the pin's /i folds ASCII only).
func TestAntigravityAgentModelsKeepTheAgentEnvelope(t *testing.T) {
	for _, model := range []string{"gemini-3-flash", "claude-sonnet-4-6", "gemini-\u0130mage"} {
		envelope, raw, err := cloudRequestRaw(t, AntigravityProtocol, `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"x"}]}`, RequestOptions{Model: model}, CloudCodeOptions{})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if strings.Contains(raw, "requestType") || envelope["model"] != model || at(envelope, "request.systemInstruction") == nil {
			t.Errorf("%s: %s", model, raw)
		}
	}
	// The Gemini CLI wire has no image path.
	envelope, raw, err := cloudRequestRaw(t, GeminiCLIProtocol, `{"messages":[{"role":"user","content":"x"}]}`, RequestOptions{Model: "gemini-3.1-flash-image-16x9"}, CloudCodeOptions{})
	if err != nil || strings.Contains(raw, "requestType") || envelope["model"] != "gemini-3.1-flash-image-16x9" {
		t.Errorf("gemini-cli: %v %s", err, raw)
	}
}

const antigravityImageAnswer = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"a cat"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1290,"totalTokenCount":1297}},"traceId":"t"}`

// The one JSON body of an image model reaches a streaming client as the chunks
// a stream would have produced, image and usage included, and a non-streaming
// client as one message with the image.
func TestAntigravityImageAnswerReachesBothKindsOfClient(t *testing.T) {
	options := ResponseOptions{Model: "gemini-3.1-flash-image", Wire: AntigravityProtocol}
	var stream bytes.Buffer
	if err := AntigravityImageStreamToChat(context.Background(), strings.NewReader(antigravityImageAnswer), &stream, options); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"content":"a cat"`, `"url":"data:image/png;base64,QUJD"`, `"finish_reason":"stop"`, `"completion_tokens":1290`, "data: [DONE]"} {
		if !strings.Contains(stream.String(), part) {
			t.Errorf("stream lacks %s:\n%s", part, stream.String())
		}
	}
	whole, err := GeminiResponseToChat([]byte(antigravityImageAnswer), options)
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	_ = json.Unmarshal(whole, &message)
	if got := asJSON(at(message, "choices.0.message.images")); got != `[{"image_url":{"url":"data:image/png;base64,QUJD"},"type":"image_url"}]` {
		t.Errorf("images %s in %s", got, whole)
	}
}

// A body cut short, an empty one, two bodies and one with no finish end the
// stream on the failure terminal, never as an answer.
func TestAntigravityImageStreamRefusesABrokenBody(t *testing.T) {
	options := ResponseOptions{Model: "gemini-3.1-flash-image", Wire: AntigravityProtocol}
	for name, body := range map[string]string{
		"cut":    antigravityImageAnswer[:len(antigravityImageAnswer)/2],
		"empty":  "",
		"twice":  antigravityImageAnswer + antigravityImageAnswer,
		"no end": `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"a"}]}}]}}`,
	} {
		var out bytes.Buffer
		err := AntigravityImageStreamToChat(context.Background(), strings.NewReader(body), &out, options)
		if err == nil || !strings.Contains(out.String(), `data: {"error":`) || strings.Contains(out.String(), `"finish_reason":"stop"`) {
			t.Errorf("%s: %v\n%s", name, err, out.String())
		}
	}
	// A client that left stops the read.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AntigravityImageStreamToChat(ctx, strings.NewReader(antigravityImageAnswer), io.Discard, options); err == nil {
		t.Error("a cancelled stream was answered")
	}
}
