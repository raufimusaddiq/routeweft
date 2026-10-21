package runtime

import (
	"context"
	"testing"
)

func TestQuotaAutoPingSettingsPersistAndValidate(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	for _, key := range []string{"claudeAutoPing", "codexAutoPing"} {
		if manager.Settings()[key] != `{"connections":{}}` {
			t.Fatal("auto-ping must default off")
		}
		for _, bad := range []string{`true`, `null`, `{}`, `{"connections":null}`, `{"connections":{"a":null}}`, `{"connections":{"a":"true"}}`, `{"connections":{"":true}}`, `{"connections":{},"enabled":true}`} {
			if _, err := manager.SetSettings(context.Background(), map[string]string{key: bad}, nil); err == nil {
				t.Fatalf("accepted %s", bad)
			}
		}
		value := `{"connections":{"fixture":true,"disabled":false}}`
		if _, err := manager.SetSettings(context.Background(), map[string]string{key: value}, nil); err != nil {
			t.Fatal(err)
		}
		var stored string
		if err := store.DB().QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&stored); err != nil || stored != value {
			t.Fatalf("stored=%q err=%v", stored, err)
		}
		if _, err := manager.SetSettings(context.Background(), nil, []string{key}); err != nil {
			t.Fatal(err)
		}
		if manager.Settings()[key] != `{"connections":{}}` {
			t.Fatal("remove did not disable")
		}
	}
	if IsWritableSetting("quotaAutoPingHistory") {
		t.Fatal("operator must not overwrite suppression history")
	}
}
