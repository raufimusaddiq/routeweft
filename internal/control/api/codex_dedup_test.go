package api

import (
	"context"
	"testing"
)

// codexReply is a Codex token reply whose id token names email and account.
func codexReply(email, account, access, refresh string) string {
	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"` + account + `","chatgpt_plan_type":"plus"}`
	if email != "" {
		claims += `,"email":"` + email + `"`
	}
	claims += `}`
	return `{"access_token":"` + access + `","refresh_token":"` + refresh + `","id_token":"` + unsignedJWT(claims) + `","expires_in":3600}`
}

// Two ChatGPT accounts behind one email are two connections: the reference
// matches a Codex row only when both sides carry the same chatgptAccountId
// (connectionsRepo.js:239-242), so the second sign-in inserts a row beside the
// first instead of being refused.
func TestCodexSameEmailDifferentAccountAddsConnection(t *testing.T) {
	exchange := &oauthExchange{}
	_, mux, creds, cookie := newOAuthAPI(t, exchange)
	first := codexSignIn(t, mux, cookie, exchange, codexReply("shared@example.invalid", "acct-1", "access-1", "refresh-1"))
	second := codexSignIn(t, mux, cookie, exchange, codexReply("shared@example.invalid", "acct-2", "access-2", "refresh-2"))
	if first["id"] == second["id"] {
		t.Fatalf("second account overwrote the first: %v %v", first, second)
	}
	connections, err := creds.ListConnections(context.Background(), "codex")
	if err != nil || len(connections) != 2 {
		t.Fatalf("connections=%+v err=%v", connections, err)
	}
	if got := connectionByID(t, creds, first["id"].(string)); got.Secret.RefreshToken != "refresh-1" || got.Identity != "shared@example.invalid" {
		t.Fatalf("first connection changed: %+v", got)
	}
}
