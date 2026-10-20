package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Synthetic v1 export: the shape of GET /api/settings/database
// (src/lib/db/index.js:78-88). Every secret below is a fake sentinel; the tests
// assert none of them survives into the plan.
const (
	v1SecretKey    = "sk-v1-SENTINEL-client-key"
	v1SecretToken  = "SENTINEL-oauth-access-token"
	v1SecretAPIKey = "SENTINEL-provider-api-key"
	v1SecretHash   = "SENTINEL-password-hash"
	v1SecretProxy  = "SENTINEL-proxy-pass"
)

const v1Fixture = `{
 "settings": {
  "password": "` + v1SecretHash + `",
  "cloudEnabled": true, "requireLogin": false, "tunnelUrl": "https://x.example", "claudeAutoPing": {"a":true},
  "observabilityMaxJsonSize": 2048, "observabilityFlushIntervalMs": 0, "observabilityMaxRecords": 500,
  "headroomEnabled": true, "headroomTimeoutMs": 4000, "pxpipeMinChars": "30000", "cavemanLevel": "ultra",
  "outboundProxyEnabled": true, "outboundProxyUrl": "http://user:` + v1SecretProxy + `@proxy.example:8080",
  "stickyRoundRobinLimit": 5, "comboStickyRoundRobinLimit": 2,
  "comboStrategies": {"fast": {"fallbackStrategy": "round-robin"},
   "panel": {"fallbackStrategy": "fusion", "judgeModel": "anthropic/claude-text", "fusionTuning": {"minPanel": 1, "stragglerGraceMs": 5000, "panelHardTimeoutMs": 60000}}}
 },
 "providerConnections": [
  {"id":"c1","provider":"claude","authType":"oauth","name":"Work","priority":1,"isActive":true,"accessToken":"` + v1SecretToken + `","refreshToken":"` + v1SecretToken + `"},
  {"id":"c2","provider":"openai","authType":"apikey","priority":2,"isActive":false,"apiKey":"` + v1SecretAPIKey + `"},
  {"id":"c3","provider":"x","authType":"cookie"}
 ],
 "providerNodes": [{"id":"n1","type":"openai-compatible","name":"Local","baseUrl":"http://h"}],
 "proxyPools": [{"id":"p1","proxyUrl":"http://u:` + v1SecretProxy + `@p:1"}],
 "apiKeys": [
  {"id":"k1","key":"` + v1SecretKey + `","name":"ci","isActive":true,"access":{"restricted":true,"allow":["fast"]}},
  {"id":"k2","key":"` + v1SecretKey + `2","name":"old","isActive":false}
 ],
 "combos": [
  {"id":"a","name":"fast","models":["openai/gpt-text","anthropic/claude-text"]},
  {"id":"b","name":"panel","kind":"fusion","models":[{"model":"openai/gpt-text"},"anthropic/claude-text"]}
 ],
 "modelAliases": {"quick": "openai/gpt-text", "bad": "no-slash"},
 "customModels": [{"providerAlias":"cc","id":"m"}],
 "pricing": {"openai": {"gpt-text": {"input": 1}}}
}`

func planFixture(t *testing.T) *V1Plan {
	t.Helper()
	plan, err := PlanV1Migration([]byte(v1Fixture))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func refusal(plan *V1Plan, path string) string {
	for _, item := range plan.Refused {
		if item.Path == path {
			return item.Reason
		}
	}
	return ""
}

func TestV1PlanMigratesSettingsWithUnitsAndCommits(t *testing.T) {
	plan := planFixture(t)
	want := map[string]string{
		"observabilityMaxJsonSize": "2097152", // 2048 KiB -> bytes
		"observabilityMaxRecords":  "500",
		"headroomEnabled":          "true",
		"headroomTimeoutMs":        "4000", // ms in both
		"pxpipeMinChars":           "30000",
		"cavemanLevel":             "ultra",
		"outboundProxyEnabled":     "true",
		"stickyRoundRobinLimit":    "5",
	}
	for key, value := range want {
		if plan.Settings[key] != value {
			t.Errorf("%s=%q, want %q", key, plan.Settings[key], value)
		}
	}
	if _, ok := plan.Settings["observabilityFlushIntervalMs"]; ok {
		t.Fatal("0 means unset in the pin and must keep the Routeweft default")
	}
	if !hasNote(plan, "observabilityFlushIntervalMs: 0 means unset") {
		t.Fatalf("notes=%v", plan.Notes)
	}
	manager, store := newTestManager(t)
	defer store.Close()
	applyImported(t, manager, plan.Settings) // the real validation path
	if got := manager.Settings()["observabilityMaxJsonSize"]; got != "2097152" {
		t.Fatalf("committed %q", got)
	}
}

func hasNote(plan *V1Plan, fragment string) bool {
	for _, note := range plan.Notes {
		if strings.Contains(note, fragment) {
			return true
		}
	}
	return false
}

func TestV1PlanRefusesUnsupportedSettingsByName(t *testing.T) {
	plan := planFixture(t)
	for path, fragment := range map[string]string{
		"settings.password":         "bootstrap password",
		"settings.cloudEnabled":     "no cloud sync",
		"settings.requireLogin":     "always requires a session",
		"settings.tunnelUrl":        "removed by v1 itself",
		"settings.claudeAutoPing":   "no Routeweft equivalent, not stored",
		"settings.outboundProxyUrl": "carries proxy credentials",
	} {
		if reason := refusal(plan, path); !strings.Contains(reason, fragment) {
			t.Errorf("%s: reason %q lacks %q", path, reason, fragment)
		}
	}
	for _, key := range []string{"password", "cloudEnabled", "requireLogin", "tunnelUrl", "claudeAutoPing", "outboundProxyUrl"} {
		if _, stored := plan.Settings[key]; stored {
			t.Errorf("%s was stored although refused", key)
		}
	}
}

func TestV1PlanRefusesWrongTypedAndInvalidValues(t *testing.T) {
	plan, err := PlanV1Migration([]byte(`{"settings":{"headroomEnabled":"yes","headroomTimeoutMs":0,"cavemanLevel":"max","outboundProxyUrl":"ftp://h","requireLogin":true,"cloudEnabled":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := refusal(plan, "settings.headroomEnabled"); got != "is not a valid bool, not carried" {
		t.Fatalf("got %q", got)
	}
	for _, path := range []string{"settings.headroomTimeoutMs", "settings.cavemanLevel", "settings.outboundProxyUrl"} {
		if !strings.Contains(refusal(plan, path), "is rejected by Routeweft") {
			t.Errorf("%s: %q", path, refusal(plan, path))
		}
	}
	if len(plan.Settings) != 0 {
		t.Fatalf("stored %v", plan.Settings)
	}
	// requireLogin:true and cloudEnabled:false equal the Routeweft behavior: nothing to report.
	for _, item := range plan.Refused {
		if strings.Contains(item.Path, "requireLogin") || strings.Contains(item.Path, "cloudEnabled") {
			t.Fatalf("spurious refusal %v", item)
		}
	}
}

func TestV1PlanNeverCopiesASecret(t *testing.T) {
	plan := planFixture(t)
	encoded, _ := json.Marshal(plan)
	text := string(encoded) + fmt.Sprintf("%+v %+v", plan.Combos, plan.Aliases)
	for _, secret := range []string{v1SecretKey, v1SecretToken, v1SecretAPIKey, v1SecretHash, v1SecretProxy} {
		if strings.Contains(text, secret) {
			t.Fatalf("plan leaks %q", secret)
		}
	}
}

func TestV1PlanKeyReissueListsNamesAndAccessOnly(t *testing.T) {
	plan := planFixture(t)
	if len(plan.KeyReissue) != 2 {
		t.Fatalf("%+v", plan.KeyReissue)
	}
	ci, old := plan.KeyReissue[0], plan.KeyReissue[1]
	if ci.Name != "ci" || !ci.Active || !ci.Restricted || len(ci.Allow) != 1 || ci.Allow[0] != "fast" {
		t.Fatalf("ci=%+v", ci)
	}
	if old.Name != "old" || old.Active || old.Restricted {
		t.Fatalf("old=%+v", old)
	}
	if !strings.Contains(refusal(plan, "apiKeys"), "Reissue 2 key(s)") {
		t.Fatalf("reason=%q", refusal(plan, "apiKeys"))
	}
}

func TestV1PlanListsEveryConnectionForReauthorization(t *testing.T) {
	plan := planFixture(t)
	if len(plan.Reauth) != 3 {
		t.Fatalf("%+v", plan.Reauth)
	}
	byID := map[string]V1Reauth{}
	for _, r := range plan.Reauth {
		byID[r.ID] = r
	}
	if byID["c1"].Action != v1ReauthOAuth || byID["c2"].Action != v1ReauthAPIKey || byID["c3"].Action != v1ReauthMissing {
		t.Fatalf("%+v", byID)
	}
	if byID["c2"].Active || byID["c1"].Priority != 1 {
		t.Fatalf("%+v", byID)
	}
	if !strings.Contains(refusal(plan, "providerConnections"), "3 connection(s)") {
		t.Fatalf("reason=%q", refusal(plan, "providerConnections"))
	}
}

func TestV1PlanRecreateByHandList(t *testing.T) {
	plan := planFixture(t)
	got := map[string]bool{}
	for _, item := range plan.Recreate {
		got[item.Path] = true
	}
	for _, path := range []string{"providerNodes[n1]", "proxyPools[p1]", "customModels", "pricing"} {
		if !got[path] {
			t.Errorf("missing %s in %v", path, plan.Recreate)
		}
	}
}

func TestV1PlanHistoryAndForeignInputsAreRefused(t *testing.T) {
	plan, err := PlanV1Migration([]byte(`{"usageHistory":[{"id":1}],"requestDetails":[],"mystery":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refusal(plan, "usageHistory"), "not imported") || !strings.Contains(refusal(plan, "requestDetails"), "not imported") {
		t.Fatalf("%+v", plan.Refused)
	}
	if refusal(plan, "mystery") != "not part of the v1 export; not read" {
		t.Fatalf("%+v", plan.Refused)
	}
	for _, input := range []string{"SQLite format 3\x00binary", "[]", "null", `{"settings":[]}`} {
		_, err := PlanV1Migration([]byte(input))
		if err == nil {
			t.Fatalf("input %q accepted", input)
		}
	}
	_, err = PlanV1Migration([]byte("SQLite format 3\x00"))
	if !strings.Contains(err.Error(), "data.sqlite file is not accepted") {
		t.Fatalf("err=%v", err)
	}
}

func TestV1PlanCombosAliasesApplyOnlyOnceMembersExist(t *testing.T) {
	ctx := context.Background()
	plan := planFixture(t)
	if len(plan.Combos) != 2 || len(plan.Aliases) != 1 || refusal(plan, "modelAliases.bad") == "" {
		t.Fatalf("combos=%d aliases=%v refused=%v", len(plan.Combos), plan.Aliases, plan.Refused)
	}
	manager, store := newTestManager(t)
	defer store.Close()

	// No providers yet: each entry fails by name, nothing is half-committed.
	failed := plan.ApplyCatalog(ctx, manager)
	if len(failed) != 3 {
		t.Fatalf("failed=%+v", failed)
	}
	if snapshot, _ := manager.Load(); len(snapshot.Combos()) != 0 {
		t.Fatalf("combos committed without members: %+v", snapshot.Combos())
	}

	comboModels(t, manager)
	if failed := plan.ApplyCatalog(ctx, manager); len(failed) != 0 {
		t.Fatalf("failed=%+v", failed)
	}
	if failed := plan.ApplyCatalog(ctx, manager); len(failed) != 0 { // re-run is idempotent
		t.Fatalf("rerun failed=%+v", failed)
	}
	snapshot, _ := manager.Load()
	combos := map[string]Combo{}
	for _, combo := range snapshot.Combos() {
		combos[combo.Name] = combo
	}
	if len(combos) != 2 || len(combos["fast"].Members) != 2 || combos["fast"].Members[1].ModelID != "claude-text" || combos["fast"].FusionEnabled {
		t.Fatalf("fast=%+v", combos["fast"])
	}
	panel := combos["panel"]
	tuning := panel.FusionTuning
	if !panel.FusionEnabled || panel.JudgeModel != "anthropic/claude-text" || tuning.MinPanel == nil || *tuning.MinPanel != 2 || *tuning.StragglerGraceMs != 5000 || *tuning.PanelHardTimeoutMs != 60000 {
		t.Fatalf("panel=%+v tuning=%+v", panel, tuning)
	}
	if hasNote(plan, `comboStrategies.panel: field "fusionTuning"`) || hasNote(plan, `comboStrategies.panel: field "judgeModel"`) {
		t.Fatalf("superseded note kept: %v", plan.Notes)
	}
	if target, ok := snapshot.ResolveModel("openai", "gpt-text"); !ok || target.ID != "gpt-text" {
		t.Fatal("seeded model missing")
	}
}

func TestV1PlanRefusesInvalidFusionTuningByName(t *testing.T) {
	plan, err := PlanV1Migration([]byte(`{"settings":{"comboStrategies":{"p":{"fallbackStrategy":"fusion","fusionTuning":{"panelHardTimeoutMs":0,"extra":1}}}},"combos":[{"name":"p","models":["openai/a"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refusal(plan, "combos.p.fusionTuning"), "panelHardTimeoutMs") || !strings.Contains(refusal(plan, "combos.p.fusionTuning.extra"), "not a Routeweft fusion field") {
		t.Fatalf("%+v", plan.Refused)
	}
	if plan.Combos[0].FusionTuning.PanelHardTimeoutMs != nil {
		t.Fatal("invalid tuning stored")
	}
}
