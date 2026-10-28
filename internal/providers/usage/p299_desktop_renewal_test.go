package usage

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// #299: the pin reads MiMo Desktop's cookie store whenever a quota read has no
// stored passToken (open-sse/shared/mimoAccount.js:181-185). Here the reader
// never opens the host itself: it asks the consented renewal, and only when the
// connection has no sealed session or its sealed session failed the handshake.

// mimoAccountFixture is the Xiaomi account service: a handshake succeeds for a
// passToken in valid, the usage endpoint answers usage, and the key endpoint
// answers 401 (the key cannot read the weekly quota).
type mimoAccountFixture struct {
	mu    sync.Mutex
	valid map[string]bool
	usage int
	paths []string
}

func (f *mimoAccountFixture) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.paths = append(f.paths, req.URL.Host+req.URL.Path)
	valid, usageStatus := f.valid, f.usage
	f.mu.Unlock()
	pass := ""
	if cookie, err := req.Cookie("passToken"); err == nil {
		pass = cookie.Value
	}
	header := http.Header{}
	status, body := http.StatusOK, ""
	callback := mimoAccountBase + "/api/sts"
	switch req.URL.Host + req.URL.Path {
	case "mimo-server-cn.xiaomimimo.com/api/user/xiaomi/me":
		if valid[pass] {
			header.Set("Location", "https://account.xiaomi.com/pass/serviceLogin?callback="+url.QueryEscape(callback))
			status = http.StatusFound
		}
	case "account.xiaomi.com/pass/serviceLogin":
		if req.URL.Query().Get("sid") == "passportapi" {
			body = `&&&START&&&{"nonce":"1","ssecurity":"s","location":"https://account.xiaomi.com/pass/confirm?nonce=1"}`
		} else {
			body = `&&&START&&&{"location":"` + callback + `?ticket=t"}`
		}
	case "account.xiaomi.com/pass/confirm":
		header.Add("Set-Cookie", "serviceToken=passport-"+pass)
		status = http.StatusFound
	case "mimo-server-cn.xiaomimimo.com/api/sts":
		header.Add("Set-Cookie", "serviceToken=session-"+pass)
	case "mimo-server-cn.xiaomimimo.com/api/user/usage":
		if usageStatus != 0 {
			status = usageStatus
		}
		body = `{"code":0,"data":{"percent":40}}`
	case "aistudio.xiaomimimo.com/open-apis/v1/user/usage":
		status = http.StatusUnauthorized
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (f *mimoAccountFixture) called(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, seen := range f.paths {
		if seen == path {
			n++
		}
	}
	return n
}

// fakeRenewal records what the reader asked for and answers renewed.
type fakeRenewal struct {
	renewed map[string]string
	asked   []string
}

func (f *fakeRenewal) Renew(_ context.Context, connectionID string, stored map[string]string) map[string]string {
	f.asked = append(f.asked, connectionID+"|"+stored["mimoPassToken"])
	return f.renewed
}

func mimoRenewalReader(t *testing.T, fixture *mimoAccountFixture, renewal MimoDesktopRenewal) quota.UsageClient {
	t.Helper()
	readers, err := Readers(fixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	if renewal != nil {
		WithMimoDesktopRenewal(readers, renewal)
	}
	return readers["xiaomi-mimo"]
}

func weeklyRemaining(t *testing.T, state quota.State) float64 {
	t.Helper()
	if len(state.Windows) != 1 || state.Windows[0].Remaining == nil {
		t.Fatalf("state = %+v", state)
	}
	return *state.Windows[0].Remaining
}

const mimoAccountUsagePath = "mimo-server-cn.xiaomimimo.com/api/user/usage"
const mimoKeyUsagePath = "aistudio.xiaomimimo.com/open-apis/v1/user/usage"

// A connection without a sealed session asks the renewal; the session it
// returns reads the weekly quota, and the read says when the host was read.
func TestMimoQuotaReadRenewsAMissingSession(t *testing.T) {
	fixture := &mimoAccountFixture{valid: map[string]bool{"desktop-pass": true}}
	renewal := &fakeRenewal{renewed: map[string]string{"apiKey": "sk-k", "mimoPassToken": "desktop-pass", "mimoDesktopRenewal": "on", "mimoDesktopReadAt": "2026-10-29T00:00:00Z"}}
	state, err := mimoRenewalReader(t, fixture, renewal).Usage(context.Background(), quota.Credential{ConnectionID: "c1", AccessToken: "sk-k", ProviderData: map[string]string{"apiKey": "sk-k", "mimoDesktopRenewal": "on"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := weeklyRemaining(t, state); got != .4 {
		t.Fatalf("remaining = %v", got)
	}
	if len(renewal.asked) != 1 || renewal.asked[0] != "c1|" {
		t.Fatalf("asked = %v", renewal.asked)
	}
	if state.Metadata["desktopSessionReadAt"] != "2026-10-29T00:00:00Z" {
		t.Fatalf("metadata = %v", state.Metadata)
	}
	if fixture.called(mimoKeyUsagePath) != 0 {
		t.Fatal("the key path ran although the renewed session read the quota")
	}
}

// Nothing renewed (no consent, host off, store absent or locked): the key path
// answers as before, with the notice that the key cannot read the quota.
func TestMimoQuotaReadWithoutARenewalKeepsTheKeyPath(t *testing.T) {
	for name, renewal := range map[string]MimoDesktopRenewal{"no renewal wired": nil, "renewal read nothing": &fakeRenewal{}} {
		t.Run(name, func(t *testing.T) {
			fixture := &mimoAccountFixture{}
			state, err := mimoRenewalReader(t, fixture, renewal).Usage(context.Background(), quota.Credential{ConnectionID: "c1", AccessToken: "sk-k"})
			if err != nil {
				t.Fatal(err)
			}
			if state.Message != mimoSessionRequired || fixture.called(mimoKeyUsagePath) != 1 || fixture.called(mimoAccountUsagePath) != 0 {
				t.Fatalf("state = %+v paths = %v", state, fixture.paths)
			}
		})
	}
}

// A sealed session that fails the handshake asks for a renewal with the
// passToken that failed; one that works never does, nor does one whose usage
// answer failed after a good handshake (the session is not the problem).
func TestMimoQuotaReadRenewsOnlyAFailedSession(t *testing.T) {
	stored := map[string]string{"mimoPassToken": "expired-pass", "mimoDesktopRenewal": "on"}
	fixture := &mimoAccountFixture{valid: map[string]bool{"desktop-pass": true}}
	renewal := &fakeRenewal{renewed: map[string]string{"mimoPassToken": "desktop-pass"}}
	state, err := mimoRenewalReader(t, fixture, renewal).Usage(context.Background(), quota.Credential{ConnectionID: "c1", AccessToken: "sk-k", ProviderData: stored})
	if err != nil {
		t.Fatal(err)
	}
	if weeklyRemaining(t, state) != .4 || len(renewal.asked) != 1 || renewal.asked[0] != "c1|expired-pass" {
		t.Fatalf("state = %+v asked = %v", state, renewal.asked)
	}

	working := &fakeRenewal{renewed: map[string]string{"mimoPassToken": "desktop-pass"}}
	fixture = &mimoAccountFixture{valid: map[string]bool{"sealed-pass": true}}
	if _, err := mimoRenewalReader(t, fixture, working).Usage(context.Background(), quota.Credential{ConnectionID: "c1", AccessToken: "sk-k", ProviderData: map[string]string{"mimoPassToken": "sealed-pass", "mimoDesktopRenewal": "on"}}); err != nil {
		t.Fatal(err)
	}
	if len(working.asked) != 0 {
		t.Fatalf("a working session asked for a renewal: %v", working.asked)
	}

	usageDown := &fakeRenewal{renewed: map[string]string{"mimoPassToken": "desktop-pass"}}
	fixture = &mimoAccountFixture{valid: map[string]bool{"sealed-pass": true, "desktop-pass": true}, usage: http.StatusInternalServerError}
	state, err = mimoRenewalReader(t, fixture, usageDown).Usage(context.Background(), quota.Credential{ConnectionID: "c1", AccessToken: "sk-k", ProviderData: map[string]string{"mimoPassToken": "sealed-pass", "mimoDesktopRenewal": "on"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(usageDown.asked) != 0 || state.Message != mimoSessionRequired {
		t.Fatalf("usage failure asked = %v state = %+v", usageDown.asked, state)
	}
}

// A renewed session that also fails the handshake falls through to the key, as
// any unusable session does; a cancelled read asks for nothing.
func TestMimoQuotaReadRenewalFailurePaths(t *testing.T) {
	fixture := &mimoAccountFixture{valid: map[string]bool{}}
	renewal := &fakeRenewal{renewed: map[string]string{"mimoPassToken": "also-bad"}}
	state, err := mimoRenewalReader(t, fixture, renewal).Usage(context.Background(), quota.Credential{ConnectionID: "c1", AccessToken: "sk-k"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Message != mimoSessionRequired || len(renewal.asked) != 1 {
		t.Fatalf("state = %+v asked = %v", state, renewal.asked)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := &fakeRenewal{renewed: map[string]string{"mimoPassToken": "desktop-pass"}}
	_, _ = mimoRenewalReader(t, &mimoAccountFixture{}, cancelled).Usage(ctx, quota.Credential{ConnectionID: "c1", AccessToken: "sk-k", ProviderData: map[string]string{"mimoPassToken": "sealed-pass"}})
	if len(cancelled.asked) != 0 {
		t.Fatalf("a cancelled read asked for a renewal: %v", cancelled.asked)
	}
}
