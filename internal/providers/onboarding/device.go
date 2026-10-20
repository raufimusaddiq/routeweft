package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// ErrRateLimited means the provider has too many pending authorizations.
var ErrRateLimited = errors.New("provider device authorization is rate limited; try again later")

// deviceCodeResponse is the RFC 8628 device authorization payload. Providers add
// their own private fields, which each flow reads from the raw document.
type deviceCodeResponse struct {
	DeviceCode string `json:"device_code"`
	// The poll response reuses this shape, so the token fields live here too.
	AccessToken             string          `json:"access_token"`
	RefreshToken            string          `json:"refresh_token"`
	IDToken                 string          `json:"id_token"`
	UserCode                string          `json:"user_code"`
	VerificationURI         string          `json:"verification_uri"`
	VerificationURIComplete string          `json:"verification_uri_complete"`
	ExpiresIn               json.RawMessage `json:"expires_in"`
	Interval                json.RawMessage `json:"interval"`
	Error                   string          `json:"error"`
	ErrorDescription        string          `json:"error_description"`
}

func (d deviceCodeResponse) request() AuthRequest {
	expires, _ := numberFromJSON(d.ExpiresIn)
	interval, _ := numberFromJSON(d.Interval)
	if interval <= 0 {
		interval = 5
	}
	// Some providers return only verification_uri, so the complete form falls back
	// to it rather than leaving the operator with no link.
	verification := d.VerificationURIComplete
	if verification == "" {
		verification = d.VerificationURI
	}
	return AuthRequest{
		DeviceCode:      d.DeviceCode,
		UserCode:        d.UserCode,
		VerificationURL: verification,
		Interval:        time.Duration(interval) * time.Second,
		ExpiresIn:       time.Duration(expires) * time.Second,
	}
}

// postFormDocument sends a form POST and decodes the JSON reply, used by every
// device authorization and device token endpoint.
func postFormDocument(ctx context.Context, client HTTPClient, endpoint string, form url.Values, headers map[string]string, target any) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("device flow requires an HTTP client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	payload, err := readLimited(response)
	if err != nil {
		return response.StatusCode, err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return response.StatusCode, fmt.Errorf("device endpoint returned an unusable payload")
	}
	return response.StatusCode, nil
}

// AccessToken exposes the token from the device payload, which reuses the token
// field names.
// githubFlow mirrors src/lib/oauth/providers/github.js. GitHub issues a device
// code, then the Copilot token and user identity are fetched post-exchange.
func githubFlow() Flow {
	const (
		clientID      = "Iv1.b507a08c87ecfe98"
		deviceCodeURL = "https://github.com/login/device/code"
		tokenURL      = "https://github.com/login/oauth/access_token"
		userInfoURL   = "https://api.github.com/user"
		copilotURL    = "https://api.github.com/copilot_internal/v2/token"
	)
	return Flow{
		Provider:       "github",
		RedirectSource: RedirectUnused,
		Type:           FlowDeviceCode,
		Completion:     CompletionToken,
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			var document deviceCodeResponse
			status, err := postFormDocument(ctx, p.Client, deviceCodeURL, url.Values{
				"client_id": {clientID},
				"scope":     {"read:user"},
			}, nil, &document)
			if err != nil {
				return AuthRequest{}, err
			}
			if status < 200 || status >= 300 {
				return AuthRequest{}, fmt.Errorf("device code request returned HTTP %d", status)
			}
			return document.request(), nil
		},
		Poll: func(ctx context.Context, p Params) (PollResult, error) {
			var document deviceCodeResponse
			status, err := postFormDocument(ctx, p.Client, tokenURL, url.Values{
				"client_id":   {clientID},
				"device_code": {p.Input},
				"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			}, nil, &document)
			if err != nil {
				return PollResult{}, err
			}
			if document.Error == "authorization_pending" || document.Error == "slow_down" {
				return PollResult{Pending: true}, nil
			}
			if document.Error != "" {
				return PollResult{Err: fmt.Errorf("github authorization failed: %s", document.Error)}, nil
			}
			if status < 200 || status >= 300 || document.AccessToken == "" {
				return PollResult{Err: fmt.Errorf("github authorization returned no token")}, nil
			}
			providerData := map[string]string{}
			identity := ""
			var user struct {
				Login string `json:"login"`
				Name  string `json:"name"`
				Email string `json:"email"`
				ID    int    `json:"id"`
			}
			headers := map[string]string{
				"Authorization":        "Bearer " + document.AccessToken,
				"X-GitHub-Api-Version": "2022-11-28",
				"User-Agent":           "GitHubCopilotChat/0.26.7",
			}
			if err := getJSON(ctx, p.Client, userInfoURL, headers, &user); err == nil {
				identity = user.Email
				if identity == "" {
					identity = user.Login
				}
				if user.Login != "" {
					providerData["githubLogin"] = user.Login
				}
				if user.ID != 0 {
					providerData["githubUserId"] = fmt.Sprintf("%d", user.ID)
				}
			}
			// The Copilot token is what inference actually sends; without it the
			// connection is unusable, so capture it now.
			var copilot struct {
				Token     string          `json:"token"`
				ExpiresAt json.RawMessage `json:"expires_at"`
			}
			if err := getJSON(ctx, p.Client, copilotURL, headers, &copilot); err == nil && copilot.Token != "" {
				providerData["copilotToken"] = copilot.Token
				providerData["copilotTokenExpiresAt"] = copilotExpiry(copilot.ExpiresAt, p.now())
			}
			if len(providerData) == 0 {
				providerData = nil
			}
			return PollResult{Done: true, Result: Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  document.AccessToken,
					RefreshToken: document.RefreshToken,
					Expiry:       expiryFrom(document.ExpiresIn, p.now()),
					ProviderData: providerData,
				},
			}}, nil
		},
	}
}

func expiryFrom(raw json.RawMessage, now time.Time) time.Time {
	seconds, ok := numberFromJSON(raw)
	if !ok || seconds <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

// grokCLIFlow mirrors src/lib/oauth/providers/grok-cli.js: xAI device code, then
// a best-effort profile read for identity.
func grokCLIFlow() Flow {
	const (
		clientID      = "b1a00492-073a-47ea-816f-4c329264a828"
		deviceCodeURL = "https://auth.x.ai/oauth2/device/code"
		tokenURL      = "https://auth.x.ai/oauth2/token"
		userURL       = "https://cli-chat-proxy.grok.com/v1/user"
		userAgent     = "grok-cli/0.2.4"
	)
	scope := "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"
	return Flow{
		Provider:       "grok-cli",
		RedirectSource: RedirectUnused,
		Type:           FlowDeviceCode,
		Completion:     CompletionToken,
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			var document deviceCodeResponse
			status, err := postFormDocument(ctx, p.Client, deviceCodeURL, url.Values{
				"client_id": {clientID},
				"scope":     {scope},
				"referrer":  {"grok-build"},
			}, map[string]string{"User-Agent": userAgent}, &document)
			if err != nil {
				return AuthRequest{}, err
			}
			if status < 200 || status >= 300 {
				return AuthRequest{}, fmt.Errorf("device code request returned HTTP %d", status)
			}
			return document.request(), nil
		},
		Poll: func(ctx context.Context, p Params) (PollResult, error) {
			var document deviceCodeResponse
			status, err := postFormDocument(ctx, p.Client, tokenURL, url.Values{
				"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
				"device_code": {p.Input},
				"client_id":   {clientID},
			}, map[string]string{"User-Agent": userAgent}, &document)
			if err != nil {
				return PollResult{}, err
			}
			if document.Error == "authorization_pending" || document.Error == "slow_down" {
				return PollResult{Pending: true}, nil
			}
			if document.Error != "" {
				return PollResult{Err: fmt.Errorf("grok-cli authorization failed: %s", document.Error)}, nil
			}
			if status < 200 || status >= 300 || document.AccessToken == "" {
				return PollResult{Err: fmt.Errorf("grok-cli authorization returned no token")}, nil
			}
			providerData := map[string]string{"authMethod": "device_code"}
			if document.IDToken != "" {
				providerData["idToken"] = document.IDToken
			}
			identity := ""
			if claims := jwtClaims(document.IDToken); claims != nil {
				if value, ok := claims["email"].(string); ok {
					identity = value
				}
			}
			// The profile call is best-effort: it only adds identity and tier.
			var user struct {
				Email            string `json:"email"`
				UserID           string `json:"userId"`
				PrincipalID      string `json:"principalId"`
				SubscriptionTier string `json:"subscriptionTier"`
			}
			if err := getJSON(ctx, p.Client, userURL, map[string]string{
				"Authorization":    "Bearer " + document.AccessToken,
				"User-Agent":       userAgent,
				"x-xai-token-auth": "xai-grok-cli",
			}, &user); err == nil {
				if identity == "" {
					identity = user.Email
				}
				if user.Email != "" {
					providerData["email"] = user.Email
				}
				if user.UserID != "" {
					providerData["userId"] = user.UserID
				}
				if user.SubscriptionTier != "" {
					providerData["subscriptionTier"] = user.SubscriptionTier
				}
			}
			return PollResult{Done: true, Result: Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  document.AccessToken,
					RefreshToken: document.RefreshToken,
					Expiry:       expiryFrom(document.ExpiresIn, p.now()),
					ProviderData: providerData,
				},
			}}, nil
		},
	}
}

// kimiFlow mirrors src/lib/oauth/providers/kimi.js: device code with a stable
// per-attempt device id that must be stored for refresh and quota reads.
func kimiFlow() Flow {
	const (
		clientID        = "17e5f671-d194-4dfb-9706-5516cb48c098"
		deviceCodeURL   = "https://auth.kimi.com/api/oauth/device_authorization"
		tokenURL        = "https://auth.kimi.com/api/oauth/token"
		authorizeDevice = "https://www.kimi.com/code/authorize_device"
	)
	return Flow{
		Provider:       "kimi",
		RedirectSource: RedirectUnused,
		Type:           FlowDeviceCode,
		Completion:     CompletionToken,
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			deviceID, err := randomToken(16)
			if err != nil {
				return AuthRequest{}, err
			}
			headers := kimiDeviceHeaders(deviceID)
			var document deviceCodeResponse
			status, err := postFormDocument(ctx, p.Client, deviceCodeURL, url.Values{
				"client_id": {clientID},
			}, headers, &document)
			if err != nil {
				return AuthRequest{}, err
			}
			if status < 200 || status >= 300 {
				return AuthRequest{}, fmt.Errorf("device code request returned HTTP %d", status)
			}
			request := document.request()
			if request.VerificationURL == "" {
				request.VerificationURL = authorizeDevice
			}
			if document.VerificationURIComplete == "" && document.UserCode != "" {
				request.VerificationURL = authorizeDevice + "?user_code=" + url.QueryEscape(document.UserCode)
			}
			request.Extra = map[string]string{"deviceId": deviceID}
			return request, nil
		},
		Poll: func(ctx context.Context, p Params) (PollResult, error) {
			deviceID := p.Meta["deviceId"]
			var document deviceCodeResponse
			status, err := postFormDocument(ctx, p.Client, tokenURL, url.Values{
				"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
				"client_id":   {clientID},
				"device_code": {p.Input},
			}, kimiDeviceHeaders(deviceID), &document)
			if err != nil {
				return PollResult{}, err
			}
			if document.Error == "authorization_pending" || document.Error == "slow_down" {
				return PollResult{Pending: true}, nil
			}
			if document.Error != "" {
				return PollResult{Err: fmt.Errorf("kimi authorization failed: %s", document.Error)}, nil
			}
			if status < 200 || status >= 300 || document.AccessToken == "" {
				return PollResult{Err: fmt.Errorf("kimi authorization returned no token")}, nil
			}
			providerData := map[string]string{"authMethod": "device_code"}
			if deviceID != "" {
				providerData["deviceId"] = deviceID
			}
			return PollResult{Done: true, Result: Result{
				AuthKind: credentials.AuthOAuth,
				Secret: credentials.Secret{
					AccessToken:  document.AccessToken,
					RefreshToken: document.RefreshToken,
					Expiry:       expiryFrom(document.ExpiresIn, p.now()),
					ProviderData: providerData,
				},
			}}, nil
		},
	}
}

// kimiDeviceHeaders mirrors the reference's buildKimiHeaders: the device id must
// be stable across the whole OAuth session.
func kimiDeviceHeaders(deviceID string) map[string]string {
	if strings.TrimSpace(deviceID) == "" {
		deviceID = "routeweft"
	}
	return map[string]string{
		"X-Msh-Platform":    "routeweft",
		"X-Msh-Device-Id":   deviceID,
		"X-Msh-Device-Name": "routeweft",
	}
}

// kilocodeFlow mirrors src/lib/oauth/providers/kilocode.js: a custom device
// handshake polled by code, with status codes carrying the state.
func kilocodeFlow() Flow {
	const (
		initiateURL = "https://api.kilo.ai/api/device-auth/codes"
		pollBase    = "https://api.kilo.ai/api/device-auth/codes"
	)
	return Flow{
		Provider:       "kilocode",
		RedirectSource: RedirectUnused,
		Type:           FlowDeviceCode,
		Completion:     CompletionToken,
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			var document struct {
				Code            string `json:"code"`
				VerificationURL string `json:"verificationUrl"`
				ExpiresIn       int    `json:"expiresIn"`
			}
			status, err := postJSONTargetStatus(ctx, p.Client, initiateURL, "{}", &document)
			// Rate limiting is a distinct, retryable state, not a generic failure,
			// so it is classified before the body is required to parse: a 429 body
			// is often not JSON at all.
			if status == http.StatusTooManyRequests {
				return AuthRequest{}, ErrRateLimited
			}
			if err != nil {
				return AuthRequest{}, err
			}
			if status < 200 || status >= 300 {
				return AuthRequest{}, fmt.Errorf("device auth initiation returned HTTP %d", status)
			}
			expires := document.ExpiresIn
			if expires <= 0 {
				expires = 300
			}
			return AuthRequest{
				DeviceCode:      document.Code,
				UserCode:        document.Code,
				VerificationURL: document.VerificationURL,
				Interval:        3 * time.Second,
				ExpiresIn:       time.Duration(expires) * time.Second,
			}, nil
		},
		Poll: func(ctx context.Context, p Params) (PollResult, error) {
			status, payload, err := getDocument(ctx, p.Client, pollBase+"/"+url.PathEscape(p.Input), nil)
			if err != nil {
				return PollResult{}, err
			}
			switch status {
			case http.StatusAccepted:
				return PollResult{Pending: true}, nil
			case http.StatusForbidden:
				return PollResult{Denied: true, Err: fmt.Errorf("kilocode authorization denied")}, nil
			case http.StatusGone:
				return PollResult{Expired: true, Err: fmt.Errorf("kilocode authorization code expired")}, nil
			}
			if status < 200 || status >= 300 {
				return PollResult{Err: fmt.Errorf("kilocode poll returned HTTP %d", status)}, nil
			}
			var document struct {
				Status    string `json:"status"`
				Token     string `json:"token"`
				UserEmail string `json:"userEmail"`
			}
			if err := json.Unmarshal(payload, &document); err != nil {
				return PollResult{Err: fmt.Errorf("kilocode poll returned an unusable payload")}, nil
			}
			if document.Status != "approved" || document.Token == "" {
				return PollResult{Pending: true}, nil
			}
			providerData := map[string]string{}
			// The organization id is required by the inference headers.
			var profile struct {
				Organizations []struct {
					ID string `json:"id"`
				} `json:"organizations"`
			}
			if err := getJSON(ctx, p.Client, "https://api.kilo.ai/api/profile", map[string]string{
				"Authorization": "Bearer " + document.Token,
			}, &profile); err == nil && len(profile.Organizations) > 0 && profile.Organizations[0].ID != "" {
				providerData["orgId"] = profile.Organizations[0].ID
			}
			if len(providerData) == 0 {
				providerData = nil
			}
			return PollResult{Done: true, Result: Result{
				AuthKind: credentials.AuthOAuth,
				Secret: credentials.Secret{
					AccessToken:  document.Token,
					ProviderData: providerData,
				},
			}}, nil
		},
	}
}

// codebuddyFlow mirrors codebuddy-cn.js and codebuddy-intl.js: a browser state
// handshake polled by state, with code 11217 meaning pending.
func codebuddyFlow(provider, baseURL, domain, userAgent, platform string) Flow {
	stateURL := baseURL + "/v2/plugin/auth/state"
	tokenURL := baseURL + "/v2/plugin/auth/token"
	return Flow{
		Provider:       provider,
		RedirectSource: RedirectUnused,
		Type:           FlowDeviceCode,
		Completion:     CompletionToken,
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			headers := map[string]string{
				"User-Agent":         userAgent,
				"X-Requested-With":   "XMLHttpRequest",
				"X-Domain":           domain,
				"X-No-Authorization": "true",
				"X-No-User-Id":       "true",
				"X-Product":          "SaaS",
			}
			var document struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
				Data struct {
					State   string `json:"state"`
					AuthURL string `json:"authUrl"`
				} `json:"data"`
			}
			status, err := postJSONTargetWithHeaders(ctx, p.Client, stateURL+"?platform="+url.QueryEscape(platform), headers, "{}", &document)
			if err != nil {
				return AuthRequest{}, err
			}
			if status < 200 || status >= 300 {
				return AuthRequest{}, fmt.Errorf("codebuddy state request returned HTTP %d", status)
			}
			if document.Code != 0 || document.Data.State == "" || document.Data.AuthURL == "" {
				return AuthRequest{}, fmt.Errorf("codebuddy state request failed: %s", document.Msg)
			}
			return AuthRequest{
				// CodeBuddy identifies the attempt by state, which is what the
				// poll endpoint takes.
				DeviceCode:      document.Data.State,
				VerificationURL: document.Data.AuthURL,
				Interval:        5 * time.Second,
				ExpiresIn:       5 * time.Minute,
				Extra:           map[string]string{"state": document.Data.State},
			}, nil
		},
		Poll: func(ctx context.Context, p Params) (PollResult, error) {
			headers := map[string]string{
				"User-Agent":           userAgent,
				"X-Requested-With":     "XMLHttpRequest",
				"X-Domain":             domain,
				"X-No-Authorization":   "true",
				"X-No-User-Id":         "true",
				"X-No-Enterprise-Id":   "true",
				"X-No-Department-Info": "true",
				"X-Product":            "SaaS",
			}
			status, payload, err := getDocument(ctx, p.Client, tokenURL+"?state="+url.QueryEscape(p.Input), headers)
			if err != nil {
				return PollResult{}, err
			}
			if status < 200 || status >= 300 {
				return PollResult{Err: fmt.Errorf("codebuddy poll returned HTTP %d", status)}, nil
			}
			var document struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
				Data struct {
					AccessToken  string          `json:"accessToken"`
					RefreshToken string          `json:"refreshToken"`
					ExpiresIn    json.RawMessage `json:"expiresIn"`
				} `json:"data"`
			}
			if err := json.Unmarshal(payload, &document); err != nil {
				return PollResult{Err: fmt.Errorf("codebuddy poll returned an unusable payload")}, nil
			}
			// 11217 is the provider's "retry fetch token" code.
			if document.Code == 11217 {
				return PollResult{Pending: true}, nil
			}
			if document.Code != 0 || document.Data.AccessToken == "" {
				return PollResult{Err: fmt.Errorf("codebuddy authorization failed: %s", document.Msg)}, nil
			}
			return PollResult{Done: true, Result: Result{
				AuthKind: credentials.AuthOAuth,
				Secret: credentials.Secret{
					AccessToken:  document.Data.AccessToken,
					RefreshToken: document.Data.RefreshToken,
					Expiry:       expiryFrom(document.Data.ExpiresIn, p.now()),
				},
			}}, nil
		},
	}
}

// postJSONTargetStatus posts a JSON body and returns the status and decoded
// document, for handshakes whose status codes carry meaning.
func postJSONTargetStatus(ctx context.Context, client HTTPClient, endpoint, body string, target any) (int, error) {
	return postJSONTargetWithHeaders(ctx, client, endpoint, nil, body, target)
}

func postJSONTargetWithHeaders(ctx context.Context, client HTTPClient, endpoint string, headers map[string]string, body string, target any) (int, error) {
	if client == nil {
		return 0, fmt.Errorf("request requires an HTTP client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	payload, err := readLimited(response)
	if err != nil {
		return response.StatusCode, err
	}
	if target != nil {
		if err := json.Unmarshal(payload, target); err != nil {
			return response.StatusCode, fmt.Errorf("endpoint returned an unusable payload")
		}
	}
	return response.StatusCode, nil
}

// getDocument reads a document and returns the status alongside it.
func getDocument(ctx context.Context, client HTTPClient, endpoint string, headers map[string]string) (int, []byte, error) {
	if client == nil {
		return 0, nil, fmt.Errorf("request requires an HTTP client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	payload, err := readLimited(response)
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, payload, nil
}

// readLimited reads a response body under the shared size cap.
func readLimited(response *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}

// Missing or invalid Copilot expiry gets a bounded TTL, never a permanent token.
func copilotExpiry(raw json.RawMessage, now time.Time) string {
	var seconds json.Number
	if err := json.Unmarshal(raw, &seconds); err == nil {
		if expires, err := seconds.Int64(); err == nil && expires > 0 {
			return fmt.Sprint(expires)
		}
	}
	return fmt.Sprint(now.Add(10 * credentials.RefreshLead).Unix())
}
