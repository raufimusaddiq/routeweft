package app

import (
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
	tooLarge := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, tooLarge)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared body status %d", recorder.Code)
	}

	unknownLength := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	unknownLength.ContentLength = -1
	recorder = httptest.NewRecorder()
	wrapped.ServeHTTP(recorder, unknownLength)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("streamed body status %d", recorder.Code)
	}
}
