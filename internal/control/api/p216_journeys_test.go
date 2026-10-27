package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// #216: the browser journeys beyond Codex. Each provider keeps its own way of
// finishing (a code, a code#state, a redirect URL, a token URL, an encrypted
// callback), and the read model and results say what the dashboard must show:
// where the browser returns, whether the account is learned, and for Google
// whether a Code Assist project was found.

// journeyUpstream answers provider calls from a script and records each URL.
type journeyUpstream struct {
	mu     sync.Mutex
	seen   []string
	answer func(*http.Request) (int, string)
}

func (u *journeyUpstream) Do(request *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.seen = append(u.seen, request.URL.String())
	u.mu.Unlock()
	status, body := http.StatusOK, `{}`
	if u.answer != nil {
		status, body = u.answer(request)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (u *journeyUpstream) count(suffix string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, seen := range u.seen {
		if strings.HasSuffix(seen, suffix) {
			n++
		}
	}
	return n
}

func journeyStart(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, provider, body string) startedFlow {
	t.Helper()
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/start", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s start: %d %s", provider, recorder.Code, recorder.Body.String())
	}
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	return flow
}

func journeyBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return decoded
}

// A provider that usually names the account starts without one (the reference
// asks for none); one that names none still asks. When the provider then reports
// no account, nothing is saved and the dashboard is told to ask.
func TestInferredIdentityIsOptionalUpFront(t *testing.T) {
	h, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	idToken := func(claims string) string {
		return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
	}
	email := `{"email":"grok@example.test"}`
	upstream := &journeyUpstream{answer: func(request *http.Request) (int, string) {
		if strings.Contains(request.URL.Path, "token") {
			return http.StatusOK, `{"access_token":"access-xai","refresh_token":"refresh-xai","expires_in":3600,"id_token":"` + idToken(email) + `"}`
		}
		return http.StatusNotFound, `{}`
	}}
	h.opts.OAuthClient = upstream
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/claude/start", `{}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("claude without identity: %d %s", recorder.Code, recorder.Body.String())
	}
	flow := journeyStart(t, mux, cookie, "xai", `{}`)
	recorder := callback(t, mux, cookie, "xai", flow.State, "http://127.0.0.1:56121/callback?code=xai-code&state="+url.QueryEscape(flow.State))
	if recorder.Code != http.StatusOK {
		t.Fatalf("xai callback: %d %s", recorder.Code, recorder.Body.String())
	}
	saved := journeyBody(t, recorder.Body.Bytes())
	assertNoTokens(t, recorder.Body.String())
	if saved["identity"] != "grok@example.test" || saved["outcome"] != onboardedAdded {
		t.Fatalf("xai saved %v", saved)
	}
	// The copied code xAI shows instead of redirecting is accepted bare.
	email = `{"sub":"no-email"}`
	flow = journeyStart(t, mux, cookie, "xai", `{}`)
	recorder = callback(t, mux, cookie, "xai", flow.State, "xai-copied-code")
	if recorder.Code != http.StatusBadGateway || apiErrorCode(journeyBody(t, recorder.Body.Bytes())) != "oauth_identity_unresolved" {
		t.Fatalf("xai without an email: %d %s", recorder.Code, recorder.Body.String())
	}
	// The operator's identity, sent on the next attempt, is the fallback.
	flow = journeyStart(t, mux, cookie, "xai", `{"identity":"named@example.test"}`)
	recorder = callback(t, mux, cookie, "xai", flow.State, "xai-copied-code")
	if recorder.Code != http.StatusOK || journeyBody(t, recorder.Body.Bytes())["identity"] != "named@example.test" {
		t.Fatalf("xai with the operator's identity: %d %s", recorder.Code, recorder.Body.String())
	}
}

// Kimchi returns its token in the callback's token parameter; the reference reads
// it wherever code is absent (OAuthModal.js:713-733). A pasted URL from another
// attempt is refused without spending this one.
func TestKimchiCallbackURLCarriesTheToken(t *testing.T) {
	h, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	var mu sync.Mutex
	var bearers []string
	h.opts.OAuthClient = &journeyUpstream{answer: func(request *http.Request) (int, string) {
		mu.Lock()
		bearers = append(bearers, request.Header.Get("Authorization"))
		mu.Unlock()
		if strings.HasSuffix(request.URL.Path, "/me") {
			return http.StatusOK, `{"id":42,"username":"kim","email":"kim@example.test","name":"Kim"}`
		}
		return http.StatusOK, `{"providers":[]}`
	}}
	flow := journeyStart(t, mux, cookie, "kimchi", `{}`)
	if flow.Completion != "token" {
		t.Fatalf("kimchi completion %q", flow.Completion)
	}
	other := "https://dashboard.example.test/callback?token=kimchi-token-sentinel&state=another-attempt"
	recorder := callback(t, mux, cookie, "kimchi", flow.State, other)
	if recorder.Code != http.StatusBadRequest || apiErrorCode(journeyBody(t, recorder.Body.Bytes())) != "oauth_state_mismatch" {
		t.Fatalf("other attempt: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = callback(t, mux, cookie, "kimchi", flow.State, "https://dashboard.example.test/callback?token=kimchi-token-sentinel&state="+url.QueryEscape(flow.State))
	if recorder.Code != http.StatusOK {
		t.Fatalf("kimchi callback: %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "kimchi-token-sentinel") {
		t.Fatal("the token was echoed")
	}
	saved := journeyBody(t, recorder.Body.Bytes())
	if saved["identity"] != "kim@example.test" || saved["name"] != "Kim" {
		t.Fatalf("kimchi saved %v", saved)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bearers) == 0 || bearers[0] != "Bearer kimchi-token-sentinel" {
		t.Fatal("the token was not validated against the gateway before storage")
	}
}

// A Google sign-in's result names the Code Assist project it found, or says none
// was found, because requests refuse a connection without one; Antigravity then
// finishes the account's onboarding in the background, as the reference does.
func TestGoogleSignInReportsItsProject(t *testing.T) {
	h, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	project := `{"cloudaicompanionProject":{"id":"fixture-project"},"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`
	upstream := &journeyUpstream{answer: func(request *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/token"):
			return http.StatusOK, `{"access_token":"access-google","refresh_token":"refresh-google","expires_in":3600}`
		case strings.HasSuffix(request.URL.Path, ":loadCodeAssist"):
			return http.StatusOK, project
		case strings.HasSuffix(request.URL.Path, ":onboardUser"):
			return http.StatusOK, `{"done":true}`
		}
		return http.StatusOK, `{"email":"g@example.test"}`
	}}
	h.opts.OAuthClient = upstream
	for _, tc := range []struct {
		provider, load, project string
		missing                 bool
	}{
		{"antigravity", project, "fixture-project", false},
		{"gemini-cli", `{}`, "", true},
	} {
		project = tc.load
		flow := journeyStart(t, mux, cookie, tc.provider, `{}`)
		recorder := callback(t, mux, cookie, tc.provider, flow.State, "https://dashboard.example.test/callback?code=google-code&state="+url.QueryEscape(flow.State))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s callback: %d %s", tc.provider, recorder.Code, recorder.Body.String())
		}
		assertNoTokens(t, recorder.Body.String())
		saved := journeyBody(t, recorder.Body.Bytes())
		if saved["project"] != tc.project || saved["projectMissing"] != tc.missing || saved["identity"] != "g@example.test" {
			t.Fatalf("%s saved %v", tc.provider, saved)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for upstream.count(":onboardUser") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := upstream.count(":onboardUser"); n != 1 {
		t.Fatalf("onboardUser called %d times, want 1 (antigravity with a project only)", n)
	}
	// A connection saved without a project takes one the operator names, through
	// the connection edit, which keeps its credential.
	connections := listConnectionIDs(t, mux, cookie, "gemini-cli")
	recorder := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+connections[0], `{"secret":{"providerData":{"projectId":"named-project"}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("set project: %d %s", recorder.Code, recorder.Body.String())
	}
	stored := connectionByID(t, h.opts.Credentials, connections[0])
	if stored.Secret.ProviderData["projectId"] != "named-project" || stored.Secret.AccessToken != "access-google" || stored.Secret.RefreshToken != "refresh-google" {
		t.Fatalf("project edit changed the credential: %+v", stored.Secret.ProviderData)
	}
}

func listConnectionIDs(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, provider string) []string {
	t.Helper()
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/connections?providerId="+provider, "")
	var list struct {
		Items []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range list.Items {
		if item.ProviderID == provider {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		t.Fatalf("no %s connection listed: %s", provider, recorder.Body.String())
	}
	return ids
}

// The read model tells each browser method's screen what it must show: the
// loopback a provider registered (xAI's 127.0.0.1:56121), or the address on this
// server's public origin the browser returns to, which a GitLab application must
// register; an unset origin is said rather than guessed.
func TestBrowserMethodsDescribeWhereTheBrowserReturns(t *testing.T) {
	h, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	read := func() map[string]signInMethodReadModel {
		recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/providers?pageSize=200", "")
		var list struct {
			Items []struct {
				ID            string                  `json:"id"`
				SignInMethods []signInMethodReadModel `json:"signInMethods"`
			} `json:"items"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		browser := map[string]signInMethodReadModel{}
		for _, item := range list.Items {
			for _, method := range item.SignInMethods {
				if method.Mode == "browser" {
					browser[item.ID] = method
				}
			}
		}
		return browser
	}
	methods := read()
	want := map[string]struct {
		completion, loopback, redirect string
		infers                         bool
	}{
		"claude":      {"code", "", "", false},
		"xai":         {"redirect-url", "http://127.0.0.1:56121/callback", "", true},
		"gemini-cli":  {"code", "", "https://dashboard.example.test/callback", true},
		"antigravity": {"code", "", "https://dashboard.example.test/callback", true},
		"gitlab":      {"code", "", "https://dashboard.example.test/callback", false},
		"cline":       {"code", "", "https://dashboard.example.test/callback", true},
		"clinepass":   {"code", "", "https://dashboard.example.test/callback", true},
		"iflow":       {"code", "", "https://dashboard.example.test/callback", false},
		"kimchi":      {"token", "", "https://dashboard.example.test/callback", true},
		"xiaomi-mimo": {"callback", "", "https://dashboard.example.test/", true},
		"zed":         {"callback", "", "", true},
	}
	for provider, expected := range want {
		method, ok := methods[provider]
		if !ok {
			t.Fatalf("%s has no browser method", provider)
		}
		if method.Completion != expected.completion || method.LoopbackRedirect != expected.loopback || method.RedirectURI != expected.redirect || method.InfersIdentity != expected.infers || method.RedirectUnconfigured {
			t.Errorf("%s browser method %+v, want %+v", provider, method, expected)
		}
	}
	if gitlab := methods["gitlab"]; !gitlab.ResolvesIdentity || strings.Join(gitlab.MetaKeys, ",") != "clientId,clientSecret,baseUrl" {
		t.Errorf("gitlab browser method %+v", gitlab)
	}
	h.opts.PublicOrigin = ""
	methods = read()
	if gitlab := methods["gitlab"]; !gitlab.RedirectUnconfigured || gitlab.RedirectURI != "" {
		t.Errorf("gitlab without a public origin %+v", gitlab)
	}
	if xai := methods["xai"]; xai.RedirectUnconfigured {
		t.Errorf("xai needs no public origin %+v", xai)
	}
}

// GitLab's browser sign-in needs its OAuth application's client id; a start
// without one is refused with a code that says so, before the browser leaves.
// The client secret stays optional (a public application sends none), and the
// personal access token import needs neither.
func TestGitLabStartNamesTheMissingClientID(t *testing.T) {
	_, mux, store, cookie := flowHandler(t)
	t.Cleanup(func() { store.Close() })
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gitlab/start", `{"meta":{"baseUrl":"https://gitlab.example.test"}}`)
	if recorder.Code != http.StatusBadRequest || apiErrorCode(journeyBody(t, recorder.Body.Bytes())) != "oauth_client_id_required" {
		t.Fatalf("gitlab start without a client id: %d %s", recorder.Code, recorder.Body.String())
	}
	flow := journeyStart(t, mux, cookie, "gitlab", `{"meta":{"clientId":"public-app","baseUrl":"https://gitlab.example.test"}}`)
	authorize, err := url.Parse(flow.AuthorizeURL)
	if err != nil || authorize.Host != "gitlab.example.test" || authorize.Query().Get("client_id") != "public-app" || authorize.Query().Get("code_challenge_method") != "S256" || authorize.Query().Get("redirect_uri") != "https://dashboard.example.test/callback" {
		t.Fatalf("gitlab authorize URL %s", flow.AuthorizeURL)
	}
}
