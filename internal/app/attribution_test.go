package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A completed request is attributed to the authenticated key by its opaque id;
// neither the key nor anything derived from it reaches the usage record.
func TestUsageEventsAreAttributedToTheAuthenticatedKeyWithoutLeakingIt(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	go a.usage.Run(ctx)
	defer func() { a.usage.Close(); cancel(); a.usage.Wait() }()

	if w := serveRequest(a.Handler(), key, "/v1/systemone", f.Request.Body); w.Code != f.Response.Status {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	flush, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := a.usage.FlushNow(flush); err != nil {
		t.Fatal(err)
	}
	var keyID string
	if err := a.store.DB().QueryRow(`SELECT id FROM api_keys WHERE name='typed-client'`).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	var attributed sql.NullString
	if err := a.store.DB().QueryRow(`SELECT api_key_id FROM usage_events`).Scan(&attributed); err != nil {
		t.Fatal(err)
	}
	if !attributed.Valid || attributed.String != keyID {
		t.Fatalf("api_key_id=%v want %q", attributed, keyID)
	}
	if strings.Contains(keyID, key) {
		t.Fatal("key id must not contain the key")
	}
	// No column of any usage or detail row may contain the plaintext key.
	for _, table := range []string{"usage_events", "request_details", "usage_daily"} {
		rows, err := a.store.DB().Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fmt.Sprint(values...), key) {
				t.Fatalf("plaintext key found in %s row %v", table, values)
			}
		}
		rows.Close()
	}
}
