package runtime

import (
	"context"
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
	for _, values := range []map[string]string{{"requireApiKey": "false"}, {"notARealSetting": "1"}} {
		if _, err := manager.SetSettings(ctx, values, nil); err == nil {
			t.Fatalf("accepted non-writable settings %v", values)
		}
	}
	if _, err := manager.SetSettings(ctx, nil, []string{"requireLogin"}); err == nil {
		t.Fatal("accepted non-writable remove")
	}
	// A rejected patch must not change any live setting.
	if manager.Settings()["requireApiKey"] != "true" {
		t.Fatal("rejected patch mutated settings")
	}
}
