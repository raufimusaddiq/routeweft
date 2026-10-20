package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func rawPin(t *testing.T, document string) map[string]json.RawMessage {
	t.Helper()
	var pin map[string]json.RawMessage
	if err := json.Unmarshal([]byte(document), &pin); err != nil {
		t.Fatal(err)
	}
	return pin
}

// applyImported commits imported settings through the real validation path.
func applyImported(t *testing.T, manager *Manager, settings map[string]string) {
	t.Helper()
	if _, err := manager.SetSettings(context.Background(), settings, nil); err != nil {
		t.Fatalf("imported settings rejected by SetSettings: %v (%v)", err, settings)
	}
}

// C04.1: nested capacityAdapter {enabled, roundRobin, models} vs four settings.
func TestC04CapacityAdapterRoundRobinStoredAndResolved(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	settings, notes, err := ImportPinSettings(rawPin(t, `{"capacityAdapter":{
		"vision":{"enabled":true,"roundRobin":true,"models":["openai/gpt-a","openai/gpt-b","openai/gpt-a"]},
		"pdf":{"models":["openai/gpt-c"]},
		"audioInput":[{"model":"openai/legacy","enabled":true},"openai/legacy2"],
		"videoInput":{"enabled":false,"roundRobin":true,"models":["noslash"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	applyImported(t, manager, settings)
	snapshot, _ := manager.Load()

	vision := snapshot.CapacityAdapter("vision")
	if !vision.Enabled || !vision.RoundRobin || vision.Strategy() != routing.StrategyRoundRobin || len(vision.Pool) != 2 {
		t.Fatalf("vision=%+v", vision)
	}
	// Pin: a missing `enabled` means enabled (capacityAdapter.js:24), roundRobin false.
	if pdf := snapshot.CapacityAdapter("pdf"); !pdf.Enabled || pdf.RoundRobin || pdf.Strategy() != routing.StrategyFillFirst {
		t.Fatalf("pdf=%+v", pdf)
	}
	// Pin: legacy array form is enabled + fallback (capacityAdapter.js:19-21).
	if audio := snapshot.CapacityAdapter("audio-input"); !audio.Enabled || audio.RoundRobin || len(audio.Pool) != 2 || audio.Pool[0].ModelID != "legacy" {
		t.Fatalf("audio=%+v", audio)
	}
	// Disagreement: roundRobin on a disabled pool is not a round-robin strategy
	// (capacityAdapter.js:59), and a prefix-less model is reported, not stored.
	video := snapshot.CapacityAdapter("video-input")
	if video.Enabled || video.Strategy() != routing.StrategyFillFirst || len(video.Pool) != 0 {
		t.Fatalf("video=%+v", video)
	}
	if !strings.Contains(strings.Join(notes, "\n"), `"noslash" has no provider prefix`) {
		t.Fatalf("notes=%v", notes)
	}
	if got := snapshot.CapacityAdapter("smell"); got.Enabled {
		t.Fatalf("unknown capability must be a disabled no-op: %+v", got)
	}
}

// Stored shape of a pre-C04 Routeweft value (no roundRobin) still resolves.
func TestC04CapacityAdapterLegacyRoutweftValueDefaultsToFallback(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	applyImported(t, manager, map[string]string{"capacityAdapterVision": `{"enabled":true,"pool":[{"providerId":"openai","modelId":"x"}]}`})
	snapshot, _ := manager.Load()
	if got := snapshot.CapacityAdapter("vision"); !got.Enabled || got.RoundRobin || got.Strategy() != routing.StrategyFillFirst {
		t.Fatalf("got=%+v", got)
	}
	if _, err := manager.SetSettings(context.Background(), map[string]string{"capacityAdapterVision": `{"enabled":true,"pool":[{"providerId":"","modelId":"x"}]}`}, nil); err == nil {
		t.Fatal("pool member without providerId accepted")
	}
}

// C04.2: outboundNoProxy comma string vs noProxy JSON array.
func TestC04NoProxyImportAndCompile(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	settings, _, err := ImportPinSettings(rawPin(t, `{"outboundNoProxy":" Localhost, .Internal.Example ,,*.skip ,"}`))
	if err != nil {
		t.Fatal(err)
	}
	if settings["noProxy"] != `["localhost",".internal.example","*.skip"]` {
		t.Fatalf("noProxy=%q", settings["noProxy"])
	}
	applyImported(t, manager, settings)
	snapshot, _ := manager.Load()
	_, _, got := snapshot.OutboundProxy()
	if want := []string{"localhost", ".internal.example", "*.skip"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("compiled=%v want %v", got, want)
	}
	// Routeweft's own value is canonicalized the same way at compile time.
	applyImported(t, manager, map[string]string{"noProxy": `[" EXAMPLE.com ",""]`})
	snapshot, _ = manager.Load()
	if _, _, got = snapshot.OutboundProxy(); !reflect.DeepEqual(got, []string{"example.com"}) {
		t.Fatalf("compiled=%v", got)
	}
	if _, err := manager.SetSettings(context.Background(), map[string]string{"noProxy": `"a.com,b.com"`}, nil); err == nil {
		t.Fatal("a comma string is the pin shape, not the stored shape")
	}
}

// C04.3: observabilityMaxJsonSize KiB (pin) vs bytes (Routeweft).
func TestC04MaxJSONSizeUnitConversion(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	if got := manager.Settings()["observabilityMaxJsonSize"]; got != "5242880" {
		t.Fatalf("default must stay 5 MiB (PRD 15), got %q", got)
	}
	// Disagreement: the pin default 5 means 5 KiB; copying "5" verbatim would
	// make Routeweft cap request details at 5 bytes.
	settings, notes, err := ImportPinSettings(rawPin(t, `{"observabilityMaxJsonSize":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if settings["observabilityMaxJsonSize"] != "5120" || len(notes) != 1 {
		t.Fatalf("settings=%v notes=%v", settings, notes)
	}
	settings, _, _ = ImportPinSettings(rawPin(t, `{"observabilityMaxJsonSize":"2048"}`))
	if settings["observabilityMaxJsonSize"] != "2097152" {
		t.Fatalf("string KiB: %v", settings)
	}
	// Unset or 0 uses the pin default through `||`; Routeweft keeps its own.
	settings, _, _ = ImportPinSettings(rawPin(t, `{"observabilityMaxJsonSize":0}`))
	if _, set := settings["observabilityMaxJsonSize"]; set {
		t.Fatalf("0 must not override the default: %v", settings)
	}
	// An existing stored Routeweft value is bytes and is never reinterpreted.
	applyImported(t, manager, map[string]string{"observabilityMaxJsonSize": "1048576"})
	if got := manager.Settings()["observabilityMaxJsonSize"]; got != "1048576" {
		t.Fatalf("stored bytes changed: %q", got)
	}
}

// C04.4: dnsToolEnabled boolean vs provider map.
func TestC04DNSToolEnabledMap(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	if got := manager.Settings()["dnsToolEnabled"]; got != "{}" {
		t.Fatalf("default=%q want {}", got)
	}
	settings, _, err := ImportPinSettings(rawPin(t, `{"dnsToolEnabled":{"antigravity":true,"kiro":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	applyImported(t, manager, settings)
	if got := manager.Settings()["dnsToolEnabled"]; got != `{"antigravity":true,"kiro":false}` {
		t.Fatalf("stored=%q", got)
	}
	for _, bad := range []string{"true", `{"x":"yes"}`, `[]`} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{"dnsToolEnabled": bad}, nil); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// A database written by the boolean build still compiles; the value cannot
	// name providers so it resolves to the empty map.
	config := Config{Settings: map[string]string{"dnsToolEnabled": "true"}}
	snapshot, err := (Compiler{}).Compile(config, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Settings()["dnsToolEnabled"]; got != "{}" {
		t.Fatalf("legacy boolean compiled to %q", got)
	}
}

// C04.5: providerStrategies / comboStrategies objects and sticky settings.
func TestC04StrategyOverrideObjects(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	applyImported(t, manager, map[string]string{
		"stickyRoundRobinLimit": "4",
		"providerStrategies":    `{"openai":{"fallbackStrategy":"sticky-round-robin","stickyRoundRobinLimit":7},"claude":"round-robin","groq":{"stickyRoundRobinLimit":0}}`,
	})
	snapshot, _ := manager.Load()
	if s, ok := snapshot.ProviderStrategyOverride("openai"); !ok || s != routing.StrategyStickyRR {
		t.Fatalf("openai=%v %v", s, ok)
	}
	if s, ok := snapshot.ProviderStrategyOverride("claude"); !ok || s != routing.StrategyRoundRobin {
		t.Fatalf("string shorthand lost: %v %v", s, ok)
	}
	if _, ok := snapshot.ProviderStrategyOverride("groq"); ok {
		t.Fatal("a sticky-only entry must not override the strategy")
	}
	// auth.js:186: override limit wins; 0/absent inherits the global limit.
	for provider, want := range map[string]uint64{"openai": 7, "claude": 4, "groq": 4, "unlisted": 4} {
		if got := snapshot.ProviderStickyLimitFor(provider); got != want {
			t.Errorf("sticky(%s)=%d want %d", provider, got, want)
		}
	}
	applyImported(t, manager, map[string]string{"comboStrategies": `{"auto":{"fallbackStrategy":"round-robin"},"b":"sticky-round-robin"}`})
	snapshot, _ = manager.Load()
	comboModels(t, manager)
	if _, err := manager.PutCombo(context.Background(), Combo{Name: "auto", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text"}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = manager.Load()
	if got := snapshot.ComboStrategy("auto"); got != "round-robin" {
		t.Fatalf("combo object override=%q", got)
	}
	// Unsupported pin fields are refused by name, never stored and ignored.
	for _, bad := range []string{
		`{"openai":{"proxyPoolId":"p1"}}`,
		`{"openai":{"rotateStrategy":"round-robin"}}`,
		`{"openai":{"fallbackStrategy":"fusion"}}`,
		`{"openai":{"stickyRoundRobinLimit":-1}}`,
		`{"openai":3}`,
	} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{"providerStrategies": bad}, nil); err == nil {
			t.Fatalf("providerStrategies accepted %s", bad)
		}
	}
	for _, bad := range []string{`{"c":{"judgeModel":"x"}}`, `{"c":{"fusionTuning":{}}}`, `{"c":{"stickyRoundRobinLimit":2}}`, `{"c":{"fallbackStrategy":"fusion"}}`} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{"comboStrategies": bad}, nil); err == nil {
			t.Fatalf("comboStrategies accepted %s", bad)
		}
	}
}

// The global comboStickyRoundRobinLimit applies to every combo that does not set
// its own (chat.js:136); a compiled default of 1 used to shadow it.
func TestC04ComboStickyInheritsGlobal(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	comboModels(t, manager)
	ctx := context.Background()
	if _, err := manager.PutCombo(ctx, Combo{Name: "inherit", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PutCombo(ctx, Combo{Name: "own", StickyLimit: 5, Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text"}}}); err != nil {
		t.Fatal(err)
	}
	applyImported(t, manager, map[string]string{"comboStickyRoundRobinLimit": "6"})
	snapshot, _ := manager.Load()
	if got := snapshot.ComboStickyLimit("inherit"); got != 6 {
		t.Fatalf("inherit=%d want 6", got)
	}
	if got := snapshot.ComboStickyLimit("own"); got != 5 {
		t.Fatalf("own=%d want 5", got)
	}
}

// The pin's "round-robin" always rotates after `limit` uses, so it maps to
// sticky-round-robin unless the limit is 1.
func TestC04PinRoundRobinMapsToSticky(t *testing.T) {
	settings, notes, err := ImportPinSettings(rawPin(t, `{
		"stickyRoundRobinLimit":3,"comboStickyRoundRobinLimit":1,
		"fallbackStrategy":"round-robin","comboStrategy":"round-robin",
		"providerStrategies":{
			"a":{"fallbackStrategy":"round-robin"},
			"b":{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":1},
			"c":{"fallbackStrategy":"fill-first","proxyPoolId":"p","rotateStrategy":"round-robin"}},
		"comboStrategies":{
			"x":{"fallbackStrategy":"round-robin"},
			"y":{"fallbackStrategy":"fusion","judgeModel":"m","fusionTuning":{"minPanel":2}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if settings["providerStrategy"] != "sticky-round-robin" || settings["comboStrategy"] != "round-robin" {
		t.Fatalf("globals=%v", settings)
	}
	var providers map[string]map[string]any
	if err := json.Unmarshal([]byte(settings["providerStrategies"]), &providers); err != nil {
		t.Fatal(err)
	}
	if providers["a"]["fallbackStrategy"] != "sticky-round-robin" || providers["b"]["fallbackStrategy"] != "round-robin" || providers["c"]["fallbackStrategy"] != "fill-first" {
		t.Fatalf("providers=%v", providers)
	}
	if _, kept := providers["c"]["proxyPoolId"]; kept {
		t.Fatal("unsupported field stored")
	}
	if settings["comboStrategies"] != `{"x":{"fallbackStrategy":"round-robin"}}` {
		t.Fatalf("combos=%s", settings["comboStrategies"])
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{`providerStrategies.c: field "proxyPoolId"`, `field "rotateStrategy"`, `comboStrategies.y: fallbackStrategy "fusion"`, `field "judgeModel"`, `field "fusionTuning"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q:\n%s", want, joined)
		}
	}
	manager, store := newTestManager(t)
	defer store.Close()
	applyImported(t, manager, settings)
}

// 0 falls through to the default in the pin.
func TestC04PinZeroStickyLimitFallsThrough(t *testing.T) {
	settings, _, err := ImportPinSettings(rawPin(t, `{"stickyRoundRobinLimit":0,"comboStickyRoundRobinLimit":0,"fallbackStrategy":"round-robin","comboStrategy":"round-robin"}`))
	if err != nil {
		t.Fatal(err)
	}
	if settings["providerStrategy"] != "sticky-round-robin" || settings["comboStrategy"] != "round-robin" {
		t.Fatalf("%v", settings)
	}
}

// C04.6: flags without a Routeweft consumer are reported, never stored.
func TestC04UnconsumedFlagsAreReportedNotStored(t *testing.T) {
	settings, notes, err := ImportPinSettings(rawPin(t, `{"ccFilterNaming":true,"headroomCodeAware":true,"headroomKompress":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != 0 || len(notes) != 3 {
		t.Fatalf("settings=%v notes=%v", settings, notes)
	}
	for _, key := range []string{"ccFilterNaming", "headroomCodeAware", "headroomKompress"} {
		if IsWritableSetting(key) {
			t.Fatalf("%s must stay unwritable until something consumes it", key)
		}
	}
}
