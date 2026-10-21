package parity

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

func TestPinnedCapabilities(t *testing.T) {
	pinnedOracle(t)
	check := exec.Command("node", "compat/parity/generate-capabilities.mjs", "--check")
	check.Dir = "../.."
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("generated data: %v\n%s", err, out)
	}
	type query struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	var cases []query
	for _, provider := range registry.Builtins() {
		for _, model := range provider.SeedModels {
			cases = append(cases, query{provider.ID, model.ID})
		}
	}
	for _, q := range []query{{"cx", "gpt-5.5"}, {"cmc", "vendor/deepseek-v4-pro"}, {"commandcode", "unknown"}, {"", "new-vl-model"}, {"", "future-image-vl"}, {"", "CLAUDE-HAIKU-NEW"}, {"", "vendor/claude-opus-4.8"}, {"", ""}} {
		cases = append(cases, q)
	}
	input, _ := json.Marshal(cases)
	cmd := exec.Command("node", "compat/parity/capabilities.mjs", "resolve")
	cmd.Dir = "../.."
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	var want []map[string]any
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(cases) {
		t.Fatalf("oracle returned %d results for %d cases", len(want), len(cases))
	}
	c := capabilities.New()
	for i, q := range cases {
		raw, _ := json.Marshal(c.Resolve(q.Provider, q.Model))
		var got map[string]any
		json.Unmarshal(raw, &got)
		if !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("%s/%s: got %s, pin %v", q.Provider, q.Model, raw, want[i])
		}
	}
}
