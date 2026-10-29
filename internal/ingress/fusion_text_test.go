package ingress

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The recorded oracle runs of handleFusionChat (compat/parity/generate-combo-routing.mjs)
// hold the exact bodies the oracle sent to its panel and to its judge. These tests
// rebuild them with Routeweft's helpers.
type fusionPin struct {
	Name    string         `json:"name"`
	Panel   []string       `json:"panel"`
	Body    map[string]any `json:"body"`
	Members map[string]struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"members"`
	Calls []struct {
		Model string         `json:"model"`
		Panel bool           `json:"panel"`
		Body  map[string]any `json:"body"`
	} `json:"calls"`
}

func loadFusionPin(t *testing.T) []fusionPin {
	t.Helper()
	raw, err := os.ReadFile("testdata/combo_routing_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Fusion []fusionPin `json:"fusion"`
	}
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.Fusion) < 20 {
		t.Fatalf("pin has %d fusion scenarios", len(pin.Fusion))
	}
	return pin.Fusion
}

func normalized(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFusionPanelBodyMatchesTheOracle(t *testing.T) {
	for _, scenario := range loadFusionPin(t) {
		for _, call := range scenario.Calls {
			if !call.Panel {
				continue
			}
			body, _ := json.Marshal(scenario.Body)
			got, ok := panelBody(body, true)
			if !ok {
				t.Fatalf("%s: panel body not built", scenario.Name)
			}
			var gotValue any
			_ = json.Unmarshal(got, &gotValue)
			if !reflect.DeepEqual(gotValue, normalized(t, call.Body)) {
				t.Errorf("%s: panel body\n got  %s\n want %s", scenario.Name, got, mustMarshal(call.Body))
			}
			break // every panel call of a scenario gets the same body
		}
	}
}

// The judge prompt and the user turn that carries it are rebuilt from the sources
// the oracle's own prompt lists, in the body shape of the request.
func TestFusionJudgeBodyMatchesTheOracle(t *testing.T) {
	judged := 0
	for _, scenario := range loadFusionPin(t) {
		for _, call := range scenario.Calls {
			if call.Panel {
				continue
			}
			body, _ := json.Marshal(scenario.Body)
			// The final call is the judge's only when its request differs from the original.
			promptBody, _ := json.Marshal(call.Body)
			marker := "=== PANEL RESPONSES ==="
			if !strings.Contains(string(promptBody), marker) {
				if !reflect.DeepEqual(normalized(t, call.Body), normalized(t, scenario.Body)) {
					t.Errorf("%s: a final call that is not the judge's must carry the original request", scenario.Name)
				}
				continue
			}
			judged++
			// Pull the prompt back out of whichever array holds it.
			var prompt string
			var sent map[string]any
			_ = json.Unmarshal(promptBody, &sent)
			for _, key := range []string{"messages", "input", "contents"} {
				if items, ok := sent[key].([]any); ok && len(items) > 0 {
					last := items[len(items)-1].(map[string]any)
					if text, ok := last["content"].(string); ok {
						prompt = text
					} else if parts, ok := last["parts"].([]any); ok {
						prompt = parts[0].(map[string]any)["text"].(string)
					}
				}
			}
			var answers []fusionAnswer
			for _, text := range promptSources(t, scenario.Name, prompt) {
				answers = append(answers, fusionAnswer{text: text})
			}
			if len(answers) < 2 {
				t.Fatalf("%s: could not read the sources out of the oracle's prompt", scenario.Name)
			}
			if rebuilt := buildJudgePrompt(answers); rebuilt != prompt {
				t.Errorf("%s: judge prompt\n got  %q\n want %q", scenario.Name, rebuilt, prompt)
			}
			got, ok := appendUserTurn(body, prompt)
			if !ok {
				t.Fatalf("%s: user turn not appended", scenario.Name)
			}
			var gotValue any
			_ = json.Unmarshal(got, &gotValue)
			if !reflect.DeepEqual(gotValue, normalized(t, call.Body)) {
				t.Errorf("%s: judge body\n got  %s\n want %s", scenario.Name, got, promptBody)
			}
		}
	}
	if judged < 10 {
		t.Fatalf("only %d judge calls were checked", judged)
	}
}

// Each panel answer's text is what the oracle takes from it, in all the shapes it
// reads, where the oracle's own judge prompt shows the text it took.
func TestFusionPanelTextMatchesTheOracle(t *testing.T) {
	checked := 0
	for _, scenario := range loadFusionPin(t) {
		var prompt string
		for _, call := range scenario.Calls {
			if call.Panel {
				continue
			}
			encoded, _ := json.Marshal(call.Body)
			var sent map[string]any
			_ = json.Unmarshal(encoded, &sent)
			for _, key := range []string{"messages", "input", "contents"} {
				items, _ := sent[key].([]any)
				if len(items) == 0 {
					continue
				}
				last, _ := items[len(items)-1].(map[string]any)
				text, _ := last["content"].(string)
				if parts, ok := last["parts"].([]any); ok {
					text, _ = parts[0].(map[string]any)["text"].(string)
				}
				if strings.Contains(text, "=== PANEL RESPONSES ===") {
					prompt = text
				}
			}
		}
		if prompt == "" {
			continue
		}
		// What Routeweft takes from each successful answer, in panel order. The
		// sources the oracle judged are those texts less any it dropped for timing.
		var texts []string
		for _, model := range scenario.Panel {
			member := scenario.Members[model]
			if answer, ok := decodeObject([]byte(member.Body)); ok && member.Status == 200 {
				if text := extractPanelText(answer); text != "" {
					texts = append(texts, text)
				}
			}
		}
		next := 0
		for _, source := range promptSources(t, scenario.Name, prompt) {
			for next < len(texts) && texts[next] != source {
				next++
			}
			if next == len(texts) {
				t.Errorf("%s: the oracle judged %q, which Routeweft does not take from any answer (it takes %q)", scenario.Name, source, texts)
				break
			}
			next++
			checked++
		}
	}
	if checked < 20 {
		t.Fatalf("only %d judged sources were checked", checked)
	}
}

// promptSources reads the sources out of a judge prompt, in order.
func promptSources(t *testing.T, name, prompt string) []string {
	t.Helper()
	panel := strings.TrimPrefix(strings.SplitN(strings.SplitN(prompt, "\n=== END PANEL RESPONSES ===", 2)[0], "=== PANEL RESPONSES ===\n", 2)[1], "[Source ")
	var sources []string
	for index, section := range strings.Split(panel, "\n\n[Source ") {
		if !strings.HasPrefix(section, string(rune('0'+index+1))+"]\n") {
			t.Fatalf("%s: source %d is not numbered in order", name, index+1)
		}
		sources = append(sources, strings.SplitN(section, "]\n", 2)[1])
	}
	return sources
}

func mustMarshal(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
