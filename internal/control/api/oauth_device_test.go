package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

const (
	deviceTestCode  = "DEVC-4321"
	deviceTestToken = "device-token-sentinel"
)

// deviceUpstream scripts Kilo's initiate and poll endpoints and counts polls.
type deviceUpstream struct {
	mu         sync.Mutex
	initStatus int
	pollStatus int
	pollBody   string
	polls      int
	pollErr    bool
}

func (d *deviceUpstream) Do(request *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
	if request.Method == http.MethodPost {
		if d.initStatus != 0 {
			return respond(d.initStatus, "upstream-secret-text")
		}
		return respond(200, `{"code":"`+deviceTestCode+`","verificationUrl":"https://kilo.ai/device-auth?code=`+deviceTestCode+`","expiresIn":600}`)
	}
	d.polls++
	if d.pollErr {
		return nil, io.ErrUnexpectedEOF
	}
	if !strings.HasSuffix(request.URL.Path, "/"+deviceTestCode) {
		return respond(404, "")
	}
	return respond(d.pollStatus, d.pollBody)
}

func (d *deviceUpstream) pollCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.polls
}

type deviceClock struct{ now time.Time }

func newDeviceAPI(t *testing.T, upstream *deviceUpstream) (*Handler, *http.ServeMux, *credentials.Store, *http.Cookie, *deviceClock) {
	t.Helper()
	h, mux, creds, cookie := newOAuthAPI(t, nil)
	h.opts.OAuthClient = upstream
	clock := &deviceClock{now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	h.oauth.now = func() time.Time { return clock.now }
	return h, mux, creds, cookie, clock
}

type deviceStarted struct {
	State           string `json:"state"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	IntervalSeconds int    `json:"intervalSeconds"`
}

func startDevice(t *testing.T, mux *http.ServeMux, cookie *http.Cookie) deviceStarted {
	t.Helper()
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/device", `{"name":"Work","identity":"work@example.test"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("device start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var started deviceStarted
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	return started
}

func pollDevice(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, state string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"state": state})
	return doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/poll", string(body))
}

func TestOAuthDeviceStartRequiresSessionAndDeviceProvider(t *testing.T) {
	_, mux, _, cookie, _ := newDeviceAPI(t, &deviceUpstream{})
	if recorder := doJSON(t, mux, nil, http.MethodPost, "/admin/v1/oauth/kilocode/device", `{"name":"a","identity":"b"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous device start status=%d want 401", recorder.Code)
	}
	if recorder := doJSON(t, mux, nil, http.MethodPost, "/admin/v1/oauth/kilocode/poll", `{"state":"x"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous poll status=%d want 401", recorder.Code)
	}
	// The two flows are not interchangeable: a browser-flow provider has no
	// device endpoints, and a device provider has no authorize/callback.
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/claude/device", `{"name":"a","identity":"b"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("claude device status=%d want 404", recorder.Code)
	}
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/start", `{"name":"a","identity":"b"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("kilocode browser start status=%d want 404", recorder.Code)
	}
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/openai/device", `{"name":"a","identity":"b"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("api-key provider device status=%d want 404", recorder.Code)
	}
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/device", `{"name":"","identity":"b"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing name status=%d want 400", recorder.Code)
	}
}

func TestOAuthDeviceStartShowsCodeButKeepsDeviceCodeServerSide(t *testing.T) {
	h, mux, _, cookie, _ := newDeviceAPI(t, &deviceUpstream{})
	started := startDevice(t, mux, cookie)
	if started.UserCode != deviceTestCode || started.VerificationURL != "https://kilo.ai/device-auth?code="+deviceTestCode || started.IntervalSeconds != 3 {
		t.Fatalf("started=%+v", started)
	}
	if started.State == deviceTestCode || h.oauth.pending[started.State].deviceCode != deviceTestCode {
		t.Fatal("state must be opaque and the device code must stay server-side")
	}
}

func TestOAuthDeviceStartHidesUpstreamFailure(t *testing.T) {
	_, mux, _, cookie, _ := newDeviceAPI(t, &deviceUpstream{initStatus: 500})
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/device", `{"name":"a","identity":"b"}`)
	if recorder.Code != http.StatusBadGateway || strings.Contains(recorder.Body.String(), "upstream-secret-text") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOAuthDevicePollIsThrottledAndStaysPending(t *testing.T) {
	upstream := &deviceUpstream{pollStatus: http.StatusAccepted}
	_, mux, _, cookie, clock := newDeviceAPI(t, upstream)
	started := startDevice(t, mux, cookie)
	// Before the provider's interval elapses the poll never reaches upstream.
	if recorder := pollDevice(t, mux, cookie, started.State); recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"pending"`) || upstream.pollCount() != 0 {
		t.Fatalf("early poll status=%d body=%s upstream polls=%d", recorder.Code, recorder.Body.String(), upstream.pollCount())
	}
	clock.now = clock.now.Add(3 * time.Second)
	if recorder := pollDevice(t, mux, cookie, started.State); recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"pending"`) || upstream.pollCount() != 1 {
		t.Fatalf("poll status=%d body=%s upstream polls=%d", recorder.Code, recorder.Body.String(), upstream.pollCount())
	}
	// A burst right after is throttled again.
	pollDevice(t, mux, cookie, started.State)
	pollDevice(t, mux, cookie, started.State)
	if upstream.pollCount() != 1 {
		t.Fatalf("burst reached upstream: %d polls", upstream.pollCount())
	}
}

func TestOAuthDeviceApprovalStoresNonExpiringBearerOnce(t *testing.T) {
	upstream := &deviceUpstream{pollStatus: 200, pollBody: `{"status":"approved","token":"` + deviceTestToken + `","userEmail":"a@b.c"}`}
	_, mux, creds, cookie, clock := newDeviceAPI(t, upstream)
	started := startDevice(t, mux, cookie)
	clock.now = clock.now.Add(3 * time.Second)
	recorder := pollDevice(t, mux, cookie, started.State)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"connected"`) {
		t.Fatalf("poll status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoSecrets(t, recorder.Body.String(), deviceTestToken, deviceTestCode)
	connections, err := creds.ListConnections(context.Background(), "kilocode")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%v err=%v", connections, err)
	}
	stored := connections[0]
	if stored.AuthKind != credentials.AuthOAuth || stored.Name != "Work" || stored.Identity != "work@example.test" || stored.Secret.AccessToken != deviceTestToken || stored.Secret.RefreshToken != "" || !stored.Secret.Expiry.IsZero() || !stored.Enabled {
		t.Fatalf("stored=%+v", stored)
	}
	clock.now = clock.now.Add(3 * time.Second)
	if replay := pollDevice(t, mux, cookie, started.State); replay.Code != http.StatusBadRequest {
		t.Fatalf("replay status=%d want 400", replay.Code)
	}
	if connections, _ := creds.ListConnections(context.Background(), "kilocode"); len(connections) != 1 {
		t.Fatalf("replay created %d connections", len(connections))
	}
}

func TestOAuthDevicePollRejectsForeignSessionWithoutBurningAttempt(t *testing.T) {
	upstream := &deviceUpstream{pollStatus: http.StatusAccepted}
	h, mux, _, cookie, clock := newDeviceAPI(t, upstream)
	started := startDevice(t, mux, cookie)
	clock.now = clock.now.Add(3 * time.Second)
	if recorder := pollDevice(t, mux, secondSession(t, h), started.State); recorder.Code != http.StatusBadRequest || upstream.pollCount() != 0 {
		t.Fatalf("foreign poll status=%d polls=%d", recorder.Code, upstream.pollCount())
	}
	if recorder := pollDevice(t, mux, cookie, started.State); recorder.Code != http.StatusOK {
		t.Fatalf("owner poll after foreign attempt status=%d", recorder.Code)
	}
	if recorder := pollDevice(t, mux, cookie, "unknown-state"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown state status=%d", recorder.Code)
	}
}

func TestOAuthDeviceDeniedAndExpiredEndTheAttempt(t *testing.T) {
	for status, code := range map[int]string{http.StatusForbidden: "oauth_denied", http.StatusGone: "oauth_expired"} {
		upstream := &deviceUpstream{pollStatus: status}
		_, mux, creds, cookie, clock := newDeviceAPI(t, upstream)
		started := startDevice(t, mux, cookie)
		clock.now = clock.now.Add(3 * time.Second)
		recorder := pollDevice(t, mux, cookie, started.State)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), code) {
			t.Fatalf("status %d: got %d %s", status, recorder.Code, recorder.Body.String())
		}
		clock.now = clock.now.Add(3 * time.Second)
		if again := pollDevice(t, mux, cookie, started.State); again.Code != http.StatusBadRequest || upstream.pollCount() != 1 {
			t.Fatalf("attempt survived %s: status=%d polls=%d", code, again.Code, upstream.pollCount())
		}
		if connections, _ := creds.ListConnections(context.Background(), "kilocode"); len(connections) != 0 {
			t.Fatalf("%s stored a connection", code)
		}
	}
}

func TestOAuthDeviceUpstreamFaultKeepsAttemptOpen(t *testing.T) {
	upstream := &deviceUpstream{pollErr: true}
	_, mux, _, cookie, clock := newDeviceAPI(t, upstream)
	started := startDevice(t, mux, cookie)
	clock.now = clock.now.Add(3 * time.Second)
	if recorder := pollDevice(t, mux, cookie, started.State); recorder.Code != http.StatusBadGateway {
		t.Fatalf("fault status=%d", recorder.Code)
	}
	upstream.mu.Lock()
	upstream.pollErr, upstream.pollStatus = false, http.StatusAccepted
	upstream.mu.Unlock()
	clock.now = clock.now.Add(3 * time.Second)
	if recorder := pollDevice(t, mux, cookie, started.State); recorder.Code != http.StatusOK {
		t.Fatalf("attempt did not survive a transient fault: %d", recorder.Code)
	}
}

func TestOAuthDeviceAttemptExpiresWithFlowTTL(t *testing.T) {
	upstream := &deviceUpstream{pollStatus: http.StatusAccepted}
	_, mux, _, cookie, clock := newDeviceAPI(t, upstream)
	started := startDevice(t, mux, cookie)
	clock.now = clock.now.Add(oauthFlowTTL + time.Second)
	if recorder := pollDevice(t, mux, cookie, started.State); recorder.Code != http.StatusBadRequest || upstream.pollCount() != 0 {
		t.Fatalf("expired attempt status=%d polls=%d", recorder.Code, upstream.pollCount())
	}
}

func TestOAuthDeviceConcurrentApprovalSavesOneConnection(t *testing.T) {
	upstream := &deviceUpstream{pollStatus: 200, pollBody: `{"status":"approved","token":"` + deviceTestToken + `"}`}
	_, mux, creds, cookie, clock := newDeviceAPI(t, upstream)
	started := startDevice(t, mux, cookie)
	clock.now = clock.now.Add(3 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); pollDevice(t, mux, cookie, started.State) }()
	}
	wg.Wait()
	if connections, _ := creds.ListConnections(context.Background(), "kilocode"); len(connections) != 1 {
		t.Fatalf("concurrent approval saved %d connections", len(connections))
	}
}
