package mimoaccount

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The five hops of mimoAccount.js:116-218, against a local TLS server standing
// in for the account and mimo-server hosts. No real credential or network.
func TestServiceCookieRunsThePinnedSSOChain(t *testing.T) {
	var hops []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops = append(hops, r.URL.Path+"?"+r.URL.RawQuery)
		base := "https://" + r.Host
		switch r.URL.Path {
		case "/api/user/xiaomi/me":
			callback := base + "/api/sts"
			w.Header().Set("Location", base+"/pass/serviceLogin?sid=mimopc&callback="+url.QueryEscape(callback))
			w.WriteHeader(http.StatusFound)
		case "/pass/serviceLogin":
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Query().Get("sid") {
			case "passportapi":
				if r.URL.Query().Get("clientSign") == "" {
					w.Write([]byte(`&&&START&&&{"location":"` + base + `/pass/serviceLogin?sid=passportapi&nonce=n1","nonce":"n1","ssecurity":"sec"}`))
					return
				}
				w.Header().Add("Set-Cookie", "userId=u1; Path=/")
				w.Write([]byte(`{}`))
			case "mimopc":
				w.Write([]byte(`&&&START&&&{"location":"` + base + `/api/sts?ticket=t1"}`))
			}
		case "/api/sts":
			w.Header().Add("Set-Cookie", "serviceToken=svc; Path=/")
			w.Header().Add("Set-Cookie", "mimopc_ph=ph; Path=/")
			w.Header().Add("Set-Cookie", "mimopc_slh=slh; Path=/")
			w.Header().Add("Set-Cookie", "userId=u1; Path=/")
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oldBase, oldHost := Base, AccountHost
	defer func() { Base, AccountHost = oldBase, oldHost }()
	Base = server.URL
	AccountHost = strings.TrimPrefix(server.URL, "https://")

	cookie, err := ServiceCookie(context.Background(), server.Client(), map[string]string{
		"mimoPassToken": "fixture-pass", "mimoUserId": "u1", "mimoCUserId": "c1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "serviceToken=svc; mimopc_ph=ph; mimopc_slh=slh; userId=u1" {
		t.Fatalf("cookie = %q", cookie)
	}
	if len(hops) != 5 {
		t.Fatalf("hops = %v", hops)
	}
}

func TestServiceCookieRefusesAnInvalidPassToken(t *testing.T) {
	if _, err := ServiceCookie(context.Background(), nil, map[string]string{"mimoPassToken": "bad;value"}); err == nil {
		t.Fatal("a cookie separator in the passToken was accepted")
	}
}

func TestSessionsShareAndInvalidate(t *testing.T) {
	now := func() time.Time { return time.Unix(1000, 0) }
	calls := 0
	sessions := NewSessions()
	handshake := func() (string, error) { calls++; return "c", nil }
	for i := 0; i < 3; i++ {
		if cookie, err := sessions.Cookie(context.Background(), "p", now, handshake); err != nil || cookie != "c" {
			t.Fatalf("cookie %q err %v", cookie, err)
		}
	}
	if calls != 1 {
		t.Fatalf("handshakes = %d, want one cached", calls)
	}
	sessions.Invalidate()
	if _, err := sessions.Cookie(context.Background(), "p", now, handshake); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("handshakes after invalidate = %d", calls)
	}
}
