// Package mimoaccount implements the Xiaomi MiMo account-session handshake the
// reference uses for the weekly quota and for Preview inference
// (open-sse/shared/mimoAccount.js:14-29,116-218; executors/xiaomi-mimo.js:67).
//
// A passToken read from the connection's sealed provider data is exchanged via
// the passportapi SSO for a mimopc serviceToken cookie. The cookie is kept for
// 30 minutes per passToken so concurrent readers share one handshake. The
// package never reads a host cookie store: callers supply only fields a user
// already sealed into the connection.
package mimoaccount

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Base and AccountHost are variables so a test can point the handshake at a
// local TLS server; production values are the reference's (mimoAccount.js:24-25).
var (
	Base        = "https://mimo-server-cn.xiaomimimo.com"
	AccountHost = "account.xiaomi.com"
)

const (
	// UserAgent is API_UA (mimoAccount.js:25).
	UserAgent = "miNative PC/Normal Windows_NT/10.0.19045 SDKV/1.0.0 DEVT/PC DEVS/Windows APP/miaccount_desktop APPV/0.1.0"
	// SSOUserAgent is SSO_UA (mimoAccount.js:26).
	SSOUserAgent = "MiClaw/1.0"
	// TTL is COOKIE_TTL_MS (mimoAccount.js:29).
	TTL            = 30 * time.Minute
	maxBody        = 1 << 20
	requestTimeout = 10 * time.Second
)

// Client is the outbound HTTP client (the app's pooled, SSRF-guarded client).
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Sessions is the per-passToken service-cookie cache (mimoAccount.js:31-34,181-218).
type Sessions struct {
	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	cookie string
	err    error
	at     time.Time
	done   chan struct{} // open while the handshake runs
}

// NewSessions returns an empty cache.
func NewSessions() *Sessions { return &Sessions{entries: map[string]*entry{}} }

// Invalidate drops every cached cookie, so the next call re-runs the handshake
// (mimoAccount.js:220-223).
func (c *Sessions) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[string]*entry{}
	c.mu.Unlock()
}

// Cookie returns the service cookie for passToken, running handshake once when
// none is cached or the cached one expired. Concurrent callers share a handshake.
func (c *Sessions) Cookie(ctx context.Context, passToken string, now func() time.Time, handshake func() (string, error)) (string, error) {
	if c == nil {
		return handshake()
	}
	sum := sha256.Sum256([]byte(passToken))
	key := hex.EncodeToString(sum[:])
	c.mu.Lock()
	if existing, ok := c.entries[key]; ok {
		if done := existing.done; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				return existing.cookie, existing.err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if now().Sub(existing.at) < TTL {
			c.mu.Unlock()
			return existing.cookie, nil
		}
	}
	created := &entry{done: make(chan struct{})}
	c.entries[key] = created
	c.mu.Unlock()
	cookie, err := handshake()
	c.mu.Lock()
	defer c.mu.Unlock()
	created.cookie, created.err = cookie, err
	close(created.done)
	created.done = nil
	if err != nil {
		delete(c.entries, key)
		return "", err
	}
	created.at = now()
	for other, kept := range c.entries {
		if kept.done == nil && created.at.Sub(kept.at) >= TTL {
			delete(c.entries, other)
		}
	}
	return cookie, nil
}

type reply struct {
	status  int
	body    []byte
	headers http.Header
}

func get(ctx context.Context, client Client, endpoint string, jar map[string]string, ua string, accept bool) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return reply{}, err
	}
	for _, name := range []string{"passToken", "userId", "cUserId", "serviceToken", "mimopc_ph", "mimopc_slh"} {
		if jar[name] != "" {
			request.AddCookie(&http.Cookie{Name: name, Value: jar[name]})
		}
	}
	request.Header.Set("User-Agent", ua)
	if accept {
		request.Header.Set("Accept", "application/json")
	}
	send := client.Do
	if standard, ok := client.(*http.Client); ok {
		// The reference uses redirect:"manual" (mimoAccount.js:126): a followed
		// 302 would consume the SSO hop the caller inspects.
		manual := *standard
		manual.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		send = manual.Do
	}
	response, err := send(request)
	if err != nil {
		return reply{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return reply{status: response.StatusCode, headers: response.Header}, err
	}
	return reply{status: response.StatusCode, body: body, headers: response.Header}, nil
}

func absorb(jar map[string]string, got reply) {
	for _, header := range got.headers.Values("Set-Cookie") {
		pair, _, _ := strings.Cut(header, ";")
		name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		value = strings.Trim(value, "\"")
		if !ok || value == "" || !validCookieValue(value) {
			continue
		}
		switch name {
		case "serviceToken", "mimopc_ph", "mimopc_slh", "userId":
			jar[name] = value
		}
	}
}

// ServiceCookie runs the five-hop handshake and returns the Cookie header value.
// data carries the sealed fields: mimoPassToken, mimoUserId, mimoCUserId.
func ServiceCookie(ctx context.Context, client Client, data map[string]string) (string, error) {
	jar := map[string]string{}
	for name, key := range map[string]string{"passToken": "mimoPassToken", "userId": "mimoUserId", "cUserId": "mimoCUserId"} {
		value := data[key]
		if !validCookieValue(value) {
			return "", errors.New("invalid Xiaomi account credential")
		}
		jar[name] = value
	}
	call := func(endpoint, ua string, accept bool) (reply, error) {
		return get(ctx, client, endpoint, jar, ua, accept)
	}
	first, err := call(Base+"/api/user/xiaomi/me", UserAgent, false)
	if err != nil {
		return "", err
	}
	redirect, err := pinnedURL(first.headers.Get("Location"), AccountHost, "/pass/serviceLogin")
	if err != nil {
		return "", err
	}
	callback := redirect.Query().Get("callback")
	if _, err := pinnedURL(callback, hostOnly(Base), "/api/sts"); err != nil {
		return "", err
	}
	sso, err := call("https://"+AccountHost+"/pass/serviceLogin?sid=passportapi&_json=true", SSOUserAgent, true)
	if err != nil {
		return "", err
	}
	var login struct {
		Location string          `json:"location"`
		Nonce    json.RawMessage `json:"nonce"`
		Security string          `json:"ssecurity"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(string(sso.body), "&&&START&&&")), &login) != nil {
		return "", errors.New("invalid Xiaomi SSO response")
	}
	location, err := pinnedURL(login.Location, AccountHost, "")
	if err != nil {
		return "", err
	}
	nonce := location.Query().Get("nonce")
	if len(login.Nonce) > 0 && string(login.Nonce) != "null" {
		if json.Unmarshal(login.Nonce, &nonce) != nil {
			var n json.Number
			if json.Unmarshal(login.Nonce, &n) != nil {
				return "", errors.New("invalid Xiaomi SSO nonce")
			}
			nonce = n.String()
		}
	}
	if nonce == "" {
		return "", errors.New("missing Xiaomi SSO nonce")
	}
	input := "nonce=" + nonce
	if strings.TrimSpace(login.Security) != "" {
		input += "&" + login.Security
	}
	digest := sha1.Sum([]byte(input))
	signed, err := pinnedURL(login.Location+"&clientSign="+EncodeURIComponent(base64.StdEncoding.EncodeToString(digest[:])), AccountHost, "")
	if err != nil {
		return "", err
	}
	account, err := call(signed.String(), SSOUserAgent, false)
	if err != nil {
		return "", err
	}
	absorb(jar, account)
	serviceURL, err := pinnedURL("https://"+AccountHost+"/pass/serviceLogin?sid=mimopc&callback="+EncodeURIComponent(callback)+"&_json=true", AccountHost, "/pass/serviceLogin")
	if err != nil {
		return "", err
	}
	service, err := call(serviceURL.String(), SSOUserAgent, true)
	if err != nil {
		return "", err
	}
	var ticket struct {
		Location string `json:"location"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(string(service.body), "&&&START&&&")), &ticket) != nil {
		return "", errors.New("invalid Xiaomi service response")
	}
	sts, err := pinnedURL(ticket.Location, hostOnly(Base), "/api/sts")
	if err != nil {
		return "", err
	}
	absorb(jar, service)
	stamped, err := call(sts.String(), UserAgent, false)
	if err != nil {
		return "", err
	}
	delete(jar, "serviceToken")
	absorb(jar, stamped)
	if jar["serviceToken"] == "" {
		return "", errors.New("missing Xiaomi service session")
	}
	var cookie strings.Builder
	for _, name := range []string{"serviceToken", "mimopc_ph", "mimopc_slh", "userId"} {
		if jar[name] == "" {
			continue
		}
		if cookie.Len() > 0 {
			cookie.WriteString("; ")
		}
		fmt.Fprintf(&cookie, "%s=%s", name, jar[name])
	}
	return cookie.String(), nil
}

func hostOnly(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func pinnedURL(raw, host, path string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != host || parsed.User != nil || (path != "" && parsed.Path != path) || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("invalid Xiaomi account redirect")
	}
	return parsed, nil
}

// encodeURIComponent percent-encodes every UTF-8 byte except ASCII letters,
// digits and -_.!~*'() (mimoAccount.js:96-99).
func EncodeURIComponent(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			out.WriteByte(c)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", c)
	}
	return out.String()
}

// validCookieValue is wire.ValidCookieValue's rule. It is repeated here because
// internal/credentials reaches the wire package, and this package must stay
// importable from it.
func validCookieValue(value string) bool {
	for _, c := range value {
		if c < 0x21 || c > 0x7e || strings.ContainsRune("\";,\\", c) {
			return false
		}
	}
	return true
}
