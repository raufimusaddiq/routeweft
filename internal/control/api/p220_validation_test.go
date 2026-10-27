package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// #220: a connection's validation lifecycle is persisted apart from its sealed
// credential. A credential typed into the form is untested, a check records
// active or failed with the model count and a redacted error, an edit that
// replaces the credential (a relink) starts it over, a metadata edit keeps it,
// a failed save leaves the stored credential and its status alone, and an
// onboarded credential is active with its origin. No credential value reaches a
// response, the read model, a stored error or an event.

const (
	relinkKeyOne   = "sk-fixture-relink-first-0001"
	relinkKeyTwo   = "sk-fixture-relink-second-0002"
	relinkKeyThree = "sk-fixture-relink-third-0003"
)

type connectionView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	TestStatus      string `json:"testStatus"`
	LastTestedAt    string `json:"lastTestedAt"`
	LastError       string `json:"lastError"`
	LastErrorAt     string `json:"lastErrorAt"`
	ModelsAvailable *int   `json:"modelsAvailable"`
	AuthOrigin      string `json:"authOrigin"`
	ExpiresAt       string `json:"expiresAt"`
	Plan            string `json:"plan"`
	QuotaObservedAt string `json:"quotaObservedAt"`
}

func listedConnection(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, id string, secrets ...string) connectionView {
	t.Helper()
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/connections?size=100", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("list: %d %s", recorder.Code, recorder.Body.String())
	}
	assertNoSecret(t, "connection read model", recorder.Body.String(), secrets...)
	var page struct {
		Items []connectionView `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("connection %s not listed: %s", id, recorder.Body.String())
	return connectionView{}
}

func TestConnectionValidationLifecycle(t *testing.T) {
	var revoked atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if revoked.Load() {
			// A revoked key, echoed back the way some providers do.
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+`"}}`)
			return
		}
		if r.URL.Path == "/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"relink-a"},{"id":"relink-b"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	h, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	bus := controlevents.New()
	h.opts.Events = bus
	events, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	plan := quota.State{Plan: "pro", ObservedAt: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)}
	h.opts.QuotaState = func(string) (quota.State, bool) { return plan, true }
	secrets := []string{relinkKeyOne, relinkKeyTwo, relinkKeyThree}

	node, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeGeneric, ProviderID: "relink", Prefix: "relink", Name: "relink", BaseURL: upstream.URL, Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"nodeId":"`+node.ID+`","name":"Relink","identity":"relink@example.invalid","authKind":"api-key","secret":{"accessToken":"`+relinkKeyOne+`"}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	assertNoSecret(t, "create response", created.Body.String(), secrets...)
	var saved struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &saved)
	id := saved.ID

	// A typed credential no provider has seen yet.
	view := listedConnection(t, mux, cookie, id, secrets...)
	if view.TestStatus != credentials.StatusUntested || view.AuthOrigin != credentials.OriginManual || view.LastTestedAt != "" || view.ModelsAvailable != nil {
		t.Fatalf("created: %+v", view)
	}
	// Quota observed is reported apart from the credential's status.
	if view.Plan != "pro" || view.QuotaObservedAt != "2026-10-27T09:00:00Z" {
		t.Fatalf("quota fields: %+v", view)
	}

	test := func(want int) string {
		t.Helper()
		recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+id+"/test", `{"modelId":"relink-a"}`)
		if recorder.Code != want {
			t.Fatalf("test: %d %s", recorder.Code, recorder.Body.String())
		}
		assertNoSecret(t, "test response", recorder.Body.String(), secrets...)
		return recorder.Body.String()
	}
	test(http.StatusOK)
	view = listedConnection(t, mux, cookie, id, secrets...)
	if view.TestStatus != credentials.StatusActive || view.LastTestedAt == "" || view.ModelsAvailable == nil || *view.ModelsAvailable != 2 || view.LastError != "" {
		t.Fatalf("passed: %+v", view)
	}

	// A metadata edit keeps the result: the credential is unchanged.
	if recorder := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"name":"Relinked"}`); recorder.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", recorder.Code, recorder.Body.String())
	}
	if view = listedConnection(t, mux, cookie, id, secrets...); view.Name != "Relinked" || view.TestStatus != credentials.StatusActive || view.LastTestedAt == "" {
		t.Fatalf("after rename: %+v", view)
	}

	// The provider revokes the key: the check fails and says so, without the key.
	revoked.Store(true)
	test(http.StatusBadGateway)
	view = listedConnection(t, mux, cookie, id, secrets...)
	if view.TestStatus != credentials.StatusFailed || view.LastError == "" || view.LastErrorAt == "" {
		t.Fatalf("failed: %+v", view)
	}

	// A relink replaces the credential: every earlier result is cleared.
	revoked.Store(false)
	if recorder := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"secret":{"accessToken":"`+relinkKeyTwo+`"}}`); recorder.Code != http.StatusOK {
		t.Fatalf("relink: %d %s", recorder.Code, recorder.Body.String())
	} else {
		assertNoSecret(t, "relink response", recorder.Body.String(), secrets...)
	}
	view = listedConnection(t, mux, cookie, id, secrets...)
	if view.TestStatus != credentials.StatusUntested || view.LastTestedAt != "" || view.LastError != "" || view.ModelsAvailable != nil || view.Name != "Relinked" {
		t.Fatalf("relinked: %+v", view)
	}
	if got := connectionByID(t, creds, id); got.Secret.AccessToken != relinkKeyTwo {
		t.Fatal("relink did not replace the credential")
	}

	// Saves that fail leave the stored credential and its status as they were.
	test(http.StatusOK)
	for _, body := range []string{
		`{"accountId":"bad/segment","secret":{"accessToken":"` + relinkKeyThree + `"}}`,
		`{"proxyPoolId":"missing-pool","secret":{"accessToken":"` + relinkKeyThree + `"}}`,
	} {
		recorder := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+id, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("failing edit %s: %d %s", body, recorder.Code, recorder.Body.String())
		}
		assertNoSecret(t, "failed edit response", recorder.Body.String(), secrets...)
		if got := connectionByID(t, creds, id); got.Secret.AccessToken != relinkKeyTwo {
			t.Fatalf("failed edit %s replaced the credential", body)
		}
		if view = listedConnection(t, mux, cookie, id, secrets...); view.TestStatus != credentials.StatusActive {
			t.Fatalf("failed edit %s changed status: %+v", body, view)
		}
	}
	// The runtime still serves with the kept credential.
	if credential, err := h.opts.CredentialRegistry.Credential(context.Background(), id); err != nil || credential != relinkKeyTwo {
		t.Fatalf("runtime credential after failed edit: err=%v kept=%v", err, credential == relinkKeyTwo)
	}

	// A check that did not run on the credential records nothing.
	connection := connectionByID(t, creds, id)
	h.recordValidation(context.Background(), connection, &validationResult{Classification: "canceled", Error: "canceled"})
	disabled := connection
	disabled.Enabled = false
	h.recordValidation(context.Background(), disabled, &validationResult{Error: "enable the connection before validation"})
	if view = listedConnection(t, mux, cookie, id, secrets...); view.TestStatus != credentials.StatusActive || view.LastError != "" {
		t.Fatalf("canceled or disabled check recorded: %+v", view)
	}

	// Delete removes the row and its status.
	if recorder := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/connections/"+id, ""); recorder.Code != http.StatusOK {
		t.Fatalf("delete: %d", recorder.Code)
	}
	if _, err := creds.GetConnectionStatus(context.Background(), id); err != credentials.ErrNotFound {
		t.Fatalf("deleted status: %v", err)
	}

	unsubscribe()
	for event := range events {
		raw, _ := json.Marshal(event)
		assertNoSecret(t, "event "+event.Type, string(raw), secrets...)
	}
}

// An upstream that echoes the key in its error never gets it into the stored
// error or the answer, including a key the registry rotated since the read.
func TestRecordValidationRedactsEchoedCredential(t *testing.T) {
	h, _, store, creds, _ := newProvidersAPI(t, true, nil)
	defer store.Close()
	c := workflowAccount(t, creds, "echo", "https://upstream.invalid", "echo-identity")
	c.Secret = credentials.Secret{AccessToken: relinkKeyOne, RefreshToken: "rt-fixture-relink-0001", ProviderData: map[string]string{"region": "us", "copilotToken": "copilot-fixture-relink"}}
	result := validationResult{Error: "denied " + relinkKeyOne + " refresh rt-fixture-relink-0001 copilot copilot-fixture-relink region us"}
	h.recordValidation(context.Background(), c, &result)
	if want := "denied [redacted] refresh [redacted] copilot [redacted] region us"; result.Error != want {
		t.Fatalf("redacted=%q, want %q", result.Error, want)
	}
	// No credential is too short to redact; metadata is not a credential.
	short := c
	short.Secret = credentials.Secret{AccessToken: "k7", Cookie: "c=1x", ProviderData: map[string]string{"apiKey": "ak", "mimoPassToken": "pt", "projectId": "proj-9"}}
	shortResult := validationResult{Error: "bad key k7 cookie c=1x api ak pass pt project proj-9"}
	h.recordValidation(context.Background(), short, &shortResult)
	if want := "bad key [redacted] cookie [redacted] api [redacted] pass [redacted] project proj-9"; shortResult.Error != want {
		t.Fatalf("short redacted=%q, want %q", shortResult.Error, want)
	}
	status, err := creds.GetConnectionStatus(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TestStatus != credentials.StatusFailed || status.LastError != shortResult.Error {
		t.Fatalf("stored: %+v", status)
	}
}

// Onboarding records the credential as accepted, with its origin, on the add,
// the duplicate update and every bulk item.
func TestOnboardedCredentialIsActiveWithItsOrigin(t *testing.T) {
	env := newImportEnv(t, map[string]string{})
	status, body := env.importAs(t, "grok-cli", "grok@example.invalid", `{"access_token":"tok-g1","refresh_token":"tok-r1","email":"grok@example.invalid"}`, nil)
	if status != http.StatusOK || body["testStatus"] != credentials.StatusActive || body["authOrigin"] != credentials.OriginImport {
		t.Fatalf("import: %d %v", status, body)
	}
	id := body["id"].(string)
	if err := env.creds.RecordValidation(context.Background(), id, connectionByID(t, env.creds, id).CredentialRevision, credentials.Validation{Error: "revoked"}); err != nil {
		t.Fatal(err)
	}
	status, body = env.importAs(t, "grok-cli", "grok@example.invalid", `{"access_token":"tok-g2","email":"grok@example.invalid"}`, nil)
	if status != http.StatusOK || body["outcome"] != onboardedUpdated || body["testStatus"] != credentials.StatusActive {
		t.Fatalf("reimport: %d %v", status, body)
	}
	view := listedConnection(t, env.mux, env.cookie, id, "tok-")
	if view.TestStatus != credentials.StatusActive || view.LastError != "" || view.AuthOrigin != credentials.OriginImport {
		t.Fatalf("reimported: %+v", view)
	}
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", `[{"access_token":"tok-b1","email":"bulk@example.invalid"}]`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", recorder.Code, recorder.Body.String())
	}
	var bulk struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &bulk)
	if len(bulk.Results) != 1 {
		t.Fatalf("bulk: %s", recorder.Body.String())
	}
	if view := listedConnection(t, env.mux, env.cookie, bulk.Results[0].ID, "tok-"); view.TestStatus != credentials.StatusActive || view.AuthOrigin != credentials.OriginImport {
		t.Fatalf("bulk item: %+v", view)
	}
}

// Xiaomi's soft validation stores a key whose model listing failed: a new
// connection is untested, and a stored duplicate keeps the status it had
// (xiaomi-mimo/api-key/route.js:79,115). A listing that succeeds is accepted.
func TestXiaomiSoftValidationKeepsTheKeyUntested(t *testing.T) {
	env := newImportEnv(t, map[string]string{"/models": `{"data":[{"id":"mimo"}]}`})
	xiaomi := func(key string) string { return `{"apiKey":"` + key + `","uid":"7"}` }
	env.exchange.status = http.StatusUnauthorized
	status, body := env.importAs(t, "xiaomi-mimo", "7@xiaomi", xiaomi("sk-fixture-soft-1"), nil)
	if status != http.StatusOK || body["outcome"] != onboardedAdded || body["testStatus"] != credentials.StatusUntested || body["authOrigin"] != credentials.OriginImport {
		t.Fatalf("unvalidated add: %d %v", status, body)
	}
	id := body["id"].(string)
	env.exchange.status = 0
	if status, body = env.importAs(t, "xiaomi-mimo", "7@xiaomi", xiaomi("sk-fixture-soft-2"), nil); status != http.StatusOK || body["outcome"] != onboardedUpdated || body["testStatus"] != credentials.StatusActive {
		t.Fatalf("validated update: %d %v", status, body)
	}
	if err := env.creds.RecordValidation(context.Background(), id, connectionByID(t, env.creds, id).CredentialRevision, credentials.Validation{Error: "provider rejected credentials"}); err != nil {
		t.Fatal(err)
	}
	env.exchange.status = http.StatusUnauthorized
	if status, body = env.importAs(t, "xiaomi-mimo", "7@xiaomi", xiaomi("sk-fixture-soft-3"), nil); status != http.StatusOK || body["outcome"] != onboardedUpdated || body["testStatus"] != credentials.StatusFailed {
		t.Fatalf("unvalidated update: %d %v", status, body)
	}
	view := listedConnection(t, env.mux, env.cookie, id, "sk-fixture-soft")
	if view.TestStatus != credentials.StatusFailed || view.LastError != "provider rejected credentials" {
		t.Fatalf("kept status: %+v", view)
	}
}

// A check that started before a relink and finishes after it never marks the
// replacement credential active or failed from the old credential's result.
func TestCheckStartedBeforeRelinkDoesNotRecordOverIt(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var rejectOld atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"relink-a"}]}`)
			return
		}
		started <- struct{}{}
		<-release
		if rejectOld.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid key"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	_, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	c := workflowAccount(t, creds, "race", upstream.URL, "race@example.invalid")
	if err := creds.RotateSecret(context.Background(), c.ID, credentials.Secret{AccessToken: relinkKeyOne}); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []struct {
		name   string
		reject bool
		code   int
	}{{"pass", false, http.StatusOK}, {"fail", true, http.StatusBadGateway}} {
		rejectOld.Store(outcome.reject)
		done := make(chan int, 1)
		go func() {
			done <- doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+c.ID+"/test", `{"modelId":"relink-a"}`).Code
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("check did not reach the upstream")
		}
		relink := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+c.ID, `{"secret":{"accessToken":"`+relinkKeyTwo+outcome.name+`"}}`)
		if relink.Code != http.StatusOK {
			t.Fatalf("%s relink: %d %s", outcome.name, relink.Code, relink.Body.String())
		}
		release <- struct{}{}
		if code := <-done; code != outcome.code {
			t.Fatalf("%s check: %d", outcome.name, code)
		}
		view := listedConnection(t, mux, cookie, c.ID, relinkKeyOne, relinkKeyTwo)
		if view.TestStatus != credentials.StatusUntested || view.LastTestedAt != "" || view.LastError != "" || view.ModelsAvailable != nil {
			t.Fatalf("%s: old check recorded over the relinked credential: %+v", outcome.name, view)
		}
	}
}
