package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// #231 (O03): the mock-provider fixtures for a browser sign-in cover every way an attempt can end.
// The wrong-state, replay, expiry, cancel, device-save and token-rotation cases live beside this
// one (see docs/P08_RELEASE_EVIDENCE.md for the index and the single command that runs them); this
// is the browser (PKCE callback) save failure, which only the device flow had.

// A save that persisted nothing must not leave the browser attempt spent in a way the operator
// cannot recover from, and must not leave a half-saved connection or a secret in the answer.
func TestOAuthBrowserSaveFailureLeavesNoConnectionAndNoSecret(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	h, mux, creds, cookie := newOAuthAPI(t, exchange)
	flow := startFlow(t, mux, cookie, "claude")
	node, err := h.builtinNode(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	// An API-key connection with the same identity is not a sign-in duplicate, so the store's
	// unique identity makes the save fail.
	clash, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "Existing", AuthKind: credentials.AuthAPIKey, Identity: "work@example.test", Secret: credentials.Secret{AccessToken: "other"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	failed := callback(t, mux, cookie, "claude", flow.State, oauthTestCode)
	if failed.Code == http.StatusOK || !strings.Contains(failed.Body.String(), "connection_save_failed") {
		t.Fatalf("save failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	assertNoSecrets(t, failed.Body.String())
	connections, _ := creds.ListConnections(context.Background(), "claude")
	if len(connections) != 1 || connections[0].ID != clash.ID {
		t.Fatalf("a failed save left connections %+v", connections)
	}
	// The operator starts again once the clash is gone, and that attempt saves.
	if err := creds.DeleteConnection(context.Background(), clash.ID); err != nil {
		t.Fatal(err)
	}
	again := startFlow(t, mux, cookie, "claude")
	if recorder := callback(t, mux, cookie, "claude", again.State, oauthTestCode); recorder.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if connections, _ := creds.ListConnections(context.Background(), "claude"); len(connections) != 1 || connections[0].Name != "Work" {
		t.Fatalf("retry left connections %+v", connections)
	}
}
