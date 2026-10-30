package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/clientip"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// #283 / #281 (SPEC 12.3) through the real listener and a real injected upstream:
// the trusted-proxy setting reaches the login throttle, a forwarded identity from
// a peer that is not a trusted proxy changes nothing, and inference is unaffected.

const c10Password = "c10-operator-password"

// c10Serve rebuilds a serving app with a trusted-proxy list and an admin
// and starts its real listener. The upstream records any forwarded identity it is
// handed, which must stay empty.
func c10Serve(t *testing.T, trusted string) (address, apiKey string, leaked *atomic.Int32) {
	t.Helper()
	leaked = new(atomic.Int32)
	upstream := newC10Upstream(t, leaked)
	a, key := servingApp(t, upstream, 1)
	prefixes, err := clientip.ParseTrusted(trusted)
	if err != nil {
		t.Fatal(err)
	}
	// A fixture admin (low-iteration hash) keeps each login cheap under -race; the
	// real bootstrap path is proven by the C09 bootstrap tests.
	if _, err := a.store.DB().Exec("INSERT INTO admin_users (id,username,password_hash) VALUES (?,?,?)", "c10-admin", "operator", storetest.AdminHash(c10Password)); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	a.cfg.TrustedProxies = prefixes
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c09Serve(t, a), key, leaked
}

func newC10Upstream(t *testing.T, leaked *atomic.Int32) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range r.Header {
			for _, value := range values {
				if strings.Contains(value, "198.51.100.") {
					leaked.Add(1)
					t.Errorf("client address reached the upstream in %s", name)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func c10Login(address, forwarded, password string) int {
	request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/admin/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"operator","password":%q}`, password)))
	if forwarded != "" {
		request.Header.Set("X-Forwarded-For", forwarded)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(request)
	if err != nil {
		return -1
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func TestC10TrustedProxyKeysTheLoginThrottleOnTheRealClient(t *testing.T) {
	// The test client connects from 127.0.0.1, so listing it makes it the proxy.
	address, key, leaked := c10Serve(t, "127.0.0.1")
	for i := 0; i < 8; i++ {
		// The "proxy" appends the real client on the right; the left is forged.
		if got := c10Login(address, fmt.Sprintf("192.0.2.%d, 198.51.100.1", i+1), "wrong"); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d want 401", i, got)
		}
	}
	if got := c10Login(address, "198.51.100.1", c10Password); got != http.StatusTooManyRequests {
		t.Fatalf("locked client status=%d want 429", got)
	}
	if got := c10Login(address, "198.51.100.2", c10Password); got != http.StatusOK {
		t.Fatalf("another client behind the same proxy status=%d want 200 (its own bucket)", got)
	}

	// Inference through the same proxy still works and the upstream never sees the
	// client address the proxy vouched for.
	request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/completions", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-Forwarded-For", "198.51.100.1")
	request.Header.Set("X-Real-Ip", "198.51.100.1")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || leaked.Load() != 0 {
		t.Fatalf("inference status=%d leaked=%d", response.StatusCode, leaked.Load())
	}
}

// The listener's peer here (127.0.0.1) is not in the configured list, so it is an
// untrusted peer: rotating or forging forwarded identities cannot escape its
// lockout, and the failures are not charged to the identity it names.
func TestC10UntrustedPeerCannotEscapeTheLoginLockoutByForging(t *testing.T) {
	address, _, _ := c10Serve(t, "10.0.0.0/8")
	for i := 0; i < 8; i++ {
		if got := c10Login(address, fmt.Sprintf("192.0.2.%d", i+1), "wrong"); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d want 401", i, got)
		}
	}
	for _, forwarded := range []string{"198.51.100.77", "10.0.0.2", "192.0.2.200, 10.0.0.2", ""} {
		if got := c10Login(address, forwarded, c10Password); got != http.StatusTooManyRequests {
			t.Fatalf("forged %q escaped the lockout: status=%d", forwarded, got)
		}
	}
}
