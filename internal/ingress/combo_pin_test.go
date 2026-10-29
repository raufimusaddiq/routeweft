package ingress

import (
	"context"
	"encoding/json"
	"os"
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
