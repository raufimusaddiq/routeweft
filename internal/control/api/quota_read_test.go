package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

type quotaPage struct {
	Items []struct {
		ConnectionID      string         `json:"connectionId"`
		ProviderID        string         `json:"providerId"`
		Name              string         `json:"name"`
		Status            string         `json:"status"`
		Remaining         *float64       `json:"remaining"`
		Stale             *bool          `json:"stale"`
		NextResetAt       *time.Time     `json:"nextResetAt"`
		WindowsObservedAt *time.Time     `json:"windowsObservedAt"`
		Windows           []quota.Window `json:"windows"`
	} `json:"items"`
	Page      int      `json:"page"`
	PageSize  int      `json:"pageSize"`
	Total     int      `json:"total"`
	Providers []string `json:"providers"`
	Sort      string   `json:"sort"`
}

// quotaFixture seeds connections c000.. across the named providers and returns
// a request helper bound to an operator session.
func quotaFixture(t *testing.T, providers map[string]int) (*Handler, func(string) (int, quotaPage)) {
	t.Helper()
	handler, mux, store := newReadAPI(t)
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	n := 0
	for provider, count := range providers {
		if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name) VALUES(?,'builtin',?,?)", "n-"+provider, provider, provider); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("c%03d", n)
			if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_connections(id,node_id,name,auth_kind,identity) VALUES(?,?,?,'api-key',?)", id, "n-"+provider, fmt.Sprintf("%s-account-%03d", provider, i), id); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	return handler, func(query string) (int, quotaPage) {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/admin/v1/quota"+query, nil)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		mux.ServeHTTP(recorder, request)
		var page quotaPage
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
		}
		return recorder.Code, page
	}
}

// TestQuotaReadReachesEveryAccountPastOneHundred is the #213 paging check: with
// 130 accounts every one is reachable through server pages, none twice.
func TestQuotaReadReachesEveryAccountPastOneHundred(t *testing.T) {
	_, get := quotaFixture(t, map[string]int{"openai": 130})
	seen := map[string]bool{}
	for page := 1; page <= 2; page++ {
		code, body := get(fmt.Sprintf("?page=%d&pageSize=100", page))
		if code != http.StatusOK || body.Total != 130 || body.PageSize != 100 {
			t.Fatalf("page %d: code=%d total=%d size=%d", page, code, body.Total, body.PageSize)
		}
		for _, item := range body.Items {
			if seen[item.ConnectionID] {
				t.Fatalf("%s returned twice", item.ConnectionID)
			}
			seen[item.ConnectionID] = true
		}
	}
	if len(seen) != 130 {
		t.Fatalf("reached %d of 130 accounts", len(seen))
	}
	if _, body := get("?page=3&pageSize=100"); len(body.Items) != 0 {
		t.Fatalf("page past the end returned %d items", len(body.Items))
	}
}

// TestQuotaReadSearchesAndFiltersBeforePaging keeps total and the page scoped to
// the provider filter and the account search, and lists every provider.
func TestQuotaReadSearchesAndFiltersBeforePaging(t *testing.T) {
	_, get := quotaFixture(t, map[string]int{"openai": 3, "groq": 2})
	_, body := get("?provider=groq")
	if body.Total != 2 || len(body.Items) != 2 || body.Items[0].ProviderID != "groq" {
		t.Fatalf("provider filter: %+v", body)
	}
	if len(body.Providers) != 2 || body.Providers[0] != "groq" || body.Providers[1] != "openai" {
		t.Fatalf("providers=%v, want every configured provider whatever the filter", body.Providers)
	}
	_, body = get("?q=ACCOUNT-002")
	if body.Total != 1 || body.Items[0].Name != "openai-account-002" {
		t.Fatalf("account search: %+v", body)
	}
	_, body = get("?q=gro")
	if body.Total != 2 {
		t.Fatalf("provider search total=%d", body.Total)
	}
	if code, _ := get("?sort=priority"); code != http.StatusBadRequest {
		t.Fatalf("unknown sort accepted: %d", code)
	}
}

// TestQuotaReadKeepsAccountStatesDistinct proves not-reported, unknown,
// unlimited and exhausted are four outcomes, plus error and stale freshness.
func TestQuotaReadKeepsAccountStatesDistinct(t *testing.T) {
	handler, get := quotaFixture(t, map[string]int{"openai": 6})
	now := time.Now().UTC()
	zero, half := 0.0, 0.5
	future, old := now.Add(time.Hour), now.Add(-time.Hour)
	details := map[string]quota.State{
		"c001": {Plan: "credit", ObservedAt: now, Windows: []quota.Window{{Name: "Balance (USD)", Credit: &quota.Credit{Currency: "USD", State: quota.CreditPositive}}}},
		"c002": {Plan: "free", ObservedAt: now, Windows: []quota.Window{{Name: "daily", Unlimited: true}}},
		"c003": {Plan: "pro", ObservedAt: now, Windows: []quota.Window{{Name: "session", Remaining: &zero, ResetAt: &future}}},
		"c005": {Plan: "pro", ObservedAt: old, Windows: []quota.Window{{Name: "weekly", Remaining: &half}}},
	}
	handler.opts.QuotaState = func(id string) (quota.State, bool) { s, ok := details[id]; return s, ok }
	handler.opts.QuotaStaleAfter = 15 * time.Minute
	state := handler.opts.Runtime.State()
	state.ObserveQuotaResult("c003", &zero, future, now, "")
	state.ObserveQuotaResult("c004", nil, time.Time{}, now, "provider said no")
	state.ObserveQuotaResult("c005", &half, time.Time{}, now.Add(-time.Hour), "")
	_, body := get("")
	want := map[string]string{"c000": "not-reported", "c001": "unknown", "c002": "unlimited", "c003": "exhausted", "c004": "error", "c005": "available"}
	for _, item := range body.Items {
		if item.Status != want[item.ConnectionID] {
			t.Errorf("%s status=%q want %q", item.ConnectionID, item.Status, want[item.ConnectionID])
		}
		switch item.ConnectionID {
		case "c000":
			if item.Remaining != nil || item.Stale != nil || item.Windows != nil {
				t.Errorf("unobserved account carries quota data: %+v", item)
			}
		case "c003":
			if item.Remaining == nil || *item.Remaining != 0 || item.NextResetAt == nil || !item.NextResetAt.Equal(future) || item.Stale == nil || *item.Stale {
				t.Errorf("exhausted account=%+v", item)
			}
		case "c005":
			if item.Stale == nil || !*item.Stale || item.WindowsObservedAt == nil || !item.WindowsObservedAt.Equal(old) {
				t.Errorf("hour-old observation not stale: %+v", item)
			}
		}
	}
	if len(body.Items) != 6 {
		t.Fatalf("items=%d", len(body.Items))
	}
}

// TestQuotaReadSortsByResetExhaustedAndError orders the whole account set, not
// one page, and keeps the default order for ties.
func TestQuotaReadSortsByResetExhaustedAndError(t *testing.T) {
	handler, get := quotaFixture(t, map[string]int{"openai": 5})
	now := time.Now().UTC()
	zero, low, high := 0.0, 0.1, 0.9
	soon, later := now.Add(10*time.Minute), now.Add(3*time.Hour)
	windowReset := now.Add(time.Minute)
	handler.opts.QuotaState = func(id string) (quota.State, bool) {
		if id == "c004" {
			return quota.State{ObservedAt: now, Windows: []quota.Window{{Name: "minute", Remaining: &high, ResetAt: &windowReset}}}, true
		}
		return quota.State{}, false
	}
	state := handler.opts.Runtime.State()
	state.ObserveQuotaResult("c001", &high, later, now, "")
	state.ObserveQuotaResult("c002", &zero, soon, now, "")
	state.ObserveQuotaResult("c003", nil, time.Time{}, now, "outage")
	state.ObserveQuotaResult("c004", &low, later, now, "")
	order := func(query string) []string {
		t.Helper()
		code, body := get(query)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", query, code)
		}
		ids := make([]string, 0, len(body.Items))
		for _, item := range body.Items {
			ids = append(ids, item.ConnectionID)
		}
		return ids
	}
	for query, want := range map[string]string{
		"":                              "c000 c001 c002 c003 c004",
		"?sort=reset":                   "c004 c002 c001 c000 c003",
		"?sort=exhausted":               "c002 c004 c001 c000 c003",
		"?sort=error":                   "c003 c000 c001 c002 c004",
		"?sort=reset&pageSize=2":        "c004 c002",
		"?sort=error&page=2&pageSize=2": "c001 c002",
	} {
		if got := fmt.Sprint(order(query)); got != "["+want+"]" {
			t.Errorf("%q order=%s want [%s]", query, got, want)
		}
	}
}
