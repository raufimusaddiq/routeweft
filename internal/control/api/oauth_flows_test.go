package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"

	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// flowUpstream answers every sign-in call generically, so a table test can drive
// all providers without a per-provider stub. Endpoints that need a specific shape
// are matched by fragment.
type flowUpstream struct {
	mu   sync.Mutex
	seen []string
}

func (f *flowUpstream) Do(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.seen = append(f.seen, request.URL.String())
	f.mu.Unlock()
	url := request.URL.String()
	body := `{}`
	switch {
	case strings.Contains(url, "client/register"):
		body = `{"clientId":"fixture-client","clientSecret":"fixture-secret"}`
	case strings.Contains(url, "plugin/auth/state"):
		body = `{"code":0,"data":{"state":"fixture-state","authUrl":"https://example.invalid/auth"}}`
	case strings.Contains(url, "plugin/auth/token"):
		// CodeBuddy polls its token endpoint by state; code 0 with a token approves.
		body = `{"code":0,"data":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresIn":3600}}`
	case strings.Contains(url, "deviceToken/poll"):
		// Qoder polls by nonce; an access token means approved.
		body = `{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expireTime":1893456000000,"userId":"fixture-user"}`
	case strings.Contains(url, "device/code"), strings.Contains(url, "device_authorization"),
		strings.Contains(url, "deviceToken"), strings.Contains(url, "device-auth/codes"):
		body = `{"device_code":"fixture-device","user_code":"ABCD-1234","verification_uri":"https://example.invalid/device","expires_in":900,"interval":5,"code":"fixture-device","verificationUrl":"https://example.invalid/device","state":"fixture-state","authUrl":"https://example.invalid/auth"}`
	case strings.Contains(url, "api.cline.bot/api/v1/auth/token"):
		// Cline's gateway answers camelCase under data (cline.js:43-47).
		body = `{"data":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":"2030-01-01T00:00:00Z","userInfo":{"email":"fixture@example.invalid"}}}`
	case strings.Contains(url, "token"), strings.Contains(url, "access_token"):
		body = `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}`
	case strings.Contains(url, "getUserInfo"), strings.Contains(url, "apikey"):
		body = `{"success":true,"data":{"apiKey":"fixture-api-key","email":"fixture@example.invalid","name":"Fixture"}}`
	case strings.Contains(url, "userinfo"), strings.Contains(url, "user"):
		body = `{"email":"fixture@example.invalid","login":"fixture","id":1}`
	case strings.Contains(url, "ListAvailableModels"):
		body = `{"models":[{"modelId":"fixture-model"}]}`
	case strings.Contains(url, "models"), strings.Contains(url, "GetUsableModels"):
		body = `{"data":[{"id":"fixture-model"}]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// flowCase says how to drive one provider's reachable endpoint.
type flowCase struct {
	// meta is the operator input a provider genuinely requires (GitLab's
	// self-hosted application client id is the only one).
	meta map[string]string
	// input is the credential an import flow validates.
	input string
}

// TestEveryProviderFlowIsReachable is the full-flow-set table test. It asserts,
// for every provider the registry lists, that the step function each endpoint can
// call exists and that the endpoint reaches it. Three review rounds shipped
// defects that a per-provider unit test could not see, because nothing exercised
// the whole set at once.
func TestEveryProviderFlowIsReachable(t *testing.T) {
	flows := onboarding.NewRegistry()
	manual := onboarding.ManualFlows()

	// The catalog must carry every provider with a flow, or the endpoint refuses
	// it before the flow runs.
	specs := registry.Builtins()
	catalog := make(map[string]registry.Spec, len(specs))
	for _, spec := range specs {
		catalog[spec.ID] = spec
	}

	ids := flows.Providers()
	sort.Strings(ids)
	if len(ids) == 0 {
		t.Fatal("the flow registry is empty")
	}

	for _, id := range ids {
		id := id
		t.Run(id, func(t *testing.T) {
			flow, ok := flows.Lookup(id)
			if !ok {
				t.Fatalf("registry lists %q but lookup fails", id)
			}
			hidden := onboarding.HiddenProviders()
			if _, ok := catalog[id]; !ok {
				if !hidden[id] {
					t.Fatalf("%q has a flow but is not in the built-in catalog, so the endpoint refuses it", id)
				}
				// A hidden provider is deliberately unreachable: its flow still has
				// to be complete, but the endpoints must refuse it rather than panic.
				_, hiddenMux, hiddenStore, hiddenCookie := flowHandler(t)
				t.Cleanup(func() { hiddenStore.Close() })
				recorder := doJSON(t, hiddenMux, hiddenCookie, http.MethodPost, "/admin/v1/oauth/"+id+"/start", `{"name":"Fixture","identity":"fixture@example.invalid"}`)
				if recorder.Code != http.StatusNotFound {
					t.Fatalf("hidden provider %q start status=%d, want 404", id, recorder.Code)
				}
				return
			}

			// 1. Every step the endpoints can call for this flow type must exist.
			//    A nil step is a panic or a 404 in production, not a build error.
			switch flow.Type {
			case onboarding.FlowDeviceCode:
				requireStep(t, id, "Authorize", flow.Authorize)
				requireStep(t, id, "Poll", flow.Poll)
			case onboarding.FlowImportToken, onboarding.FlowManualToken:
				requireStep(t, id, "Import", flow.Import)
			default:
				requireStep(t, id, "Authorize", flow.Authorize)
				requireStep(t, id, "Exchange", flow.Exchange)
			}

			// 2. An importable provider must be reachable through /import, even
			//    when its browser flow is registered under the same id.
			importable := flow.Import != nil
			if manualFlow, ok := manual[id]; ok && manualFlow.Import != nil {
				importable = true
			}

			// 3. Drive the endpoint(s) the dashboard would call.
			if importable {
				exerciseImport(t, id, importCase(id))
			}
			// /start is exercised for every provider, including import-only ones:
			// an import-only flow must be refused there, and before the nil-step
			// guard it panicked. Skipping this is what hid that.
			exerciseStart(t, id, flow, startCase(id))
		})
	}
}

func requireStep(t *testing.T, provider, name string, step any) {
	t.Helper()
	switch fn := step.(type) {
	case func(context.Context, onboarding.Params) (onboarding.AuthRequest, error):
		if fn == nil {
			t.Fatalf("%s (%s flow) has no Authorize step; the start endpoint would panic", provider, name)
		}
	case func(context.Context, onboarding.Params) (onboarding.Result, error):
		if fn == nil {
			t.Fatalf("%s has no %s step; that endpoint would report unsupported", provider, name)
		}
	case func(context.Context, onboarding.Params) (onboarding.PollResult, error):
		if fn == nil {
			t.Fatalf("%s (%s flow) has no Poll step; the poll endpoint would report unsupported", provider, name)
		}
	default:
		t.Fatalf("%s step %s has unexpected type %T", provider, name, step)
	}
}

// startCase is the operator input a provider genuinely requires to start. It must
// be the same input the dashboard can send; a test that supplies something the UI
// cannot is how the GitLab client id went missing in production.
func startCase(provider string) flowCase {
	switch provider {
	case "gitlab":
		// GitLab Duo is self-hosted, so the operator supplies the application's
		// client id; the reference requires the same.
		return flowCase{meta: map[string]string{"clientId": "fixture-client-id"}}
	}
	return flowCase{}
}

// The pin accepts a Cursor token of 50 or more characters and a UUID-shaped
// machine id (services/cursor.js:105-114); fixtures meet the same bar a real
// import must.
const (
	fixtureCursorToken     = "fixture-cursor-token-0123456789-0123456789-0123456789"
	fixtureCursorMachineID = "0123abcd-0123-4abc-8def-0123456789ab"
)

// importCase is the credential an import flow validates.
func importCase(provider string) flowCase {
	switch provider {
	case "codex":
		return flowCase{input: "fixture-access-token"}
	case "grok-cli":
		return flowCase{input: `{"access_token":"fixture-access","refresh_token":"fixture-refresh"}`}
	case "iflow":
		return flowCase{input: "BXAuth=fixture;"}
	case "kiro":
		return flowCase{input: "fixture-kiro-key"}
	case "xiaomi-mimo":
		return flowCase{input: "sk-fixture"}
	case "cursor":
		return flowCase{input: fixtureCursorToken, meta: map[string]string{"accessToken": fixtureCursorToken, "machineId": fixtureCursorMachineID}}
	}
	return flowCase{input: "fixture-credential"}
}

// exerciseStart drives /start (and /poll for a device flow) and requires the
// endpoint to reach the flow. A documented precondition is a 4xx with a code; an
// unreachable flow is a 404, and a nil step is a 500 or a panic.
func exerciseStart(t *testing.T, provider string, flow onboarding.Flow, tc flowCase) {
	t.Helper()
	h, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })

	body := map[string]any{"name": "Fixture", "identity": "fixture@example.invalid"}
	if len(tc.meta) > 0 {
		body["meta"] = tc.meta
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/v1/oauth/" + provider + "/start"
	if flow.Type == onboarding.FlowDeviceCode {
		path = "/admin/v1/oauth/" + provider + "/device"
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, path, string(encoded))
	if recorder.Code == http.StatusNotFound {
		// An import-only provider has no start step; refusing it is correct. What is
		// not acceptable is reaching a nil step, which panics instead of answering.
		if flow.Startable() {
			t.Fatalf("%s: %s is unreachable although the flow has an Authorize step (404 %s)", provider, path, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "oauth_unsupported") {
			t.Fatalf("%s: %s refused with %s, want oauth_unsupported", provider, path, recorder.Body.String())
		}
		return
	}
	if recorder.Code >= 500 {
		t.Fatalf("%s: %s failed with %d %s", provider, path, recorder.Code, recorder.Body.String())
	}
	if recorder.Code != http.StatusOK {
		// A documented precondition must name itself, never look like a crash.
		if !strings.Contains(recorder.Body.String(), `"code"`) {
			t.Fatalf("%s: %s returned %d without an error code: %s", provider, path, recorder.Code, recorder.Body.String())
		}
		return
	}
	if flow.Type != onboarding.FlowDeviceCode {
		// Drive the callback too: operator input captured at start must reach the
		// exchange, and a flow that reads meta only at Exchange would otherwise
		// look healthy here.
		var started struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
			t.Fatal(err)
		}
		// A callback flow must round-trip a real callback through the endpoint. The
		// decryption tests call flow.Exchange directly, which is why skipping this
		// left zed and xiaomi-mimo dead while the gate was green: the endpoint never
		// handed them the URL at all.
		input := "fixture-code"
		if flow.Completion == onboarding.CompletionCallback {
			input = callbackPayloadFor(t, provider, recorder.Body.Bytes())
		}
		callback := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/callback",
			`{"state":"`+started.State+`","input":`+quoteJSON(input)+`}`)
		// A callback carrying what the provider's own browser sends must succeed. A
		// 4xx here means the endpoint rejected input the operator was told to paste,
		// which is exactly how zed and xiaomi-mimo were dead while this gate passed.
		if callback.Code != http.StatusOK {
			t.Fatalf("%s: callback with a real payload returned %d %s", provider, callback.Code, callback.Body.String())
		}
		return
	}
	var started struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.State == "" {
		t.Fatalf("%s: device start returned no state: %s", provider, recorder.Body.String())
	}
	// The poll must reach the flow too. Pending and connected are both fine; an
	// unsupported or server error is not.
	h.oauth.now = func() time.Time { return time.Now().Add(time.Minute) }
	poll := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/poll", `{"state":"`+started.State+`"}`)
	if poll.Code == http.StatusNotFound || poll.Code >= 500 {
		t.Fatalf("%s: poll failed with %d %s", provider, poll.Code, poll.Body.String())
	}
}

// exerciseImport drives /import and requires it to reach the flow.
func exerciseImport(t *testing.T, provider string, tc flowCase) {
	t.Helper()
	_, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })

	body := map[string]any{"name": "Fixture", "identity": "fixture@example.invalid", "input": tc.input}
	if len(tc.meta) > 0 {
		body["meta"] = tc.meta
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/import", string(encoded))
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("%s: /import is unreachable (404 %s)", provider, recorder.Body.String())
	}
	if recorder.Code >= 500 {
		t.Fatalf("%s: /import failed with %d %s", provider, recorder.Code, recorder.Body.String())
	}
}

// callbackPayloadFor builds the callback URL a provider's own browser would send,
// using the public half the start response published. It proves the endpoint
// forwards a callback to a flow that parses it itself.
func callbackPayloadFor(t *testing.T, provider string, startBody []byte) string {
	t.Helper()
	var started struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(startBody, &started); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(started.AuthorizeURL)
	if err != nil {
		t.Fatalf("%s: authorize url: %v", provider, err)
	}
	switch provider {
	case "zed":
		// Zed returns user_id plus an RSA-encrypted token. The encrypted value is
		// generated against the public key the start URL published.
		publicKey := parsed.Query().Get("native_app_public_key")
		encrypted := encryptWithPublishedKey(t, publicKey, "fixture-zed-token")
		return "http://127.0.0.1:58443/?user_id=fixture-user&access_token=" + url.QueryEscape(encrypted)
	case "xiaomi-mimo":
		publicKey := parsed.Query().Get("pk")
		payload := encryptXiaomiForPublishedKey(t, publicKey, `{"uid":"fixture-uid","sk":"fixture-sk","url":"https://api.xiaomimimo.com/v1"}`)
		return "http://127.0.0.1:58443/?u=" + url.QueryEscape(payload)
	}
	t.Fatalf("no callback payload builder for %q", provider)
	return ""
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// flowHandler builds a handler carrying the whole built-in catalog, a scripted
// upstream, and a public origin, with no injected per-provider behavior.
// bulkHandler is flowHandler with the built-in catalog, because bulk import resolves
// a provider node and the fixture catalog only carries three providers.
func bulkHandler(t *testing.T) (*Handler, *http.ServeMux, io.Closer, *http.Cookie) {
	t.Helper()
	handler, mux, store, cookie := flowHandler(t)
	handler.opts.Providers = registry.Builtins()
	return handler, mux, store, cookie
}

func flowHandler(t *testing.T) (*Handler, *http.ServeMux, io.Closer, *http.Cookie) {
	t.Helper()
	t.Setenv(onboarding.GoogleClientIDEnv, "fixture-client-id")
	t.Setenv(onboarding.GoogleClientSecretEnv, "fixture-client-secret")
	// iflow's OAuth client is operator-supplied too (SPEC 19.1).
	t.Setenv(onboarding.IflowClientIDEnv, "fixture-iflow-client-id")
	t.Setenv(onboarding.IflowClientSecretEnv, "fixture-iflow-client-secret")
	h, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	h.opts.OAuthClient = &flowUpstream{}
	h.opts.PublicOrigin = "https://dashboard.example.test"
	h.opts.Providers = registry.Builtins()
	return h, mux, store, cookie
}

// encryptWithPublishedKey builds the RSA payload Zed's browser would send, using
// the public key from the start URL.
func encryptWithPublishedKey(t *testing.T, publicKeyB64, token string) string {
	t.Helper()
	der, err := base64.RawURLEncoding.DecodeString(publicKeyB64)
	if err != nil {
		t.Fatalf("zed public key: %v", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("zed public key: %v", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatal("zed public key is not RSA")
	}
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key, []byte(token), nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(encrypted)
}

// encryptXiaomiForPublishedKey builds the ECDH + AES-256-GCM payload the Xiaomi
// platform sends, using the public key from the start URL.
func encryptXiaomiForPublishedKey(t *testing.T, publicKeyB64, plaintext string) string {
	t.Helper()
	spki, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		t.Fatalf("xiaomi public key: %v", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		t.Fatalf("xiaomi public key: %v", err)
	}
	recipient, ok := parsed.(interface{ Bytes() []byte })
	if !ok {
		t.Fatal("xiaomi public key has no raw bytes")
	}
	recipientKey, err := ecdh.X25519().NewPublicKey(recipient.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ephemeral.ECDH(recipientKey)
	if err != nil {
		t.Fatal(err)
	}
	derived := sha256.Sum256(shared)
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append([]byte(nil), nonce...)
	payload = append(payload, ephemeral.PublicKey().Bytes()...)
	payload = append(payload, sealed...)
	return base64.StdEncoding.EncodeToString(payload)
}

// TestBrowserTokenFlowCompletesOnCallback proves the path the dashboard actually
// takes for a browser flow that yields a token (kimchi): the operator pastes the
// token and the endpoint completes it on /callback. It must not require an import
// step, which is what sent kimchi to a 404.
func TestBrowserTokenFlowCompletesOnCallback(t *testing.T) {
	exchange := &routeExchange{routes: map[string]string{
		"supported-providers": `{"providers":[]}`,
		"api/v1/me":           `{"id":7,"username":"fixture","email":"fixture@example.invalid","name":"Fixture"}`,
	}}
	_, mux, creds, cookie := newOnboardingAPI(t, exchange)
	started := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kimchi/start", `{"name":"Work","identity":"work@example.test"}`)
	if started.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
	}
	var flow struct {
		State      string `json:"state"`
		Completion string `json:"completion"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	if flow.Completion != "token" {
		t.Fatalf("kimchi completion=%q, want token", flow.Completion)
	}
	// /import must not be the route: kimchi has no import step.
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kimchi/import",
		`{"name":"Work","identity":"work@example.test","input":"fixture-token"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("kimchi import status=%d, want 404 (it has no import step)", recorder.Code)
	}
	done := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kimchi/callback",
		`{"state":"`+flow.State+`","input":"fixture-token"}`)
	if done.Code != http.StatusOK {
		t.Fatalf("kimchi callback status=%d body=%s", done.Code, done.Body.String())
	}
	connections, err := creds.ListConnections(context.Background(), "kimchi")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%+v err=%v", connections, err)
	}
}

// TestCallbackFlowsReceiveTheRawCallback proves the two flows that parse the
// callback URL themselves get it intact through the endpoint, and that a hostile
// paste is still bounded and control-character checked.
func TestCallbackFlowsReceiveTheRawCallback(t *testing.T) {
	for _, provider := range []string{"zed", "xiaomi-mimo"} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			h, mux, store, cookie := flowHandler(t)
			t.Cleanup(func() { store.Close() })
			started := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/start", `{"name":"Work","identity":"work@example.test"}`)
			if started.Code != http.StatusOK {
				t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
			}
			var flow struct {
				State      string `json:"state"`
				Completion string `json:"completion"`
			}
			if err := json.Unmarshal(started.Body.Bytes(), &flow); err != nil {
				t.Fatal(err)
			}
			if flow.Completion != "callback" {
				t.Fatalf("%s completion=%q, want callback", provider, flow.Completion)
			}
			payload := callbackPayloadFor(t, provider, started.Body.Bytes())
			done := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/callback",
				`{"state":"`+flow.State+`","input":`+quoteJSON(payload)+`}`)
			if done.Code != http.StatusOK {
				t.Fatalf("%s callback status=%d body=%s", provider, done.Code, done.Body.String())
			}
			_ = h
		})
	}
	// The bounds parseOAuthInput applied still hold for a raw paste.
	_, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	started := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/zed/start", `{"name":"Work","identity":"work@example.test"}`)
	var flow struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(started.Body.Bytes(), &flow)
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/zed/callback",
		`{"state":"`+flow.State+`","input":"\u0000bad"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("control character accepted: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestFlowWithNoRedirectNeedsNoPublicOrigin is the seam test for the second
// blocker: a flow that never sends a redirect URI must not require a configured
// public origin. Zed returns to a native-app loopback port it already knows, so
// demanding the origin 503'd a sign-in that could have worked. A browser flow that
// does send one must still fail closed on the same handler.
func TestFlowWithNoRedirectNeedsNoPublicOrigin(t *testing.T) {
	h, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	h.opts.PublicOrigin = ""

	started := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/zed/start", `{"name":"Work","identity":"work@example.test"}`)
	if started.Code != http.StatusOK {
		t.Fatalf("zed start without a public origin status=%d body=%s", started.Code, started.Body.String())
	}
	var flow struct {
		State        string `json:"state"`
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	if flow.State == "" || flow.AuthorizeURL == "" {
		t.Fatalf("zed start=%+v", flow)
	}
	// A real callback must still complete: the origin was never part of it.
	payload := callbackPayloadFor(t, "zed", started.Body.Bytes())
	done := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/zed/callback",
		`{"state":"`+flow.State+`","input":`+quoteJSON(payload)+`}`)
	if done.Code != http.StatusOK {
		t.Fatalf("zed callback without a public origin status=%d body=%s", done.Code, done.Body.String())
	}

	// A flow that does send a redirect still refuses to guess. gitlab reads
	// Params.RedirectURI; claude does not (it pins the console callback), so claude
	// is the wrong probe here and asserting it 503s was the regression itself.
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gitlab/start", `{"name":"Work","identity":"work@example.test","meta":{"clientId":"fixture-client"}}`); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("gitlab start without a public origin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestStartNeedsNoPublicOriginUnlessTheFlowConsumesIt is the one table over every
// flow, replacing the per-case seam tests that let claude regress. The rule is
// declared by the flow (RedirectSource), not inferred from its type: a flow that
// hardcodes its redirect (claude, codex, xai), never uses one (device flows, zed)
// or has no browser step (cursor) must start with PublicOrigin empty, and a flow
// that sends Params.RedirectURI must still fail closed without one.
func TestStartNeedsNoPublicOriginUnlessTheFlowConsumesIt(t *testing.T) {
	flows := onboarding.NewRegistry()
	for _, id := range flows.Providers() {
		id := id
		t.Run(id, func(t *testing.T) {
			flow, ok := flows.Lookup(id)
			if !ok {
				t.Fatalf("lookup %q", id)
			}
			h, mux, store, cookie := flowHandler(t)
			t.Cleanup(func() { store.Close() })
			h.opts.PublicOrigin = ""

			path := "/admin/v1/oauth/" + id + "/start"
			if flow.Type == onboarding.FlowDeviceCode {
				path = "/admin/v1/oauth/" + id + "/device"
			}
			body := `{"name":"Fixture","identity":"fixture@example.invalid"}`
			if id == "gitlab" {
				body = `{"name":"Fixture","identity":"fixture@example.invalid","meta":{"clientId":"fixture-client-id"}}`
			}
			recorder := doJSON(t, mux, cookie, http.MethodPost, path, body)

			// A hidden provider has no catalog entry, so the endpoint refuses it before
			// the flow runs; that is not an origin decision.
			if onboarding.HiddenProviders()[id] {
				if recorder.Code != http.StatusNotFound {
					t.Fatalf("hidden provider %q status=%d, want 404", id, recorder.Code)
				}
				return
			}
			// The declaration must match what the flow does. A flow that consumes
			// Params.RedirectURI must fail closed without an origin; one that does not
			// must start.
			if flow.NeedsRedirectURI() {
				if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "oauth_redirect_unconfigured") {
					t.Fatalf("%s sends a redirect URI but started without a public origin: %d %s", id, recorder.Code, recorder.Body.String())
				}
				return
			}
			if recorder.Code == http.StatusServiceUnavailable && strings.Contains(recorder.Body.String(), "oauth_redirect_unconfigured") {
				t.Fatalf("%s does not send a redirect URI yet demanded a public origin: %s", id, recorder.Body.String())
			}
			if !flow.Startable() {
				// Import-only: the start endpoint refuses it, which is not an origin error.
				if recorder.Code != http.StatusNotFound {
					t.Fatalf("%s has no start step but answered %d %s", id, recorder.Code, recorder.Body.String())
				}
				return
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s started without a public origin: %d %s", id, recorder.Code, recorder.Body.String())
			}
			var started struct {
				AuthorizeURL string `json:"authorizeUrl"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
				t.Fatal(err)
			}
			// A pinned redirect must still be the provider's registered one, never
			// the dashboard origin the flow would discard.
			if flow.RedirectSource == onboarding.RedirectPinned {
				if !strings.Contains(started.AuthorizeURL, "redirect_uri=") {
					t.Fatalf("%s pinned flow sent no redirect_uri: %s", id, started.AuthorizeURL)
				}
				if strings.Contains(started.AuthorizeURL, "dashboard.example.test") {
					t.Fatalf("%s pinned flow used the dashboard origin: %s", id, started.AuthorizeURL)
				}
			}
		})
	}
}

// TestBulkImportNormalizesAndPersists checks the three accepted payload shapes and
// that a per-item failure does not sink the batch: the reference reports
// success/failed per entry rather than failing the whole call.
//
// The provider table is derived from the flows that declare a Bulk hook, so a
// provider can never ship a bulk route without this test covering it — codex did
// exactly that: its Bulk lives on the browser flow while its Import lives in the
// manual table, and first-table-wins made the endpoint 404.
func TestBulkImportNormalizesAndPersists(t *testing.T) {
	providers := bulkProviders(t)
	if len(providers) == 0 {
		t.Fatal("no flow declares a Bulk hook; this test would prove nothing")
	}
	handler, mux, store, cookie := bulkHandler(t)
	t.Cleanup(func() { store.Close() })
	for _, provider := range providers {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			body := `[{"access_token":"fixture-access","refresh_token":"fixture-refresh","email":"bulk-` + provider + `@example.invalid"}]`
			if provider == "codex" {
				// The codex route reads camelCase only (bulk-import/route.js:72-99).
				body = `[{"accessToken":"fixture-access","refreshToken":"fixture-refresh","email":"bulk-codex@example.invalid"}]`
			}
			recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/bulk-import", body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s declares a Bulk hook but /bulk-import answered %d %s", provider, recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "fixture-access") {
				t.Fatalf("%s echoed a credential: %s", provider, recorder.Body.String())
			}
			// A stored row is not a usable connection: the runtime registry must
			// resolve the secret, or inference answers credentials_unavailable until
			// some later refresh happens to load it. Bulk import shipped that way.
			var response struct {
				Results []struct {
					ID string `json:"id"`
				} `json:"results"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Results) == 0 || response.Results[0].ID == "" {
				t.Fatalf("%s bulk import returned no connection id: %s", provider, recorder.Body.String())
			}
			secret, err := handler.opts.CredentialRegistry.Secret(context.Background(), response.Results[0].ID)
			if err != nil {
				t.Fatalf("%s bulk-imported connection is not visible to the runtime: %v", provider, err)
			}
			if secret.AccessToken != "fixture-access" {
				t.Fatalf("%s bulk-imported secret resolved as %q", provider, secret.AccessToken)
			}
		})
	}

	// Distinct identities per shape: a re-import of the same identity is a
	// different case (dedup), and this test is about payload normalization.
	shapes := map[string]string{
		"array":   `[{"access_token":"fixture-access","refresh_token":"fixture-refresh","email":"bulk-array@example.invalid"},{"access_token":"fixture-2","email":"bulk-array-2@example.invalid"}]`,
		"single":  `{"access_token":"fixture-access","refresh_token":"fixture-refresh","email":"bulk-single@example.invalid"}`,
		"wrapped": `{"accounts":[{"access_token":"fixture-access","refresh_token":"fixture-refresh","email":"bulk-wrapped@example.invalid"}]}`,
	}
	for name, body := range shapes {
		body := body
		t.Run(name, func(t *testing.T) {
			recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("bulk import %s: %d %s", name, recorder.Code, recorder.Body.String())
			}
			var response struct {
				Total   int `json:"total"`
				Success int `json:"success"`
				Failed  int `json:"failed"`
				Results []struct {
					Index int    `json:"index"`
					OK    bool   `json:"ok"`
					ID    string `json:"id"`
				} `json:"results"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Success != response.Total || response.Failed != 0 {
				t.Fatalf("bulk import %s: success=%d failed=%d total=%d", name, response.Success, response.Failed, response.Total)
			}
			// No credential may be echoed back.
			if strings.Contains(recorder.Body.String(), "fixture-access") || strings.Contains(recorder.Body.String(), "fixture-refresh") {
				t.Fatalf("bulk import %s echoed a credential: %s", name, recorder.Body.String())
			}
		})
	}

	// A provider without a bulk route refuses rather than accepting a batch it
	// would silently drop. Pick a registered provider that declares no Bulk hook
	// instead of naming kiro, which now has one; naming it skipped this assertion
	// unconditionally (#361).
	bulk := make(map[string]bool, len(bulkProviders(t)))
	for _, provider := range bulkProviders(t) {
		bulk[provider] = true
	}
	nonBulk := ""
	for _, provider := range onboarding.NewRegistry().Providers() {
		if !bulk[provider] {
			nonBulk = provider
			break
		}
	}
	if nonBulk == "" {
		t.Skip("every registered provider declares a Bulk hook")
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+nonBulk+"/bulk-import", `[{"access_token":"fixture-access","email":"fixture@example.invalid"}]`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("%s bulk import: expected 404, got %d %s", nonBulk, recorder.Code, recorder.Body.String())
	}
}

// TestBulkImportPartialFailureIsReported keeps one bad entry from hiding the good
// ones, and names the failing index.
func TestBulkImportPartialFailureIsReported(t *testing.T) {
	_, mux, store, cookie := bulkHandler(t)
	t.Cleanup(func() { store.Close() })
	body := `[{"access_token":"fixture-access","email":"ok@example.invalid"},{"refresh_token":"no-access"}]`
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bulk import: %d %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Success int `json:"success"`
		Failed  int `json:"failed"`
		Results []struct {
			Index int    `json:"index"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Success != 1 || response.Failed != 1 {
		t.Fatalf("expected 1 success and 1 failure, got %+v", response)
	}
	if response.Results[1].OK || response.Results[1].Error == "" {
		t.Fatalf("the failing item must report its index and an error: %+v", response.Results[1])
	}
}

// TestImportStoresTheFlowAuthKind covers the case Hermes caught: iflow's cookie
// import yields AuthCookie while the provider declared only AuthOAuth, so /import
// stored a connection the PATCH path then refused. The stored mode must be the one
// the flow produced and the provider permits.
func TestImportStoresTheFlowAuthKind(t *testing.T) {
	_, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	body := `{"name":"Cookie","identity":"cookie@example.invalid","input":"BXAuth=fixture"}`
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/iflow/import", body)
	if recorder.Code >= 500 {
		t.Fatalf("iflow cookie import: %d %s", recorder.Code, recorder.Body.String())
	}
	var saved struct {
		AuthKind string `json:"authKind"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	// The fixture cannot reach iflow, so the exchange may fail upstream; what must
	// never happen is a stored mode the provider refuses. When it does persist, it
	// must be the cookie mode.
	if saved.AuthKind != "" && saved.AuthKind != string(credentials.AuthCookie) {
		t.Fatalf("iflow stored authKind=%q, want cookie", saved.AuthKind)
	}
}

// bulkProviders lists every provider whose flow declares a Bulk hook, read from
// both flow tables so neither half can hide a provider from the test above.
func bulkProviders(t *testing.T) []string {
	t.Helper()
	return onboarding.BulkProviders()
}

// TestStartPassesStateToTheFlow proves the state is minted before Authorize runs,
// so a flow that embeds {p.State} writes the real value rather than an empty one
// the handler then repairs. The URL is not rewritten afterwards, so if the flow had
// not written it the authorize URL would carry an empty or missing state.
func TestStartPassesStateToTheFlow(t *testing.T) {
	_, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	for _, provider := range []string{"claude", "codex", "xai"} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/start", `{"name":"Fixture","identity":"fixture@example.invalid"}`)
			if recorder.Code != http.StatusOK {
				t.Fatalf("start: %d %s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				State        string `json:"state"`
				AuthorizeURL string `json:"authorizeUrl"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.State == "" {
				t.Fatal("start returned no state")
			}
			// The flow wrote this value; the handler no longer patches the URL.
			if !strings.Contains(response.AuthorizeURL, "state="+response.State) {
				t.Fatalf("authorize url does not carry the state the flow was given: %s", response.AuthorizeURL)
			}
		})
	}
}

// TestBulkImportPublishesOnce proves a batch recompiles the runtime snapshot once,
// not once per account. Every RefreshPoolBindings call runs a full candidate
// recompile, so per-item publishing turned one paste into thousands of recompiles
// on the control plane.
func TestBulkImportPublishesOnce(t *testing.T) {
	handler, mux, store, cookie := bulkHandler(t)
	t.Cleanup(func() { store.Close() })
	manager, ok := handler.opts.PoolBindings.(*runtime.Manager)
	if !ok {
		t.Fatalf("PoolBindings is %T, want *runtime.Manager", handler.opts.PoolBindings)
	}
	before := manager.Version()
	body := `[
		{"access_token":"fixture-1","email":"one@example.invalid"},
		{"access_token":"fixture-2","email":"two@example.invalid"},
		{"access_token":"fixture-3","email":"three@example.invalid"}
	]`
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bulk import: %d %s", recorder.Code, recorder.Body.String())
	}
	if after := manager.Version(); after-before != 1 {
		t.Fatalf("3 accounts caused %d snapshot recompiles, want 1", after-before)
	}
}

// TestBulkImportRejectsAnUnboundedBatch bounds the work one request can ask for.
func TestBulkImportRejectsAnUnboundedBatch(t *testing.T) {
	_, mux, store, cookie := bulkHandler(t)
	t.Cleanup(func() { store.Close() })
	items := make([]string, oauthMaxBulkItems+1)
	for i := range items {
		items[i] = `{"access_token":"fixture","email":"bulk-cap-` + strconv.Itoa(i) + `@example.invalid"}`
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", "["+strings.Join(items, ",")+"]")
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for %d items, got %d %s", len(items), recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "bulk_too_large") {
		t.Fatalf("expected bulk_too_large, got %s", recorder.Body.String())
	}
}
