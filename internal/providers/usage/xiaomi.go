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

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

const mimoAccountBase = "https://mimo-server-cn.xiaomimimo.com"
const mimoAPIUserAgent = "miNative PC/Normal Windows_NT/10.0.19045 SDKV/1.0.0 DEVT/PC DEVS/Windows APP/miaccount_desktop APPV/0.1.0"
const mimoSessionRequired = "Weekly quota requires Xiaomi account session. API key alone is insufficient."

func (r apiKeyReader) xiaomi(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if client, ok := r.HTTP.(*http.Client); ok {
		manual := *client
		manual.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		r.HTTP = &manual
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	endpoint := "https://aistudio.xiaomimimo.com/open-apis/v1/user/usage"
	headers := map[string]string{"Accept": "application/json", "X-Mimo-Source": "mimocode-cli"}
	if credential.ProviderData["mimoPassToken"] != "" {
		cookie, err := r.mimoSession(ctx, credential.ProviderData)
		if err != nil {
			return failure(mimoSessionRequired)
		}
		endpoint = mimoAccountBase + "/api/user/usage"
		headers["Cookie"] = cookie
		headers["User-Agent"] = mimoAPIUserAgent
	} else {
		key := strings.TrimSpace(credential.AccessToken)
		if key == "" {
			key = strings.TrimSpace(credential.ProviderData["apiKey"])
		}
		if key == "" {
			return failure("Xiaomi MiMo Desktop not connected. Add credentials to view usage.")
		}
		headers["Authorization"] = "Bearer " + key
	}
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
		Code *int `json:"code"`
		Data struct {
			Percent   *float64 `json:"percent"`
			ResetDate string   `json:"resetDate"`
			ResetAt   float64  `json:"resetAt"`
		} `json:"data"`
	}
	if decode(reply.body, &body) != nil || body.Code == nil || *body.Code != 0 {
		return failure("Usage endpoint returned unexpected response.")
	}
	if body.Data.Percent == nil {
		return failure("Usage data missing percent field.")
	}
	remaining := math.Max(0, math.Min(100, math.Round(*body.Data.Percent)))
	reset := resetAt(body.Data.ResetDate)
	if body.Data.ResetAt > 0 {
		reset = resetAt(body.Data.ResetAt)
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
		for _, c := range value {
			if c < 0x21 || c > 0x7e || strings.ContainsRune("\";,\\", c) {
				return "", errors.New("invalid Xiaomi account credential")
			}
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
		response := http.Response{Header: reply.headers}
		for _, cookie := range response.Cookies() {
			switch cookie.Name {
			case "serviceToken", "mimopc_ph", "mimopc_slh", "userId":
				jar[cookie.Name] = cookie.Value
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
	location.RawQuery = query.Encode()
	account, err := call(location.String(), "MiClaw/1.0")
	if err != nil {
		return "", err
	}
	absorb(account)
	service, err := call("https://account.xiaomi.com/pass/serviceLogin?"+url.Values{"sid": {"mimopc"}, "callback": {callback}, "_json": {"true"}}.Encode(), "MiClaw/1.0")
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
