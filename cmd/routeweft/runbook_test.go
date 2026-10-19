package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/app"
)

// TestRunbookCLISurface verifies the RUNBOOK §26 CLI surface: migrate, backup,
// check-only restore, version and help all work against a real data directory.
func TestRunbookCLISurface(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	build := filepath.Join(t.TempDir(), "routeweft")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", build, ".")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		t.Fatalf("build CLI: %v", err)
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, build, args...)
		cmd.Env = append(os.Environ(), "ROUTEWEFT_DATA_DIR="+dataDir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	runOK := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("routeweft %v: %v\n%s", args, err, out)
		}
		return out
	}
	if out := runOK("version"); !strings.Contains(out, "routeweft ") {
		t.Fatalf("version output: %q", out)
	}
	if out := runOK("migrate", "--data-dir", dataDir); !strings.Contains(out, "schema version") {
		t.Fatalf("migrate output: %q", out)
	}
	artifact := filepath.Join(t.TempDir(), "runbook.sqlite")
	if out := runOK("backup", "--data-dir", dataDir, "--output", artifact); !strings.Contains(out, "backup ") {
		t.Fatalf("backup output: %q", out)
	}
	if _, err := os.Stat(artifact + ".meta.json"); err != nil {
		t.Fatalf("backup metadata: %v", err)
	}
	if out := runOK("restore", "--input", artifact, "--data-dir", dataDir, "--check"); !strings.Contains(out, "restore --check") {
		t.Fatalf("restore check output: %q", out)
	}
	if out, err := run("restore", "--input", artifact, "--data-dir", dataDir); err == nil {
		t.Fatalf("restore without --check activated: %q", out)
	}
	if out := runOK("help"); !strings.Contains(out, "routeweft [serve|migrate|backup|restore|version|help]") {
		t.Fatalf("help output: %q", out)
	}
}

// TestRunbookHealthAndAdminGate verifies the RUNBOOK §1/§3/§8 served surface:
// liveness is upstream-independent, readiness becomes healthy after migrations
// and snapshot compilation, and the admin/inference surfaces stay gated.
func TestRunbookHealthAndAdminGate(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	application := app.New(app.Config{
		Listen:                 address,
		DataDir:                dataDir,
		AdminBootstrapUsername: "operator",
		AdminBootstrapPassword: "runbook-secret",
	}, nil)
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- application.Serve(serveCtx) }()
	defer func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	}()
	waitRunbookReady(t, address)
	assertGet(t, "http://"+address+"/health/live", http.StatusOK)
	assertGet(t, "http://"+address+"/health/ready", http.StatusOK)
	assertGet(t, "http://"+address+"/admin/v1/overview", http.StatusUnauthorized)
	assertGet(t, "http://"+address+"/v1/models", http.StatusUnauthorized)

	login, err := http.Post("http://"+address+"/admin/v1/auth/login", "application/json", strings.NewReader(`{"username":"operator","password":"runbook-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("admin login status=%d", login.StatusCode)
	}
	cookies := login.Cookies()
	if len(cookies) == 0 || !cookies[0].HttpOnly {
		t.Fatalf("admin session cookie not HttpOnly: %+v", cookies)
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/admin/v1/overview", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookies[0])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated overview status=%d", response.StatusCode)
	}
}

func waitRunbookReady(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
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
	t.Fatal("readiness did not become healthy")
}

func assertGet(t *testing.T, url string, want int) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("GET %s status=%d want %d", url, response.StatusCode, want)
	}
}
