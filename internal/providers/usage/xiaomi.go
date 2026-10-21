package usage

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

const mimoAccountBase = "https://mimo-server-cn.xiaomimimo.com"
const mimoAPIUserAgent = "miNative PC/Normal Windows_NT/10.0.19045 SDKV/1.0.0 DEVT/PC DEVS/Windows APP/miaccount_desktop APPV/0.1.0"
const mimoSessionRequired = "Weekly quota requires Xiaomi account session. API key alone is insufficient."

func (r apiKeyReader) xiaomi(ctx context.Context, credential quota.Credential) (quota.State, error) {
	// Bound the SSO exchange and key fallback together, beyond Client.call’s per-hop deadline.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	key := strings.TrimSpace(credential.AccessToken)
	if key == "" {
		key = strings.TrimSpace(credential.ProviderData["apiKey"])
	}
	if credential.ProviderData["mimoPassToken"] != "" {
		cookie, err := r.mimoSession(ctx, credential.ProviderData)
		if err == nil {
			state, err := r.xiaomiUsage(ctx, mimoAccountBase+"/api/user/usage", map[string]string{
				"Cookie": cookie, "User-Agent": mimoAPIUserAgent, "Accept": "application/json",
			})
			if err == nil {
				return state, nil
			}
		}
		if key == "" {
			return failure(mimoSessionRequired)
		}
	}
	if key == "" {
		return failure("Xiaomi MiMo Desktop not connected. Add credentials to view usage.")
	}
	return r.xiaomiUsage(ctx, "https://aistudio.xiaomimimo.com/open-apis/v1/user/usage", map[string]string{
		"Authorization": "Bearer " + key, "X-Mimo-Source": "mimocode-cli", "Accept": "application/json",
	})
}

func (r apiKeyReader) xiaomiUsage(ctx context.Context, endpoint string, headers map[string]string) (quota.State, error) {
	reply, err := r.call(ctx, http.MethodGet, endpoint, headers, nil)
	if err != nil {
		return failure("Xiaomi MiMo Desktop usage request failed.")
	}
	if reply.status == http.StatusUnauthorized {
		return failure(mimoSessionRequired)
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("Usage API error (%d)", reply.status)
	}
	var body struct {
		Code json.RawMessage `json:"code"`
		Data struct {
			Percent   json.RawMessage `json:"percent"`
			ResetDate string          `json:"resetDate"`
			ResetAt   json.RawMessage `json:"resetAt"`
		} `json:"data"`
	}
	if decode(reply.body, &body) != nil {
		return failure("Usage endpoint returned unexpected response.")
	}
	code, ok := number(body.Code)
	if !ok || code != 0 {
		return failure("Usage endpoint returned unexpected response.")
	}
	percent, ok := number(body.Data.Percent)
	if !ok || math.IsNaN(percent) || math.IsInf(percent, 0) {
		return failure("Usage data missing percent field.")
	}
	remaining := math.Max(0, math.Min(100, math.Round(percent)))
	reset := resetAt(body.Data.ResetDate)
	if seconds, ok := number(body.Data.ResetAt); ok && seconds > 0 {
		reset = resetAt(seconds)
	}
	return r.state("Xiaomi MiMo Desktop", percentWindow("Weekly", remaining, reset)), nil
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
	call := func(endpoint, ua string) (response, error) {
		request := &http.Request{Header: http.Header{}}
		for _, name := range []string{"passToken", "userId", "cUserId", "serviceToken", "mimopc_ph", "mimopc_slh"} {
			if jar[name] != "" {
				request.AddCookie(&http.Cookie{Name: name, Value: jar[name]})
			}
		}
		return r.call(ctx, http.MethodGet, endpoint, map[string]string{"Cookie": request.Header.Get("Cookie"), "User-Agent": ua, "Accept": "application/json"}, nil)
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
	first, err := call(mimoAccountBase+"/api/user/xiaomi/me", mimoAPIUserAgent)
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
	sso, err := call("https://account.xiaomi.com/pass/serviceLogin?sid=passportapi&_json=true", "MiClaw/1.0")
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
	query := location.Query()
	query.Set("clientSign", base64.StdEncoding.EncodeToString(digest[:]))
	signed := wire.Target{BaseURL: location.String(), Query: query.Encode()}
	signedURL, err := signed.URL(func(raw string) (*url.URL, error) {
		return mimoURL(raw, "account.xiaomi.com", "")
	})
	if err != nil {
		return "", err
	}
	account, err := call(signedURL.String(), "MiClaw/1.0")
	if err != nil {
		return "", err
	}
	absorb(account)
	serviceTarget := wire.Target{BaseURL: "https://account.xiaomi.com", Endpoint: "pass/serviceLogin", Query: url.Values{"sid": {"mimopc"}, "callback": {callback}, "_json": {"true"}}.Encode()}
	serviceURL, err := serviceTarget.URL(func(raw string) (*url.URL, error) {
		return mimoURL(raw, "account.xiaomi.com", "")
	})
	if err != nil {
		return "", err
	}
	service, err := call(serviceURL.String(), "MiClaw/1.0")
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
	stamped, err := call(sts.String(), mimoAPIUserAgent)
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
