package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
)

// The observer is process-level: it must receive PXPIPE attempts from the
// initial snapshot and from every snapshot published afterwards.
func TestPXPIPEObserverSurvivesSnapshotPublication(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []pxpipe.Diagnostic
	manager, err := NewManagerObserved(ctx, store.DB(), func(d pxpipe.Diagnostic) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, d)
	})
	if err != nil {
		t.Fatal(err)
	}
	run := func(content string) {
		t.Helper()
		snapshot, err := manager.Load()
		if err != nil {
			t.Fatal(err)
		}
		body := `{"messages":[{"role":"user","content":"` + content + `"}]}`
		if _, err := snapshot.TransformBody(ctx, "openai-chat", []byte(body), false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.SetSettings(ctx, map[string]string{"pxpipeEnabled": "true", "pxpipeMinChars": "20"}, nil); err != nil {
		t.Fatal(err)
	}
	run("short")
	run(strings.Repeat("x", 40))
	// A later publication must keep reporting into the same sink.
	if _, err := manager.SetSettings(ctx, map[string]string{"pxpipeMinChars": "30"}, nil); err != nil {
		t.Fatal(err)
	}
	run(strings.Repeat("y", 40))

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("diagnostics=%d want 3: %+v", len(seen), seen)
	}
	if !seen[0].Skipped || seen[1].Succeeded || !seen[1].Attempted || seen[1].Error == "" || seen[2].Skipped {
		t.Fatalf("unexpected diagnostics: %+v", seen)
	}
}
