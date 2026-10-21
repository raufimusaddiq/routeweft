package onboarding_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

type mimoModelsClient struct{}

func (mimoModelsClient) Do(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
}

func TestMimoImportSealedAndAbsentFromReadModelsAndLogs(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "mimo.sqlite"))
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
	api.New(api.Options{Sessions: sessions, DB: db.DB(), Credentials: store, Providers: registry.Builtins(), OAuthClient: mimoModelsClient{}}).Attach(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "routeweft_admin_session", Value: session.ID})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	payload := map[string]any{"name": "MiMo", "identity": "fixture-account", "input": `{"apiKey":"sk-fixture","uid":"123","mimoPassToken":"fixture-private-pass","mimoUserId":"fixture-private-user","mimoCUserId":"fixture-private-cuser"}`}
	body, _ := json.Marshal(payload)
	imported := request(http.MethodPost, "/admin/v1/oauth/xiaomi-mimo/import", string(body))
	if imported.Code != 200 {
		t.Fatalf("import status=%d body=%s", imported.Code, imported.Body.String())
	}
	connections, err := store.ListConnections(ctx, "xiaomi-mimo")
	if err != nil || len(connections) != 1 {
		t.Fatal("connection not saved")
	}
	saved, err := store.GetConnection(ctx, connections[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"mimoPassToken": "fixture-private-pass", "mimoUserId": "fixture-private-user", "mimoCUserId": "fixture-private-cuser"} {
		if saved.Secret.ProviderData[key] != want {
			t.Fatalf("sealed field %s did not round trip", key)
		}
	}
	var blob string
	if err := db.DB().QueryRowContext(ctx, "SELECT secret_blob FROM provider_connections WHERE id=?", saved.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	configuration, err := store.Configuration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	configJSON, _ := json.Marshal(configuration)
	exposed := imported.Body.String() + blob + string(configJSON)
	for _, path := range []string{"/admin/v1/connections", "/admin/v1/provider-nodes", "/admin/v1/providers"} {
		w := request(http.MethodGet, path, "")
		if w.Code != 200 {
			t.Fatalf("read %s status=%d", path, w.Code)
		}
		exposed += w.Body.String()
	}
	exposed += logs.String()
	for _, secret := range []string{"fixture-private-pass", "fixture-private-user", "fixture-private-cuser"} {
		if strings.Contains(exposed, secret) {
			t.Fatal("account credential exposed outside sealed storage")
		}
	}
}
