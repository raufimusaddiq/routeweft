package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// TestConnectionDeleteRetention pins the C06 delete row of the retention table:
// the connection row and its sealed secret (which carries every provider-data
// field) go with it, the process-local cooldown and quota record are dropped,
// and the non-secret credential audit rows stay.
func TestConnectionDeleteRetention(t *testing.T) {
	ctx := context.Background()
	handler, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"openai","name":"primary","identity":"acct","secret":{"accessToken":"sk-live"}}`)
	var connection struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	if err := creds.RecordCredentialEvent(ctx, connection.ID, "rotated", "refresh"); err != nil {
		t.Fatal(err)
	}
	state := handler.opts.Runtime.State()
	state.Cooldown(connection.ID, time.Now().Add(time.Hour))
	remaining := 3.0
	state.ObserveQuotaResult(connection.ID, &remaining, time.Time{}, time.Now(), "")

	if deleted := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/connections/"+connection.ID, ""); deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, active := state.CooldownUntil(connection.ID, time.Now()); active {
		t.Fatal("cooldown outlived the connection")
	}
	if _, ok := state.Quota(connection.ID); ok {
		t.Fatal("quota observation outlived the connection")
	}
	if _, err := creds.GetConnection(ctx, connection.ID); err == nil {
		t.Fatal("connection and its sealed secret survived delete")
	}
	var events int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_events WHERE connection_id=?", connection.ID).Scan(&events); err != nil || events == 0 {
		t.Fatalf("credential audit rows were dropped with the connection: n=%d err=%v", events, err)
	}
}

// TestChatGPTAccountIDProjectsIntoServingSettings pins the C06 item-1 mapping:
// the Codex account ID stored at onboarding reaches the serving projection, on
// the write path and on a cold reload.
func TestChatGPTAccountIDProjectsIntoServingSettings(t *testing.T) {
	ctx := context.Background()
	_, _, store, creds, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "codex-like"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "acct", Identity: "acct", AuthKind: credentials.AuthOAuth, Enabled: true,
		Secret: credentials.Secret{AccessToken: "t1", RefreshToken: "r1", ProviderData: map[string]string{"chatgptAccountId": "acct-chatgpt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Settings.ChatGPTAccountID != "acct-chatgpt" {
		t.Fatalf("stored settings %+v", connection.Settings)
	}
	loaded, err := creds.GetConnection(ctx, connection.ID)
	if err != nil || loaded.Settings.ChatGPTAccountID != "acct-chatgpt" {
		t.Fatalf("reload settings %+v err=%v", loaded.Settings, err)
	}
}
