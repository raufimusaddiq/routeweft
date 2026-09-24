package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
