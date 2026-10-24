package wire

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Sanitized fixtures of executors/mimo-free.js. The JWTs are unsigned and name
// nobody; the server is the test's own.

const mimoChatPath = "/api/free-ai/openai/chat"

type mimoCall struct {
	Path   string
	Header http.Header
	Body   string
}

type mimoUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	calls []mimoCall

	bootstrap, chat http.HandlerFunc
}

func jwtWithExp(exp int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"fixture","exp":%d}`, exp)))
	return "eyJhbGciOiJub25lIn0." + payload + ".sig"
}

func newMimoUpstream(t *testing.T) *mimoUpstream {
	t.Helper()
	mimoReset()
	t.Cleanup(mimoReset)
	u := &mimoUpstream{}
	issued := 0
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.calls = append(u.calls, mimoCall{r.URL.Path, r.Header.Clone(), string(raw)})
		issued++
		n := issued
		u.mu.Unlock()
		switch r.URL.Path {
		case mimoBootstrapPath:
			if u.bootstrap != nil {
				u.bootstrap(w, r)
				return
			}
			fmt.Fprintf(w, `{"jwt":%q}`, fmt.Sprintf("%s-%d", jwtWithExp(time.Now().Add(time.Hour).Unix()), n))
		case mimoChatPath:
			if u.chat != nil {
				u.chat(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *mimoUpstream) seen(path string) []mimoCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []mimoCall
	for _, call := range u.calls {
		if call.Path == path {
			out = append(out, call)
		}
	}
	return out
}

func (u *mimoUpstream) do(t *testing.T, id, body string) (*http.Response, string, error) {
	t.Helper()
	provider := routing.ProviderRef{ProviderID: id, BaseURL: u.URL, Protocol: chat}
	request, err := http.NewRequest(http.MethodPost, u.URL+mimoChatPath, strings.NewReader(FinalizeBodyString(provider, body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer must-not-survive")
	response, err := MimoFreeDo(provider, u.Client(), request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	text, _ := io.ReadAll(response.Body)
	return response, string(text), nil
}

// FinalizeBodyString runs the serving finalizer on a body, as the dispatcher does.
func FinalizeBodyString(provider routing.ProviderRef, body string) string {
	return string(FinalizeBody(provider, []byte(body)))
}

const mimoBody = `{"model":"mimo-auto","stream":true,"messages":[{"role":"user","content":"ping"}]}`

func TestMimoFreeBootstrapsAJWTAndSendsTheGatewaysHeaders(t *testing.T) {
	for _, id := range []string{"mimo-free", "mmf"} {
		t.Run(id, func(t *testing.T) {
			u := newMimoUpstream(t)
			response, text, err := u.do(t, id, mimoBody)
			if err != nil || response.StatusCode != 200 || !strings.Contains(text, "pong") {
				t.Fatalf("%v %v %s", err, response, text)
			}
			boot := u.seen(mimoBootstrapPath)
			if len(boot) != 1 {
				t.Fatalf("bootstraps %d", len(boot))
			}
			var client struct{ Client string }
			if err := json.Unmarshal([]byte(boot[0].Body), &client); err != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(client.Client) {
				t.Errorf("bootstrap body %q", boot[0].Body)
			}
			if boot[0].Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(boot[0].Header.Get("User-Agent"), "Mozilla/5.0") {
				t.Errorf("bootstrap headers %v", boot[0].Header)
			}

			sent := u.seen(mimoChatPath)
			if len(sent) != 1 {
				t.Fatalf("chat calls %d", len(sent))
			}
			h := sent[0].Header
			if got := h.Get("Authorization"); !strings.HasPrefix(got, "Bearer eyJ") || strings.Contains(got, "must-not-survive") {
				t.Errorf("Authorization %q", got)
			}
			if h.Get("X-Mimo-Source") != "mimocode-cli-free" || h.Get("Accept") != "text/event-stream" || h.Get("Content-Type") != "application/json" {
				t.Errorf("headers %v", h)
			}
			if !strings.HasPrefix(h.Get("User-Agent"), "Mozilla/5.0") || !strings.Contains(h.Get("User-Agent"), "Chrome/131") {
				t.Errorf("User-Agent %q", h.Get("User-Agent"))
			}
			if !regexp.MustCompile(`^ses_[a-z0-9]{24}$`).MatchString(h.Get("x-session-affinity")) {
				t.Errorf("x-session-affinity %q", h.Get("x-session-affinity"))
			}
			var body struct {
				Messages []struct{ Role, Content string }
			}
			if err := json.Unmarshal([]byte(sent[0].Body), &body); err != nil || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Content != MimoSystemMarker {
				t.Errorf("the MiMoCode marker is not the first system message: %s", sent[0].Body)
			}
		})
	}
}

func TestMimoFreeAcceptFollowsTheBodysStream(t *testing.T) {
	u := newMimoUpstream(t)
	if _, _, err := u.do(t, "mimo-free", `{"model":"m","stream":false,"messages":[]}`); err != nil {
		t.Fatal(err)
	}
	if got := u.seen(mimoChatPath)[0].Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept %q", got)
	}
}

func TestMimoFreeKeepsOneSessionAndOneJWT(t *testing.T) {
	u := newMimoUpstream(t)
	for _, id := range []string{"mimo-free", "mmf", "mimo-free"} {
		if _, _, err := u.do(t, id, mimoBody); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(u.seen(mimoBootstrapPath)); n != 1 {
		t.Errorf("bootstraps %d: the JWT is cached across requests and across the alias", n)
	}
	sent := u.seen(mimoChatPath)
	if len(sent) != 3 || sent[0].Header.Get("x-session-affinity") != sent[2].Header.Get("x-session-affinity") {
		t.Errorf("the session id changed between requests")
	}
}

// A token within five minutes of its exp is replaced before it is used
// (mimo-free.js:80-82); one without a readable exp lives 3000 seconds (:52-61).
func TestMimoFreeRefreshesBeforeExpiryAndFallsBackToAFixedTTL(t *testing.T) {
	u := newMimoUpstream(t)
	now := time.Now()
	mimoJWTs.now = func() time.Time { return now }
	soon := jwtWithExp(now.Add(mimoExpiryBuffer - time.Second).Unix())
	u.bootstrap = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, `{"jwt":%q}`, soon) }
	for i := 0; i < 2; i++ {
		if _, _, err := u.do(t, "mimo-free", mimoBody); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(u.seen(mimoBootstrapPath)); n != 2 {
		t.Errorf("a token inside the expiry buffer was reused: %d bootstraps", n)
	}

	opaque := newMimoUpstream(t)
	now = time.Now()
	mimoJWTs.now = func() time.Time { return now }
	opaque.bootstrap = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"jwt":"opaque.not-base64!.sig"}`) }
	opaque.do(t, "mimo-free", mimoBody)
	now = now.Add(mimoFallbackTTL - mimoExpiryBuffer - time.Second)
	opaque.do(t, "mimo-free", mimoBody)
	if n := len(opaque.seen(mimoBootstrapPath)); n != 1 {
		t.Errorf("bootstraps %d before the fallback TTL", n)
	}
	now = now.Add(2 * time.Second)
	opaque.do(t, "mimo-free", mimoBody)
	if n := len(opaque.seen(mimoBootstrapPath)); n != 2 {
		t.Errorf("bootstraps %d after the fallback TTL", n)
	}
}

func TestMimoJWTExpiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if got := mimoJWTExpiry(jwtWithExp(2_000_000), now); !got.Equal(time.Unix(2_000_000, 0)) {
		t.Errorf("exp claim ignored: %v", got)
	}
	// The pin decodes with Node's lenient base64: padding and the standard alphabet.
	padded := "h." + base64.StdEncoding.EncodeToString([]byte(`{"exp":3000000}`)) + ".s"
	if got := mimoJWTExpiry(padded, now); !got.Equal(time.Unix(3_000_000, 0)) {
		t.Errorf("padded payload: %v", got)
	}
	for _, bad := range []string{"", "x", "a.b.c", "a." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":1}`)) + ".c", "a." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":0}`)) + ".c"} {
		if got := mimoJWTExpiry(bad, now); !got.Equal(now.Add(mimoFallbackTTL)) {
			t.Errorf("%q: %v", bad, got)
		}
	}
}

// 401 and 403 forget the token and send once more with a fresh one; the second
// answer stands (mimo-free.js:148-156).
func TestMimoFreeRetriesOnceWithAFreshJWTOnAuthFailure(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			u := newMimoUpstream(t)
			u.chat = func(w http.ResponseWriter, r *http.Request) {
				if len(u.seen(mimoChatPath)) == 1 {
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":"Illegal access"}`)
					return
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
			}
			response, text, err := u.do(t, "mimo-free", mimoBody)
			if err != nil || response.StatusCode != 200 || !strings.Contains(text, "pong") {
				t.Fatalf("%v %v %s", err, response, text)
			}
			sent := u.seen(mimoChatPath)
			if len(sent) != 2 || len(u.seen(mimoBootstrapPath)) != 2 {
				t.Fatalf("chat %d bootstraps %d", len(sent), len(u.seen(mimoBootstrapPath)))
			}
			if sent[0].Header.Get("Authorization") == sent[1].Header.Get("Authorization") {
				t.Error("the retry reused the rejected token")
			}
			if sent[0].Body != sent[1].Body {
				t.Error("the retry sent a different body")
			}
		})
	}
	u := newMimoUpstream(t)
	u.chat = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) }
	response, _, err := u.do(t, "mimo-free", mimoBody)
	if err != nil || response.StatusCode != 403 || len(u.seen(mimoChatPath)) != 2 {
		t.Errorf("a second rejection is the answer: %v %v after %d sends", err, response, len(u.seen(mimoChatPath)))
	}
}

// The bootstrap JWT is a fail-closed input: when it cannot be minted nothing is
// sent, and nothing stands in for it.
func TestMimoFreeRefusesWhenTheJWTCannotBeMinted(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"http error":         func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"forbidden":          func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) },
		"no jwt":             func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) },
		"empty jwt":          func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"jwt":""}`) },
		"blank jwt":          func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"jwt":"   "}`) },
		"not JSON":           func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `<html>`) },
		"jwt with a space":   func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"jwt":"a b"}`) },
		"jwt with a newline": func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"jwt":"a\r\nX-Injected: 1"}`) },
		"oversized jwt": func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"jwt":%q}`, strings.Repeat("a", mimoJWTLimit+1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			u := newMimoUpstream(t)
			u.bootstrap = handler
			response, _, err := u.do(t, "mimo-free", mimoBody)
			if err == nil {
				t.Fatalf("a request was sent without a minted JWT: %v", response)
			}
			if n := len(u.seen(mimoChatPath)); n != 0 {
				t.Errorf("chat called %d times", n)
			}
			// And the failure is not cached as a token.
			u.bootstrap = nil
			if _, _, err := u.do(t, "mimo-free", mimoBody); err != nil {
				t.Errorf("recovery: %v", err)
			}
		})
	}
}

func TestMimoFreeRefusesWhenTheRetryCannotBootstrap(t *testing.T) {
	u := newMimoUpstream(t)
	u.chat = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }
	boots := 0
	u.bootstrap = func(w http.ResponseWriter, _ *http.Request) {
		boots++
		if boots > 1 {
			w.WriteHeader(500)
			return
		}
		fmt.Fprintf(w, `{"jwt":%q}`, jwtWithExp(time.Now().Add(time.Hour).Unix()))
	}
	if _, _, err := u.do(t, "mimo-free", mimoBody); err == nil {
		t.Fatal("the retry went out without a fresh JWT")
	}
	if n := len(u.seen(mimoChatPath)); n != 1 {
		t.Errorf("chat calls %d", n)
	}
}

func TestMimoSystemMarker(t *testing.T) {
	marker := func(content string) string {
		return `{"role":"system","content":` + fmt.Sprintf("%q", content) + `}`
	}
	for name, tc := range map[string]struct{ in, want string }{
		"added first": {
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			`{"messages":[` + marker(MimoSystemMarker) + `,{"role":"user","content":"hi"}],"model":"m"}`,
		},
		"kept when a system message holds it": {
			`{"model":"m","messages":[` + marker("preamble. "+MimoSystemMarker) + `,{"role":"user","content":"hi"}]}`,
			``,
		},
		"a user message holding it does not count": {
			`{"model":"m","messages":[{"role":"user","content":"` + MimoSystemMarker + `"}]}`,
			`{"messages":[` + marker(MimoSystemMarker) + `,{"role":"user","content":"` + MimoSystemMarker + `"}],"model":"m"}`,
		},
		// The pin counts a system message only when its content is a string.
		"array content does not count": {
			`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"` + MimoSystemMarker + `"}]}]}`,
			`{"messages":[` + marker(MimoSystemMarker) + `,{"role":"system","content":[{"type":"text","text":"` + MimoSystemMarker + `"}]}],"model":"m"}`,
		},
		"empty messages":      {`{"model":"m","messages":[]}`, `{"messages":[` + marker(MimoSystemMarker) + `],"model":"m"}`},
		"no messages":         {`{"model":"m","input":"hi"}`, ``},
		"messages not a list": {`{"model":"m","messages":"hi"}`, ``},
	} {
		for _, id := range []string{"mimo-free", "mmf"} {
			t.Run(name+"/"+id, func(t *testing.T) {
				provider := routing.ProviderRef{ProviderID: id, Protocol: chat}
				got := FinalizeBodyString(provider, tc.in)
				want := tc.want
				if want == "" {
					want = tc.in
				}
				same(t, got, want)
				// Idempotent: a probe replaying a served body does not stack markers.
				if again := FinalizeBodyString(provider, got); again != got {
					t.Errorf("not idempotent:\n %s\n %s", got, again)
				}
			})
		}
	}
	// Only the Chat wire carries messages.
	if got := FinalizeBodyString(routing.ProviderRef{ProviderID: "mimo-free", Protocol: responses}, `{"model":"m","messages":[]}`); got != `{"model":"m","messages":[]}` {
		t.Errorf("responses body changed: %s", got)
	}
}

func TestMimoFreeFingerprintIsAStableDigest(t *testing.T) {
	first := mimoDeviceFingerprint()
	if first != mimoDeviceFingerprint() || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Errorf("fingerprint %q", first)
	}
}
