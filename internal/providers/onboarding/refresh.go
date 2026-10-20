// Package onboarding also owns credential refresh, because a rotating token that
// is not persisted on rotation kills the connection later without a visible
// cause. The reference dispatches refresh per provider
// (open-sse/services/tokenRefresh.js REFRESH_HANDLERS); this file mirrors that
// table so every provider it can refresh here is refreshed here.
package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// Refresher rotates one connection's credential. It returns a new Secret or an
// error; an error means the connection needs reauthorization.
type Refresher func(ctx context.Context, current credentials.Secret) (credentials.Secret, error)

// RefreshResolver returns the refresher for one connection, or nil when the
// credential does not rotate (a static API key, a long-lived seat key).
//
// The provider set mirrors the reference refresh table. Providers whose
// reference refresh is explicitly a no-op (cursor, kilocode, zed, windsurf)
// deliberately return nil so a refresh is never attempted against an endpoint
// that does not exist.
func RefreshResolver(client HTTPClient, now func() time.Time) (func(credentials.Connection) Refresher, error) {
	// A nil client used to be forwarded into every token request, which then failed
	// per call — invisible, because the resolver itself looked healthy. It is
	// refused where it is supplied, and refused with an error: a library must not
	// panic on its caller's wiring mistake.
	if client == nil {
		return nil, ErrNoClient
	}
	if now == nil {
		now = time.Now
	}
	return func(connection credentials.Connection) Refresher {
		if !refreshable(connection.ProviderID) {
			return nil
		}
		if connection.Secret.RefreshToken == "" && !refreshWithoutToken(connection.ProviderID) {
			return nil
		}
		return refreshFor(connection, client, now)
	}, nil
}

// ErrNoClient means the caller supplied no HTTP client.
var ErrNoClient = errors.New("an HTTP client is required; without one every refresh fails at request time")

// refreshable names the providers with a refresh grant, mirroring the reference's
// REFRESH_HANDLERS table. A provider absent here has a long-lived credential, so
// no refresh is attempted and the connection is not failed by a refresh that
// could never succeed.
func refreshable(provider string) bool {
	switch provider {
	case "claude", "codex", "xai", "grok-cli", "gemini-cli", "antigravity", "iflow",
		"github", "kimi", "cline", "clinepass", "kiro", "codebuddy-cn", "codebuddy-intl",
		"trae":
		return true
	}
	return false
}

// refreshWithoutToken names providers that rotate without a refresh token: the
// GitHub Copilot token is re-minted from the GitHub access token. Zed is not here:
// it is absent from refreshable, so no refresh is attempted for it at all.
func refreshWithoutToken(provider string) bool {
	return provider == "github"
}

// refreshFor builds the provider's refresher.
func refreshFor(connection credentials.Connection, client HTTPClient, now func() time.Time) Refresher {
	provider := connection.ProviderID
	return func(ctx context.Context, current credentials.Secret) (credentials.Secret, error) {
		// ProviderData is read from the secret handed to this call, not captured at
		// registration: a rotation can change it, and a captured map would silently
		// refresh against the values the connection had when it was published.
		data := current.ProviderData
		if data == nil {
			data = map[string]string{}
		}
		switch provider {
		case "claude":
			return refreshOAuthGrant(ctx, client, now, current, grantClaude)
		case "codex":
			return refreshOAuthGrant(ctx, client, now, current, grantCodex)
		case "xai", "grok-cli":
			return refreshOAuthGrant(ctx, client, now, current, grantXAI)
		case "gemini-cli", "antigravity":
			if _, _, err := googleClient(); err != nil {
				return credentials.Secret{}, err
			}
			return refreshOAuthGrant(ctx, client, now, current, grantGoogle)
		case "iflow":
			if _, _, err := iflowClient(); err != nil {
				return credentials.Secret{}, err
			}
			return refreshOAuthGrant(ctx, client, now, current, grantIflow)
		case "github":
			return refreshGitHub(ctx, client, now, current, data)
		case "kimi":
			deviceID := data["deviceId"]
			return refreshOAuthGrant(ctx, client, now, current, func() grant { return grantKimi(deviceID) })
		case "cline", "clinepass":
			return refreshCline(ctx, client, now, current)
		case "kiro":
			return refreshKiro(ctx, client, now, current, data)
		case "codebuddy-cn", "codebuddy-intl":
			return refreshCodeBuddy(ctx, client, now, current, provider)
		case "trae":
			return refreshTrae(ctx, client, now, current)
		}
		// Everything else has no refresh grant in the reference (cursor, kilocode,
		// zed, windsurf and every static API-key provider): the credential is
		// long-lived and reauthorization is the only renewal.
		return credentials.Secret{}, credentials.ErrNoExchanger
	}
}

// grant describes one standard OAuth refresh request.
type grant struct {
	url       string
	clientID  string
	secret    string
	json      bool
	extraBody map[string]string
	headers   map[string]string
}

func grantClaude() grant {
	return grant{
		url:      "https://api.anthropic.com/v1/oauth/token",
		clientID: "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
		json:     true,
	}
}

func grantCodex() grant {
	return grant{
		url:      "https://auth.openai.com/oauth/token",
		clientID: "app_EMoamEEZ73f0CkXaXp7hrann",
		json:     true,
	}
}

func grantXAI() grant {
	return grant{
		url:      "https://auth.x.ai/oauth2/token",
		clientID: "b1a00492-073a-47ea-816f-4c329264a828",
	}
}

// grantGoogle reads the operator-supplied client; refresh fails closed when it is
// absent rather than falling back to a built-in credential.
func grantGoogle() grant {
	clientID, clientSecret, _ := googleClient()
	return grant{
		url:      "https://oauth2.googleapis.com/token",
		clientID: clientID,
		secret:   clientSecret,
	}
}

// grantIflow reads the operator-supplied client; refresh fails closed when it is
// absent rather than falling back to a built-in credential.
func grantIflow() grant {
	clientID, clientSecret, _ := iflowClient()
	return grant{
		url:      "https://iflow.cn/oauth/token",
		clientID: clientID,
		secret:   clientSecret,
	}
}

func grantKimi(deviceID string) grant {
	return grant{
		url:      "https://auth.kimi.com/api/oauth/token",
		clientID: "17e5f671-d194-4dfb-9706-5516cb48c098",
		headers:  kimiDeviceHeaders(deviceID),
	}
}

// refreshOAuthGrant performs one standard refresh and maps the result. A rotated
// refresh token is always carried forward: dropping it would revoke the session.
func refreshOAuthGrant(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, build func() grant) (credentials.Secret, error) {
	spec := build()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {current.RefreshToken},
		"client_id":     {spec.clientID},
	}
	if spec.secret != "" {
		form.Set("client_secret", spec.secret)
	}
	for key, value := range spec.extraBody {
		form.Set(key, value)
	}
	headers := map[string]string{}
	for key, value := range spec.headers {
		headers[key] = value
	}
	var tokens tokenResponse
	var err error
	if spec.json {
		body := map[string]string{
			"grant_type":    "refresh_token",
			"refresh_token": current.RefreshToken,
			"client_id":     spec.clientID,
		}
		for key, value := range spec.extraBody {
			body[key] = value
		}
		tokens, err = postJSON(ctx, client, spec.url, body, headers)
	} else {
		tokens, err = postForm(ctx, client, spec.url, form, headers)
	}
	if err != nil {
		return credentials.Secret{}, err
	}
	next := current
	next.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		next.RefreshToken = tokens.RefreshToken
	}
	if expiry := tokens.expiry(now()); !expiry.IsZero() {
		next.Expiry = expiry
	}
	if tokens.IDToken != "" {
		next.IDToken = tokens.IDToken
	}
	return next, nil
}

// Copilot renewal uses the GitHub token; the two tokens have separate expiries.
func refreshGitHub(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, data map[string]string) (credentials.Secret, error) {
	accessToken := current.AccessToken
	if current.RefreshToken != "" {
		tokens, err := postForm(ctx, client, "https://github.com/login/oauth/access_token", url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {current.RefreshToken},
			"client_id":     {"Iv1.b507a08c87ecfe98"},
		}, nil)
		if err == nil && tokens.AccessToken != "" {
			accessToken = tokens.AccessToken
		}
	}
	var copilot struct {
		Token     string          `json:"token"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/copilot_internal/v2/token", map[string]string{
		"Authorization":         "token " + accessToken,
		"User-Agent":            "GitHubCopilotChat/0.26.7",
		"Editor-Version":        "vscode/1.100.0",
		"Editor-Plugin-Version": "copilot-chat/0.26.7",
		"x-github-api-version":  "2022-11-28",
	}, &copilot); err != nil {
		return credentials.Secret{}, fmt.Errorf("github copilot token refresh failed")
	}
	if copilot.Token == "" {
		return credentials.Secret{}, fmt.Errorf("github copilot token refresh returned no token")
	}
	next := current
	next.AccessToken = accessToken
	providerData := map[string]string{}
	for key, value := range data {
		providerData[key] = value
	}
	providerData["copilotToken"] = copilot.Token
	providerData["copilotTokenExpiresAt"] = copilotExpiry(copilot.ExpiresAt, now())
	next.ProviderData = providerData
	return next, nil
}

// refreshCline mirrors refreshClineToken: a JSON body with the WorkOS scheme
// prefixed onto the access token.
func refreshCline(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret) (credentials.Secret, error) {
	var body struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    any    `json:"expiresAt"`
		} `json:"data"`
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    any    `json:"expiresAt"`
	}
	encoded, err := json.Marshal(map[string]string{
		"refreshToken": current.RefreshToken,
		"grantType":    "refresh_token",
		"clientType":   "extension",
	})
	if err != nil {
		return credentials.Secret{}, err
	}
	status, err := postJSONTargetWithHeaders(ctx, client, "https://api.cline.bot/api/v1/auth/refresh", nil, string(encoded), &body)
	if err != nil || status < 200 || status >= 300 {
		return credentials.Secret{}, fmt.Errorf("cline refresh failed")
	}
	accessToken := body.Data.AccessToken
	if accessToken == "" {
		accessToken = body.AccessToken
	}
	if accessToken == "" {
		return credentials.Secret{}, fmt.Errorf("cline refresh returned no token")
	}
	// The gateway expects the WorkOS scheme prefix on refreshed tokens.
	if !strings.HasPrefix(accessToken, "workos:") {
		accessToken = "workos:" + accessToken
	}
	next := current
	next.AccessToken = accessToken
	if refresh := body.Data.RefreshToken; refresh != "" {
		next.RefreshToken = refresh
	} else if refresh := body.RefreshToken; refresh != "" {
		next.RefreshToken = refresh
	}
	expires := body.Data.ExpiresAt
	if expires == nil {
		expires = body.ExpiresAt
	}
	if parsed := resetAtValue(expires); !parsed.IsZero() {
		next.Expiry = parsed
	}
	return next, nil
}

// refreshKiro mirrors refreshKiroToken: AWS SSO OIDC needs the registered client
// credentials, and an IDC session uses the region-specific endpoint.
func refreshKiro(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, data map[string]string) (credentials.Secret, error) {
	clientID, clientSecret := data["clientId"], data["clientSecret"]
	if clientID == "" || clientSecret == "" {
		return credentials.Secret{}, fmt.Errorf("kiro refresh needs the registered client credentials")
	}
	region := data["region"]
	if region == "" {
		region = "us-east-1"
	}
	if !awsRegionPattern.MatchString(region) {
		return credentials.Secret{}, fmt.Errorf("kiro region is invalid")
	}
	endpoint := "https://oidc.us-east-1.amazonaws.com/token"
	if data["authMethod"] == "idc" {
		endpoint = "https://oidc." + region + ".amazonaws.com/token"
	}
	body, err := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"refreshToken": current.RefreshToken,
		"grantType":    "refresh_token",
	})
	if err != nil {
		return credentials.Secret{}, err
	}
	var document struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
	}
	status, err := postJSONTargetWithHeaders(ctx, client, endpoint, nil, string(body), &document)
	if err != nil || status < 200 || status >= 300 {
		return credentials.Secret{}, fmt.Errorf("kiro refresh failed")
	}
	if document.AccessToken == "" {
		return credentials.Secret{}, fmt.Errorf("kiro refresh returned no token")
	}
	next := current
	next.AccessToken = document.AccessToken
	if document.RefreshToken != "" {
		next.RefreshToken = document.RefreshToken
	}
	if document.ExpiresIn > 0 {
		next.Expiry = now().Add(time.Duration(document.ExpiresIn) * time.Second)
	}
	return next, nil
}

// refreshCodeBuddy mirrors refreshCodebuddyToken: the refresh token travels in a
// header and the body is an empty object.
func refreshCodeBuddy(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, provider string) (credentials.Secret, error) {
	endpoint := "https://copilot.tencent.com/v2/plugin/auth/token/refresh"
	domain := "copilot.tencent.com"
	if provider == "codebuddy-intl" {
		endpoint = "https://www.codebuddy.ai/v2/plugin/auth/token/refresh"
		domain = "www.codebuddy.ai"
	}
	var document struct {
		Code int `json:"code"`
		Data struct {
			AccessToken  string          `json:"accessToken"`
			RefreshToken string          `json:"refreshToken"`
			ExpiresIn    json.RawMessage `json:"expiresIn"`
		} `json:"data"`
	}
	status, err := postJSONTargetWithHeaders(ctx, client, endpoint, map[string]string{
		"User-Agent":            "Routeweft",
		"X-Requested-With":      "XMLHttpRequest",
		"X-Domain":              domain,
		"X-Refresh-Token":       current.RefreshToken,
		"X-Auth-Refresh-Source": "plugin",
		"X-Product":             "SaaS",
	}, "{}", &document)
	if err != nil || status < 200 || status >= 300 || document.Code != 0 {
		return credentials.Secret{}, fmt.Errorf("codebuddy refresh failed")
	}
	if document.Data.AccessToken == "" {
		return credentials.Secret{}, fmt.Errorf("codebuddy refresh returned no token")
	}
	next := current
	next.AccessToken = document.Data.AccessToken
	if document.Data.RefreshToken != "" {
		next.RefreshToken = document.Data.RefreshToken
	}
	if expiry := expiryFrom(document.Data.ExpiresIn, now()); !expiry.IsZero() {
		next.Expiry = expiry
	}
	return next, nil
}

// refreshTrae mirrors refreshTraeToken: the exchange endpoint takes the
// PascalCase body and returns a Result envelope.
func refreshTrae(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret) (credentials.Secret, error) {
	body, err := json.Marshal(map[string]string{
		"ClientID":     "ono9krqynydwx5",
		"RefreshToken": current.RefreshToken,
		"ClientSecret": "-",
		"UserID":       "",
	})
	if err != nil {
		return credentials.Secret{}, err
	}
	var document map[string]any
	status, err := postJSONTargetWithHeaders(ctx, client,
		"https://api.marscode.com/cloudide/api/v3/trae/oauth/ExchangeToken",
		map[string]string{"User-Agent": "Trae/1.0.0 antigravity-cockpit-tools"},
		string(body), &document)
	if err != nil || status < 200 || status >= 300 {
		return credentials.Secret{}, fmt.Errorf("trae refresh failed")
	}
	accessToken := jsonPath(document, []string{"Result", "AccessToken"})
	if accessToken == "" {
		accessToken = jsonPath(document, []string{"Result", "accessToken"})
	}
	if accessToken == "" {
		return credentials.Secret{}, fmt.Errorf("trae refresh returned no access token")
	}
	next := current
	next.AccessToken = accessToken
	if refresh := jsonPath(document, []string{"Result", "RefreshToken"}); refresh != "" {
		next.RefreshToken = refresh
	}
	if expires := resetAtValue(jsonPath(document, []string{"Result", "ExpiresAt"})); !expires.IsZero() {
		next.Expiry = expires
	}
	return next, nil
}
