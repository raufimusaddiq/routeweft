package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// #231 (O08): a thousand accounts. The connection list is served a bounded page at a time, a
// request for more is clamped, every account is reachable exactly once across the pages, and no
// page carries a credential. The time and heap it measures are logged, not asserted as budgets.
func TestAThousandConnectionsPageWithinTheBoundAndLeakNothing(t *testing.T) {
	const accounts = 1000
	_, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	ctx := context.Background()
	node, err := creds.PutNode(ctx, credentials.Node{ID: "scale-node", Kind: credentials.NodeGeneric, ProviderID: "scale", Name: "scale", Prefix: "scale", BaseURL: "https://scale.example.test", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	// One transaction of plain rows: the list reads the table, and a thousand sealed saves under the
	// race detector would measure the seal and the fsync, not the paging.
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < accounts; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,secret_blob,enabled,priority) VALUES(?,?,?,?,?,?,1,?)`,
			fmt.Sprintf("scale-%04d", i), node.ID, fmt.Sprintf("Account %04d", i), "api-key", fmt.Sprintf("id-%04d", i), fmt.Sprintf("scale-secret-%04d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	type page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		Page     int `json:"page"`
		PageSize int `json:"pageSize"`
		Total    int `json:"total"`
	}
	fetch := func(path string) (page, int) {
		t.Helper()
		recorder := doJSON(t, mux, cookie, http.MethodGet, path, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
		}
		for i := 0; i < accounts; i += 97 {
			if bytesContains(recorder.Body.Bytes(), fmt.Sprintf("scale-secret-%04d", i)) {
				t.Fatalf("%s carries a stored credential", path)
			}
		}
		var out page
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out, recorder.Body.Len()
	}

	// A request for far more than the bound is clamped to it.
	big, _ := fetch("/admin/v1/connections?provider=scale&page=1&pageSize=100000")
	if big.PageSize != maxPageSize || len(big.Items) != maxPageSize || big.Total != accounts {
		t.Fatalf("clamped page: size=%d items=%d total=%d", big.PageSize, len(big.Items), big.Total)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	seen := map[string]bool{}
	var slowest time.Duration
	var largest int
	pages := 0
	for p := 1; ; p++ {
		start := time.Now()
		out, size := fetch(fmt.Sprintf("/admin/v1/connections?provider=scale&page=%d&pageSize=%d", p, maxPageSize))
		if elapsed := time.Since(start); elapsed > slowest {
			slowest = elapsed
		}
		if size > largest {
			largest = size
		}
		if len(out.Items) == 0 {
			break
		}
		pages++
		for _, item := range out.Items {
			if seen[item.ID] {
				t.Fatalf("%s appears on two pages", item.ID)
			}
			seen[item.ID] = true
		}
		if p > accounts {
			t.Fatal("paging did not end")
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if len(seen) != accounts || pages != accounts/maxPageSize {
		t.Fatalf("reached %d accounts over %d pages, want %d over %d", len(seen), pages, accounts, accounts/maxPageSize)
	}
	t.Logf("O08 measured: %d accounts, %d pages of %d, slowest page %s, largest page %d KiB, heap in use %+d KiB after GC (in-process handler, SQLite on disk)",
		accounts, pages, maxPageSize, slowest.Round(time.Millisecond), largest/1024, (int64(after.HeapInuse)-int64(before.HeapInuse))/1024)
}

func bytesContains(haystack []byte, needle string) bool {
	return strings.Contains(string(haystack), needle)
}
