package runtime

import (
	"context"
	"reflect"
	"testing"
)

func TestSetSettingsAppliesAllowlistedChange(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	before := manager.Settings()
	revision, err := manager.SetSettings(ctx, map[string]string{"cavemanEnabled": "true", "stickyRoundRobinLimit": "5"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if revision <= 0 {
		t.Fatalf("revision=%d", revision)
	}
	after := manager.Settings()
	if after["cavemanEnabled"] != "true" || after["stickyRoundRobinLimit"] != "5" {
		t.Fatalf("settings not applied: %v", after)
	}
	if after["rtkEnabled"] != before["rtkEnabled"] {
		t.Fatal("unrelated setting changed")
	}
	// A remove reverts to the compiled default rather than deleting the key.
	if _, err := manager.SetSettings(ctx, nil, []string{"cavemanEnabled"}); err != nil {
		t.Fatal(err)
	}
	if got := manager.Settings()["cavemanEnabled"]; got != "false" {
		t.Fatalf("cavemanEnabled=%q want compiled default false", got)
	}
}

func TestSetSettingsRejectsNonWritableKeys(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	for _, values := range []map[string]string{{"requireLogin": "false"}, {"notARealSetting": "1"}} {
		if _, err := manager.SetSettings(ctx, values, nil); err == nil {
			t.Fatalf("accepted non-writable settings %v", values)
		}
	}
	if _, err := manager.SetSettings(ctx, nil, []string{"requireLogin"}); err == nil {
		t.Fatal("accepted non-writable remove")
	}
	// A rejected patch must not change any live setting.
	if manager.Settings()["requireLogin"] != "true" {
		t.Fatal("rejected patch mutated settings")
	}
}

// TestSetSettingsAllowsRequireAPIKey is the Endpoint & Key toggle (PRD §14):
// requireApiKey is a normal writable setting and defaults to requiring keys.
func TestSetSettingsAllowsRequireAPIKey(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	if manager.Settings()["requireApiKey"] != "true" {
		t.Fatalf("requireApiKey default=%q want true", manager.Settings()["requireApiKey"])
	}
	if _, err := manager.SetSettings(ctx, map[string]string{"requireApiKey": "false"}, nil); err != nil {
		t.Fatal(err)
	}
	if manager.Settings()["requireApiKey"] != "false" {
		t.Fatalf("requireApiKey=%q want false", manager.Settings()["requireApiKey"])
	}
	if _, err := manager.SetSettings(ctx, map[string]string{"requireApiKey": "maybe"}, nil); err == nil {
		t.Fatal("accepted a non-boolean requireApiKey value")
	}
	if _, err := manager.SetSettings(ctx, nil, []string{"requireApiKey"}); err != nil {
		t.Fatal(err)
	}
	if manager.Settings()["requireApiKey"] != "true" {
		t.Fatalf("requireApiKey after remove=%q want compiled default true", manager.Settings()["requireApiKey"])
	}
}

func TestSetSettingsRejectsMalformedValuesWithoutMutation(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	before := manager.Settings()
	cases := []struct{ key, value string }{
		{"requireApiKey", "maybe"},
		{"providerStrategy", "random"},
		{"comboStrategy", "roundrobin"},
		{"stickyRoundRobinLimit", "-1"},
		{"observabilityBatchSize", "0"},
		{"providerStrategies", `[]`},
		{"providerStrategies", `{"openai":"unknown"}`},
		{"comboStrategies", `null`},
		{"quotaVisibility", `[]`},
		{"providerCompatibility", `null`},
		{"noProxy", `{}`},
		{"outboundProxyUrl", "file:///etc/passwd"},
		{"outboundProxyUrl", "http://proxy.example:65536"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			if _, err := manager.SetSettings(ctx, map[string]string{tc.key: tc.value}, nil); err == nil {
				t.Fatalf("accepted malformed setting %s=%q", tc.key, tc.value)
			}
			if after := manager.Settings(); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected mutation changed settings: got=%v want=%v", after, before)
			}
		})
	}
}
