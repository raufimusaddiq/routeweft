package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The ingress completion hook is the only publisher: a real proxied request
// reaches a live subscriber with metadata only, and a subscriber that never
// reads neither delays the request nor survives past its buffer.
func TestCompletedRequestReachesLiveFeedAsMetadataOnly(t *testing.T) {
	f := systemOneFixture(t, "systemone-nonstreaming")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range f.Response.Headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(f.Response.Status)
		fmt.Fprint(w, f.Response.Body)
	}))
	defer upstream.Close()
	a, key, _ := systemOneApp(t, "typesafe", upstream.URL+"/typed/full-endpoint", 1)

	live, err := a.feed.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	wedged, err := a.feed.Subscribe() // never read
	if err != nil {
		t.Fatal(err)
	}
	defer wedged.Close()

	const requests = 80 // more than the subscriber buffer
	start := time.Now()
	for i := 0; i < requests; i++ {
		if w := serveRequest(a.Handler(), key, "/v1/systemone", f.Request.Body); w.Code != f.Response.Status {
			t.Fatalf("request %d status=%d", i, w.Code)
		}
		select { // keep the healthy subscriber drained
		case <-live.C():
		case <-time.After(2 * time.Second):
			t.Fatalf("request %d produced no live event", i)
		}
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("requests took %s with a wedged subscriber", elapsed)
	}
	if !wedged.Lagged() || a.feed.Subscribers() != 1 {
		t.Fatalf("wedged subscriber not cut: lagged=%v subscribers=%d", wedged.Lagged(), a.feed.Subscribers())
	}

	if w := serveRequest(a.Handler(), key, "/v1/systemone", f.Request.Body); w.Code != f.Response.Status {
		t.Fatal(w.Code)
	}
	event := <-live.C()
	raw, _ := json.Marshal(event)
	var keyID string
	if err := a.store.DB().QueryRow(`SELECT id FROM api_keys WHERE name='typed-client'`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if event.APIKeyID != keyID || event.Status != f.Response.Status || event.ProviderID != "typesafe" || event.RequestID == "" {
		t.Fatalf("event=%s", raw)
	}
	if strings.Contains(string(raw), key) {
		t.Fatalf("event carries the client key: %s", raw)
	}
	for _, field := range []string{"body", "prompt", "messages", "content", "authorization"} {
		if strings.Contains(strings.ToLower(string(raw)), field) {
			t.Fatalf("event carries %q: %s", field, raw)
		}
	}
}
