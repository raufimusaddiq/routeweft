package gemini

import (
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func cloudInbound(t *testing.T, wire, body string) (map[string]any, error) {
	t.Helper()
	parsed, err := concerns.WireObject([]byte(body))
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	var out []byte
	if wire == AntigravityProtocol {
		out, err = AntigravityRequestToChat(parsed, "m", true)
	} else {
		out, err = GeminiCLIRequestToChat(parsed, "m", true)
	}
	if err != nil {
		return nil, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, nil
}

// A bare Gemini body and a request-wrapped one are the same conversation; the
// envelope's routing fields are not used; a body that says it twice, or says
// something the gateway cannot honour, is refused.
func TestCloudCodeEnvelopeOnTheClientLegs(t *testing.T) {
	inner := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0.5}}`
	for _, wire := range bothWires {
		bare, err := cloudInbound(t, wire, inner)
		if err != nil {
			t.Fatal(err)
		}
		wrapped := `{"project":"p","model":"x","userAgent":"u","requestId":"r","user_prompt_id":"q","request":` + inner + `}`
		got, err := cloudInbound(t, wire, wrapped)
		if err != nil || asJSON(got) != asJSON(bare) {
			t.Errorf("%s: wrapped differs: %v\n%s\n%s", wire, err, asJSON(got), asJSON(bare))
		}
		if got["model"] != "m" {
			t.Errorf("%s: the envelope's model replaced the routed one: %v", wire, got["model"])
		}
		// The client's own session is dropped with the envelope fields.
		withSession := `{"request":{"sessionId":"-1","session_id":"2",` + inner[1:] + `}`
		if got, err := cloudInbound(t, wire, withSession); err != nil || strings.Contains(asJSON(got), "-1") {
			t.Errorf("%s: session: %v %s", wire, err, asJSON(got))
		}
		for _, fields := range []string{
			`"project":"x","model":"y"`, `"userAgent":"u"`,
			`"requestId":"r","user_prompt_id":"q"`, `"sessionId":"-1","session_id":"2"`,
		} {
			for _, body := range []string{
				`{` + fields + `,` + inner[1:],
				`{` + fields + `,"request":` + inner + `}`,
			} {
				got, err := cloudInbound(t, wire, body)
				if err != nil || asJSON(got) != asJSON(bare) {
					t.Errorf("%s routing fields %s: %v %s", wire, fields, err, asJSON(got))
				}
			}
		}
		for name, c := range map[string]struct {
			body string
			ok   bool
		}{
			"bare agent type":   {`{"requestType":"agent",` + inner[1:], true},
			"bare null type":    {`{"requestType":null,` + inner[1:], true},
			"bare image type":   {`{"requestType":"image_gen",` + inner[1:], false},
			"bare search type":  {`{"requestType":"search",` + inner[1:], false},
			"bare empty type":   {`{"requestType":"",` + inner[1:], false},
			"bare number type":  {`{"requestType":1,` + inner[1:], false},
			"bare object type":  {`{"requestType":{},` + inner[1:], false},
			"bare array type":   {`{"requestType":[],` + inner[1:], false},
			"bare unknown":      {`{"surprise":1,` + inner[1:], false},
			"inner unknown":     {`{"request":{"surprise":1,` + inner[1:] + `}`, false},
			"inner image type":  {`{"request":{"requestType":"image_gen",` + inner[1:] + `}`, false},
			"inner search type": {`{"request":{"requestType":"search",` + inner[1:] + `}`, false},
			"agent type":        {`{"requestType":"agent","request":` + inner + `}`, true},
			"null type":         {`{"requestType":null,"request":` + inner + `}`, true},
			"null request":      {inner[:len(inner)-1] + `,"request":null}`, true},
			"image type":        {`{"requestType":"image_gen","request":` + inner + `}`, false},
			"empty type":        {`{"requestType":"","request":` + inner + `}`, false},
			"number type":       {`{"requestType":1,"request":` + inner + `}`, false},
			"search type":       {`{"requestType":"search","request":` + inner + `}`, false},
			"request string":    {`{"request":"x"}`, false},
			"request array":     {`{"request":[]}`, false},
			"request number":    {`{"request":1}`, false},
			"request bool":      {`{"request":true}`, false},
			"request empty":     {`{"request":{}}`, false},
			"both contents":     {`{"contents":[{"role":"user","parts":[{"text":"x"}]}],"request":` + inner + `}`, false},
			"both tools":        {`{"tools":[],"request":` + inner + `}`, false},
			"both generation":   {`{"generationConfig":{},"request":` + inner + `}`, false},
			"unknown envelope":  {`{"surprise":1,"request":` + inner + `}`, false},
			"no contents":       {`{"request":{"generationConfig":{}}}`, false},
			"null contents":     {`{"request":{"contents":null}}`, false},
			"empty contents":    {`{"request":{"contents":[]}}`, false},
			"contents object":   {`{"request":{"contents":{}}}`, false},
			"bare empty":        {`{}`, false},
			"bare no contents":  {`{"generationConfig":{}}`, false},
		} {
			_, err := cloudInbound(t, wire, c.body)
			if (err == nil) != c.ok {
				t.Errorf("%s/%s: err=%v", wire, name, err)
			}
		}
	}
}

// topK is not carried by any wire of the family, and the thinkingConfig rule
// belongs to Antigravity alone (PARITY 6.8 F1, F2).
func TestInboundTopKAndThinking(t *testing.T) {
	for _, wire := range bothWires {
		got, err := cloudInbound(t, wire, `{`+hello+`,"generationConfig":{"topK":3,"topP":0.5}}`)
		if err != nil || got["top_k"] != nil || got["top_p"] == nil {
			t.Errorf("%s: %v %s", wire, err, asJSON(got))
		}
	}
	cases := map[string]struct {
		config string
		want   any
		ok     bool
	}{
		"absent": {``, nil, true}, "null": {`"thinkingConfig":null`, nil, true}, "empty": {`"thinkingConfig":{}`, nil, true},
		"budget 1": {`"thinkingConfig":{"thinkingBudget":1}`, "low", true}, "budget 2048": {`"thinkingConfig":{"thinkingBudget":2048}`, "low", true},
		"budget 2049": {`"thinkingConfig":{"thinkingBudget":2049}`, "medium", true}, "budget 16384": {`"thinkingConfig":{"thinkingBudget":16384}`, "medium", true},
		"budget 16385": {`"thinkingConfig":{"thinkingBudget":16385}`, "high", true}, "budget huge": {`"thinkingConfig":{"thinkingBudget":9999999}`, "high", true},
		"zero":                {`"thinkingConfig":{"thinkingBudget":0}`, nil, true},
		"dynamic":             {`"thinkingConfig":{"thinkingBudget":-1}`, nil, false},
		"negative":            {`"thinkingConfig":{"thinkingBudget":-5}`, nil, false},
		"string budget":       {`"thinkingConfig":{"thinkingBudget":"8"}`, nil, false},
		"null budget":         {`"thinkingConfig":{"thinkingBudget":null}`, nil, true},
		"include true":        {`"thinkingConfig":{"includeThoughts":true}`, nil, true},
		"include true+budget": {`"thinkingConfig":{"includeThoughts":true,"thinkingBudget":5000}`, "medium", true},
		"include false":       {`"thinkingConfig":{"includeThoughts":false}`, nil, true},
		"include string":      {`"thinkingConfig":{"includeThoughts":"yes"}`, nil, false},
		"level":               {`"thinkingConfig":{"thinkingLevel":"HIGH"}`, nil, false},
		"unknown key":         {`"thinkingConfig":{"mystery":1}`, nil, false},
		"string config":       {`"thinkingConfig":"on"`, nil, false},
		"array config":        {`"thinkingConfig":[]`, nil, false},
	}
	for name, c := range cases {
		body := `{` + hello
		if c.config != "" {
			body += `,"generationConfig":{` + c.config + `}`
		}
		body += `}`
		got, err := cloudInbound(t, AntigravityProtocol, body)
		if (err == nil) != c.ok {
			t.Errorf("antigravity/%s: err=%v", name, err)
			continue
		}
		if err == nil && got["reasoning_effort"] != c.want {
			t.Errorf("antigravity/%s: reasoning_effort %v, want %v", name, got["reasoning_effort"], c.want)
		}
		// The CLI keeps the Gemini rule: thinkingConfig is not carried, whatever it says.
		if cli, err := cloudInbound(t, GeminiCLIProtocol, body); err != nil || cli["reasoning_effort"] != nil {
			t.Errorf("gemini-cli/%s: %v %v", name, err, cli["reasoning_effort"])
		}
	}
}

// Antigravity lowercases schema types and drops enumDescriptions at the schema,
// each property and items (antigravity-to-openai.js:89-116); the CLI passes the
// parameters through, as the pin's shared function does.
func TestInboundToolSchemas(t *testing.T) {
	tool := `"tools":[{"functionDeclarations":[{"name":"f","parameters":{"type":"OBJECT","enumDescriptions":["x"],` +
		`"properties":{"a":{"type":"STRING","enumDescriptions":["d"]},"b":{"type":"ARRAY","items":{"type":"INTEGER"}},"enumDescriptions":{"type":"STRING"}}}}]}]`
	got, err := cloudInbound(t, AntigravityProtocol, `{`+hello+`,`+tool+`}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"properties":{"a":{"type":"string"},"b":{"items":{"type":"integer"},"type":"array"},"enumDescriptions":{"type":"string"}},"type":"object"}`
	if params := asJSON(at(got, "tools.0.function.parameters")); params != want {
		t.Errorf("parameters %s", params)
	}
	cli, err := cloudInbound(t, GeminiCLIProtocol, `{`+hello+`,`+tool+`}`)
	if err != nil || at(cli, "tools.0.function.parameters.type") != "OBJECT" {
		t.Errorf("the CLI must pass parameters through: %v %v", err, at(cli, "tools.0.function.parameters.type"))
	}
	// A tool with no parameters, or non-object parameters, survives the walk.
	for _, params := range []string{``, `,"parameters":null`, `,"parameters":"x"`} {
		if _, err := cloudInbound(t, AntigravityProtocol, `{`+hello+`,"tools":[{"functionDeclarations":[{"name":"f"`+params+`}]}]}`); err != nil {
			t.Errorf("parameters %q: %v", params, err)
		}
	}
}
