package wire

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// utils/opencodeFingerprint.js: the quartet is canonicalised to lowercase, case
// duplicates collapse to one declaration, missing members are appended, and a
// caller's spelling is restored on the answer.
func TestOpenCodeFingerprintToolsMatchThePin(t *testing.T) {
	raw := `{"model":"grok-code","messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"Bash","description":"run","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"bash","description":"dup","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"Glob","description":"glob","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"weather","description":"w","parameters":{"type":"object"}}}]}`
	body, renamed, err := opencodeFingerprint([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := concerns.WireObject(body)
	tools, _ := parsed["tools"].([]any)
	names := map[string]int{}
	for _, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		names[concerns.Text(function["name"])]++
	}
	for _, want := range []string{"bash", "glob", "grep", "read", "weather"} {
		if names[want] != 1 {
			t.Fatalf("tool %q appears %d times: %v", want, names[want], names)
		}
	}
	if renamed["bash"] != "Bash" || renamed["glob"] != "Glob" {
		t.Fatalf("rename map %v", renamed)
	}
	if _, ok := parsed["tool_choice"]; ok {
		t.Fatalf("a caller with tools must not get a tool_choice: %v", parsed["tool_choice"])
	}
	if !strings.Contains(string(body), opencodeFingerprintHint) {
		t.Fatalf("appended tools do not carry the pin's description: %s", body)
	}
}

func TestOpenCodeFingerprintWithoutClientToolsForbidsTheDecoys(t *testing.T) {
	body, renamed, err := opencodeFingerprint([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := concerns.WireObject(body)
	if parsed["tool_choice"] != "none" {
		t.Fatalf("tool_choice = %v, want none", parsed["tool_choice"])
	}
	if len(renamed) != 0 {
		t.Fatalf("unexpected renames %v", renamed)
	}
}

func TestOpenCodeResponseRestoresCallerToolNames(t *testing.T) {
	renamed := map[string]string{"bash": "Bash"}
	payload, _ := concerns.WireObject([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"bash","arguments":"{}"}}]}}]}`))
	if !opencodeRestore(payload, renamed) {
		t.Fatal("name was not restored")
	}
	function := payload["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "Bash" {
		t.Fatalf("name = %v", function["name"])
	}
}

func TestOpenCodeDoSendsTheQuartetAndRestoresTheAnswer(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sent, _ = concerns.WireObject(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"bash\",\"arguments\":\"{}\"}}]}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}]}`))
	response, err := opencodeDo(routing.ProviderRef{ProviderID: "opencode"}, server.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(answer), `"name":"Bash"`) {
		t.Fatalf("caller name not restored: %s", answer)
	}
	tools, _ := sent["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("sent %d tools, want the quartet: %v", len(tools), sent["tools"])
	}
}
