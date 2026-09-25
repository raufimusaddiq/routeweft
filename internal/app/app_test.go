package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestQuotaServiceIsWiredIntoTheApp proves the production quota path exists: a
// configured credential key builds the service and the operator refresh entry
// point runs against the real store/registry wiring.
func TestQuotaServiceIsWiredIntoTheApp(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	app := New(Config{Listen: ":0", DataDir: t.TempDir(), CredentialKey: key, QuotaRefreshInterval: time.Hour}, nil)
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	if app.quota == nil {
		t.Fatal("quota service was not initialized")
	}
	if err := app.RefreshQuota(context.Background()); err != nil {
		t.Fatalf("refresh with no configured connections: %v", err)
	}
}

// TestQuotaServiceSkippedWithoutKey documents that a keyless deployment still
// boots, since there are then no sealed provider connections to read quota for.
func TestQuotaServiceSkippedWithoutKey(t *testing.T) {
	app := New(Config{Listen: ":0", DataDir: t.TempDir()}, nil)
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	if app.quota != nil {
		t.Fatal("quota service initialized without a credential key")
	}
	if err := app.RefreshQuota(context.Background()); err != nil {
		t.Fatalf("no-op refresh returned an error: %v", err)
	}
}

func TestLiveHealth(t *testing.T) {
	rr := httptest.NewRecorder()
	New(Config{}, nil).Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok\n" {
		t.Fatalf("got %d %q, want 200 %q", rr.Code, rr.Body.String(), "ok\n")
	}
}

func TestReadinessRequiresSuccessfulInitialization(t *testing.T) {
	app := New(Config{Listen: ":0", DataDir: t.TempDir()}, nil)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("uninitialized readiness status %d", rr.Code)
	}
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("initialized readiness status %d", rr.Code)
	}
	if err := app.store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessStaysFalseOnInitializationFailure(t *testing.T) {
	app := New(Config{DataDir: ""}, nil)
	if err := app.Initialize(context.Background()); err == nil {
		t.Fatal("expected missing data directory error")
	}
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status %d", rr.Code)
	}
}
