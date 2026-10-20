package gemini

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func cloudRequest(t *testing.T, wire, chat string, options RequestOptions, cloud CloudCodeOptions) (map[string]any, error) {
	t.Helper()
	result, _, err := cloudRequestRaw(t, wire, chat, options, cloud)
	return result, err
}

// cloudRequestRaw returns the envelope bytes as well, for assertions that must
// see the wire text rather than a decoded value.
func cloudRequestRaw(t *testing.T, wire, chat string, options RequestOptions, cloud CloudCodeOptions) (map[string]any, string, error) {
	t.Helper()
	body, err := concerns.WireObject([]byte(chat))
	if err != nil {
		t.Fatalf("%s: %v", chat, err)
	}
	if options.Model == "" {
		options.Model = "gemini-3-flash"
	}
	if cloud.Project == "" {
		cloud.Project = "proj-1"
	}
	var out []byte
	if wire == AntigravityProtocol {
		out, _, err = ChatRequestToAntigravity(body, options, cloud)
	} else {
		out, _, err = ChatRequestToGeminiCLI(body, options, cloud)
	}
	if err != nil {
		return nil, "", err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, string(out), nil
}

var bothWires = []string{GeminiCLIProtocol, AntigravityProtocol}

// The envelope names the project, model, user agent, request id and the Gemini
// body (openai-to-gemini.js:343-376).
func TestCloudCodeEnvelopeShape(t *testing.T) {
	for _, wire := range bothWires {
		result, err := cloudRequest(t, wire, `{`+user+`,`+oneTool+`}`, RequestOptions{Model: "gemini-3-flash", Repair: true}, CloudCodeOptions{Project: "p-1", RequestID: "agent-x", SessionID: "42"})
		if err != nil {
			t.Fatalf("%s: %v", wire, err)
		}
		for path, want := range map[string]string{
			"project": `"p-1"`, "model": `"gemini-3-flash"`, "userAgent": `"` + wire + `"`, "requestId": `"agent-x"`,
			"request.sessionId": `"42"`, "request.contents.0.role": `"user"`,
			"request.toolConfig.functionCallingConfig.mode": `"VALIDATED"`,
		} {
			if got := asJSON(at(result, path)); got != want {
				t.Errorf("%s %s = %s, want %s", wire, path, got, want)
			}
		}
		if at(result, "request.requestType") != nil || at(result, "requestType") != nil {
			t.Errorf("%s sends requestType, which the pin comment says earns a 429 (:360-363)", wire)
		}
		// safetySettings: the five OFF for the CLI, absent for Antigravity (:364-367).
		settings, has := concerns.Object(result["request"])["safetySettings"]
		if has != (wire == GeminiCLIProtocol) {
			t.Errorf("%s safetySettings present=%v", wire, has)
		}
		if has {
			if got := asJSON(settings); got != asJSON(defaultSafetySettings) || strings.Contains(got, "null") {
				t.Errorf("%s safetySettings = %s", wire, got)
			}
		}
	}
}

// A project is required. Every shape of "no usable project" is refused and
// nothing is invented (the pin made one up, formats/gemini.js:137-143).
func TestCloudCodeProjectIsRequiredCrossProduct(t *testing.T) {
	cases := map[string]struct {
		project string
		ok      bool
	}{
		"empty": {"", false}, "space": {" ", false}, "padded": {" p ", false}, "newline": {"p\n", false},
		"quote": {`p"x`, false}, "slash": {"a/b", false}, "unicode": {"projé", false},
		"too long": {strings.Repeat("a", 129), false}, "longest": {strings.Repeat("a", 128), true},
		"id": {"my-project-123", true}, "number": {"123456789012", true}, "colon": {"example.com:proj", true},
	}
	for _, wire := range bothWires {
		for name, c := range cases {
			body, _ := concerns.WireObject([]byte(`{` + user + `}`))
			var err error
			if wire == AntigravityProtocol {
				_, _, err = ChatRequestToAntigravity(body, RequestOptions{Model: "gemini-3-flash", Repair: true}, CloudCodeOptions{Project: c.project})
			} else {
				_, _, err = ChatRequestToGeminiCLI(body, RequestOptions{Model: "gemini-3-flash", Repair: true}, CloudCodeOptions{Project: c.project})
			}
			if (err == nil) != c.ok {
				t.Errorf("%s/%s: err=%v, ok want %v", wire, name, err, c.ok)
			}
		}
	}
}

func TestCloudCodeSessionAndRequestIDs(t *testing.T) {
	make1 := func(cloud CloudCodeOptions, scope string) (map[string]any, error) {
		return cloudRequest(t, GeminiCLIProtocol, `{`+user+`}`, RequestOptions{Repair: true, Scope: scope}, cloud)
	}
	// A numeric session passes through; any other is hashed to the numeric form.
	for session, check := range map[string]func(any) bool{
		"12345": func(v any) bool { return v == "12345" }, "-77": func(v any) bool { return v == "-77" },
		"abc": func(v any) bool { s, _ := v.(string); return regexp.MustCompile(`^-[0-9]+$`).MatchString(s) },
	} {
		result, err := make1(CloudCodeOptions{SessionID: session}, "")
		if err != nil || !check(at(result, "request.sessionId")) {
			t.Errorf("session %q: %v %v", session, err, at(result, "request.sessionId"))
		}
	}
	// No session: derived from the scope, the same every time and not the scope
	// itself; with no scope the field is left out, never made up.
	a, _ := make1(CloudCodeOptions{}, "5:alice")
	b, _ := make1(CloudCodeOptions{}, "5:alice")
	c, _ := make1(CloudCodeOptions{}, "3:bob")
	if at(a, "request.sessionId") != toNumericSession("routeweft:5:alice") || at(a, "request.sessionId") != at(b, "request.sessionId") || at(a, "request.sessionId") == at(c, "request.sessionId") {
		t.Errorf("derived sessions: %v %v %v", at(a, "request.sessionId"), at(b, "request.sessionId"), at(c, "request.sessionId"))
	}
	if strings.Contains(asJSON(a), "alice") {
		t.Error("the scope reached the wire")
	}
	if none, _ := make1(CloudCodeOptions{}, ""); concerns.Object(none["request"])["sessionId"] != nil {
		t.Error("a session was invented for a request with no scope")
	}
	// A request id: the caller's, else agent-<uuid>.
	generated, _ := make1(CloudCodeOptions{}, "")
	if !regexp.MustCompile(`^agent-[0-9a-f-]{36}$`).MatchString(fmt.Sprint(generated["requestId"])) {
		t.Errorf("generated request id %v", generated["requestId"])
	}
	for name, cloud := range map[string]CloudCodeOptions{
		"session space": {SessionID: "a b"}, "session long": {SessionID: strings.Repeat("1", 257)}, "session newline": {SessionID: "1\n"},
		"request space": {RequestID: "a b"}, "request long": {RequestID: strings.Repeat("r", 257)}, "request control": {RequestID: "r\x00"},
	} {
		if _, err := make1(cloud, ""); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// An explicit tool choice survives; the pin's VALIDATED replaced it.
func TestCloudCodeToolConfigKeepsTheClientsChoice(t *testing.T) {
	for _, wire := range bothWires {
		for name, c := range map[string]struct{ body, mode string }{
			"no choice":          {`{` + user + `,` + oneTool + `}`, "VALIDATED"},
			"auto":               {`{` + user + `,` + oneTool + `,"tool_choice":"auto"}`, "VALIDATED"},
			"none":               {`{` + user + `,` + oneTool + `,"tool_choice":"none"}`, "NONE"},
			"required":           {`{` + user + `,` + oneTool + `,"tool_choice":"required"}`, "ANY"},
			"named":              {`{` + user + `,` + oneTool + `,"tool_choice":{"type":"function","function":{"name":"f"}}}`, "ANY"},
			"no tools":           {`{` + user + `}`, ""},
			"no tools, a choice": {`{` + user + `,"tool_choice":"none"}`, ""},
		} {
			result, err := cloudRequest(t, wire, c.body, RequestOptions{Repair: true}, CloudCodeOptions{})
			if err != nil {
				t.Fatalf("%s/%s: %v", wire, name, err)
			}
			got, _ := at(result, "request.toolConfig.functionCallingConfig.mode").(string)
			if got != c.mode {
				t.Errorf("%s/%s: mode %q, want %q", wire, name, got, c.mode)
			}
		}
	}
	result, _ := cloudRequest(t, AntigravityProtocol, `{`+user+`,`+oneTool+`,"tool_choice":{"type":"function","function":{"name":"f"}}}`, RequestOptions{Repair: true}, CloudCodeOptions{})
	if got := asJSON(at(result, "request.toolConfig.functionCallingConfig.allowedFunctionNames")); got != `["f"]` {
		t.Errorf("allowed %s", got)
	}
	// An undeclared name is still refused.
	if _, err := cloudRequest(t, GeminiCLIProtocol, `{`+user+`,`+oneTool+`,"tool_choice":{"type":"function","function":{"name":"zzz"}}}`, RequestOptions{Repair: true}, CloudCodeOptions{}); err == nil {
		t.Error("a choice naming an undeclared tool was accepted")
	}
}

// The pin's phase 5 gave every empty object schema a required "reason" argument
// the client never declared and the response never removed. It is omitted, and
// a tool with no properties is declared without parameters (PARITY 6.9 A3).
func TestNoReasonArgumentIsInjected(t *testing.T) {
	for _, wire := range bothWires {
		for name, tool := range map[string]string{
			"no parameters": `{"type":"function","function":{"name":"f"}}`,
			"empty object":  `{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}`,
			"no properties": `{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{}}}}`,
			"nested object": `{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"o":{"type":"object"}}}}}`,
		} {
			result, err := cloudRequest(t, wire, `{`+user+`,"tools":[`+tool+`]}`, RequestOptions{Repair: true}, CloudCodeOptions{})
			if err != nil {
				t.Fatalf("%s/%s: %v", wire, name, err)
			}
			if strings.Contains(asJSON(result), "reason") {
				t.Errorf("%s/%s injected an argument: %s", wire, name, asJSON(at(result, "request.tools")))
			}
		}
	}
}

// top_k is not read by any wire of the family: Chat defines no such field
// (PARITY 6.9 F1). The request is still served.
func TestTopKIsNotCarriedByAnyWire(t *testing.T) {
	for _, wire := range []string{GeminiProtocolForTest, GeminiCLIProtocol, AntigravityProtocol} {
		var result map[string]any
		var err error
		if wire == GeminiProtocolForTest {
			result, _, err = convertWith(t, `{`+user+`,"top_k":40,"top_p":0.5}`, RequestOptions{Repair: true})
			result = map[string]any{"request": result}
		} else {
			result, err = cloudRequest(t, wire, `{`+user+`,"top_k":40,"top_p":0.5}`, RequestOptions{Repair: true}, CloudCodeOptions{})
		}
		if err != nil {
			t.Fatalf("%s: %v", wire, err)
		}
		config := concerns.Object(at(result, "request.generationConfig"))
		if _, present := config["topK"]; present || config["topP"] == nil {
			t.Errorf("%s: %s", wire, asJSON(config))
		}
	}
}

// GeminiProtocolForTest names the plain Gemini wire in the table above.
const GeminiProtocolForTest = Protocol

// The Gemini 4 sampling omission and the max_completion_tokens fallback are the
// base's, shared by both wires.
func TestCloudCodeSharesTheGeminiBase(t *testing.T) {
	for _, wire := range bothWires {
		result, err := cloudRequest(t, wire, `{`+user+`,"temperature":0.4,"max_completion_tokens":9}`, RequestOptions{Model: "gemini-4-pro", Repair: true}, CloudCodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		config := concerns.Object(at(result, "request.generationConfig"))
		if _, present := config["temperature"]; present || asJSON(config["maxOutputTokens"]) != "9" {
			t.Errorf("%s: %s", wire, asJSON(config))
		}
	}
	// Collisions after normalisation are refused, not skipped (executors/antigravity.js:242-248).
	collide := `{` + user + `,"tools":[{"type":"function","function":{"name":"a b"}},{"type":"function","function":{"name":"a_b"}}]}`
	for _, wire := range bothWires {
		if _, err := cloudRequest(t, wire, collide, RequestOptions{Repair: true}, CloudCodeOptions{}); err == nil {
			t.Errorf("%s accepted colliding tool names", wire)
		}
	}
}

// generationConfig keeps an integer above 2^53 byte for byte through both
// Cloud Code wires. The base leg emits json.Number; re-decoding it into
// map[string]any through float64 would round the seed silently.
func TestCloudCodeGenerationConfigKeepsExactIntegers(t *testing.T) {
	const seed = `9007199254740993`
	for _, wire := range bothWires {
		_, raw, err := cloudRequestRaw(t, wire, `{`+user+`,"seed":`+seed+`}`, RequestOptions{Repair: true}, CloudCodeOptions{})
		if err != nil {
			t.Fatalf("%s: %v", wire, err)
		}
		if !strings.Contains(raw, `"seed":`+seed) {
			t.Errorf("%s: the seed was rounded: %s", wire, raw)
		}
	}
}

// Replayed reasoning is sent to the CLI and not to Antigravity, whose executor
// removes thought parts and signature-only parts before sending
// (executors/antigravity.js:203-207).
func TestReplayedReasoningPerWire(t *testing.T) {
	chat := `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_content":"thinking"},{"role":"user","content":"c"}]}`
	cli, err := cloudRequest(t, GeminiCLIProtocol, chat, RequestOptions{Repair: true}, CloudCodeOptions{})
	if err != nil || !strings.Contains(asJSON(cli), `"thought":true`) {
		t.Fatalf("the CLI must carry replayed reasoning: %v %s", err, asJSON(cli))
	}
	ag, err := cloudRequest(t, AntigravityProtocol, chat, RequestOptions{Repair: true}, CloudCodeOptions{})
	if err != nil || strings.Contains(asJSON(ag), "thought") || !strings.Contains(asJSON(ag), `"text":"b"`) {
		t.Fatalf("Antigravity must send the answer and not the thought: %v %s", err, asJSON(ag))
	}
	// A turn that was only reasoning leaves nothing to send, and the request is still valid.
	only := `{"messages":[{"role":"user","content":"a"},{"role":"assistant","reasoning_content":"x"},{"role":"user","content":"c"}]}`
	if _, err := cloudRequest(t, AntigravityProtocol, only, RequestOptions{Repair: true}, CloudCodeOptions{}); err != nil {
		t.Fatal(err)
	}
}

// Antigravity refuses above 64000 output tokens (Q9); the CLI has no such limit
// at the pin.
func TestAntigravityOutputTokenCeiling(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		ok   bool
	}{
		"absent": {`{` + user + `}`, true}, "null": {`{` + user + `,"max_tokens":null}`, true},
		"at the ceiling": {`{` + user + `,"max_tokens":64000}`, true}, "over": {`{` + user + `,"max_tokens":64001}`, false},
		"over by completion name": {`{` + user + `,"max_completion_tokens":64001}`, false},
		"huge":                    {`{` + user + `,"max_tokens":1e9}`, false}, "zero": {`{` + user + `,"max_tokens":0}`, false},
		"wrong type": {`{` + user + `,"max_tokens":"9"}`, false}, "negative": {`{` + user + `,"max_tokens":-1}`, false},
	} {
		_, err := cloudRequest(t, AntigravityProtocol, c.body, RequestOptions{Repair: true}, CloudCodeOptions{})
		if (err == nil) != c.ok {
			t.Errorf("antigravity/%s: err=%v", name, err)
		}
	}
	if _, err := cloudRequest(t, GeminiCLIProtocol, `{`+user+`,"max_tokens":100000}`, RequestOptions{Repair: true}, CloudCodeOptions{}); err != nil {
		t.Errorf("the CLI must not inherit Antigravity's ceiling: %v", err)
	}
}

// The Claude models behind Antigravity: what the pin dropped is refused; the
// temperature default applies only when none was named.
func TestAntigravityClaudeFamily(t *testing.T) {
	claude := RequestOptions{Model: "claude-sonnet-4-6", Repair: true}
	text := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]`
	refused := map[string]string{
		"image":         `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}]}`,
		"remote image":  `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`,
		"document":      `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,QUJD"}}]}]}`,
		"audio":         `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"QUJD","format":"wav"}}]}]}`,
		"untyped part":  `{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`,
		"json_object":   text + `,"response_format":{"type":"json_object"}}`,
		"json_schema":   text + `,"response_format":{"type":"json_schema","json_schema":{"name":"n","schema":{"type":"object","properties":{"a":{"type":"string"}}}}}}`,
		"format string": text + `,"response_format":"json"}`,
		"format array":  text + `,"response_format":[]}`,
	}
	for name, body := range refused {
		if _, err := cloudRequest(t, AntigravityProtocol, body, claude, CloudCodeOptions{}); err == nil {
			t.Errorf("claude/%s was accepted", name)
		}
		// The same request for a Gemini model is not refused by this rule (images
		// and JSON are carried by the shared base), except where the base refuses.
	}
	for name, body := range map[string]string{
		"text part": text + `}`, "string": `{` + user + `}`, "text format": text + `,"response_format":{"type":"text"}}`,
		"null format": text + `,"response_format":null}`, "empty type": text + `,"response_format":{}}`,
	} {
		if _, err := cloudRequest(t, AntigravityProtocol, body, claude, CloudCodeOptions{}); err != nil {
			t.Errorf("claude/%s: %v", name, err)
		}
	}
	image := refused["image"]
	if _, err := cloudRequest(t, AntigravityProtocol, image, RequestOptions{Model: "gemini-3-flash", Repair: true}, CloudCodeOptions{}); err != nil {
		t.Errorf("a Gemini model behind Antigravity must keep its images: %v", err)
	}
	if _, err := cloudRequest(t, GeminiCLIProtocol, image, claude, CloudCodeOptions{}); err != nil {
		t.Errorf("the CLI has no Claude branch: %v", err)
	}
	// Temperature: default 1 when none, an explicit value (0 included) kept.
	for name, c := range map[string]struct{ body, want string }{
		"absent": {`{` + user + `}`, "1"}, "null": {`{` + user + `,"temperature":null}`, "1"},
		"zero": {`{` + user + `,"temperature":0}`, "0"}, "explicit": {`{` + user + `,"temperature":0.3}`, "0.3"},
	} {
		result, err := cloudRequest(t, AntigravityProtocol, c.body, claude, CloudCodeOptions{})
		if err != nil || asJSON(at(result, "request.generationConfig.temperature")) != c.want {
			t.Errorf("temperature/%s: %v %s", name, err, asJSON(at(result, "request.generationConfig")))
		}
	}
	if result, _ := cloudRequest(t, AntigravityProtocol, `{`+user+`}`, RequestOptions{Model: "gemini-3-flash", Repair: true}, CloudCodeOptions{}); at(result, "request.generationConfig.temperature") != nil {
		t.Error("the temperature default leaked to a Gemini model")
	}
	if result, _ := cloudRequest(t, GeminiCLIProtocol, `{`+user+`}`, claude, CloudCodeOptions{}); at(result, "request.generationConfig.temperature") != nil {
		t.Error("the temperature default leaked to the CLI")
	}
}

// Signatures: the placeholder goes to the Gemini family only; a Claude call with
// no held signature is unsigned (PARITY 6.9 A6).
func TestPlaceholderSignatureIsGeminiFamilyOnly(t *testing.T) {
	chat := `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"r"}]}`
	for model, want := range map[string]any{"gemini-3-flash": PlaceholderSignature, "claude-opus-4-6-thinking": nil, "gpt-oss-120b-medium": PlaceholderSignature} {
		result, err := cloudRequest(t, AntigravityProtocol, chat, RequestOptions{Model: model, Repair: true}, CloudCodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := at(result, "request.contents.1.parts.0.thoughtSignature"); got != want {
			t.Errorf("%s: signature %v, want %v", model, got, want)
		}
	}
	// The CLI keeps the placeholder whatever the model is named.
	result, _ := cloudRequest(t, GeminiCLIProtocol, chat, RequestOptions{Model: "claude-x", Repair: true}, CloudCodeOptions{})
	if at(result, "request.contents.1.parts.0.thoughtSignature") != PlaceholderSignature {
		t.Error("the CLI lost the placeholder")
	}
}

// FamilyScope keeps the client/connection scope as its leading, length-prefixed
// part. Two tenants never meet, whatever family or model they use, and an empty
// scope stays empty.
func TestFamilyScopeKeepsTheTenant(t *testing.T) {
	if FamilyScope("", "claude-opus") != "" || FamilyScope("", "gemini-3") != "" || FamilyScope("", "") != "" {
		t.Error("an empty scope gained a family and could now be shared")
	}
	seen := map[string]string{}
	for _, scope := range []string{"1:a", "a", "3:bcd", "bcd", "1:ab", "ab", "0:", "alice|conn", "bob|conn"} {
		for _, model := range []string{"claude-sonnet-4-6", "CLAUDE-OPUS", "gemini-3-flash", "gemini-3.8-flash-tiered", "gpt-oss-120b-medium", "other", "gemini", ""} {
			key := FamilyScope(scope, model)
			if key == "" {
				t.Fatalf("(%q,%q) has no scope", scope, model)
			}
			label := scope + "|" + modelFamily(model)
			if prior, dup := seen[key]; dup && prior != label {
				t.Errorf("(%q,%q) collides with %s", scope, model, prior)
			}
			seen[key] = label
		}
	}
	if FamilyScope("t", "claude-sonnet-4-6") != FamilyScope("t", "claude-opus-4-6-thinking") || FamilyScope("t", "gemini-3-flash") != FamilyScope("t", "gemini-3.8-flash") {
		t.Error("models of one family must share their signatures")
	}
	if FamilyScope("t", "claude-sonnet-4-6") == FamilyScope("t", "gemini-3-flash") {
		t.Error("the families share a scope")
	}
}

// A signature a response carried is replayed to the same tenant and family only:
// the other family of the same tenant, and every family of another tenant, are
// served none (thoughtSignatureStore.js:14-24; antigravity-thought-signature-family.test.js).
func TestSignaturesAreScopedByTenantAndFamily(t *testing.T) {
	for _, stream := range []bool{false, true} {
		cache := NewSignatures(20, time.Hour)
		stored := func(tenant, model, signature string) {
			t.Helper()
			object := `{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{}},"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}]}}`
			options := ResponseOptions{Model: model, Signatures: cache, Scope: tenant}
			var err error
			if stream {
				_, _, err = streamed(t, context.Background(), sse(object, object), options)
			} else {
				_, err = buffered(t, object, options)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		stored("alice|conn", "claude-opus-4-6-thinking", "CLAUDE-SIG")
		stored("alice|conn", "gemini-3-flash", "GEMINI-SIG")
		chat := `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"r"}]}`
		sent := func(wire, tenant, model string) any {
			result, err := cloudRequest(t, wire, chat, RequestOptions{Model: model, Repair: true, Signatures: cache, Scope: tenant}, CloudCodeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return at(result, "request.contents.1.parts.0.thoughtSignature")
		}
		for _, c := range []struct {
			tenant, model string
			want          any
		}{
			{"alice|conn", "claude-opus-4-6-thinking", "CLAUDE-SIG"}, {"alice|conn", "claude-sonnet-4-6", "CLAUDE-SIG"},
			{"alice|conn", "gemini-3-flash", "GEMINI-SIG"}, {"alice|conn", "gemini-3.8-flash-tiered", "GEMINI-SIG"},
			{"bob|conn", "claude-opus-4-6-thinking", nil}, {"bob|conn", "gemini-3-flash", PlaceholderSignature},
			{"alice|other", "claude-opus-4-6-thinking", nil}, {"", "claude-opus-4-6-thinking", nil},
			{"", "gemini-3-flash", PlaceholderSignature},
		} {
			for _, wire := range bothWires {
				want := c.want
				if want == nil && wire == GeminiCLIProtocol {
					want = PlaceholderSignature
				}
				if got := sent(wire, c.tenant, c.model); got != want {
					t.Errorf("stream=%v %s/%s/%s: %v, want %v", stream, wire, c.tenant, c.model, got, want)
				}
			}
		}
		// With no tenant nothing is stored, whatever the family.
		before := cache.Len()
		stored("", "claude-x", "S")
		if cache.Len() != before {
			t.Errorf("stored %d with no tenant", cache.Len()-before)
		}
	}
}
