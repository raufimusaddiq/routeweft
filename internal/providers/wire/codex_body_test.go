package wire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func codexOut(t *testing.T, body string) map[string]any {
	t.Helper()
	return decodeBody(t, finalize(t, "codex", responses, body))
}

func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, body)
	}
	return out
}

func TestCodexBodyIsAlwaysStreamedAndNeverStored(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5","input":"hi"}`,
		`{"model":"gpt-5","input":"hi","stream":false,"store":true}`,
		`{"model":"gpt-5","input":"hi","stream":"no","store":null}`,
	} {
		out := codexOut(t, body)
		if out["stream"] != true || out["store"] != false {
			t.Errorf("%s -> stream=%v store=%v", body, out["stream"], out["store"])
		}
	}
}

func TestCodexInputBecomesAUserTurn(t *testing.T) {
	user := func(text string) []any {
		return []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}
	}
	for body, want := range map[string][]any{
		`{"model":"m","input":"hello"}`:      user("hello"),
		`{"model":"m","input":"  "}`:         user("..."),
		`{"model":"m","input":""}`:           user("..."),
		`{"model":"m","input":[]}`:           user("..."),
		`{"model":"m"}`:                      user("..."),
		`{"model":"m","input":null}`:         user("..."),
		`{"model":"m","input":[{"x":1}]}`:    {map[string]any{"x": float64(1)}},
		`{"model":"m","input":{"odd":true}}`: nil, // not ours to reshape
	} {
		out := codexOut(t, body)
		if want == nil {
			if !reflect.DeepEqual(out["input"], map[string]any{"odd": true}) {
				t.Errorf("%s: input %v", body, out["input"])
			}
			continue
		}
		if !reflect.DeepEqual(out["input"], any(want)) {
			t.Errorf("%s: input %v, want %v", body, out["input"], want)
		}
	}
}

func TestCodexSystemTurnsBecomeDeveloperTurns(t *testing.T) {
	out := codexOut(t, `{"model":"m","input":[
		{"role":"system","content":"a"},
		{"type":"message","role":"system","content":"b"},
		{"type":"function_call_output","role":"system","call_id":"c","output":"x"},
		{"role":"user","content":"u"}]}`)
	roles := []any{}
	for _, item := range out["input"].([]any) {
		roles = append(roles, item.(map[string]any)["role"])
	}
	if want := []any{"developer", "developer", "system", "user"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles %v, want %v", roles, want)
	}
}

func TestCodexStoredItemReferencesAreRemoved(t *testing.T) {
	out := codexOut(t, `{"model":"m","input":[
		"rs_abc", "just text",
		{"type":"item_reference","id":"rs_1"},
		{"type":"reasoning","id":"rs_2","summary":[]},
		{"type":"function_call","id":"fc_3","call_id":"call_3","name":"f","arguments":"{}"},
		{"type":"message","id":"msg_4","role":"assistant","content":[]},
		{"type":"message","id":"mine","role":"user","content":[]}]}`)
	input := out["input"].([]any)
	if len(input) != 5 || input[0] != "just text" {
		t.Fatalf("input %v", input)
	}
	for _, item := range input[1:] {
		object := item.(map[string]any)
		if id, has := object["id"]; has && id != "mine" {
			t.Errorf("server id kept: %v", object)
		}
	}
	if input[2].(map[string]any)["call_id"] != "call_3" {
		t.Errorf("the call id is the client's and stays: %v", input[2])
	}
}

func TestCodexImageURLPartsBecomeInputImages(t *testing.T) {
	out := codexOut(t, `{"model":"m","input":[{"role":"user","content":[
		{"type":"image_url","image_url":"data:image/png;base64,AAAA"},
		{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"high"}},
		{"type":"image_url","image_url":{}},
		{"type":"input_text","text":"x"}]}]}`)
	parts := out["input"].([]any)[0].(map[string]any)["content"].([]any)
	want := []any{
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA", "detail": "auto"},
		map[string]any{"type": "input_image", "image_url": "https://example.com/a.png", "detail": "high"},
		map[string]any{"type": "image_url", "image_url": map[string]any{}},
		map[string]any{"type": "input_text", "text": "x"},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts %v\nwant  %v", parts, want)
	}
}

func TestCodexInstructionsDefaultOnlyWhenBlank(t *testing.T) {
	for body, wantDefault := range map[string]bool{
		`{"model":"m","input":"x"}`:                             true,
		`{"model":"m","input":"x","instructions":""}`:           true,
		`{"model":"m","input":"x","instructions":" \n "}`:       true,
		`{"model":"m","input":"x","instructions":null}`:         true,
		`{"model":"m","input":"x","instructions":"be brief"}`:   false,
		`{"model":"m","input":"x","instructions":{"odd":true}}`: false,
	} {
		out := codexOut(t, body)
		if got := out["instructions"] == codexprovider.DefaultInstructions; got != wantDefault {
			t.Errorf("%s: default=%v", body, got)
		}
	}
	if out := codexOut(t, `{"model":"m","input":"x","instructions":"be brief"}`); out["instructions"] != "be brief" {
		t.Fatalf("the client's instructions were replaced: %v", out["instructions"])
	}
}

func TestCodexToolsAreFlattenedAndFiltered(t *testing.T) {
	out := codexOut(t, `{"model":"m","input":"x","tool_choice":{"type":"function","name":"f"},"tools":[
		{"type":"function","function":{"name":" f ","description":"d","parameters":{"type":"object","properties":{"a":{"type":"string"}}},"strict":true},"cache_control":{"type":"ephemeral"}},
		{"type":"function","name":"g"},
		{"type":"function","name":"  "},
		{"type":"function","function":{}},
		{"function":{"name":"typeless"}},
		{"name":"bare"},
		{"type":"web_search"},
		{"type":"custom","name":"apply_patch","format":{"type":"grammar"}},
		{"type":"namespace","name":"ns","tools":[{"type":"function","name":"inner"}]},
		{"type":"made_up"},
		"junk", null, 7]}`)
	tools := out["tools"].([]any)
	var summary []string
	for _, tool := range tools {
		object := tool.(map[string]any)
		summary = append(summary, object["type"].(string)+":"+asString(object["name"]))
	}
	if want := []string{"function:f", "function:g", "web_search:", "custom:apply_patch", "namespace:ns"}; !reflect.DeepEqual(summary, want) {
		t.Fatalf("tools %v, want %v", summary, want)
	}
	flat := tools[0].(map[string]any)
	wantFlat := map[string]any{"type": "function", "name": "f", "description": "d", "strict": true,
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}}
	if !reflect.DeepEqual(flat, wantFlat) {
		t.Fatalf("flat tool %v\nwant      %v", flat, wantFlat)
	}
	if !reflect.DeepEqual(tools[1].(map[string]any)["parameters"], map[string]any{"type": "object", "properties": map[string]any{}}) {
		t.Fatalf("a tool without a schema gets the empty object schema: %v", tools[1])
	}
	if out["tool_choice"] == nil {
		t.Fatal("a tool_choice naming a declared function was dropped")
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func TestCodexToolChoiceNamingAnUndeclaredFunctionIsDropped(t *testing.T) {
	out := codexOut(t, `{"model":"m","input":"x","tool_choice":{"type":"function","name":"gone"},"tools":[{"type":"function","name":"here"}]}`)
	if _, present := out["tool_choice"]; present {
		t.Fatalf("tool_choice kept: %v", out["tool_choice"])
	}
	out = codexOut(t, `{"model":"m","input":"x","tool_choice":"required","tools":[{"type":"function","name":"here"}]}`)
	if out["tool_choice"] != "required" {
		t.Fatalf("a string tool_choice names nothing and stays: %v", out["tool_choice"])
	}
}

func TestCodexSchemaPatternsTheEndpointCannotParseAreRemoved(t *testing.T) {
	body := `{"model":"m","input":"x","tools":[{"type":"function","name":"Artifact","parameters":{"type":"object","properties":{
		"title":{"type":"string","pattern":"^(?!__.*__$)[^\\p{Cc}\\p{Cf}]{1,200}$"},
		"ok":{"type":"string","pattern":"^[a-z]+$"},
		"escaped":{"type":"string","pattern":"^\\\\p{x}$"},
		"pattern":{"type":"object","properties":{"pattern":{"type":"string","pattern":"\\P{L}"}}},
		"nested":{"anyOf":[{"type":"string","pattern":"\\p{L}+"},{"type":"null"}]}}}}]}`
	properties := codexOut(t, body)["tools"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
	if _, has := properties["title"].(map[string]any)["pattern"]; has {
		t.Error("a \\p{...} pattern was kept")
	}
	if properties["ok"].(map[string]any)["pattern"] != "^[a-z]+$" {
		t.Error("a valid pattern was removed")
	}
	if properties["escaped"].(map[string]any)["pattern"] != `^\\p{x}$` {
		t.Errorf("an escaped backslash followed by p is a literal and stays: %v", properties["escaped"])
	}
	// A property that is named "pattern" is a property, not the keyword.
	named := properties["pattern"].(map[string]any)["properties"].(map[string]any)["pattern"].(map[string]any)
	if _, has := named["pattern"]; has {
		t.Error("a nested \\P{...} pattern was kept")
	}
	if _, has := properties["nested"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)["pattern"]; has {
		t.Error("a pattern inside anyOf was kept")
	}
}

func TestCodexReasoningEffort(t *testing.T) {
	type want struct{ effort, summary string }
	for name, test := range map[string]struct {
		body string
		want want
	}{
		"default is low":                     {`{"model":"gpt-5.3-codex","input":"x"}`, want{"low", "auto"}},
		"reasoning_effort":                   {`{"model":"gpt-5.3-codex","input":"x","reasoning_effort":"high"}`, want{"high", "auto"}},
		"model suffix":                       {`{"model":"gpt-5.3-codex-xhigh","input":"x"}`, want{"xhigh", "auto"}},
		"suffix loses to reasoning_effort":   {`{"model":"gpt-5.3-codex-low","input":"x","reasoning_effort":"medium"}`, want{"medium", "auto"}},
		"none on a model that cannot":        {`{"model":"gpt-5.3-codex","input":"x","reasoning_effort":"none"}`, want{"low", "auto"}},
		"max falls to xhigh":                 {`{"model":"gpt-5.3-codex","input":"x","reasoning_effort":"max"}`, want{"xhigh", "auto"}},
		"explicit reasoning":                 {`{"model":"gpt-5.3-codex","input":"x","reasoning":{"effort":"max","summary":"detailed"}}`, want{"xhigh", "detailed"}},
		"explicit reasoning without summary": {`{"model":"gpt-5.3-codex","input":"x","reasoning":{"effort":"medium"}}`, want{"medium", "auto"}},
		"gpt-6 keeps max":                    {`{"model":"gpt-6-sol","input":"x","reasoning_effort":"max"}`, want{"max", "auto"}},
		"5.6 sol keeps ultra":                {`{"model":"gpt-5.6-sol","input":"x","reasoning_effort":"ultra"}`, want{"ultra", "auto"}},
		"gpt-6.1-sol cannot disable":         {`{"model":"gpt-6.1-sol","input":"x","reasoning_effort":"none"}`, want{"low", "auto"}},
		"unknown model passes the value on":  {`{"model":"not-a-model","input":"x","reasoning_effort":"max"}`, want{"xhigh", "auto"}},
	} {
		t.Run(name, func(t *testing.T) {
			out := codexOut(t, test.body)
			reasoning := out["reasoning"].(map[string]any)
			if reasoning["effort"] != test.want.effort || reasoning["summary"] != test.want.summary {
				t.Fatalf("reasoning %v, want %+v", reasoning, test.want)
			}
			if _, kept := out["reasoning_effort"]; kept {
				t.Error("reasoning_effort was sent")
			}
			if model := out["model"].(string); strings.HasSuffix(model, "-low") || strings.HasSuffix(model, "-xhigh") {
				t.Errorf("model keeps its effort suffix: %s", model)
			}
			include, _ := out["include"].([]any)
			if want := []any{"reasoning.encrypted_content"}; !reflect.DeepEqual(include, want) {
				t.Errorf("include %v, want %v", include, want)
			}
		})
	}
}

func TestCodexEncryptedReasoningIsNotRequestedWhenReasoningIsOff(t *testing.T) {
	out := codexOut(t, `{"model":"not-a-model","input":"x","reasoning":{"effort":"none"},"include":["file_search_call.results"]}`)
	if !reflect.DeepEqual(out["include"], []any{"file_search_call.results"}) {
		t.Fatalf("include %v", out["include"])
	}
	if out["reasoning"].(map[string]any)["effort"] != "none" {
		t.Fatalf("reasoning %v", out["reasoning"])
	}
}

func TestCodexOnlyTheEndpointsFieldsAreSent(t *testing.T) {
	out := codexOut(t, `{"model":"m","input":"x","temperature":0.2,"top_p":1,"max_output_tokens":10,"max_tokens":10,"metadata":{"a":1},
		"user":"u","previous_response_id":"resp_1","parallel_tool_calls":true,"stream_options":{"include_usage":true},
		"safety_identifier":"s","prompt_cache_retention":"24h","text":{"verbosity":"low"},"client_metadata":{"k":"v"},"prompt_cache_key":"k"}`)
	for _, dropped := range []string{"temperature", "top_p", "max_output_tokens", "max_tokens", "metadata", "user", "previous_response_id", "parallel_tool_calls", "stream_options", "safety_identifier", "prompt_cache_retention"} {
		if _, present := out[dropped]; present {
			t.Errorf("%s was sent", dropped)
		}
	}
	for _, kept := range []string{"text", "client_metadata", "prompt_cache_key"} {
		if _, present := out[kept]; !present {
			t.Errorf("%s was dropped", kept)
		}
	}
}

func TestCodexServiceTier(t *testing.T) {
	for tier, want := range map[string]any{`"fast"`: "priority", `"priority"`: "priority", `"flex"`: nil, `"auto"`: nil, `""`: ""} {
		out := codexOut(t, `{"model":"m","input":"x","service_tier":`+tier+`}`)
		if got := out["service_tier"]; got != want {
			t.Errorf("service_tier %s -> %v, want %v", tier, got, want)
		}
	}
}

func TestCodexModelIsTheUpstreamOne(t *testing.T) {
	for model, want := range map[string]string{
		"gpt-5.4-review":      "gpt-5.4",
		"gpt-5.4-review-high": "gpt-5.4-review", // the pin maps before it reads the suffix: a suffixed review id is not a catalog id
		"codex-auto-review":   "codex-auto-review",
		"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
	} {
		out := codexOut(t, `{"model":"`+model+`","input":"x"}`)
		if out["model"] != want {
			t.Errorf("%s -> %v, want %s", model, out["model"], want)
		}
	}
}

func TestCodexBodyIsIdempotent(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "codex", Protocol: responses, ConnectionID: "acct"}
	for _, body := range []string{
		`{"model":"gpt-5.3-codex-high","input":"hi","tools":[{"type":"function","function":{"name":"f"}}],"reasoning_effort":"max"}`,
		`{"model":"gpt-5.4-review","input":[{"role":"system","content":"s"},"rs_1"],"service_tier":"fast"}`,
		`{"model":"m"}`,
	} {
		once := FinalizeBody(provider, []byte(body))
		twice := FinalizeBody(provider, once)
		same(t, string(twice), string(once))
	}
}

func TestCodexBodyFailsOpen(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "codex", Protocol: responses}
	for _, body := range []string{``, `not json`, `[]`, `null`, `"str"`} {
		if got := string(FinalizeBody(provider, []byte(body))); got != body {
			t.Errorf("%q changed to %q", body, got)
		}
	}
	chat := routing.ProviderRef{ProviderID: "codex", Protocol: "openai-chat"}
	if got := string(FinalizeBody(chat, []byte(`{"messages":[]}`))); got != `{"messages":[]}` {
		t.Errorf("a Chat body was rewritten: %s", got)
	}
}

func TestCodexCompactBodyIsTheSmallDocument(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "codex", Protocol: responses}
	out := decodeBody(t, string(FinalizeBodyAt(provider, "responses/compact", []byte(
		`{"model":"gpt-5.4-review","input":"keep as sent","instructions":"i","stream":true,"store":true,"tools":[],"reasoning":{"effort":"high"},"prompt_cache_key":"k"}`))))
	want := map[string]any{"model": "gpt-5.4", "input": "keep as sent", "instructions": "i"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("compact body %v, want %v", out, want)
	}
	// The same body to the streaming endpoint is the full rewrite.
	if full := decodeBody(t, string(FinalizeBodyAt(provider, "responses", []byte(`{"model":"m","input":"x"}`)))); full["stream"] != true {
		t.Fatalf("the streaming endpoint was treated as compact: %v", full)
	}
	// Another provider's compact path is not Codex's.
	other := routing.ProviderRef{ProviderID: "openai", Protocol: responses}
	if got := string(FinalizeBodyAt(other, "responses/compact", []byte(`{"model":"m","input":"x","stream":false}`))); got != `{"model":"m","input":"x","stream":false}` {
		t.Fatalf("another provider's compact body changed: %s", got)
	}
}

func TestCodexPromptCacheKey(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "codex", Protocol: responses, ConnectionID: "acct-1"}
	out := decodeBody(t, string(FinalizeBody(provider, []byte(`{"model":"m","input":"x"}`))))
	key, _ := out["prompt_cache_key"].(string)
	if len(key) != 32 || strings.Contains(key, "acct-1") {
		t.Fatalf("key %q must be an opaque digest of the connection", key)
	}
	other := decodeBody(t, string(FinalizeBody(routing.ProviderRef{ProviderID: "codex", Protocol: responses, ConnectionID: "acct-2"}, []byte(`{"model":"m","input":"x"}`))))
	if other["prompt_cache_key"] == key {
		t.Fatal("two connections share a cache key")
	}
	kept := decodeBody(t, string(FinalizeBody(provider, []byte(`{"model":"m","input":"x","prompt_cache_key":"client-thread"}`))))
	if kept["prompt_cache_key"] != "client-thread" {
		t.Fatalf("the client's cache key was replaced: %v", kept["prompt_cache_key"])
	}
	none := decodeBody(t, string(FinalizeBody(routing.ProviderRef{ProviderID: "codex", Protocol: responses}, []byte(`{"model":"m","input":"x"}`))))
	if _, present := none["prompt_cache_key"]; present {
		t.Fatalf("a request with no connection got a key: %v", none["prompt_cache_key"])
	}
}

func TestCodexSessionHeaderNamesTheCacheKey(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "codex", Protocol: responses, ConnectionID: "acct-1"}
	headers, err := codexSession(provider, []byte(`{"prompt_cache_key":"client-thread"}`), "https://x/responses")
	if err != nil || headers["session_id"] != "client-thread" || headers["Accept"] != "text/event-stream" {
		t.Fatalf("%v %v", headers, err)
	}
	if compact, _ := codexSession(provider, []byte(`{}`), "https://x/backend-api/codex/responses/compact"); compact["Accept"] != "" {
		t.Fatalf("the compact endpoint answers JSON, not a stream: %v", compact)
	}
	headers, _ = codexSession(provider, []byte(`{}`), "")
	if headers["session_id"] != connectionSession(provider) {
		t.Fatalf("%v", headers)
	}
	headers, _ = codexSession(routing.ProviderRef{ProviderID: "codex"}, []byte(`not json`), "")
	if headers["session_id"] != "default" {
		t.Fatalf("%v", headers)
	}
}

// A Codex or Grok CLI request is rewritten once, on the way out. The body is a
// typical agent turn: instructions, forty tools and a long input.
func benchResponsesBody() []byte {
	var tools, input strings.Builder
	for i := 0; i < 40; i++ {
		if i > 0 {
			tools.WriteString(",")
		}
		tools.WriteString(`{"type":"function","function":{"name":"tool_` + strings.Repeat("x", i%7) + `","description":"does a thing","parameters":{"type":"object","properties":{"path":{"type":"string","pattern":"^[a-z/]+$"},"n":{"type":"integer"}},"required":["path"]}}}`)
	}
	for i := 0; i < 60; i++ {
		if i > 0 {
			input.WriteString(",")
		}
		input.WriteString(`{"type":"message","role":"user","id":"msg_` + strings.Repeat("a", 24) + `","content":[{"type":"input_text","text":"` + strings.Repeat("some text ", 40) + `"}]}`)
	}
	return []byte(`{"model":"gpt-5.3-codex-high","instructions":"be brief","tools":[` + tools.String() + `],"input":[` + input.String() + `],"reasoning_effort":"high"}`)
}

func BenchmarkCodexFinalizeBody(b *testing.B) {
	provider := routing.ProviderRef{ProviderID: "codex", Protocol: responses, ConnectionID: "acct"}
	body := benchResponsesBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		FinalizeBody(provider, body)
	}
}

func BenchmarkGrokCLIFinalizeBody(b *testing.B) {
	provider := routing.ProviderRef{ProviderID: "grok-cli", Protocol: responses, ConnectionID: "acct"}
	body := benchResponsesBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		FinalizeBody(provider, body)
	}
}

func TestCodexIncludeKeepsTheClientsEntries(t *testing.T) {
	for body, want := range map[string][]any{
		`{"model":"gpt-5.3-codex","input":"x","include":["file_search_call.results"]}`:                               {"file_search_call.results", "reasoning.encrypted_content"},
		`{"model":"gpt-5.3-codex","input":"x","include":["file_search_call.results","reasoning.encrypted_content"]}`: {"file_search_call.results", "reasoning.encrypted_content"},
		`{"model":"gpt-5.3-codex","input":"x","include":[]}`:                                                         {"reasoning.encrypted_content"},
		`{"model":"gpt-5.3-codex","input":"x"}`:                                                                      {"reasoning.encrypted_content"},
	} {
		if got := codexOut(t, body)["include"]; !reflect.DeepEqual(got, any(want)) {
			t.Errorf("%s -> %v, want %v", body, got, want)
		}
	}
}
