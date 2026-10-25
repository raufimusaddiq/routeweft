package onboarding_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/control/api"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// The grok-web and perplexity-web session cookie is a credential like the MiMo
// account session (#183): sealed at rest, and absent from read models, logs and
// the errors the wire raises for it.
func TestWebSessionCookieSealedAndAbsentFromReadModelsLogsAndErrors(t *testing.T) {
	for providerID, secret := range map[string]string{"grok-web": "fixture-private-sso", "perplexity-web": "fixture-private-next-auth"} {
		t.Run(providerID, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "web.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := migrations.NewRunner(db.DB()).Apply(ctx); err != nil {
				t.Fatal(err)
			}
			sealer, err := credentials.NewSealer(bytes.Repeat([]byte{7}, credentials.MasterKeySize))
			if err != nil {
				t.Fatal(err)
			}
			store, err := credentials.NewStore(db.DB(), sealer)
			if err != nil {
				t.Fatal(err)
			}
			sessions := adminauth.NewSessionManager(time.Hour)
			session, err := sessions.Create(adminauth.Account{ID: "fixture-admin", Username: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			oldLog := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(oldLog) })
			oldSlog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(oldSlog) })
			mux := http.NewServeMux()
			api.New(api.Options{Sessions: sessions, DB: db.DB(), Credentials: store, Providers: registry.Builtins()}).Attach(mux)
			request := func(method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.AddCookie(&http.Cookie{Name: "routeweft_admin_session", Value: session.ID})
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}
			payload, _ := json.Marshal(map[string]any{"providerId": providerID, "name": "Web", "authKind": "cookie", "identity": "fixture-account",
				"secret": map[string]any{"cookie": secret}})
			saved := request(http.MethodPost, "/admin/v1/connections", string(payload))
			if saved.Code != 200 {
				t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
			}
			connections, err := store.ListConnections(ctx, providerID)
			if err != nil || len(connections) != 1 {
				t.Fatal("connection not saved")
			}
			connection, err := store.GetConnection(ctx, connections[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if connection.AuthKind != credentials.AuthCookie || connection.Secret.Cookie != secret {
				t.Fatal("cookie did not round trip through the sealed store")
			}
			var blob string
			if err := db.DB().QueryRowContext(ctx, "SELECT secret_blob FROM provider_connections WHERE id=?", connection.ID).Scan(&blob); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(blob, secret) {
				t.Fatal("cookie stored in the clear")
			}
			configuration, err := store.Configuration(ctx)
			if err != nil {
				t.Fatal(err)
			}
			configJSON, _ := json.Marshal(configuration)
			exposed := saved.Body.String() + string(configJSON)
			for _, path := range []string{"/admin/v1/connections", "/admin/v1/provider-nodes", "/admin/v1/providers"} {
				w := request(http.MethodGet, path, "")
				if w.Code != 200 {
					t.Fatalf("read %s status=%d", path, w.Code)
				}
				exposed += w.Body.String()
			}
			// The credential travels only as the request's own Cookie header; a value the
			// wire refuses is refused without being quoted.
			headers, err := wire.For(providerID).RequestHeaders(routing.ProviderRef{ProviderID: providerID, APIToken: connection.Secret.Cookie}, nil, "")
			if err != nil || !strings.Contains(headers["Cookie"], secret) {
				t.Fatalf("cookie header = %q, err = %v", headers["Cookie"], err)
			}
			_, refusal := wire.For(providerID).RequestHeaders(routing.ProviderRef{ProviderID: providerID, APIToken: secret + ";"}, nil, "")
			if refusal == nil {
				t.Fatal("a cookie with a separator was sent")
			}
			exposed += refusal.Error() + logs.String()
			if strings.Contains(exposed, secret) {
				t.Fatal("session cookie exposed outside sealed storage")
			}
		})
	}
}
