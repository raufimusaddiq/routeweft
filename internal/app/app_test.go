package app

import (
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
