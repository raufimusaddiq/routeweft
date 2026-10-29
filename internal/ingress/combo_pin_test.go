package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// combo_routing_pin.json is written by compat/parity/generate-combo-routing.mjs,
// which executes the oracle's own combo.js and capacityAdapter.js (#270). The
// tests replay each recorded case through Routeweft's planner; none regenerates
// the file.
type comboPin struct {
	SHA          string              `json:"sha"`
	Capabilities map[string][]string `json:"capabilities"`
	Detect       []struct {
		Name     string          `json:"name"`
		Body     json.RawMessage `json:"body"`
		Required []string        `json:"required"`
	} `json:"detect"`
	Order []struct {
		Name     string   `json:"name"`
		Solo     bool     `json:"solo"`
		Models   []string `json:"models"`
		Strategy string   `json:"strategy"`
		Sticky   uint64   `json:"sticky"`
		Required []string `json:"required"`
		Adapters map[string]struct {
			Enabled    bool     `json:"enabled"`
			RoundRobin bool     `json:"roundRobin"`
			Models     []string `json:"models"`
		} `json:"adapters"`
		Calls []struct {
			Rescued bool     `json:"rescued"`
			Order   []string `json:"order"`
		} `json:"calls"`
	} `json:"order"`
}

func loadComboPin(t *testing.T) comboPin {
	t.Helper()
	raw, err := os.ReadFile("testdata/combo_routing_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin comboPin
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.SHA) != 40 || len(pin.Detect) < 30 || len(pin.Order) < 20 {
		t.Fatalf("pin is incomplete: sha=%q detect=%d order=%d", pin.SHA, len(pin.Detect), len(pin.Order))
	}
	return pin
}

func TestDetectRequiredCapabilitiesMatchesTheOracle(t *testing.T) {
	pin := loadComboPin(t)
	for _, c := range pin.Detect {
		got := detectRequiredCapabilities(c.Body)
		if strings.Join(got, ",") != strings.Join(c.Required, ",") {
			t.Errorf("%s: got %v, oracle %v (body %s)", c.Name, got, c.Required, c.Body)
		}
	}
}

func TestComboMemberOrderMatchesTheOracle(t *testing.T) {
	pin := loadComboPin(t)
	adapterSetting := map[string]string{"vision": "capacityAdapterVision", "pdf": "capacityAdapterPDF", "audio-input": "capacityAdapterAudioInput", "video-input": "capacityAdapterVideoInput"}
	kebab := map[string]string{"audioInput": "audio-input", "videoInput": "video-input"}
	for _, scenario := range pin.Order {
		t.Run(scenario.Name, func(t *testing.T) {
			ctx := context.Background()
			manager := newManager(t)
			for ref, capabilities := range pin.Capabilities {
				provider, id, _ := strings.Cut(ref, "/")
				if err := manager.PutModel(ctx, runtime.Model{ProviderID: provider, ID: id, Capabilities: capabilities}); err != nil {
					t.Fatal(err)
				}
			}
			settings := map[string]string{}
			for name, pool := range scenario.Adapters {
				if mapped, ok := kebab[name]; ok {
					name = mapped
				}
				settings[adapterSetting[name]] = adapterPool(pool.Enabled, pool.RoundRobin, pool.Models...)
			}
			if scenario.Sticky > 0 {
				settings["comboStickyRoundRobinLimit"] = string(rune('0' + scenario.Sticky))
			}
			if len(settings) > 0 {
				if _, err := manager.SetSettings(ctx, settings, nil); err != nil {
					t.Fatal(err)
				}
			}
			if !scenario.Solo {
				members := make([]runtime.ComboMember, 0, len(scenario.Models))
				for index, ref := range scenario.Models {
					provider, id, _ := strings.Cut(ref, "/")
					members = append(members, runtime.ComboMember{ProviderID: provider, ModelID: id, Position: index, Selected: true})
				}
				strategy := scenario.Strategy
				if strategy == "fallback" {
					strategy = ""
				}
				if _, err := manager.SetCombos(ctx, []runtime.Combo{{ID: "c", Name: "c", Strategy: strategy, Members: members}}); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := mustLoad(t, manager)
			handler := New(manager, Options{State: runtime.NewState()})
			for index, call := range scenario.Calls {
				var planned []runtime.ComboMember
				var rescued bool
				if scenario.Solo {
					provider, id, _ := strings.Cut(scenario.Models[0], "/")
					planned, rescued = handler.PlanSolo(snapshot, auth.Entry{}, provider, id, scenario.Required)
				} else {
					planned, rescued = handler.PlanCombo(snapshot, auth.Entry{}, "c", scenario.Required)
				}
				if scenario.Solo && rescued != call.Rescued {
					t.Fatalf("call %d: rescued=%v, oracle %v", index, rescued, call.Rescued)
				}
				if scenario.Solo && !rescued {
					continue
				}
				got := make([]string, 0, len(planned))
				for _, member := range planned {
					got = append(got, member.ProviderID+"/"+member.ModelID)
				}
				if strings.Join(got, " ") != strings.Join(call.Order, " ") {
					t.Fatalf("call %d: got %v, oracle %v", index, got, call.Order)
				}
			}
		})
	}
}

type comboPinPart2 struct {
	Strip []struct {
		Name   string          `json:"name"`
		Body   json.RawMessage `json:"body"`
		Window *int            `json:"window"`
		Result json.RawMessage `json:"result"`
	} `json:"strip"`
	Outcomes []struct {
		Name     string `json:"name"`
		Terminal bool   `json:"terminal"`
		Success  bool   `json:"success"`
		Members  []struct {
			Status     int    `json:"status"`
			Body       string `json:"body"`
			StatusText string `json:"statusText"`
		} `json:"members"`
		Tried   int    `json:"tried"`
		Status  int    `json:"status"`
		Message string `json:"message"`
		Waits   []int  `json:"waits"`
	} `json:"outcomes"`
	NestedTable    map[string][]string `json:"nestedTable"`
	NestedStrategy map[string]string   `json:"nestedStrategy"`
	Nested         []struct {
		Name  string `json:"name"`
		Root  string `json:"root"`
		Calls []struct {
			Results map[string]int `json:"results"`
			Tried   []string       `json:"tried"`
			Status  int            `json:"status"`
		} `json:"calls"`
	} `json:"nested"`
}

func loadComboPinPart2(t *testing.T) comboPinPart2 {
	t.Helper()
	raw, err := os.ReadFile("testdata/combo_routing_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin comboPinPart2
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.Strip) < 15 || len(pin.Outcomes) < 12 || len(pin.Nested) < 6 {
		t.Fatalf("pin is incomplete: strip=%d outcomes=%d nested=%d", len(pin.Strip), len(pin.Outcomes), len(pin.Nested))
	}
	return pin
}

func TestStripHistoryForContextMatchesTheOracle(t *testing.T) {
	pin := loadComboPinPart2(t)
	for _, c := range pin.Strip {
		window := 0
		if c.Window != nil {
			window = *c.Window
		}
		got := stripHistoryForContext(c.Body, window)
		var gotValue, wantValue any
		if err := json.Unmarshal(got, &gotValue); err != nil {
			t.Fatalf("%s: result is not JSON: %v", c.Name, err)
		}
		if err := json.Unmarshal(c.Result, &wantValue); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Errorf("%s:\n got  %s\n want %s", c.Name, got, c.Result)
		}
	}
}

// What the oracle answers when its models fail, and how long it waits first.
// The fallback decision itself is the attempt loop's (the app-level tests); this
// pins the pieces that turn failures into the answer.
func TestComboFailureAnswerAndWaitsMatchTheOracle(t *testing.T) {
	pin := loadComboPinPart2(t)
	for _, c := range pin.Outcomes {
		t.Run(c.Name, func(t *testing.T) {
			var waits []int
			firstStatus, lastText := 0, ""
			failed := c.Tried
			if c.Success || c.Terminal {
				failed-- // the model that served, or ended the walk, did not fall through
			}
			for i := 0; i < failed; i++ {
				member := c.Members[i]
				text := failureText(member.Status, member.StatusText, []byte(member.Body))
				if firstStatus == 0 {
					firstStatus = member.Status
				}
				lastText = text
				if wait := transientWait(member.Status, text); wait > 0 {
					waits = append(waits, int(wait.Milliseconds()))
				}
			}
			if strings.Join(intStrings(waits), ",") != strings.Join(intStrings(c.Waits), ",") {
				t.Fatalf("waits %v, oracle %v", waits, c.Waits)
			}
			if c.Success || c.Terminal {
				return
			}
			answer := comboExhausted(firstStatus, lastText)
			if answer.status != c.Status || answer.message != c.Message {
				t.Fatalf("answer %d %q, oracle %d %q", answer.status, answer.message, c.Status, c.Message)
			}
		})
	}
}

func intStrings(values []int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.Itoa(value))
	}
	return out
}

// swapRequest is what every protocol's body rewriter is built on: the fitted
// request replaces the parsed one for an adapter model's attempt and the original
// comes back afterwards, also when the new body does not parse.
func TestSwapRequestReplacesAndRestores(t *testing.T) {
	type request struct{ Items []string }
	current := &request{Items: []string{"a", "b", "c"}}
	restore, err := swapRequest(current, func() (*request, error) { return &request{Items: []string{"c"}}, nil })
	if err != nil || len(current.Items) != 1 {
		t.Fatalf("swap: %v %+v", err, current)
	}
	restore()
	if len(current.Items) != 3 {
		t.Fatalf("restore: %+v", current)
	}
	if restore, err = swapRequest(current, func() (*request, error) { return nil, errors.New("unparseable") }); err == nil || restore != nil || len(current.Items) != 3 {
		t.Fatalf("a body that does not parse must leave the request alone: %v %+v", err, current)
	}
}
