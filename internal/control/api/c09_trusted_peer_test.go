package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestForwardedHeadersNeverChangeTheThrottleBucket pins PRD-SEC-002 / SPEC §12:
// forwarded-IP headers are ignored for every peer, so the login throttle keys on
// the socket host alone. Rotating X-Forwarded-For, X-Real-Ip or Forwarded cannot
// escape a full bucket, and a spoofed value naming another host cannot push the
// attacker's failures into that host's bucket.
func TestForwardedHeadersNeverChangeTheThrottleBucket(t *testing.T) {
	handler, _, store := newAPI(t, &fakeSettings{values: map[string]string{}})
	defer store.Close()
	handler.throttler.now = func() time.Time { return time.Unix(100, 0) }
	mux := http.NewServeMux()
	handler.Attach(mux)
	login := func(peer, password string, headers map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"operator","password":%q}`, password)))
		request.RemoteAddr = peer
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	if got := clientKey(&http.Request{RemoteAddr: "203.0.113.7:4444", Header: http.Header{"X-Forwarded-For": {"198.51.100.9"}}}); got != "203.0.113.7" {
		t.Fatalf("client key=%q want the socket host", got)
	}
	if got := clientKey(&http.Request{RemoteAddr: "[2001:db8::1]:4444"}); got != "2001:db8::1" {
		t.Fatalf("ipv6 client key=%q", got)
	}

	// Every failure claims a different forwarded identity; all land in the one
	// bucket of the real peer.
	for i := 0; i < loginMaxFailures; i++ {
		recorder := login("203.0.113.7:4000", "wrong", map[string]string{
			"X-Forwarded-For": fmt.Sprintf("192.0.2.%d", i+1),
			"X-Real-Ip":       fmt.Sprintf("192.0.2.%d", i+1),
			"Forwarded":       fmt.Sprintf("for=192.0.2.%d", i+1),
		})
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d want 401", i, recorder.Code)
		}
	}
	// The bucket is full: a fresh spoofed identity, and even the right password,
	// are refused for the same socket peer.
	for _, password := range []string{"wrong", "s3cret"} {
		recorder := login("203.0.113.7:4001", password, map[string]string{"X-Forwarded-For": "192.0.2.200"})
		if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
			t.Fatalf("spoofed identity escaped the bucket: status=%d", recorder.Code)
		}
	}
	// Naming the throttled peer in a header from another socket does not inherit
	// its lockout, and a clean peer is not affected by the attacker's headers.
	if recorder := login("198.51.100.9:5555", "s3cret", map[string]string{"X-Forwarded-For": "203.0.113.7"}); recorder.Code != http.StatusOK {
		t.Fatalf("other socket peer status=%d want 200", recorder.Code)
	}

	// Consequence documented in RUNBOOK 7: clients behind one reverse proxy are
	// one socket peer, so one client's failures lock out the rest.
	for i := 0; i < loginMaxFailures; i++ {
		login("10.0.0.2:3000", "wrong", map[string]string{"X-Forwarded-For": "192.0.2.50"})
	}
	if recorder := login("10.0.0.2:3001", "s3cret", map[string]string{"X-Forwarded-For": "192.0.2.51"}); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("second client behind the same proxy status=%d want 429 (shared bucket)", recorder.Code)
	}
}
