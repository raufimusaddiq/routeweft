package usage

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

const mimoAccountBase = "https://mimo-server-cn.xiaomimimo.com"
const mimoAPIUserAgent = "miNative PC/Normal Windows_NT/10.0.19045 SDKV/1.0.0 DEVT/PC DEVS/Windows APP/miaccount_desktop APPV/0.1.0"
const mimoSessionRequired = "Weekly quota requires Xiaomi account session. API key alone is insufficient."

// xiaomi mirrors getXiaomiMimoUsage (open-sse/services/usage/xiaomi-mimo.js:22-125).
// With an account credential the weekly quota is read through the account
// session (mimoAccount.js:246-264); if that yields no finite percent, or there
// is no account credential, the key path reads the aistudio endpoint. MiMo
// Desktop's cookie store is never read here: #296 seals its session into the
// connection only on a consented discovery, and the pin's read-time fallback
// (mimoAccount.js:181-185) is the open parity gap #299.
func (r apiKeyReader) xiaomi(ctx context.Context, credential quota.Credential) (quota.State, error) {
	// Bound the SSO exchange and key fallback together, beyond Client.call’s per-hop deadline.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if credential.ProviderData["mimoPassToken"] != "" {
		if state, read, err := r.mimoAccountUsage(ctx, credential.ProviderData); read {
			return state, err
		}
	}
	// `accessToken || providerSpecificData.apiKey`, then trimmed (xiaomi-mimo.js:47-58).
	key := credential.AccessToken
	if key == "" {
		key = credential.ProviderData["apiKey"]
	}
	if key = shared.Trim(key); key == "" {
		return failure("Xiaomi MiMo Desktop not connected. Add credentials to view usage.")
	}
	reply, err := r.callWithin(ctx, mimoUsageTimeout, http.MethodGet, "https://aistudio.xiaomimimo.com/open-apis/v1/user/usage", map[string]string{
		"Authorization": "Bearer " + key, "X-Mimo-Source": "mimocode-cli", "Accept": "application/json",
	}, nil)
	if err != nil {
		return failure("Xiaomi MiMo Desktop usage request failed.")
	}
	if reply.status == http.StatusUnauthorized {
		// The key cannot read the weekly quota: unsupported, not an outage.
		return quota.State{Plan: mimoPlan, Message: mimoSessionRequired, ObservedAt: r.now()}, nil
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("Usage API error (%d)", reply.status)
	}
	data, ok := mimoData(reply.body)
	if !ok {
		return failure("Usage endpoint returned unexpected response.")
	}
	percent, ok := shared.IsNumber(shared.Field(data, "percent"))
	if !ok {
		return quota.State{Plan: mimoPlan, Message: "Usage data missing percent field.", ObservedAt: r.now()}, nil
	}
	// The key path reads resetDate only (xiaomi-mimo.js:101-108).
	return r.state(mimoPlan, mimoWeekly(percent, mimoResetDate(shared.Field(data, "resetDate")))), nil
}

const mimoPlan = "Xiaomi MiMo Desktop"

// mimoAccountUsage is the account path. read is false when the pin would move
// on to the key: no session, or a usage answer without a finite percent.
func (r apiKeyReader) mimoAccountUsage(ctx context.Context, account map[string]string) (state quota.State, read bool, err error) {
	cookie, err := r.mimo.cookie(ctx, account["mimoPassToken"], r.now, func() (string, error) { return r.mimoSession(ctx, account) })
	if err != nil {
		return quota.State{}, false, nil
	}
	reply, err := r.callWithin(ctx, mimoUsageTimeout, http.MethodGet, mimoAccountBase+"/api/user/usage", map[string]string{
		"User-Agent": mimoAPIUserAgent, "Cookie": cookie, "Accept": "application/json",
	}, nil)
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return quota.State{}, false, nil
	}
	data, ok := mimoData(reply.body)
	if !ok {
		return quota.State{}, false, nil
	}
	percent, ok := shared.IsNumber(shared.Field(data, "percent"))
	if !ok {
		return quota.State{}, false, nil
	}
	// A positive numeric resetAt is Unix seconds and wins over resetDate
	// (xiaomi-mimo.js:30-36). One past the Date range throws outside the pin's
	// try, so the whole read fails.
	reset := mimoResetDate(shared.Field(data, "resetDate"))
	if seconds, ok := shared.IsNumber(shared.Field(data, "resetAt")); ok && seconds > 0 {
		if math.Abs(seconds*1000) > jsMaxTime {
			state, err = failure("Xiaomi MiMo usage reset is out of range")
			return state, true, err
		}
		at := time.UnixMilli(int64(seconds * 1000)).UTC()
		reset = &at
	}
	return r.state(mimoPlan, mimoWeekly(percent, reset)), true, nil
}

// mimoData is `data.data` of a usage answer whose `code` is the number 0;
// anything else (not JSON, another code, a falsy data) is unexpected.
func mimoData(body []byte) (json.RawMessage, bool) {
	payload, ok := jsonBody(body)
	if !ok {
		return nil, false
	}
	code, ok := shared.IsNumber(shared.Field(payload, "code"))
	data := shared.Field(payload, "data")
	return data, ok && code == 0 && shared.Truthy(data)
}

// mimoWeekly is the one Weekly window: `percent` is the remaining percentage,
// rounded and clamped to [0, 100].
func mimoWeekly(percent float64, reset *time.Time) quota.Window {
	return percentWindow("Weekly", math.Max(0, math.Min(100, jsRound(percent))), reset)
}

// mimoResetDate is `new Date(`${resetDate}T00:00:00Z`)` for a string resetDate:
// an ISO date (year, year-month or full date) at UTC midnight.
func mimoResetDate(raw json.RawMessage) *time.Time {
	text, ok := stringValue(raw)
	if !ok || text == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout+"T15:04:05Z07:00", text+"T00:00:00Z"); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

// mimoUsageTimeout bounds each usage read: the reference sets
// AbortSignal.timeout(10000) on both the account-session read
// (open-sse/shared/mimoAccount.js:252-254) and the API-key fallback
// (xiaomi-mimo.js:53-62). The SSO hops before them keep the shared bound.
const mimoUsageTimeout = 10 * time.Second

// mimoSessionTTL is COOKIE_TTL_MS (mimoAccount.js:29).
const mimoSessionTTL = 30 * time.Minute

// mimoSessions is the reference's session cache (mimoAccount.js:31-34,181-218):
// one service cookie per passToken, kept for 30 minutes from the end of its
// handshake and shared by concurrent reads while a handshake is in flight. A
// failed handshake is not kept, and a usage read that fails does not drop the
// session (the reference drops it only from Preview inference, which is
// unwired here). The key is the passToken's SHA-256, so the cache holds no
// second copy of the credential; the cookie lives in memory only.
type mimoSessions struct {
	mu      sync.Mutex
	entries map[string]*mimoEntry
}

type mimoEntry struct {
	cookie string
	err    error
	at     time.Time
	// done is open while the handshake runs.
	done chan struct{}
}

func newMimoSessions() *mimoSessions { return &mimoSessions{entries: map[string]*mimoEntry{}} }

func (c *mimoSessions) cookie(ctx context.Context, passToken string, now func() time.Time, handshake func() (string, error)) (string, error) {
	if c == nil {
		return handshake()
	}
	sum := sha256.Sum256([]byte(passToken))
	key := hex.EncodeToString(sum[:])
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok {
		if done := entry.done; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				return entry.cookie, entry.err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if now().Sub(entry.at) < mimoSessionTTL {
			c.mu.Unlock()
			return entry.cookie, nil
		}
	}
	entry := &mimoEntry{done: make(chan struct{})}
	c.entries[key] = entry
	c.mu.Unlock()
	cookie, err := handshake()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.cookie, entry.err = cookie, err
	close(entry.done)
	entry.done = nil
	if err != nil {
		delete(c.entries, key)
		return "", err
	}
	entry.at = now()
	for other, kept := range c.entries {
		if kept.done == nil && entry.at.Sub(kept.at) >= mimoSessionTTL {
			delete(c.entries, other)
		}
	}
	return cookie, nil
}

// encodeURIComponent percent-encodes every UTF-8 byte except ASCII letters,
// digits and -_.!~*'().
func encodeURIComponent(text string) string {
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

// Account redirects are untrusted: passToken must only reach the pinned SSO hosts.
func mimoURL(raw, host, path string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != host || u.User != nil || (path != "" && u.Path != path) || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("invalid Xiaomi account redirect")
	}
	return u, nil
}

func (r apiKeyReader) mimoSession(ctx context.Context, data map[string]string) (string, error) {
	jar := map[string]string{}
	for name, key := range map[string]string{"passToken": "mimoPassToken", "userId": "mimoUserId", "cUserId": "mimoCUserId"} {
		value := data[key]
		if !credentials.ValidMimoCookieValue(value) {
			return "", errors.New("invalid Xiaomi account credential")
		}
		jar[name] = value
	}
	// Only the two JSON SSO hops send Accept (mimoAccount.js:126,137,147,155,165).
	call := func(endpoint, ua string, accept bool) (response, error) {
		request := &http.Request{Header: http.Header{}}
		for _, name := range []string{"passToken", "userId", "cUserId", "serviceToken", "mimopc_ph", "mimopc_slh"} {
			if jar[name] != "" {
				request.AddCookie(&http.Cookie{Name: name, Value: jar[name]})
			}
		}
		headers := map[string]string{"Cookie": request.Header.Get("Cookie"), "User-Agent": ua}
		if accept {
			headers["Accept"] = "application/json"
		}
		return r.call(ctx, http.MethodGet, endpoint, headers, nil)
	}
	absorb := func(reply response) {
		for _, header := range reply.headers.Values("Set-Cookie") {
			pair, _, _ := strings.Cut(header, ";")
			name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			value = strings.Trim(value, "\"")
			if !ok || value == "" || !credentials.ValidMimoCookieValue(value) {
				continue
			}
			switch name {
			case "serviceToken", "mimopc_ph", "mimopc_slh", "userId":
				jar[name] = value
			}
		}
	}
	first, err := call(mimoAccountBase+"/api/user/xiaomi/me", mimoAPIUserAgent, false)
	if err != nil {
		return "", err
	}
	redirect, err := mimoURL(first.headers.Get("Location"), "account.xiaomi.com", "/pass/serviceLogin")
	if err != nil {
		return "", err
	}
	callback := redirect.Query().Get("callback")
	if _, err := mimoURL(callback, "mimo-server-cn.xiaomimimo.com", "/api/sts"); err != nil {
		return "", err
	}
	sso, err := call("https://account.xiaomi.com/pass/serviceLogin?sid=passportapi&_json=true", "MiClaw/1.0", true)
	if err != nil {
		return "", err
	}
	var login struct {
		Location string          `json:"location"`
		Nonce    json.RawMessage `json:"nonce"`
		Security string          `json:"ssecurity"`
	}
	if decode([]byte(strings.TrimPrefix(string(sso.body), "&&&START&&&")), &login) != nil {
		return "", errors.New("invalid Xiaomi SSO response")
	}
	location, err := mimoURL(login.Location, "account.xiaomi.com", "")
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
	// The pin appends the signature to the location as sent
	// (mimoAccount.js:96-99,145-146), so its own parameters keep their order and
	// spelling.
	signedURL, err := mimoURL(login.Location+"&clientSign="+encodeURIComponent(base64.StdEncoding.EncodeToString(digest[:])), "account.xiaomi.com", "")
	if err != nil {
		return "", err
	}
	account, err := call(signedURL.String(), "MiClaw/1.0", false)
	if err != nil {
		return "", err
	}
	absorb(account)
	serviceURL, err := mimoURL("https://account.xiaomi.com/pass/serviceLogin?sid=mimopc&callback="+encodeURIComponent(callback)+"&_json=true", "account.xiaomi.com", "/pass/serviceLogin")
	if err != nil {
		return "", err
	}
	service, err := call(serviceURL.String(), "MiClaw/1.0", true)
	if err != nil {
		return "", err
	}
	var ticket struct {
		Location string `json:"location"`
	}
	if decode([]byte(strings.TrimPrefix(string(service.body), "&&&START&&&")), &ticket) != nil {
		return "", errors.New("invalid Xiaomi service response")
	}
	sts, err := mimoURL(ticket.Location, "mimo-server-cn.xiaomimimo.com", "/api/sts")
	if err != nil {
		return "", err
	}
	absorb(service)
	// The passportapi token cannot substitute for a missing mimopc session cookie.
	stamped, err := call(sts.String(), mimoAPIUserAgent, false)
	if err != nil {
		return "", err
	}
	delete(jar, "serviceToken")
	absorb(stamped)
	if jar["serviceToken"] == "" {
		return "", errors.New("missing Xiaomi service session")
	}
	request := &http.Request{Header: http.Header{}}
	for _, name := range []string{"serviceToken", "mimopc_ph", "mimopc_slh", "userId"} {
		if jar[name] != "" {
			request.AddCookie(&http.Cookie{Name: name, Value: jar[name]})
		}
	}
	return request.Header.Get("Cookie"), nil
}
