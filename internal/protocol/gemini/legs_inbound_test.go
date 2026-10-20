package gemini

import (
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func inbound(t *testing.T, gemini string) (map[string]any, error) {
	t.Helper()
	body, err := concerns.WireObject([]byte(gemini))
	if err != nil {
		t.Fatal(err)
	}
	out, err := GeminiRequestToChat(body, "m", true)
	if err != nil {
		return nil, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func mustInbound(t *testing.T, gemini string) map[string]any {
	t.Helper()
	result, err := inbound(t, gemini)
	if err != nil {
		t.Fatalf("%s: %v", gemini, err)
	}
	return result
}

const hello = `"contents":[{"role":"user","parts":[{"text":"hi"}]}]`
const declared = `"tools":[{"functionDeclarations":[{"name":"f","description":"d","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}]}]`

// Every Gemini request field is carried, not carried for a stated reason, or
// refused. PARITY 6.7 is this table.
func TestGeminiRequestFieldsAreCarriedOrRefused(t *testing.T) {
	carried := map[string]struct{ body, path, want string }{
		"model and stream":     {`{` + hello + `}`, "model", `"m"`},
		"stream":               {`{` + hello + `}`, "stream", `true`},
		"contents":             {`{` + hello + `}`, "messages.0.content", `"hi"`},
		"maxOutputTokens":      {`{` + hello + `,"generationConfig":{"maxOutputTokens":100}}`, "max_tokens", `100`},
		"maxOutputTokens+tool": {`{` + hello + `,` + declared + `,"generationConfig":{"maxOutputTokens":100}}`, "max_tokens", `32000`},
		"temperature":          {`{` + hello + `,"generationConfig":{"temperature":0.3}}`, "temperature", `0.3`},
		"topP":                 {`{` + hello + `,"generationConfig":{"topP":0.8}}`, "top_p", `0.8`},
		"stopSequences":        {`{` + hello + `,"generationConfig":{"stopSequences":["a","b"]}}`, "stop", `["a","b"]`},
		"seed":                 {`{` + hello + `,"generationConfig":{"seed":5}}`, "seed", `5`},
		"presencePenalty":      {`{` + hello + `,"generationConfig":{"presencePenalty":0.1}}`, "presence_penalty", `0.1`},
		"frequencyPenalty":     {`{` + hello + `,"generationConfig":{"frequencyPenalty":0.2}}`, "frequency_penalty", `0.2`},
		"candidateCount 1":     {`{` + hello + `,"generationConfig":{"candidateCount":1}}`, "model", `"m"`},
		"json mime":            {`{` + hello + `,"generationConfig":{"responseMimeType":"application/json"}}`, "response_format.type", `"json_object"`},
		"json schema":          {`{` + hello + `,"generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"object"}}}`, "response_format.json_schema.schema.type", `"object"`},
		"json json schema":     {`{` + hello + `,"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":{"type":"string"}}}`, "response_format.json_schema.schema.type", `"string"`},
		"text mime":            {`{` + hello + `,"generationConfig":{"responseMimeType":"text/plain"}}`, "response_format", `null`},
		"systemInstruction":    {`{` + hello + `,"systemInstruction":{"parts":[{"text":"be"},{"text":" brief"}]}}`, "messages.0.content", `"be brief"`},
		"system_instruction":   {`{` + hello + `,"system_instruction":{"parts":[{"text":"s"}]}}`, "messages.0.role", `"system"`},
		"string instruction":   {`{` + hello + `,"systemInstruction":"s"}`, "messages.0.content", `"s"`},
		"tools":                {`{` + hello + `,` + declared + `}`, "tools.0.function.name", `"f"`},
		"parametersJsonSchema": {`{` + hello + `,"tools":[{"functionDeclarations":[{"name":"f","parametersJsonSchema":{"type":"object"}}]}]}`, "tools.0.function.parameters.type", `"object"`},
		"no parameters":        {`{` + hello + `,"tools":[{"functionDeclarations":[{"name":"f"}]}]}`, "tools.0.function.parameters.type", `"object"`},
		"toolConfig none":      {`{` + hello + `,` + declared + `,"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`, "tool_choice", `"none"`},
		"toolConfig any":       {`{` + hello + `,` + declared + `,"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}}`, "tool_choice", `"required"`},
		"toolConfig one":       {`{` + hello + `,` + declared + `,"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}}}`, "tool_choice.function.name", `"f"`},
		"toolConfig auto":      {`{` + hello + `,` + declared + `,"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}}}`, "tool_choice", `null`},
		"image":                {`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}]}`, "messages.0.content.0.image_url.url", `"data:image/png;base64,QUJD"`},
		"snake image":          {`{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"QUJD"}}]}]}`, "messages.0.content.0.type", `"image_url"`},
		// Not carried, still served.
		"topK":                {`{` + hello + `,"generationConfig":{"topK":3}}`, "top_k", `null`},
		"thinkingConfig":      {`{` + hello + `,"generationConfig":{"thinkingConfig":{"thinkingBudget":5}}}`, "reasoning_effort", `null`},
		"safetySettings":      {`{` + hello + `,"safetySettings":[{"category":"c","threshold":"OFF"}]}`, "model", `"m"`},
		"labels":              {`{` + hello + `,"labels":{"a":"b"}}`, "model", `"m"`},
		"empty tools":         {`{` + hello + `,"tools":[]}`, "tools", `null`},
		"toolConfig no tools": {`{` + hello + `,"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`, "tool_choice", `null`},
	}
	for name, c := range carried {
		t.Run("carried/"+name, func(t *testing.T) {
			result := mustInbound(t, c.body)
			if got := asJSON(at(result, c.path)); got != c.want {
				t.Fatalf("%s = %s, want %s\n%s", c.path, got, c.want, asJSON(result))
			}
		})
	}
	refused := map[string]string{
		"cachedContent":         `{` + hello + `,"cachedContent":"cachedContents/1"}`,
		"candidateCount 2":      `{` + hello + `,"generationConfig":{"candidateCount":2}}`,
		"responseLogprobs":      `{` + hello + `,"generationConfig":{"responseLogprobs":true}}`,
		"logprobs":              `{` + hello + `,"generationConfig":{"logprobs":3}}`,
		"mime unknown":          `{` + hello + `,"generationConfig":{"responseMimeType":"text/x.enum"}}`,
		"maxOutputTokens text":  `{` + hello + `,"generationConfig":{"maxOutputTokens":"9"}}`,
		"temperature text":      `{` + hello + `,"generationConfig":{"temperature":"9"}}`,
		"stop not list":         `{` + hello + `,"generationConfig":{"stopSequences":5}}`,
		"generationConfig list": `{` + hello + `,"generationConfig":[]}`,
		"googleSearch":          `{` + hello + `,"tools":[{"googleSearch":{}}]}`,
		"codeExecution":         `{` + hello + `,"tools":[{"codeExecution":{}}]}`,
		"urlContext":            `{` + hello + `,"tools":[{"urlContext":{}}]}`,
		"declaration + search":  `{` + hello + `,"tools":[{"functionDeclarations":[{"name":"f"}],"googleSearch":{}}]}`,
		"tools not list":        `{` + hello + `,"tools":{}}`,
		"declaration no name":   `{` + hello + `,"tools":[{"functionDeclarations":[{"description":"d"}]}]}`,
		"toolConfig extra":      `{` + hello + `,` + declared + `,"toolConfig":{"retrievalConfig":{}}}`,
		"toolConfig not object": `{` + hello + `,` + declared + `,"toolConfig":"ANY"}`,
		"mode unknown":          `{` + hello + `,` + declared + `,"toolConfig":{"functionCallingConfig":{"mode":"VALIDATED"}}}`,
		"two allowed":           `{` + hello + `,"tools":[{"functionDeclarations":[{"name":"f"},{"name":"g"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f","g"]}}}`,
		"allowed unknown":       `{` + hello + `,` + declared + `,"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["zzz"]}}}`,
		"allowed under auto":    `{` + hello + `,` + declared + `,"toolConfig":{"functionCallingConfig":{"mode":"AUTO","allowedFunctionNames":["f"]}}}`,
		"system image":          `{` + hello + `,"systemInstruction":{"parts":[{"inlineData":{"mimeType":"image/png","data":"x"}}]}}`,
		"system number":         `{` + hello + `,"systemInstruction":5}`,
		"contents text":         `{"contents":"hi"}`,
		"content not object":    `{"contents":["hi"]}`,
		"parts not list":        `{"contents":[{"role":"user","parts":"hi"}]}`,
		"part not object":       `{"contents":[{"role":"user","parts":["hi"]}]}`,
		"role system":           `{"contents":[{"role":"system","parts":[{"text":"hi"}]}]}`,
		"fileData":              `{"contents":[{"role":"user","parts":[{"fileData":{"fileUri":"gs://x","mimeType":"image/png"}}]}]}`,
		"executableCode":        `{"contents":[{"role":"model","parts":[{"executableCode":{"code":"1"}}]}]}`,
		"videoMetadata":         `{"contents":[{"role":"user","parts":[{"text":"x","videoMetadata":{}}]}]}`,
		"pdf":                   `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"application/pdf","data":"x"}}]}]}`,
		"audio":                 `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"x"}}]}]}`,
		"image no data":         `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png"}}]}]}`,
		"image in model turn":   `{"contents":[{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"x"}}]}]}`,
		"text not string":       `{"contents":[{"role":"user","parts":[{"text":5}]}]}`,
		"call no name":          `{"contents":[{"role":"model","parts":[{"functionCall":{"args":{}}}]}]}`,
		"response answers none": `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}]}`,
		"response wrong id":     `{"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"f"}}]},{"role":"user","parts":[{"functionResponse":{"id":"b","name":"f","response":{}}}]}]}`,
		"response wrong name":   `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f"}}]},{"role":"user","parts":[{"functionResponse":{"name":"g","response":{}}}]}]}`,
		"response twice":        `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f"}}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}},{"functionResponse":{"name":"f","response":{}}}]}]}`,
	}
	for name, body := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			result, err := inbound(t, body)
			if err == nil {
				t.Fatalf("served instead of refused: %s", asJSON(result))
			}
			if !strings.HasPrefix(err.Error(), "unsupported: ") {
				t.Fatalf("not a translation error: %v", err)
			}
		})
	}
}

func TestGeminiContentsBecomeMessages(t *testing.T) {
	t.Run("a missing role is the user", func(t *testing.T) {
		result := mustInbound(t, `{"contents":[{"parts":[{"text":"hi"}]}]}`)
		if at(result, "messages.0.role") != "user" {
			t.Fatalf("role = %v", at(result, "messages.0.role"))
		}
	})
	t.Run("model text and thoughts", func(t *testing.T) {
		result := mustInbound(t, `{"contents":[{"role":"user","parts":[{"text":"q"}]},{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"answer"},{"text":" more"}]}]}`)
		message := concerns.Object(at(result, "messages.1"))
		if message["role"] != "assistant" || message["reasoning_content"] != "thinking" || asJSON(message["content"]) != `[{"text":"answer","type":"text"},{"text":" more","type":"text"}]` {
			t.Fatalf("message = %s", asJSON(message))
		}
	})
	t.Run("text and image share a turn", func(t *testing.T) {
		result := mustInbound(t, `{"contents":[{"role":"user","parts":[{"text":"look"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]}]}`)
		if got := asJSON(at(result, "messages.0.content")); got != `[{"text":"look","type":"text"},{"image_url":{"url":"data:image/png;base64,QUJD"},"type":"image_url"}]` {
			t.Fatalf("content = %s", got)
		}
	})
	t.Run("calls and responses pair by id", func(t *testing.T) {
		result := mustInbound(t, `{"contents":[
			{"role":"user","parts":[{"text":"go"}]},
			{"role":"model","parts":[{"functionCall":{"id":"x1","name":"f","args":{"a":1}}}]},
			{"role":"user","parts":[{"functionResponse":{"id":"x1","name":"f","response":{"result":{"ok":true}}}}]}]}`)
		if got := asJSON(at(result, "messages.1.tool_calls.0")); got != `{"function":{"arguments":"{\"a\":1}","name":"f"},"id":"x1","type":"function"}` {
			t.Errorf("call = %s", got)
		}
		if got := asJSON(at(result, "messages.2")); got != `{"content":"{\"ok\":true}","role":"tool","tool_call_id":"x1"}` {
			t.Errorf("tool message = %s", got)
		}
	})
	// The pin returns at the first functionResponse of a content and loses every
	// other part in it (gemini-to-openai.js:112-118), and numbers two parallel
	// calls to one tool call_<name>, so their results cannot be told apart.
	t.Run("parallel calls of one tool, no ids anywhere", func(t *testing.T) {
		result := mustInbound(t, `{"contents":[
			{"role":"user","parts":[{"text":"go"}]},
			{"role":"model","parts":[{"functionCall":{"name":"f","args":{"n":1}}},{"functionCall":{"name":"f","args":{"n":2}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"result":"one"}}},{"functionResponse":{"name":"f","response":{"result":"two"}}},{"text":"and then?"}]}]}`)
		ids := []any{at(result, "messages.1.tool_calls.0.id"), at(result, "messages.1.tool_calls.1.id")}
		if ids[0] == ids[1] || ids[0] == "" || ids[1] == "" {
			t.Fatalf("ids = %v", ids)
		}
		if at(result, "messages.2.tool_call_id") != ids[0] || at(result, "messages.2.content") != `"one"` ||
			at(result, "messages.3.tool_call_id") != ids[1] || at(result, "messages.3.content") != `"two"` {
			t.Errorf("results are not paired in order: %s", asJSON(at(result, "messages")))
		}
		if at(result, "messages.4.role") != "user" || at(result, "messages.4.content") != "and then?" {
			t.Errorf("the text beside the results was lost: %s", asJSON(at(result, "messages")))
		}
	})
	t.Run("response payloads", func(t *testing.T) {
		for payload, want := range map[string]string{
			`{"result":"x"}`:     `"x"`,
			`{"result":{"a":1}}`: `{"a":1}`,
			`{"other":1}`:        `{"other":1}`,
			`{}`:                 `{}`,
			`{"result":0}`:       `{"result":0}`,
			`null`:               `{}`,
		} {
			result := mustInbound(t, `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f"}}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":`+payload+`}}]}]}`)
			if got := at(result, "messages.1.content"); got != want {
				t.Errorf("response %s -> %v, want %s", payload, got, want)
			}
		}
	})
	t.Run("a call with no args", func(t *testing.T) {
		result := mustInbound(t, `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f"}}]}]}`)
		if at(result, "messages.0.tool_calls.0.function.arguments") != "{}" {
			t.Fatal(asJSON(result))
		}
	})
	t.Run("empty parts add nothing", func(t *testing.T) {
		result := mustInbound(t, `{"contents":[{"role":"user"},{"role":"user","parts":[]},{"role":"user","parts":[{"text":"x"}]}]}`)
		if len(concerns.Items(result["messages"])) != 1 {
			t.Fatalf("messages = %s", asJSON(result["messages"]))
		}
	})
}

// The id of a call a Gemini client sent, crossed with the id of its response.
func TestGeminiIDResolutionCrossProduct(t *testing.T) {
	callIDs := map[string]string{"given": `"id":"g1",`, "empty": `"id":"",`, "absent": ``}
	responseIDs := map[string]string{"same": `"id":"g1",`, "empty": `"id":"",`, "absent": ``, "other": `"id":"zz",`}
	for callName, callID := range callIDs {
		for responseName, responseID := range responseIDs {
			body := `{"contents":[{"role":"model","parts":[{"functionCall":{` + callID + `"name":"f","args":{}}}]},{"role":"user","parts":[{"functionResponse":{` + responseID + `"name":"f","response":{"result":"r"}}}]}]}`
			result, err := inbound(t, body)
			// A response that names an id the call does not carry answers nothing.
			if responseName == "other" || (responseName == "same" && callName != "given") {
				if err == nil {
					t.Errorf("call=%s response=%s: served a response that answers no call: %s", callName, responseName, asJSON(result))
				}
				continue
			}
			if err != nil {
				t.Errorf("call=%s response=%s: %v", callName, responseName, err)
				continue
			}
			callPair, reply := at(result, "messages.0.tool_calls.0.id"), at(result, "messages.1.tool_call_id")
			if callPair == nil || callPair == "" || callPair != reply {
				t.Errorf("call=%s response=%s: call id %v, result id %v", callName, responseName, callPair, reply)
			}
		}
	}
}
