package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func newKeyAPI(t *testing.T) (*Handler, *http.ServeMux, *http.Cookie, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	manager, err := runtime.NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	accounts := adminauth.NewStore(store.DB())
	if _, err := accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
		t.Fatal(err)
	}
	sessions := adminauth.NewSessionManager(time.Hour)
	handler := New(Options{Accounts: accounts, Sessions: sessions, Settings: manager, Keys: manager, DB: store.DB(), Runtime: manager})
	session, err := sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Attach(mux)
	return handler, mux, &http.Cookie{Name: sessionCookieName, Value: session.ID}, store
}

func TestKeyRoutesRequireSessionAndSameOrigin(t *testing.T) {
	_, mux, _, store := newKeyAPI(t)
	defer store.Close()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"name":"ci"}`)),
		httptest.NewRequest(http.MethodPatch, "/admin/v1/keys/k1", strings.NewReader(`{"paused":true}`)),
		httptest.NewRequest(http.MethodDelete, "/admin/v1/keys/k1", nil),
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d want 401", request.Method, request.URL.Path, recorder.Code)
		}
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"name":"ci"}`))
	crossOrigin.Header.Set("Origin", "https://evil.example")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, crossOrigin)
	if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", recorder.Code)
	}
}

func TestKeyLifecycleCreatePauseRevoke(t *testing.T) {
	handler, mux, cookie, store := newKeyAPI(t)
	defer store.Close()
	ctx := context.Background()

	create := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"name":"ci-pipeline"}`))
	create.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, create)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		Key struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
			Paused bool   `json:"paused"`
		} `json:"key"`
		Secret         string `json:"secret"`
		ConfigRevision uint64 `json:"configRevision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, "rw_") {
		t.Fatalf("plaintext secret missing: %q", created.Secret)
	}
	if created.ConfigRevision < 1 {
		t.Fatalf("create response omitted the active config revision: %d", created.ConfigRevision)
	}
	if created.Key.Name != "ci-pipeline" || created.Key.Prefix == "" || created.Key.Paused {
		t.Fatalf("unexpected created key: %+v", created.Key)
	}
	// The plaintext secret is only returned once; the list model exposes the prefix.
	snapshot, err := handler.opts.Runtime.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := snapshot.APIKeys().Lookup(created.Secret)
	if !ok || entry.ID != created.Key.ID {
		t.Fatalf("created key not active in compiled index: %+v ok=%v", entry, ok)
	}
	var storedDigest string
	if err := store.DB().QueryRowContext(ctx, "SELECT key_hash FROM api_keys WHERE id=?", created.Key.ID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if storedDigest != auth.Hash(created.Secret) || storedDigest == created.Secret {
		t.Fatal("database did not store only the API key digest")
	}
	loadIndex := func() *auth.KeyIndex {
		t.Helper()
		state, err := handler.opts.Runtime.Load()
		if err != nil {
			t.Fatal(err)
		}
		return state.APIKeys()
	}

	list := httptest.NewRequest(http.MethodGet, "/admin/v1/keys", nil)
	list.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, list)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), created.Secret) {
		t.Fatalf("key list leaked plaintext or failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	pause := httptest.NewRequest(http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, strings.NewReader(`{"paused":true}`))
	pause.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, pause)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, active := loadIndex().Lookup(created.Secret); active {
		t.Fatal("paused key still authenticates")
	}

	resume := httptest.NewRequest(http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, strings.NewReader(`{"paused":false}`))
	resume.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, resume)
	if recorder.Code != http.StatusOK {
		t.Fatalf("resume status=%d", recorder.Code)
	}
	if _, ok := loadIndex().Lookup(created.Secret); !ok {
		t.Fatal("resumed key does not authenticate")
	}

	missing := httptest.NewRequest(http.MethodPatch, "/admin/v1/keys/does-not-exist", strings.NewReader(`{"paused":true}`))
	missing.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, missing)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown key status=%d", recorder.Code)
	}

	revoke := httptest.NewRequest(http.MethodDelete, "/admin/v1/keys/"+created.Key.ID, nil)
	revoke.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, revoke)
	if recorder.Code != http.StatusOK {
		t.Fatalf("revoke status=%d", recorder.Code)
	}
	if _, ok := loadIndex().Lookup(created.Secret); ok {
		t.Fatal("revoked key still authenticates")
	}
	var remaining int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE id=?", created.Key.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("revoked key still persisted")
	}
}

func TestCreateKeyRejectsBlankName(t *testing.T) {
	_, mux, cookie, store := newKeyAPI(t)
	defer store.Close()
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/keys", strings.NewReader(`{"name":"   "}`))
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("blank name status=%d", recorder.Code)
	}
}

func TestRequireAPIKeySettingRoundTrip(t *testing.T) {
	handler, mux, cookie, store := newKeyAPI(t)
	defer store.Close()
	patch := httptest.NewRequest(http.MethodPatch, "/admin/v1/settings", strings.NewReader(`{"set":{"requireApiKey":"false"}}`))
	patch.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, patch)
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings patch status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if handler.opts.Settings.Settings()["requireApiKey"] != "false" {
		t.Fatalf("requireApiKey=%q", handler.opts.Settings.Settings()["requireApiKey"])
	}
	invalid := httptest.NewRequest(http.MethodPatch, "/admin/v1/settings", strings.NewReader(`{"set":{"requireApiKey":"maybe"}}`))
	invalid.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, invalid)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid boolean status=%d", recorder.Code)
	}
	if handler.opts.Settings.Settings()["requireApiKey"] != "false" {
		t.Fatal("rejected patch changed the live setting")
	}
}
