package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDGeneratedAndValidated(t *testing.T) {
	seen := ""
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get(requestIDHeader)
		w.WriteHeader(http.StatusNoContent)
	})
	wrapped := withRequestID(next)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(requestIDHeader, "client-id/invalid")
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, request)
	if seen == "" || seen == "client-id/invalid" || recorder.Header().Get(requestIDHeader) != seen {
		t.Fatalf("request id not replaced/echoed: handler=%q response=%q", seen, recorder.Header().Get(requestIDHeader))
	}
	if !strings.HasPrefix(seen, "req_") {
		t.Fatalf("generated request id %q lacks prefix", seen)
	}
}

func TestBodyLimitRejectsDeclaredOversizeAndCapsReads(t *testing.T) {
	wrapped := withBodyLimit(4, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		_, _ = w.Write(body)
	}))
	tooLarge := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, tooLarge)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared body status %d", recorder.Code)
	}

	unknownLength := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("12345"))
	unknownLength.ContentLength = -1
	recorder = httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, unknownLength)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("streamed body status %d", recorder.Code)
	}
}

func TestHandlerBodyLimitOnlyWrapsInference(t *testing.T) {
	app := New(Config{DataDir: t.TempDir(), MaxBodyBytes: 4}, nil)
	if err := app.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	handler := app.Handler()

	admin := httptest.NewRecorder()
	handler.ServeHTTP(admin, httptest.NewRequest(http.MethodPost, "/admin/v1/backup/restore", strings.NewReader("12345")))
	if admin.Code != http.StatusUnauthorized {
		t.Fatalf("admin route was blocked by public body limit: status=%d", admin.Code)
	}

	for _, path := range []string{"/v1/chat/completions", "/v1beta/models/m:generateContent"} {
		inference := httptest.NewRecorder()
		handler.ServeHTTP(inference, httptest.NewRequest(http.MethodPost, path, strings.NewReader("12345")))
		if inference.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s body limit status=%d", path, inference.Code)
		}
	}
}

// TestQuiescenceGateRefusesNewWorkDuringRestore proves that while a restore is
// reinitializing process state, new requests are refused instead of racing the
// database swap, while liveness probing still works (SPEC §25).
func TestQuiescenceGateRefusesNewWorkDuringRestore(t *testing.T) {
	app := New(Config{}, nil)
	app.quiescent.Store(true)
	handler := app.quiescenceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	inference := httptest.NewRecorder()
	handler.ServeHTTP(inference, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if inference.Code != http.StatusServiceUnavailable {
		t.Fatalf("inference during restore status=%d want 503", inference.Code)
	}

	control := httptest.NewRecorder()
	handler.ServeHTTP(control, httptest.NewRequest(http.MethodGet, "/admin/v1/backup", nil))
	if control.Code != http.StatusServiceUnavailable {
		t.Fatalf("control request during restore status=%d want 503", control.Code)
	}

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusNoContent {
		t.Fatalf("liveness during restore status=%d", live.Code)
	}
}
