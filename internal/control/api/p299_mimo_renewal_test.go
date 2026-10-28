package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/usage"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// #299: the pin's quota reads fall back to MiMo Desktop's cookie store for a
// connection without a stored passToken (open-sse/shared/mimoAccount.js:181-185).
// Here the operator turns that on for one connection, with the host's consent
// and their own; it can be turned off at any time, and what is read is sealed
// into the connection and never answered or logged.

func (env *discoveryEnv) renewal(t *testing.T, id, body string) (int, map[string]any, string) {
	t.Helper()
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPut, "/admin/v1/connections/"+id+"/desktop-session-renewal", body)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded, recorder.Body.String()
}

// discoverKeyOnly makes a MiMo connection from auth.json alone, with no session.
func (env *discoveryEnv) discoverKeyOnly(t *testing.T) string {
	t.Helper()
	writeDesktopAuthFile(t, env.desktop.MimoDesktop, desktopAuthFile)
	status, body, raw := env.discover(t, `{"consent":true}`)
	if status != http.StatusOK || body["desktopSessionRenewal"] != false {
		t.Fatalf("discover: %d %s", status, raw)
	}
	id, _ := body["id"].(string)
	return id
}

func (env *discoveryEnv) storedRenewal(t *testing.T) map[string]string {
	t.Helper()
	rows := env.connections(t, "xiaomi-mimo")
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	return rows[0].Secret.ProviderData
}

func listedRenewal(t *testing.T, env *discoveryEnv, id string) map[string]any {
	t.Helper()
	list := doJSON(t, env.mux, env.cookie, http.MethodGet, "/admin/v1/connections?provider=xiaomi-mimo", "")
	if list.Code != http.StatusOK {
		t.Fatalf("connections: %d", list.Code)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("connection %s not listed: %s", id, list.Body.String())
	return nil
}

// Turning renewal on needs both consents and reads nothing; turning it off
// needs neither. A generic edit can neither turn it on nor drop it.
func TestMimoRenewalConsentIsExplicitAndRevocable(t *testing.T) {
	env := newDiscoveryEnv(t, true)
	id := env.discoverKeyOnly(t)
	readsBefore := env.desktop.reads()

	if status, body, _ := env.renewal(t, id, `{"enabled":true}`); status != http.StatusBadRequest || discoveryError(body) != "consent_required" {
		t.Fatalf("no consent: %d %v", status, body)
	}
	if status, _, _ := env.renewal(t, id, `{"consent":true}`); status != http.StatusBadRequest {
		t.Fatalf("no enabled: %d", status)
	}
	if status, _, _ := env.renewal(t, "missing", `{"enabled":true,"consent":true}`); status != http.StatusNotFound {
		t.Fatalf("unknown connection: %d", status)
	}
	if anonymous := doJSON(t, env.mux, nil, http.MethodPut, "/admin/v1/connections/"+id+"/desktop-session-renewal", `{"enabled":true,"consent":true}`); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", anonymous.Code)
	}
	if onboarding.MimoDesktopRenewalEnabled(env.storedRenewal(t)) {
		t.Fatal("renewal turned on without consent")
	}

	status, body, raw := env.renewal(t, id, `{"enabled":true,"consent":true}`)
	if status != http.StatusOK || body["desktopSessionRenewal"] != true {
		t.Fatalf("enable: %d %s", status, raw)
	}
	if !onboarding.MimoDesktopRenewalEnabled(env.storedRenewal(t)) || env.desktop.reads() != readsBefore {
		t.Fatalf("enable: stored = %v reads = %d", env.storedRenewal(t), env.desktop.reads())
	}
	if item := listedRenewal(t, env, id); item["desktopSessionRenewal"] != true {
		t.Fatalf("listed = %v", item)
	}

	// PATCH cannot set or drop the consent or forge a host read.
	patch := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"name":"renamed","secret":{"providerData":{"mimoDesktopRenewal":"","mimoDesktopReadAt":"2000-01-01T00:00:00Z"}}}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", patch.Code, patch.Body.String())
	}
	if data := env.storedRenewal(t); !onboarding.MimoDesktopRenewalEnabled(data) || data[onboarding.MimoDesktopReadAtKey] != "" {
		t.Fatalf("after patch: %v", data)
	}

	// Revoking works even after the host withdrew its consent.
	env.handler.opts.HostCredentialDiscovery = false
	if status, body, _ := env.renewal(t, id, `{"enabled":true,"consent":true}`); status != http.StatusForbidden || discoveryError(body) != "host_discovery_disabled" {
		t.Fatalf("host off enable: %d %v", status, body)
	}
	status, body, raw = env.renewal(t, id, `{"enabled":false}`)
	if status != http.StatusOK || body["desktopSessionRenewal"] != false {
		t.Fatalf("disable: %d %s", status, raw)
	}
	if onboarding.MimoDesktopRenewalEnabled(env.storedRenewal(t)) {
		t.Fatal("still on after revoke")
	}
	patch = doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"secret":{"providerData":{"mimoDesktopRenewal":"on"}}}`)
	if patch.Code != http.StatusOK || onboarding.MimoDesktopRenewalEnabled(env.storedRenewal(t)) {
		t.Fatalf("patch turned renewal on: %d %v", patch.Code, env.storedRenewal(t))
	}
	if env.desktop.reads() != readsBefore {
		t.Fatalf("the consent endpoint read the host: %d", env.desktop.reads())
	}
}

// renewalAccount is the Xiaomi account service for an end-to-end read: a
// handshake works only for the passTokens in valid.
type renewalAccount struct {
	valid map[string]bool
	usage int
}

func (a *renewalAccount) Do(req *http.Request) (*http.Response, error) {
	pass := ""
	if cookie, err := req.Cookie("passToken"); err == nil {
		pass = cookie.Value
	}
	header, status, body := http.Header{}, http.StatusOK, ""
	callback := "https://mimo-server-cn.xiaomimimo.com/api/sts"
	switch req.URL.Host + req.URL.Path {
	case "mimo-server-cn.xiaomimimo.com/api/user/xiaomi/me":
		if a.valid[pass] {
			header.Set("Location", "https://account.xiaomi.com/pass/serviceLogin?callback="+url.QueryEscape(callback))
			status = http.StatusFound
		}
	case "account.xiaomi.com/pass/serviceLogin":
		body = `&&&START&&&{"location":"` + callback + `?ticket=t"}`
		if req.URL.Query().Get("sid") == "passportapi" {
			body = `&&&START&&&{"nonce":"1","location":"https://account.xiaomi.com/pass/confirm?nonce=1"}`
		}
	case "account.xiaomi.com/pass/confirm":
		header.Add("Set-Cookie", "serviceToken=passport")
	case "mimo-server-cn.xiaomimimo.com/api/sts":
		header.Add("Set-Cookie", "serviceToken=session")
	case "mimo-server-cn.xiaomimimo.com/api/user/usage":
		a.usage++
		body = `{"code":0,"data":{"percent":75}}`
	case "aistudio.xiaomimimo.com/open-apis/v1/user/usage":
		status = http.StatusUnauthorized
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// End to end through the real registry and store: a quota read of a connection
// without a session renews it from Desktop and seals it durably; the next read
// uses the sealed session without opening the host; after the operator revokes,
// a failing session no longer reaches the host and the key path answers.
func TestMimoQuotaReadRenewsAndSealsEndToEnd(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	env := newDiscoveryEnv(t, true)
	id := env.discoverKeyOnly(t)
	if status, _, raw := env.renewal(t, id, `{"enabled":true,"consent":true}`); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, raw)
	}
	writeDesktopCookieStore(t, env.desktop.MimoDesktop)
	account := &renewalAccount{valid: map[string]bool{discoveredPass: true}}
	var readers map[string]quota.UsageClient
	// fresh is a new process: no cached service cookie, no renewal history.
	fresh := func() {
		t.Helper()
		var err error
		if readers, err = usage.Readers(account, nil); err != nil {
			t.Fatal(err)
		}
		usage.WithMimoDesktopRenewal(readers, &onboarding.MimoDesktopRenewal{Desktop: env.desktop, Connections: env.handler.opts.CredentialRegistry, Allowed: true})
	}
	fresh()
	read := func() quota.State {
		t.Helper()
		secret, err := env.handler.opts.CredentialRegistry.Secret(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{ConnectionID: id, AccessToken: secret.AccessToken, ProviderData: secret.ProviderData})
		if err != nil {
			t.Fatal(err)
		}
		return state
	}

	sessionReads := env.desktop.sessionReads.Load()
	state := read()
	if len(state.Windows) != 1 || state.Windows[0].Remaining == nil || *state.Windows[0].Remaining != .75 || state.Metadata["desktopSessionReadAt"] == "" {
		t.Fatalf("renewed read: %+v", state)
	}
	if env.desktop.sessionReads.Load() != sessionReads+1 {
		t.Fatalf("session reads = %d", env.desktop.sessionReads.Load())
	}
	data := env.storedRenewal(t)
	if data["mimoPassToken"] != discoveredPass || data["mimoUserId"] != discoveredUser || data[onboarding.MimoDesktopReadAtKey] == "" {
		t.Fatalf("sealed = %v", data)
	}
	// The sealed session serves the next read; the host is not opened again.
	if state := read(); state.Windows[0].Remaining == nil || env.desktop.sessionReads.Load() != sessionReads+1 {
		t.Fatalf("second read: %+v reads = %d", state, env.desktop.sessionReads.Load())
	}
	item := listedRenewal(t, env, id)
	if item["desktopSessionRenewal"] != true || item["desktopSessionReadAt"] != data[onboarding.MimoDesktopReadAtKey] {
		t.Fatalf("listed = %v", item)
	}
	list := doJSON(t, env.mux, env.cookie, http.MethodGet, "/admin/v1/connections", "")
	assertNoSecret(t, "connections read model", list.Body.String(), discoveredKey, discoveredPass, discoveredUser, discoveredCUID)
	encoded, _ := json.Marshal(state)
	assertNoSecret(t, "quota state", string(encoded), discoveredKey, discoveredPass, discoveredUser, discoveredCUID)

	// Revoke, then make the sealed session fail: the host is not read, though
	// Desktop's store is there and no interval holds the read back. (Revoking
	// stops host reads; it does not unseal the session already sealed, whose
	// service cookie the reader keeps for its 30 minutes as the pin does.)
	if status, _, _ := env.renewal(t, id, `{"enabled":false}`); status != http.StatusOK {
		t.Fatal("revoke failed")
	}
	if state := read(); state.Windows[0].Remaining == nil {
		t.Fatalf("cached session after revoke: %+v", state)
	}
	account.valid = map[string]bool{}
	fresh()
	state = read()
	if state.Message == "" || len(state.Windows) != 0 || env.desktop.sessionReads.Load() != sessionReads+1 {
		t.Fatalf("after revoke: %+v reads = %d", state, env.desktop.sessionReads.Load())
	}
	assertNoSecret(t, "log output", logs.String(), discoveredKey, discoveredPass, discoveredUser, discoveredCUID)
}

// hookedDesktop runs during while Desktop's store is being read.
type hookedDesktop struct {
	*countingDesktop
	during func()
}

func (d *hookedDesktop) ReadSession(ctx context.Context) map[string]string {
	if d.during != nil {
		d.during()
	}
	return d.countingDesktop.ReadSession(ctx)
}

// renewalReader is the MiMo quota reader with renewal over env's registry.
func renewalReader(t *testing.T, env *discoveryEnv, desktop onboarding.MimoSessionSource) quota.UsageClient {
	t.Helper()
	readers, err := usage.Readers(&renewalAccount{valid: map[string]bool{discoveredPass: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	usage.WithMimoDesktopRenewal(readers, &onboarding.MimoDesktopRenewal{Desktop: desktop, Connections: env.handler.opts.CredentialRegistry, Allowed: true})
	return readers["xiaomi-mimo"]
}

// renewalEnabledConnection is a key-only MiMo connection with renewal on and
// Desktop's store present, and the credential a quota read captures from it.
func renewalEnabledConnection(t *testing.T) (*discoveryEnv, string, quota.Credential) {
	t.Helper()
	env := newDiscoveryEnv(t, true)
	id := env.discoverKeyOnly(t)
	if status, _, raw := env.renewal(t, id, `{"enabled":true,"consent":true}`); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, raw)
	}
	writeDesktopCookieStore(t, env.desktop.MimoDesktop)
	secret, err := env.handler.opts.CredentialRegistry.Secret(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !onboarding.MimoDesktopRenewalEnabled(secret.ProviderData) {
		t.Fatalf("captured = %v", secret.ProviderData)
	}
	return env, id, quota.Credential{ConnectionID: id, AccessToken: secret.AccessToken, ProviderData: secret.ProviderData}
}

// Hermes #301 finding, end to end: a quota read captured the credential while
// renewal was on; the operator revoked it before the read reached Desktop's
// store. The store is not opened and the key path answers.
func TestMimoRenewalRevokedAfterCaptureNeverOpensTheStore(t *testing.T) {
	env, id, captured := renewalEnabledConnection(t)
	if status, _, raw := env.renewal(t, id, `{"enabled":false}`); status != http.StatusOK {
		t.Fatalf("revoke: %d %s", status, raw)
	}
	reads := env.desktop.sessionReads.Load()
	state, err := renewalReader(t, env, env.desktop).Usage(context.Background(), captured)
	if err != nil {
		t.Fatal(err)
	}
	if env.desktop.sessionReads.Load() != reads {
		t.Fatal("Desktop's store was opened after the operator revoked renewal")
	}
	if state.Message != "Weekly quota requires Xiaomi account session. API key alone is insufficient." || len(state.Windows) != 0 {
		t.Fatalf("state = %+v", state)
	}
	if data := env.storedRenewal(t); data["mimoPassToken"] != "" || onboarding.MimoDesktopRenewalEnabled(data) {
		t.Fatalf("stored = %v", data)
	}
}

// A revocation that arrives while the store is open waits until the read is
// sealed, then holds for every later read. The renewal is a rotation, not a
// replacement: the credential revision stays.
func TestMimoRenewalRevocationIsOrderedAfterAnOpenRead(t *testing.T) {
	env, id, captured := renewalEnabledConnection(t)
	before, err := env.creds.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	revoked := make(chan int, 1)
	desktop := &hookedDesktop{countingDesktop: env.desktop}
	desktop.during = func() {
		desktop.during = nil
		go func() {
			revoked <- doJSON(t, env.mux, env.cookie, http.MethodPut, "/admin/v1/connections/"+id+"/desktop-session-renewal", `{"enabled":false}`).Code
		}()
		select {
		case code := <-revoked:
			t.Errorf("the revocation (%d) completed while the store was open", code)
		case <-time.After(100 * time.Millisecond):
		}
	}
	state, err := renewalReader(t, env, desktop).Usage(context.Background(), captured)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 1 {
		t.Fatalf("the read that began with consent: %+v", state)
	}
	if code := <-revoked; code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	after, err := env.creds.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	data := after.Secret.ProviderData
	if data["mimoPassToken"] != discoveredPass || onboarding.MimoDesktopRenewalEnabled(data) {
		t.Fatalf("stored after revoke = %v", data)
	}
	if after.CredentialRevision != before.CredentialRevision {
		t.Fatalf("revision %d -> %d", before.CredentialRevision, after.CredentialRevision)
	}
}

// A connection deleted while the store is open is not saved again, and nothing
// read is used.
func TestMimoRenewalDoesNotRecreateADeletedConnection(t *testing.T) {
	env, id, captured := renewalEnabledConnection(t)
	desktop := &hookedDesktop{countingDesktop: env.desktop}
	desktop.during = func() {
		desktop.during = nil
		if code := doJSON(t, env.mux, env.cookie, http.MethodDelete, "/admin/v1/connections/"+id, "").Code; code != http.StatusOK {
			t.Errorf("delete: %d", code)
		}
	}
	state, err := renewalReader(t, env, desktop).Usage(context.Background(), captured)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Windows) != 0 {
		t.Fatalf("a session read for a deleted connection was used: %+v", state)
	}
	if rows := env.connections(t, "xiaomi-mimo"); len(rows) != 0 {
		t.Fatalf("the deleted connection was saved again: %+v", rows)
	}
}
