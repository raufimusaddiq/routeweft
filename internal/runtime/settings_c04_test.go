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
	// An enabled but empty pool is never round-robin either.
	if empty := (CapacityAdapterConfig{Enabled: true, RoundRobin: true}); empty.Strategy() != routing.StrategyFillFirst {
		t.Fatalf("empty pool strategy=%v", empty.Strategy())
	}
	// Disagreement: roundRobin on a disabled pool is not a round-robin strategy
	// (capacityAdapter.js:59), and a prefix-less model is reported, not stored.
	video := snapshot.CapacityAdapter("video-input")
	if video.Enabled || video.Strategy() != routing.StrategyFillFirst || len(video.Pool) != 0 {
		t.Fatalf("video=%+v", video)
	}
	if !strings.Contains(strings.Join(notes, "\n"), `a model has no provider prefix`) {
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
	settings, noProxyNotes, err := ImportPinSettings(rawPin(t, `{"outboundNoProxy":" Localhost, .Internal.Example ,,*.skip ,"}`))
	if err != nil {
		t.Fatal(err)
	}
	if settings["noProxy"] != `["localhost",".internal.example"]` {
		t.Fatalf("noProxy=%q", settings["noProxy"])
	}
	if !strings.Contains(strings.Join(noProxyNotes, "\n"), `a wildcard-prefixed entry never matches`) {
		t.Fatalf("notes=%v", noProxyNotes)
	}
	applyImported(t, manager, settings)
	snapshot, _ := manager.Load()
	_, _, got := snapshot.OutboundProxy()
	if want := []string{"localhost", ".internal.example"}; !reflect.DeepEqual(got, want) {
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
	// A malformed map is not the legacy boolean and must not be discarded.
	malformed := `{"openai":"true"}`
	snapshot, err = (Compiler{}).Compile(Config{Settings: map[string]string{"dnsToolEnabled": malformed}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Settings()["dnsToolEnabled"]; got != malformed {
		t.Fatalf("malformed map rewritten to %q", got)
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
	// The pin's no-auth proxy choice is accepted (PARITY 6.38) and parsed.
	parsed, err := parseStrategyOverrides(`{"opencode":{"proxyPoolId":" p1 ","rotateStrategy":"round-robin"}}`, false)
	if err != nil || parsed["opencode"].ProxyPoolID != "p1" || parsed["opencode"].RotateStrategy != "round-robin" || parsed["opencode"].Strategy != "" {
		t.Fatalf("proxy choice: %+v %v", parsed, err)
	}
	if _, err := parseStrategyOverrides(`{"c":{"proxyPoolId":"p1"}}`, true); err == nil {
		t.Fatal("a combo has no proxy of its own")
	}
	// Unsupported pin fields are refused by name, never stored and ignored.
	for _, bad := range []string{
		`{"openai":{"proxyPoolId":3}}`,
		`{"openai":{"rotateStrategy":["random"]}}`,
		`{"openai":{"fallbackStrategy":"fusion"}}`,
		`{"openai":{"stickyRoundRobinLimit":-1}}`,
		`{"openai":3}`,
	} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{"providerStrategies": bad}, nil); err == nil {
			t.Fatalf("providerStrategies accepted %s", bad)
		}
	}
	for _, bad := range []string{
		`{"c":{"stickyRoundRobinLimit":2}}`,
		`{"c":{"judgeModel":3}}`,
		`{"c":{"fusionTuning":3}}`,
		`{"c":{"fusionTuning":{"timeout":5}}}`,
		`{"c":{"fusionTuning":{"minPanel":"2"}}}`,
		`{"c":{"fusionTuning":{"minPanel":2.5}}}`,
		`{"c":{"fusionTuning":{"minPanel":-1}}}`,
		`{"c":{"fusionTuning":{"panelHardTimeoutMs":0}}}`,
		`{"c":{"fusionTuning":{"stragglerGraceMs":600001}}}`,
		`{"c":{"fallbackStrategy":"fusion-ish"}}`,
	} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{"comboStrategies": bad}, nil); err == nil {
			t.Fatalf("comboStrategies accepted %s", bad)
		}
	}
	// The pin's fusion fields (chat.js:116-133) are accepted, alone or together.
	for _, good := range []string{
		`{"c":{"fallbackStrategy":"fusion"}}`,
		`{"c":{"fallbackStrategy":"fusion","judgeModel":"openai/gpt-text"}}`,
		`{"c":{"fallbackStrategy":"fusion","judgeModel":null,"fusionTuning":{"minPanel":3,"stragglerGraceMs":0,"panelHardTimeoutMs":5000}}}`,
		`{"c":{"fusionTuning":{"minPanel":null}}}`,
	} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{"comboStrategies": good}, nil); err != nil {
			t.Fatalf("comboStrategies refused %s: %v", good, err)
		}
	}
}

// comboStrategies[name] decides whether a Combo is served as Fusion and carries its
// judge and timing (chat.js:116-133); a plain strategy turns a stored fusion flag
// off, and an entry for a name that is not a Combo does nothing.
func TestComboStrategiesFusionOverlay(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	comboModels(t, manager)
	ctx := context.Background()
	members := []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Selected: true}, {ProviderID: "anthropic", ModelID: "claude-text", Position: 1, Selected: true}}
	two, thirty := 2, int64(30000)
	if _, err := manager.SetCombos(ctx, []Combo{
		{ID: "a", Name: "plain", Members: members},
		{ID: "b", Name: "stored", FusionEnabled: true, JudgeModel: "own/judge", FusionTuning: routing.FusionTuning{MinPanel: &two}, Members: members},
		{ID: "c", Name: "empty", Members: nil},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := manager.Load()
	if snapshot.HasFusion() != true {
		t.Fatal("a stored fusion Combo must be found")
	}
	if _, ok := snapshot.FusionCombo("plain"); ok {
		t.Fatal("a plain Combo is not Fusion")
	}
	if _, ok := snapshot.FusionCombo("stored"); !ok {
		t.Fatal("a stored fusion Combo is Fusion")
	}
	if _, err := manager.SetSettings(ctx, map[string]string{"comboStrategies": `{"plain":{"fallbackStrategy":"fusion","judgeModel":"openai/gpt-text","fusionTuning":{"minPanel":0,"stragglerGraceMs":0,"panelHardTimeoutMs":30000}},"stored":"round-robin","nobody":{"fallbackStrategy":"fusion"}}`}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = manager.Load()
	plain, ok := snapshot.FusionCombo("plain")
	if !ok || plain.JudgeModel != "openai/gpt-text" || plain.FusionTuning.MinPanel == nil || *plain.FusionTuning.MinPanel != 2 || *plain.FusionTuning.PanelHardTimeoutMs != thirty || *plain.FusionTuning.StragglerGraceMs != 0 {
		t.Fatalf("settings did not make plain a Fusion Combo: %+v ok=%v", plain, ok)
	}
	if _, ok := snapshot.FusionCombo("stored"); ok {
		t.Fatal("a plain strategy must turn the stored fusion flag off")
	}
	if _, ok := snapshot.FusionCombo("nobody"); ok {
		t.Fatal("an entry for a name that is not a Combo does nothing")
	}
	if _, ok := snapshot.FusionCombo("empty"); ok {
		t.Fatal("a Combo with no members is not Fusion")
	}
	if _, ok := snapshot.FusionCombo("openai/plain"); ok {
		t.Fatal("a name with a slash is never a Combo")
	}
	// Clearing the entry returns the Combo to its stored configuration.
	if _, err := manager.SetSettings(ctx, map[string]string{"comboStrategies": `{}`}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = manager.Load()
	if _, ok := snapshot.FusionCombo("plain"); ok {
		t.Fatal("plain must stop being Fusion when its entry is cleared")
	}
	if _, ok := snapshot.FusionCombo("stored"); !ok {
		t.Fatal("stored must be Fusion again when its entry is cleared")
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
			"y":{"fallbackStrategy":"fusion","judgeModel":"m","fusionTuning":{"minPanel":2}},
			"z":{"fallbackStrategy":"fallback","stickyRoundRobinLimit":5}}}`))
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
	if settings["comboStrategies"] != `{"x":{"fallbackStrategy":"round-robin"},"z":{"fallbackStrategy":"fallback"}}` {
		t.Fatalf("combos=%s", settings["comboStrategies"])
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{`providerStrategies.c: field "proxyPoolId"`, `field "rotateStrategy"`, `comboStrategies.y: fallbackStrategy has no Routeweft equivalent`, `field "judgeModel"`, `field "fusionTuning"`, `comboStrategies.z: field "stickyRoundRobinLimit"`} {
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

// A non-string fallbackStrategy is reported, never silently dropped.
func TestC04ImportReportsNonStringFallbackStrategy(t *testing.T) {
	for _, document := range []string{
		`{"providerStrategies":{"openai":{"fallbackStrategy":5}}}`,
		`{"providerStrategies":{"openai":{"fallbackStrategy":null}}}`,
		`{"comboStrategies":{"auto":{"fallbackStrategy":["round-robin"]}}}`,
	} {
		_, notes, err := ImportPinSettings(rawPin(t, document))
		if err != nil {
			t.Fatal(err)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "is not a string, not carried") {
			t.Errorf("%s: notes=%v", document, notes)
		}
	}
}

// One malformed stored entry is skipped alone; the other overrides survive.
func TestC04TolerantReaderSkipsOnlyTheBadEntry(t *testing.T) {
	raw := `{"openai":{"fallbackStrategy":"round-robin"},"bad":{"unknownField":"x"},"claude":"sticky-round-robin","worse":7}`
	got := strategyOverridesTolerant(raw, false)
	if len(got) != 2 || got["openai"].Strategy != "round-robin" || got["claude"].Strategy != "sticky-round-robin" {
		t.Fatalf("got=%v", got)
	}
	if _, err := parseStrategyOverrides(raw, false); err == nil {
		t.Fatal("the write-side validator must still reject the document")
	}
	if got := strategyOverridesTolerant(`[]`, false); len(got) != 0 {
		t.Fatalf("non-object: %v", got)
	}
}

// TestC04ImportNeverDropsWrongTypedFieldSilently feeds a wrong-typed value for
// every field the importer accepts. Each must fail the import or leave a note;
// none may be swallowed, and none may store a degraded value in its place.
func TestC04ImportNeverDropsWrongTypedFieldSilently(t *testing.T) {
	cases := map[string]string{
		"sticky string":                               `{"stickyRoundRobinLimit":"many"}`,
		"sticky null":                                 `{"stickyRoundRobinLimit":null}`,
		"sticky fraction":                             `{"stickyRoundRobinLimit":2.5}`,
		"combo sticky negative":                       `{"comboStickyRoundRobinLimit":-1}`,
		"combo sticky object":                         `{"comboStickyRoundRobinLimit":{}}`,
		"maxJson string":                              `{"observabilityMaxJsonSize":"big"}`,
		"maxJson negative":                            `{"observabilityMaxJsonSize":-5}`,
		"maxJson null":                                `{"observabilityMaxJsonSize":null}`,
		"dns array":                                   `{"dnsToolEnabled":[]}`,
		"dns string value":                            `{"dnsToolEnabled":{"a":"yes"}}`,
		"fallbackStrategy number":                     `{"fallbackStrategy":5}`,
		"fallbackStrategy null":                       `{"fallbackStrategy":null}`,
		"comboStrategy object":                        `{"comboStrategy":{}}`,
		"noProxy number":                              `{"outboundNoProxy":5}`,
		"capacityAdapter array":                       `{"capacityAdapter":[]}`,
		"adapter string entry":                        `{"capacityAdapter":{"vision":"openai/gpt-a"}}`,
		"adapter number entry":                        `{"capacityAdapter":{"pdf":3}}`,
		"adapter null entry":                          `{"capacityAdapter":{"pdf":null}}`,
		"adapter enabled string":                      `{"capacityAdapter":{"vision":{"enabled":"yes","models":["a/b"]}}}`,
		"adapter roundRobin string":                   `{"capacityAdapter":{"vision":{"roundRobin":"yes","models":["a/b"]}}}`,
		"adapter models string":                       `{"capacityAdapter":{"pdf":{"enabled":true,"models":"openai/x"}}}`,
		"adapter model number":                        `{"capacityAdapter":{"pdf":{"models":[5]}}}`,
		"adapter legacy element":                      `{"capacityAdapter":{"audioInput":[7]}}`,
		"adapter legacy element object without model": `{"capacityAdapter":{"audioInput":[{"enabled":true}]}}`,
		"providerStrategies array":                    `{"providerStrategies":[]}`,
		"providerStrategies null":                     `{"providerStrategies":{"a":null}}`,
		"providerStrategies number":                   `{"providerStrategies":{"a":3}}`,
		"providerStrategies strategy":                 `{"providerStrategies":{"a":{"fallbackStrategy":["x"]}}}`,
		"providerStrategies sticky":                   `{"providerStrategies":{"a":{"stickyRoundRobinLimit":"x"}}}`,
		"comboStrategies string":                      `{"comboStrategies":"x"}`,
		"comboStrategies null":                        `{"comboStrategies":{"a":null}}`,
		"comboStrategies strategy null":               `{"comboStrategies":{"a":{"fallbackStrategy":null}}}`,
	}
	for name, document := range cases {
		settings, notes, err := ImportPinSettings(rawPin(t, document))
		if err != nil {
			continue // failing the import is an acceptable outcome
		}
		if len(notes) == 0 {
			t.Errorf("%s: silently accepted %s -> settings=%v", name, document, settings)
		}
		if strings.Contains(name, "model") || strings.Contains(name, "element") {
			continue // a bad element is noted and skipped; the rest of the entry still imports
		}
		for key, value := range settings {
			switch key {
			case "capacityAdapterVision", "capacityAdapterPDF", "capacityAdapterAudioInput", "capacityAdapterVideoInput":
				t.Errorf("%s: stored a degraded adapter %s=%s", name, key, value)
			}
		}
	}
	// A readable legacy entry still imports and a null models list is the pin's
	// empty pool (Array.isArray is false), not an error.
	settings, notes, err := ImportPinSettings(rawPin(t, `{"capacityAdapter":{"vision":{"models":null}}}`))
	if err != nil || len(notes) != 0 || settings["capacityAdapterVision"] != `{"enabled":true,"pool":[],"roundRobin":false}` {
		t.Fatalf("settings=%v notes=%v err=%v", settings, notes, err)
	}
}
