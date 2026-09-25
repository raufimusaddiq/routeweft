package app

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/backup"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// TestActivateRestorePreservesRollbackAndRecompiles proves that only a
// validated candidate replaces the live DB, the previous DB remains available
// as rollback, and the new snapshot is published before readiness returns.
func TestActivateRestorePreservesRollbackAndRecompiles(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	application := New(Config{Listen: ":0", DataDir: dataDir}, nil)
	if err := application.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if application.store != nil {
			_ = application.store.Close()
		}
	}()

	if _, err := application.runtime.SetSettings(ctx, map[string]string{"rtkEnabled": "false"}, nil); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "restore.sqlite")
	if _, err := backup.Create(ctx, application.store, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := application.runtime.SetSettings(ctx, map[string]string{"rtkEnabled": "true"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('postBackup','preserve rollback')"); err != nil {
		t.Fatal(err)
	}
	candidate, err := backup.StageRestore(ctx, artifact, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	restoredFile, err := sqlite.Open(ctx, candidate.Path)
	if err != nil {
		t.Fatal(err)
	}
	var staged string
	if err := restoredFile.DB().QueryRowContext(ctx, "SELECT value FROM settings WHERE key='rtkEnabled'").Scan(&staged); err != nil {
		t.Fatal(err)
	}
	_ = restoredFile.Close()
	if staged != "false" {
		t.Fatalf("staged candidate value=%q, want false", staged)
	}
	if err := application.activateRestore(ctx, candidate); err != nil {
		t.Fatal(err)
	}

	if !application.ready.Load() {
		t.Fatal("readiness was not restored after activation")
	}
	settings := application.runtime.Settings()
	if settings["rtkEnabled"] != "false" {
		t.Fatalf("restored setting=%q, want false", settings["rtkEnabled"])
	}
	var count int
	if err := application.store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE key='postBackup'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("post-backup row survived database restore")
	}
	rollbacks, err := filepath.Glob(application.DatabasePath() + ".pre-restore-*")
	if err != nil || len(rollbacks) != 1 {
		t.Fatalf("rollback artifacts=%v err=%v", rollbacks, err)
	}
	rollback, err := os.Stat(rollbacks[0])
	if err != nil || rollback.Size() == 0 {
		t.Fatalf("rollback artifact stat=%v err=%v", rollback, err)
	}
}

func TestServeResumesAfterRestoreRequest(t *testing.T) {
	dataDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	application := New(Config{Listen: address, DataDir: dataDir}, nil)
	if err := application.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := application.runtime.SetSettings(ctx, map[string]string{"rtkEnabled": "false"}, nil); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "restore.sqlite")
	if _, err := backup.Create(ctx, application.store, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := application.runtime.SetSettings(ctx, map[string]string{"rtkEnabled": "true"}, nil); err != nil {
		t.Fatal(err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- application.Serve(ctx) }()
	waitHTTPReady(t, address)
	metadata, err := application.QueueRestore(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ConfigRevision == 0 {
		t.Fatal("restore metadata omitted config revision")
	}
	deadline := time.Now().Add(10 * time.Second)
	for application.quiescent.Load() || !application.ready.Load() {
		if time.Now().After(deadline) {
			t.Fatal("service did not resume after restore")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if application.runtime.Settings()["rtkEnabled"] != "false" {
		t.Fatalf("restored setting=%q", application.runtime.Settings()["rtkEnabled"])
	}
	waitHTTPReady(t, address)

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func waitHTTPReady(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + address + "/health/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("health readiness did not become healthy")
}
