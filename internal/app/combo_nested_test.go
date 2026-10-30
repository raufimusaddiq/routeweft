package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// multiProviderApp is servingApp with one provider (and one account) per model,
// so a failure cools only that model's account, the way the oracle's per-model
// locks keep one model's failure from taking its neighbours out.
func multiProviderApp(t *testing.T, upstream *upstreamLog, models []string) (*App, string) {
	t.Helper()
	server := httptest.NewServer(upstream.handler())
	t.Cleanup(server.Close)
	upstream.stop = server.Close
	a := New(Config{DataDir: storetest.DataDirWithAdmin(t, "operator", c03Password), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true, AdminBootstrapUsername: "operator", AdminBootstrapPassword: c03Password}, nil)
	ctx := context.Background()
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() })
	_, key, err := a.runtime.CreateAPIKey(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		provider := "p" + model
		node, err := a.creds.PutNode(ctx, credentials.Node{ID: "node-" + provider, Kind: credentials.NodeGeneric, ProviderID: provider, Name: provider, Prefix: provider, BaseURL: server.URL + "/v1", Transports: []string{"openai-chat", "openai-responses", "anthropic-messages"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.creds.PutConnection(ctx, credentials.Connection{ID: "account-" + provider, NodeID: node.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{AccessToken: "synthetic-" + provider}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: provider, ID: model}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return a, key
}

// Nested Combos follow the oracle's walk (compat/parity/generate-combo-routing.mjs
// runs its combo.js over the same Combos): a nested Combo is served by its own
// strategy when the outer Combo reaches it, and its rotation moves only then.
func TestComboServingNestedCombosMatchTheOracle(t *testing.T) {
	raw, err := os.ReadFile("../ingress/testdata/combo_routing_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		NestedTable    map[string][]string `json:"nestedTable"`
		NestedStrategy map[string]string   `json:"nestedStrategy"`
		Nested         []struct {
			Name  string `json:"name"`
			Root  string `json:"root"`
			Calls []struct {
				Results          map[string]json.RawMessage `json:"results"`
				Tried            []string                   `json:"tried"`
				Status           int                        `json:"status"`
				Message          *string                    `json:"message"`
				RetryAfterHeader *string                    `json:"retryAfterHeader"`
				Waits            []int                      `json:"waits"`
			} `json:"calls"`
		} `json:"nested"`
	}
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.Nested) < 6 {
		t.Fatalf("pin has %d nested scenarios", len(pin.Nested))
	}
	// Leaves are written "p/a" in the pin; here a is a model of provider pa.
	leaf := func(ref string) string { return strings.TrimPrefix(ref, "p/") }
	for _, scenario := range pin.Nested {
		t.Run(scenario.Name, func(t *testing.T) {
			upstream := &upstreamLog{fail: map[string]int{}}
			a, key := multiProviderApp(t, upstream, []string{"a", "b", "c", "d", "z"})
			var combos []runtime.Combo
			for name, models := range pin.NestedTable {
				combo := runtime.Combo{ID: name, Name: name, Strategy: pin.NestedStrategy[name]}
				for position, model := range models {
					member := runtime.ComboMember{Position: position, Selected: true}
					if strings.Contains(model, "/") {
						member.ProviderID, member.ModelID = "p"+leaf(model), leaf(model)
					} else {
						member.ModelID = model // a nested Combo
					}
					combo.Members = append(combo.Members, member)
				}
				combos = append(combos, combo)
			}
			if _, err := a.runtime.SetCombos(context.Background(), combos); err != nil {
				t.Fatal(err)
			}
			for index, call := range scenario.Calls {
				upstream.reset()
				upstream.mu.Lock()
				upstream.fail = map[string]int{}
				upstream.count = nil
				upstream.script = func(model string, _ int) (time.Duration, int, string) {
					spec, ok := call.Results["p/"+model]
					if !ok {
						return 0, 200, `{"choices":[{"message":{"content":"ok"}}]}`
					}
					var status struct {
						Status       int    `json:"status"`
						Text         string `json:"text"`
						RetryOffsetM *int64 `json:"retryOffsetMs"`
					}
					if json.Unmarshal(spec, &status.Status) != nil {
						_ = json.Unmarshal(spec, &status)
					}
					if status.Status == 200 {
						return 0, 200, `{"choices":[{"message":{"content":"ok"}}]}`
					}
					if status.Text == "" {
						status.Text = "scripted"
					}
					failure := map[string]any{"error": map[string]any{"message": status.Text}}
					if status.RetryOffsetM != nil {
						failure["retryAfter"] = time.Now().Add(time.Duration(*status.RetryOffsetM) * time.Millisecond).UTC().Format(time.RFC3339Nano)
					}
					encoded, _ := json.Marshal(failure)
					return 0, status.Status, string(encoded)
				}
				upstream.mu.Unlock()
				for _, model := range []string{"a", "b", "c", "d", "z"} {
					a.runtime.State().ClearCooldown("account-p" + model)
				}
				started := time.Now()
				w := post(a, key, fmt.Sprintf(textRequest, scenario.Root))
				elapsed := time.Since(started)
				want := make([]string, 0, len(call.Tried))
				for _, ref := range call.Tried {
					want = append(want, leaf(ref))
				}
				if got := upstream.seen(); got != strings.Join(want, " ") || w.Code != call.Status {
					t.Fatalf("call %d: tried %q status %d, oracle %q status %d (body %s)", index, got, w.Code, strings.Join(want, " "), call.Status, w.Body)
				}
				if call.Message != nil {
					var body struct {
						Error struct {
							Message string `json:"message"`
						} `json:"error"`
					}
					_ = json.Unmarshal(w.Body.Bytes(), &body)
					if body.Error.Message != *call.Message {
						t.Fatalf("call %d: message %q, oracle %q", index, body.Error.Message, *call.Message)
					}
				}
				header := w.Header().Get("Retry-After")
				if (call.RetryAfterHeader == nil) != (header == "") || call.RetryAfterHeader != nil && header != *call.RetryAfterHeader {
					t.Fatalf("call %d: Retry-After %q, oracle %v", index, header, call.RetryAfterHeader)
				}
				// The pauses before falling past a transient failure are real time.
				var paused time.Duration
				for _, wait := range call.Waits {
					paused += time.Duration(wait) * time.Millisecond
				}
				if elapsed < paused-100*time.Millisecond || elapsed > paused+3*time.Second {
					t.Fatalf("call %d: took %v, the oracle's pauses add up to %v", index, elapsed, paused)
				}
			}
		})
	}
}

// A Combo that reaches itself is refused when it is saved, and the nested name
// must be a Combo; the oracle would recurse until the process failed.
func TestNestedComboCompileRules(t *testing.T) {
	upstream := &upstreamLog{}
	a, _ := multiProviderApp(t, upstream, []string{"a", "b"})
	ctx := context.Background()
	member := func(provider, model string, position int) runtime.ComboMember {
		return runtime.ComboMember{ProviderID: provider, ModelID: model, Position: position, Selected: true}
	}
	if _, err := a.runtime.SetCombos(ctx, []runtime.Combo{
		{ID: "x", Name: "x", Members: []runtime.ComboMember{member("", "y", 0), member("pa", "a", 1)}},
		{ID: "y", Name: "y", Members: []runtime.ComboMember{member("pb", "b", 0)}},
	}); err != nil {
		t.Fatalf("a Combo naming another Combo must be accepted: %v", err)
	}
	for name, combos := range map[string][]runtime.Combo{
		"itself":          {{ID: "x", Name: "x", Members: []runtime.ComboMember{member("", "x", 0)}}},
		"a cycle":         {{ID: "x", Name: "x", Members: []runtime.ComboMember{member("", "y", 0)}}, {ID: "y", Name: "y", Members: []runtime.ComboMember{member("", "x", 0)}}},
		"an unknown name": {{ID: "x", Name: "x", Members: []runtime.ComboMember{member("", "nobody", 0)}}},
	} {
		if _, err := a.runtime.SetCombos(ctx, combos); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	deep := make([]runtime.Combo, 0, runtime.MaxComboDepth+2)
	for i := 0; i <= runtime.MaxComboDepth; i++ {
		combo := runtime.Combo{ID: fmt.Sprint("c", i), Name: fmt.Sprint("c", i)}
		if i < runtime.MaxComboDepth {
			combo.Members = []runtime.ComboMember{member("", fmt.Sprint("c", i+1), 0)}
		} else {
			combo.Members = []runtime.ComboMember{member("pa", "a", 0)}
		}
		deep = append(deep, combo)
	}
	if _, err := a.runtime.SetCombos(ctx, deep); err == nil {
		t.Fatal("a chain deeper than the limit must be refused")
	}
	// The limit must not depend on which Combo is checked first: here the deepest
	// end of the chain sorts first by name, so every shallower Combo is settled
	// before the one at the top is reached.
	reversed := make([]runtime.Combo, 0, runtime.MaxComboDepth+1)
	for i := 0; i <= runtime.MaxComboDepth; i++ {
		combo := runtime.Combo{ID: fmt.Sprint("a", i), Name: fmt.Sprint("a", i)}
		if i == 0 {
			combo.Members = []runtime.ComboMember{member("pa", "a", 0)}
		} else {
			combo.Members = []runtime.ComboMember{member("", fmt.Sprint("a", i-1), 0)}
		}
		reversed = append(reversed, combo)
	}
	if _, err := a.runtime.SetCombos(ctx, reversed); err == nil {
		t.Fatal("a chain deeper than the limit must be refused whichever end sorts first")
	}
	// A chain exactly at the limit is accepted.
	if _, err := a.runtime.SetCombos(ctx, reversed[:runtime.MaxComboDepth]); err != nil {
		t.Fatalf("a chain at the limit must be accepted: %v", err)
	}
}
