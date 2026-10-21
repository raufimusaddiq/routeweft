package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

func TestPublishedTransformSettingsChangeNextBody(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	transform := func() string {
		t.Helper()
		snapshot, err := manager.Load()
		if err != nil {
			t.Fatal(err)
		}
		got, err := snapshot.TransformBody(context.Background(), openai.ChatProtocol, body, false)
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	for _, enabled := range []string{"false", "true", "false"} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{"rtkEnabled": "false", "cavemanEnabled": enabled}, nil); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(transform(), "[routeweft:caveman]"); got != (enabled == "true") {
			t.Fatalf("enabled=%s marker=%v", enabled, got)
		}
	}
}

func TestStorageOnlySettingsAndAbsentControls(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	values := map[string]string{"quotaVisibility": `{"codex":{"hidden":["window"]}}`, "providerCompatibility": `{"codex":{"unknown":true}}`, "dnsToolEnabled": `{"codex":true}`, "pxpipeAutoInstall": "false"}
	if _, err := manager.SetSettings(context.Background(), values, nil); err != nil {
		t.Fatal(err)
	}
	for key, want := range values {
		if got := manager.Settings()[key]; got != want {
			t.Fatalf("%s=%q want %q", key, got, want)
		}
	}
	for _, key := range []string{"claudeAutoPing", "codexAutoPing", "cacheEnabled", "observabilityMaxBuffered", "observabilitySampleRate", "headroomCodeAware", "headroomKompress"} {
		if _, err := manager.SetSettings(context.Background(), map[string]string{key: "true"}, nil); err == nil {
			t.Fatalf("absent control %s accepted", key)
		}
	}
}
