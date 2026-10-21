package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adminauth "github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

type readinessItem struct {
	ID              string `json:"id"`
	EnabledAccounts int    `json:"enabledAccounts"`
	Readiness       struct {
		Status string `json:"status"`
		Served *bool  `json:"served"`
		Reason string `json:"reason"`
	} `json:"readiness"`
}

func readinessItems(t *testing.T, handler *Handler, mux *http.ServeMux) map[string]readinessItem {
	t.Helper()
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/providers?pageSize=200", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("providers status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Items []readinessItem `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	items := map[string]readinessItem{}
	for _, item := range payload.Items {
		items[item.ID] = item
	}
	return items
}

// Readiness is a build fact. An unavailable provider stays unavailable when it
// is enabled and holds a credential; a ready one is ready with no account at all.
func TestProviderReadinessIgnoresEnabledAndCredential(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	handler.opts.Providers = registry.Builtins()
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO provider_nodes(id,kind,provider_id,name,transports) VALUES('n1','builtin','cursor','Cursor','["cursor"]');
INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,secret_blob,enabled) VALUES('c1','n1','acct','oauth','me','sealed',1)`); err != nil {
		t.Fatal(err)
	}
	items := readinessItems(t, handler, mux)

	cursor := items["cursor"]
	if cursor.EnabledAccounts != 1 {
		t.Fatalf("cursor enabledAccounts=%d, the fixture must enable it", cursor.EnabledAccounts)
	}
	if cursor.Readiness.Status != "unavailable" || cursor.Readiness.Served == nil || *cursor.Readiness.Served || cursor.Readiness.Reason == "" {
		t.Errorf("enabled+credentialed cursor readiness = %+v, want unavailable, not served, with a reason", cursor.Readiness)
	}
	groq := items["groq"]
	if groq.EnabledAccounts != 0 {
		t.Fatalf("groq enabledAccounts=%d, the fixture must give it no account", groq.EnabledAccounts)
	}
	if groq.Readiness.Status != "ready" || groq.Readiness.Served == nil || !*groq.Readiness.Served {
		t.Errorf("accountless groq readiness = %+v, want ready and served", groq.Readiness)
	}
	if azure := items["azure"]; azure.Readiness.Status != "experimental" || azure.Readiness.Served == nil || !*azure.Readiness.Served {
		t.Errorf("azure readiness = %+v, want experimental and served", azure.Readiness)
	}
	// Every served catalog row reports a readiness; none reads unknown.
	for id, item := range items {
		if item.Readiness.Status == "" || item.Readiness.Status == "unknown" {
			t.Errorf("%s reports readiness %q", id, item.Readiness.Status)
		}
	}
}

// A spec with no declared readiness reads unknown, never ready.
func TestProviderReadinessAbsentReadsUnknown(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	if err := bootstrapOperator(context.Background(), store.DB()); err != nil {
		t.Fatal(err)
	}
	got := readinessItems(t, handler, mux)["openai"].Readiness
	if got.Status != "unknown" || got.Served != nil {
		t.Errorf("undeclared readiness = %+v, want unknown with no served flag", got)
	}
}
