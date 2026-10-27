package onboarding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// iflowFlow mirrors src/lib/oauth/providers/iflow.js: an authorization code with
// HTTP Basic client auth, then a mandatory user-info call that yields the API key.
// The API key is the credential inference uses, so a failure there fails sign-in.
func iflowFlow() Flow {
	const (
		authorizeURL = "https://iflow.cn/oauth"
		tokenURL     = "https://iflow.cn/oauth/token"
		userInfoURL  = "https://iflow.cn/api/oauth/getUserInfo"
	)
	return Flow{
		Provider:   "iflow",
		Type:       FlowAuthorizationCode,
		Completion: CompletionCode,
		// The exchange fails rather than return without an email or phone.
		ResolvesIdentity: true,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			clientID, _, err := iflowClient()
			if err != nil {
				return AuthRequest{}, err
			}
			return pkceAuthorize(authorizeURL, url.Values{
				"loginMethod": {"phone"},
				"type":        {"phone"},
				"redirect":    {p.RedirectURI},
				"state":       {p.State},
				"client_id":   {clientID},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			clientID, clientSecret, err := iflowClient()
			if err != nil {
				return Result{}, err
			}
			basic := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
			tokens, err := postForm(ctx, p.Client, tokenURL, url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {p.Input},
				"redirect_uri":  {p.RedirectURI},
				"client_id":     {clientID},
				"client_secret": {clientSecret},
			}, map[string]string{"Authorization": "Basic " + basic})
			if err != nil {
				return Result{}, err
			}
			var userInfo struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
				Data    struct {
					APIKey   string `json:"apiKey"`
					Email    string `json:"email"`
					Phone    string `json:"phone"`
					Nickname string `json:"nickname"`
					Name     string `json:"name"`
				} `json:"data"`
			}
			if err := getJSON(ctx, p.Client, userInfoURL+"?accessToken="+url.QueryEscape(tokens.AccessToken), nil, &userInfo); err != nil {
				return Result{}, fmt.Errorf("iflow user info unavailable")
			}
			if !userInfo.Success {
				return Result{}, fmt.Errorf("iflow user info failed")
			}
			// iFlow authenticates inference with this API key, so an empty one is a
			// failed connection, not a degraded one.
			if strings.TrimSpace(userInfo.Data.APIKey) == "" {
				return Result{}, fmt.Errorf("iflow returned an empty API key")
			}
			identity := strings.TrimSpace(userInfo.Data.Email)
			if identity == "" {
				identity = strings.TrimSpace(userInfo.Data.Phone)
			}
			if identity == "" {
				return Result{}, fmt.Errorf("iflow returned no account identity")
			}
			display := userInfo.Data.Nickname
			if display == "" {
				display = userInfo.Data.Name
			}
			return Result{
				AuthKind:    credentials.AuthOAuth,
				Identity:    identity,
				DisplayName: display,
				Secret: credentials.Secret{
					AccessToken:  userInfo.Data.APIKey,
					RefreshToken: tokens.RefreshToken,
					Expiry:       tokens.expiry(p.now()),
					ProviderData: map[string]string{"apiKey": userInfo.Data.APIKey},
				},
			}, nil
		},
	}
}

// ErrGitLabClientIDRequired means a GitLab browser sign-in was started without
// the OAuth application's client id. The reference sends an empty one
// (gitlab.js:10) and GitLab refuses it on its own page; saying so before the
// browser leaves is the same outcome, earlier.
var ErrGitLabClientIDRequired = errors.New("enter the client id of the GitLab OAuth application, or use a personal access token instead")

// gitlabFlow mirrors src/lib/oauth/providers/gitlab.js. GitLab is self-hostable,
// so the base URL and client id come from the operator; the PAT path is a
// separate manual_token flow on the same provider.
func gitlabFlow() Flow {
	const (
		defaultBase = "https://gitlab.com"
		scope       = "api read_user"
	)
	return Flow{
		Provider: "gitlab",
		// clientSecret is optional: a confidential GitLab OAuth application sends it
		// at exchange, a public one does not. Declared because the flow reads it, so
		// the dashboard can offer it.
		MetaKeys: []string{"clientId", "clientSecret", "baseUrl"},
		// The PAT import talks to the same instance but uses no OAuth application.
		ImportMetaKeys: []string{"baseUrl"},
		Type:           FlowAuthorizationCodePKCE,
		Completion:     CompletionCode,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			base := strings.TrimSuffix(metaOr(p, "baseUrl", defaultBase), "/")
			clientID := strings.TrimSpace(p.Meta["clientId"])
			if clientID == "" {
				return AuthRequest{}, ErrGitLabClientIDRequired
			}
			return pkceAuthorize(base+"/oauth/authorize", url.Values{
				"client_id":             {clientID},
				"redirect_uri":          {p.RedirectURI},
				"response_type":         {"code"},
				"state":                 {p.State},
				"scope":                 {scope},
				"code_challenge":        {challengeFor(p.Verifier)},
				"code_challenge_method": {"S256"},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			base := strings.TrimSuffix(metaOr(p, "baseUrl", defaultBase), "/")
			form := url.Values{
				"client_id":     {p.Meta["clientId"]},
				"grant_type":    {"authorization_code"},
				"code":          {p.Input},
				"redirect_uri":  {p.RedirectURI},
				"code_verifier": {p.Verifier},
			}
			if secret := p.Meta["clientSecret"]; secret != "" {
				form.Set("client_secret", secret)
			}
			tokens, err := postForm(ctx, p.Client, base+"/oauth/token", form, nil)
			if err != nil {
				return Result{}, err
			}
			var user struct {
				Username    string `json:"username"`
				Email       string `json:"email"`
				PublicEmail string `json:"public_email"`
				Name        string `json:"name"`
			}
			_ = getJSON(ctx, p.Client, base+"/api/v4/user", map[string]string{
				"Authorization": "Bearer " + tokens.AccessToken,
			}, &user)
			identity := user.Email
			if identity == "" {
				identity = user.PublicEmail
			}
			if identity == "" {
				identity = user.Username
			}
			return Result{
				AuthKind:    credentials.AuthOAuth,
				Identity:    identity,
				DisplayName: user.Name,
				Secret: credentials.Secret{
					AccessToken:  tokens.AccessToken,
					RefreshToken: tokens.RefreshToken,
					Expiry:       tokens.expiry(p.now()),
					ProviderData: map[string]string{
						"baseUrl":  base,
						"clientId": p.Meta["clientId"],
						"authKind": "oauth",
					},
				},
			}, nil
		},
		ResolvesIdentity: true,
		Import:           gitlabPAT,
	}
}

// gitlabPAT mirrors src/app/api/oauth/gitlab/pat/route.js: a personal access
// token verified against /api/v4/user, stored as an oauth connection with
// authKind "personal_access_token".
func gitlabPAT(ctx context.Context, p Params) (Result, error) {
	token := strings.TrimSpace(p.Input)
	if token == "" {
		return Result{}, fmt.Errorf("gitlab personal access token is required")
	}
	base := strings.TrimSuffix(metaOr(p, "baseUrl", "https://gitlab.com"), "/")
	var user struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		PublicEmail string `json:"public_email"`
		Name        string `json:"name"`
	}
	if err := getJSON(ctx, p.Client, base+"/api/v4/user", map[string]string{
		"Private-Token": token,
		"Accept":        "application/json",
	}, &user); err != nil {
		return Result{}, fmt.Errorf("gitlab token verification failed")
	}
	identity := user.Email
	if identity == "" {
		identity = user.PublicEmail
	}
	if identity == "" {
		identity = user.Username
	}
	if identity == "" {
		return Result{}, fmt.Errorf("gitlab token has no account identity")
	}
	display := user.Name
	if display == "" {
		display = user.Username
	}
	return Result{
		AuthKind:    credentials.AuthOAuth,
		Identity:    identity,
		DisplayName: display,
		Secret: credentials.Secret{
			AccessToken: token,
			ProviderData: map[string]string{
				"baseUrl":  base,
				"authKind": "personal_access_token",
			},
		},
	}, nil
}

// metaOr reads one operator-supplied parameter, falling back to a default.
//
// An operator-supplied value can be an absolute URL (GitLab's self-hosted base
// URL is the case that matters). That is deliberate: GitLab Duo is deployed on
// operator-owned hosts, so the endpoint cannot be a fixed literal. The value is
// only reachable from an authenticated admin session and every request goes
// through the SSRF-guarded client, which rejects loopback/LAN/metadata
// destinations unless the trusted-local policy is explicitly enabled. This is the
// same posture as the AWS region allowlist, and the reason the guard is shared
// rather than re-implemented per flow.
func metaOr(p Params, key, fallback string) string {
	if value := strings.TrimSpace(p.Meta[key]); value != "" {
		return value
	}
	return fallback
}

// clineFlow mirrors cline.js and clinepass.js: an authorization code that is
// either base64-encoded token JSON or an exchange against the gateway. Both are
// Cline's WorkOS sign-in (ClinePass shares its endpoints, tokenRefresh.js:151);
// the stored access token is the bare WorkOS JWT, and the workos: prefix is added
// where it is sent (internal/providers/cline, refreshCline).
func clineFlow(provider, authorizeURL, exchangeURL string) Flow {
	return Flow{
		Provider:   provider,
		Type:       FlowAuthorizationCode,
		Completion: CompletionCode,
		// The bundle and the gateway both name the account's email when they can
		// (cline.js:27,46); the gateway's is best effort ("|| \"\"").
		InfersIdentity: true,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			return pkceAuthorize(authorizeURL, url.Values{
				"client_type":  {"extension"},
				"callback_url": {p.RedirectURI},
				"redirect_uri": {p.RedirectURI},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			// The extension encodes the token bundle in the code parameter.
			if token, ok := decodeClineCode(p.Input, p.now()); ok {
				return token, nil
			}
			return clineGatewayExchange(ctx, p, exchangeURL)
		},
	}
}

// clineGatewayExchange is the reference's fallback when the code is not a bundle
// (cline.js:32-48): a JSON exchange whose answer is camelCase, either at the top
// level or under data, with the email under data.userInfo.
func clineGatewayExchange(ctx context.Context, p Params, exchangeURL string) (Result, error) {
	type tokens struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    any    `json:"expiresAt"`
	}
	var document struct {
		tokens
		Data struct {
			tokens
			UserInfo struct {
				Email string `json:"email"`
			} `json:"userInfo"`
		} `json:"data"`
	}
	body, err := json.Marshal(map[string]string{
		"grant_type":   "authorization_code",
		"code":         p.Input,
		"client_type":  "extension",
		"redirect_uri": p.RedirectURI,
	})
	if err != nil {
		return Result{}, err
	}
	status, err := postJSONTargetWithHeaders(ctx, p.Client, exchangeURL, nil, string(body), &document)
	if err != nil || status < 200 || status >= 300 {
		return Result{}, fmt.Errorf("token exchange failed")
	}
	access := firstNonEmpty(document.Data.AccessToken, document.AccessToken)
	if strings.TrimSpace(access) == "" {
		return Result{}, fmt.Errorf("token exchange returned no access token")
	}
	expiresAt := document.Data.ExpiresAt
	if expiresAt == nil {
		expiresAt = document.ExpiresAt
	}
	return Result{
		AuthKind: credentials.AuthOAuth,
		Identity: strings.TrimSpace(document.Data.UserInfo.Email),
		Secret: credentials.Secret{
			AccessToken:  access,
			RefreshToken: firstNonEmpty(document.Data.RefreshToken, document.RefreshToken),
			Expiry:       clineExpiry(expiresAt, p.now()),
		},
	}, nil
}

// clineExpiry is mapTokens' expiresIn (cline.js:54-56): the reported expiresAt,
// or an hour from now when the answer names none.
func clineExpiry(raw any, now time.Time) time.Time {
	if expires := resetAtValue(raw); !expires.IsZero() {
		return expires
	}
	return now.Add(time.Hour)
}

// decodeClineCode reads the base64 token bundle the Cline extension puts in the
// code parameter. A code that is not that bundle falls through to the exchange.
// Node's base64 decoder reads both alphabets, so a URL-safe bundle is accepted too.
func decodeClineCode(input string, now time.Time) (Result, bool) {
	base64Code := strings.TrimSpace(input)
	if padding := 4 - len(base64Code)%4; padding != 4 {
		base64Code += strings.Repeat("=", padding)
	}
	decoded, err := base64.StdEncoding.DecodeString(base64Code)
	if err != nil {
		if decoded, err = base64.URLEncoding.DecodeString(base64Code); err != nil {
			return Result{}, false
		}
	}
	text := string(decoded)
	last := strings.LastIndex(text, "}")
	if last < 0 {
		return Result{}, false
	}
	var bundle struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		Email        string `json:"email"`
		FirstName    string `json:"firstName"`
		LastName     string `json:"lastName"`
		ExpiresAt    any    `json:"expiresAt"`
	}
	if err := json.Unmarshal([]byte(text[:last+1]), &bundle); err != nil || bundle.AccessToken == "" {
		return Result{}, false
	}
	secret := credentials.Secret{AccessToken: bundle.AccessToken, RefreshToken: bundle.RefreshToken, Expiry: clineExpiry(bundle.ExpiresAt, now)}
	// providerSpecificData {firstName, lastName} (cline.js:58), names only.
	for key, value := range map[string]string{"firstName": bundle.FirstName, "lastName": bundle.LastName} {
		if value = strings.TrimSpace(value); value != "" {
			if secret.ProviderData == nil {
				secret.ProviderData = map[string]string{}
			}
			secret.ProviderData[key] = value
		}
	}
	display := strings.TrimSpace(strings.TrimSpace(bundle.FirstName) + " " + strings.TrimSpace(bundle.LastName))
	return Result{AuthKind: credentials.AuthOAuth, Identity: strings.TrimSpace(bundle.Email), DisplayName: display, Secret: secret}, true
}

// resetAtValue parses an absolute expiry the providers report as ISO or Unix ms.
func resetAtValue(raw any) time.Time {
	switch value := raw.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed
		}
	case float64:
		if value > 0 {
			return time.UnixMilli(int64(value)).UTC()
		}
	}
	return time.Time{}
}

// kimchiFlow mirrors src/lib/oauth/providers/kimchi.js: the browser returns a
// token, which is validated before it is stored.
func kimchiFlow() Flow {
	const (
		webAppURL     = "https://app.kimchi.dev"
		validationURL = "https://api.cast.ai/v1/llm/openai/supported-providers"
		userInfoURL   = "https://app.kimchi.dev/api/v1/me"
	)
	return Flow{
		Provider:   "kimchi",
		Type:       FlowBrowserToken,
		Completion: CompletionToken,
		// /api/v1/me names the email or user id when it answers (kimchi.js:56-71).
		InfersIdentity: true,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			return pkceAuthorize(webAppURL+"/cli-auth", url.Values{
				"callback": {p.RedirectURI},
				"state":    {p.State},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			token := strings.TrimSpace(p.Input)
			if token == "" {
				return Result{}, fmt.Errorf("kimchi token is required")
			}
			// The token is only useful if the LLM gateway accepts it, so validate
			// before storing.
			var validation json.RawMessage
			if err := getJSON(ctx, p.Client, validationURL, map[string]string{
				"Authorization": "Bearer " + token,
			}, &validation); err != nil {
				return Result{}, fmt.Errorf("kimchi token validation failed")
			}
			var user struct {
				ID       json.RawMessage `json:"id"`
				Username string          `json:"username"`
				Email    string          `json:"email"`
				Name     string          `json:"name"`
			}
			providerData := map[string]string{"authMethod": "browser_token"}
			identity := ""
			if err := getJSON(ctx, p.Client, userInfoURL, map[string]string{
				"Authorization": "Bearer " + token,
			}, &user); err == nil {
				identity = user.Email
				if user.Username != "" {
					providerData["username"] = user.Username
				}
				if id, ok := numberFromJSON(user.ID); ok {
					providerData["userId"] = fmt.Sprintf("%.0f", id)
					if identity == "" {
						identity = fmt.Sprintf("kimchi-user-%.0f", id)
					}
				}
			}
			return Result{
				AuthKind:    credentials.AuthOAuth,
				Identity:    identity,
				DisplayName: user.Name,
				Secret: credentials.Secret{
					AccessToken:  token,
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// qoderFlow mirrors src/lib/oauth/providers/qoder.js: a custom device handshake
// where the poll endpoint identifies the attempt by nonce and PKCE verifier, and
// the machine id must be stored for request signing.
func qoderFlow() Flow {
	const (
		loginURL  = "https://qoder.com/device/selectAccounts"
		deviceURL = "https://openapi.qoder.sh/api/v1/deviceToken/poll"
		userURL   = "https://openapi.qoder.sh/api/v1/userinfo"
	)
	return Flow{
		Provider:       "qoder",
		RedirectSource: RedirectUnused,
		Type:           FlowDeviceCode,
		Completion:     CompletionToken,
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			nonce, err := randomToken(16)
			if err != nil {
				return AuthRequest{}, err
			}
			machineID, err := randomToken(16)
			if err != nil {
				return AuthRequest{}, err
			}
			verifier, err := randomToken(32)
			if err != nil {
				return AuthRequest{}, err
			}
			userCode := strings.ToUpper(nonce)
			if len(userCode) > 8 {
				userCode = userCode[:8]
			}
			verification := loginURL + "?" + url.Values{
				"nonce":      {nonce},
				"machine_id": {machineID},
			}.Encode()
			return AuthRequest{
				// Qoder polls by nonce, which is what device_code carries here.
				DeviceCode:      nonce,
				UserCode:        userCode,
				VerificationURL: verification,
				Interval:        2 * time.Second,
				ExpiresIn:       5 * time.Minute,
				// Qoder mints its own PKCE pair, so the verifier is overridden.
				Verifier: verifier,
				Extra: map[string]string{
					"nonce":     nonce,
					"machineId": machineID,
				},
			}, nil
		},
		Poll: func(ctx context.Context, p Params) (PollResult, error) {
			nonce := p.Meta["nonce"]
			if nonce == "" {
				nonce = p.Input
			}
			if nonce == "" || p.Verifier == "" {
				return PollResult{Err: fmt.Errorf("qoder device state is incomplete; restart the sign-in")}, nil
			}
			var document struct {
				AccessToken  string          `json:"accessToken"`
				RefreshToken string          `json:"refreshToken"`
				ExpireTime   json.RawMessage `json:"expireTime"`
				Status       string          `json:"status"`
				UserID       string          `json:"userId"`
			}
			status, err := postJSONTargetWithHeaders(ctx, p.Client, deviceURL, nil, fmt.Sprintf(
				`{"nonce":%q,"codeVerifier":%q}`, nonce, p.Verifier), &document)
			if err != nil {
				return PollResult{}, err
			}
			if document.Status == "pending" || (status < 200 || status >= 300) {
				return PollResult{Pending: true}, nil
			}
			if document.AccessToken == "" {
				return PollResult{Err: fmt.Errorf("qoder authorization returned no token")}, nil
			}
			// Qoder's tokens last ~30 days; floor the expiry so a skewed upstream
			// timestamp cannot truncate the stored credential below usefulness.
			expiry := resetAtValue(document.ExpireTime)
			minimum := p.now().Add(24 * time.Hour)
			if expiry.Before(minimum) {
				expiry = minimum
			}
			providerData := map[string]string{
				"authMethod": "device",
				"userId":     document.UserID,
				"machineId":  p.Meta["machineId"],
			}
			identity := ""
			var user struct {
				Name           string `json:"name"`
				Email          string `json:"email"`
				OrganizationID string `json:"organizationId"`
			}
			if err := getJSON(ctx, p.Client, userURL, map[string]string{
				"Authorization": "Bearer " + document.AccessToken,
			}, &user); err == nil {
				identity = strings.TrimSpace(user.Email)
				providerData["organizationId"] = user.OrganizationID
			}
			// Dedup needs a non-empty identity, so fall back to a stable synthetic
			// one derived from the user id rather than creating duplicates.
			if identity == "" && document.UserID != "" {
				identity = "qoder-user-" + document.UserID
			}
			return PollResult{Done: true, Result: Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  document.AccessToken,
					RefreshToken: document.RefreshToken,
					Expiry:       expiry,
					ProviderData: providerData,
				},
			}}, nil
		},
	}
}
