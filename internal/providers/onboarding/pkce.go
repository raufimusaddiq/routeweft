package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/cloudcode"
)

// tokenResponse is the OAuth token payload. Providers differ only in field
// naming, so each flow maps what it needs.
type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	IDToken      string          `json:"id_token"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	Scope        string          `json:"scope"`
}

// expiry converts expires_in into an absolute expiry. A provider that omits it
// leaves the expiry zero, which the credential registry reads as "no proactive
// refresh" rather than "expired now".
func (t tokenResponse) expiry(now time.Time) time.Time {
	seconds, ok := numberFromJSON(t.ExpiresIn)
	if !ok || seconds <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

func numberFromJSON(raw json.RawMessage) (float64, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		return value, true
	}
	var quoted string
	if err := json.Unmarshal([]byte(text), &quoted); err == nil {
		var parsed float64
		if _, err := fmt.Sscan(strings.TrimSpace(quoted), &parsed); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// challengeFor derives the S256 PKCE challenge.
func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// postForm sends a form-encoded token request, the shape most providers accept.
func postForm(ctx context.Context, client HTTPClient, endpoint string, form url.Values, headers map[string]string) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("build token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return doToken(request, client)
}

// postJSON sends a JSON token request (Claude's token endpoint requires it).
func postJSON(ctx context.Context, client HTTPClient, endpoint string, body map[string]string, headers map[string]string) (tokenResponse, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("encode token request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("build token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return doToken(request, client)
}

// doToken executes a token request. Provider error text is never surfaced: it can
// echo the code, and the operator only needs to know the exchange failed.
func doToken(request *http.Request, client HTTPClient) (tokenResponse, error) {
	if client == nil {
		return tokenResponse{}, fmt.Errorf("token exchange requires an HTTP client")
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token exchange failed")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token exchange response unreadable")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token exchange rejected with HTTP %d", response.StatusCode)
	}
	var tokens tokenResponse
	if err := json.Unmarshal(payload, &tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("token exchange returned an unusable payload")
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return tokenResponse{}, fmt.Errorf("token exchange returned no access token")
	}
	return tokens, nil
}

// getJSON reads one authenticated JSON document, used by post-exchange steps
// (user info, project id, copilot token).
func getJSON(ctx context.Context, client HTTPClient, endpoint string, headers map[string]string, target any) error {
	if client == nil {
		return fmt.Errorf("request requires an HTTP client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

// postJSONTarget reads a JSON document from a POST, used by the Cloud Code
// Assist and CodeBuddy billing calls.
func postJSONTarget(ctx context.Context, client HTTPClient, endpoint string, headers map[string]string, body string, target any) error {
	if client == nil {
		return fmt.Errorf("request requires an HTTP client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

// pkceAuthorize builds a standard PKCE authorization URL.
func pkceAuthorize(endpoint string, values url.Values) (AuthRequest, error) {
	built, err := appendQuery(endpoint, values)
	if err != nil {
		return AuthRequest{}, err
	}
	return AuthRequest{URL: built}, nil
}

// claudeFlow mirrors src/lib/oauth/providers/claude.js.
//
// The redirect points at console.anthropic.com, so the operator pastes
// "code#state" back. The token endpoint takes a JSON body.
func claudeFlow() Flow {
	const (
		clientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
		authorizeURL = "https://claude.ai/oauth/authorize"
		tokenURL     = "https://api.anthropic.com/v1/oauth/token"
		redirectURI  = "https://console.anthropic.com/oauth/code/callback"
		scope        = "org:create_api_key user:profile user:inference"
	)
	return Flow{
		Provider: "claude",
		// The redirect is the console page the provider registered, not an
		// operator origin: the operator pastes the code back.
		RedirectSource: RedirectPinned,
		Type:           FlowAuthorizationCodePKCE,
		Completion:     CompletionCode,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			return pkceAuthorize(authorizeURL, url.Values{
				"code":                  {"true"},
				"client_id":             {clientID},
				"response_type":         {"code"},
				"redirect_uri":          {redirectURI},
				"scope":                 {scope},
				"code_challenge":        {challengeFor(p.Verifier)},
				"code_challenge_method": {"S256"},
				"state":                 {p.State},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			code, pastedState := splitCodeState(p.Input)
			state := pastedState
			if state == "" {
				state = p.State
			}
			tokens, err := postJSON(ctx, p.Client, tokenURL, map[string]string{
				"code":          code,
				"state":         state,
				"grant_type":    "authorization_code",
				"client_id":     clientID,
				"redirect_uri":  redirectURI,
				"code_verifier": p.Verifier,
			}, nil)
			if err != nil {
				return Result{}, err
			}
			return Result{
				AuthKind: credentials.AuthOAuth,
				Secret: credentials.Secret{
					AccessToken:  tokens.AccessToken,
					RefreshToken: tokens.RefreshToken,
					Expiry:       tokens.expiry(p.now()),
				},
			}, nil
		},
	}
}

// splitCodeState separates Claude's "code#state" paste from a bare code.
func splitCodeState(input string) (string, string) {
	code, state, found := strings.Cut(strings.TrimSpace(input), "#")
	if !found {
		return code, ""
	}
	return code, state
}

// codexFlow mirrors src/lib/oauth/providers/codex.js. The registered redirect is
// a fixed loopback port, so the operator pastes the URL their browser failed to
// load. The ChatGPT account id and plan come out of the id token.
// codexBulkAccount mirrors src/app/api/oauth/codex/bulk-import/route.js: one
// exported account object, with identity and ChatGPT account claims backfilled
// from the JWT when the exporter omitted them.
func codexBulkAccount(item map[string]any) (BulkAccount, error) {
	access := bulkString(item, "accessToken", "access_token")
	if access == "" {
		return BulkAccount{}, errors.New("missing accessToken")
	}
	refresh := bulkString(item, "refreshToken", "refresh_token")
	idToken := bulkString(item, "idToken", "id_token")
	identity, claims := codexAccount(idToken, access)
	if given := bulkString(item, "email"); given != "" {
		identity = given
	}
	// An exported providerSpecificData wins; the JWT only backfills what is missing
	// (bulk-import/route.js:77-95).
	providerData := map[string]string{}
	for key, value := range claims {
		providerData[key] = value
	}
	if given, ok := item["providerSpecificData"].(map[string]any); ok {
		for key, value := range given {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				providerData[key] = strings.TrimSpace(text)
			}
		}
	}
	return BulkAccount{
		Name:     bulkString(item, "displayName", "name"),
		Identity: identity,
		AuthKind: credentials.AuthOAuth,
		Secret: credentials.Secret{
			AccessToken: access, RefreshToken: refresh, IDToken: idToken,
			Expiry: bulkExpiry(item, time.Now()), ProviderData: providerData,
		},
	}, nil
}

// grokBulkAccount mirrors src/app/api/oauth/grok-cli/bulk-import/route.js, which
// accepts snake_case or camelCase and defaults the auth method to device_code.
func grokBulkAccount(item map[string]any) (BulkAccount, error) {
	access := bulkString(item, "accessToken", "access_token")
	if access == "" {
		return BulkAccount{}, errors.New("missing access_token / accessToken")
	}
	refresh := bulkString(item, "refreshToken", "refresh_token")
	idToken := bulkString(item, "idToken", "id_token")
	identity := bulkString(item, "email")
	if identity == "" && idToken != "" {
		if claims := jwtClaims(idToken); claims != nil {
			if value, ok := claims["email"].(string); ok {
				identity = value
			}
		}
	}
	providerData := map[string]string{"authMethod": "device_code"}
	if idToken != "" {
		providerData["idToken"] = idToken
	}
	if identity != "" {
		providerData["email"] = identity
	}
	if given, ok := item["providerSpecificData"].(map[string]any); ok {
		for key, value := range given {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				providerData[key] = strings.TrimSpace(text)
			}
		}
	}
	return BulkAccount{
		Name:     bulkString(item, "displayName", "name"),
		Identity: identity,
		AuthKind: credentials.AuthOAuth,
		Secret: credentials.Secret{
			AccessToken: access, RefreshToken: refresh, IDToken: idToken,
			Expiry: bulkExpiry(item, time.Now()), ProviderData: providerData,
		},
	}, nil
}

// bulkExpiry reads an exported credential's expiry: an absolute timestamp, then a
// lifetime in seconds. An export that carries neither has an unknown expiry, left
// zero, which the registry reads as "no proactive refresh". The old code computed
// now+0 for it, marking a freshly imported token as already expired and sending a
// refresh-token-less account straight to a refresh that could not succeed.
func bulkExpiry(item map[string]any, now time.Time) time.Time {
	if at := bulkString(item, "expiresAt", "expires_at"); at != "" {
		if parsed, err := time.Parse(time.RFC3339, at); err == nil {
			return parsed
		}
	}
	if seconds := bulkInt(item, "expiresIn", "expires_in"); seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	return time.Time{}
}

// bulkString reads the first non-empty string among the accepted key spellings.
func bulkString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// bulkInt reads the first positive number among the accepted key spellings.
func bulkInt(item map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := item[key].(float64); ok && value > 0 {
			return int64(value)
		}
	}
	return 0
}

func codexFlow() Flow {
	const (
		clientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
		authorizeURL = "https://auth.openai.com/oauth/authorize"
		tokenURL     = "https://auth.openai.com/oauth/token"
		redirectURI  = "http://localhost:1455/auth/callback"
		scope        = "openid profile email offline_access"
	)
	return Flow{
		Provider:       "codex",
		RedirectSource: RedirectPinned,
		Type:           FlowAuthorizationCodePKCE,
		Completion:     CompletionRedirectURL,
		FixedPort:      1455,
		CallbackPath:   "/auth/callback",
		Bulk:           codexBulkAccount,
		// The id token names the account (codexAccount), as in the reference
		// (codex.js mapTokens), so sign-in starts without a name or identity.
		ResolvesIdentity: true,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			return pkceAuthorize(authorizeURL, url.Values{
				"response_type":              {"code"},
				"client_id":                  {clientID},
				"redirect_uri":               {redirectURI},
				"scope":                      {scope},
				"code_challenge":             {challengeFor(p.Verifier)},
				"code_challenge_method":      {"S256"},
				"id_token_add_organizations": {"true"},
				"codex_cli_simplified_flow":  {"true"},
				"originator":                 {"codex_cli_rs"},
				"state":                      {p.State},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			tokens, err := postForm(ctx, p.Client, tokenURL, url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {clientID},
				"code":          {p.Input},
				"redirect_uri":  {redirectURI},
				"code_verifier": {p.Verifier},
			}, nil)
			if err != nil {
				return Result{}, err
			}
			identity, providerData := codexAccount(tokens.IDToken, tokens.AccessToken)
			return Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  tokens.AccessToken,
					RefreshToken: tokens.RefreshToken,
					IDToken:      tokens.IDToken,
					Expiry:       tokens.expiry(p.now()),
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// codexAccount reads the ChatGPT account id, plan and email out of the JWT
// claims, so the connection is identifiable without a second round trip.
func codexAccount(idToken, accessToken string) (string, map[string]string) {
	claims := jwtClaims(idToken)
	if len(claims) == 0 {
		claims = jwtClaims(accessToken)
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	data := map[string]string{}
	if value, ok := auth["chatgpt_account_id"].(string); ok && value != "" {
		data["chatgptAccountId"] = value
	}
	if value, ok := auth["chatgpt_plan_type"].(string); ok && value != "" {
		data["chatgptPlanType"] = value
	}
	identity := ""
	if value, ok := profile["email"].(string); ok {
		identity = value
	}
	if identity == "" {
		if value, ok := claims["email"].(string); ok {
			identity = value
		}
	}
	if identity == "" {
		if value, ok := claims["preferred_username"].(string); ok {
			identity = value
		}
	}
	// A token without an email still names its ChatGPT account, which is stable
	// across sign-ins and so a sound dedup key.
	if identity == "" {
		identity = data["chatgptAccountId"]
	}
	if len(data) == 0 {
		return identity, nil
	}
	return identity, data
}

// jwtClaims decodes a JWT payload without verifying it. The token was just
// received from the provider over TLS, so the claims are used for display and
// routing only, never for an authorization decision.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil
	}
	return claims
}

// xaiFlow mirrors src/lib/oauth/providers/xai.js: PKCE against a fixed loopback
// port, with endpoint discovery and a manual-code fallback.
func xaiFlow() Flow {
	const (
		clientID    = "b1a00492-073a-47ea-816f-4c329264a828"
		redirectURI = "http://127.0.0.1:56121/callback"
		scope       = "openid profile email offline_access"
	)
	return Flow{
		Provider:       "xai",
		RedirectSource: RedirectPinned,
		Type:           FlowAuthorizationCodePKCE,
		Completion:     CompletionRedirectURL,
		FixedPort:      56121,
		FixedHost:      "127.0.0.1",
		CallbackPath:   "/callback",
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			authorizeURL, _, err := xaiEndpoints(ctx, p.Client)
			if err != nil {
				return AuthRequest{}, err
			}
			nonce, err := randomToken(16)
			if err != nil {
				return AuthRequest{}, err
			}
			return pkceAuthorize(authorizeURL, url.Values{
				"response_type":         {"code"},
				"client_id":             {clientID},
				"redirect_uri":          {redirectURI},
				"scope":                 {scope},
				"code_challenge":        {challengeFor(p.Verifier)},
				"code_challenge_method": {"S256"},
				"state":                 {p.State},
				"nonce":                 {nonce},
				"plan":                  {"generic"},
				"referrer":              {"cli-proxy-api"},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			_, tokenURL, err := xaiEndpoints(ctx, p.Client)
			if err != nil {
				return Result{}, err
			}
			tokens, err := postForm(ctx, p.Client, tokenURL, url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {clientID},
				"code":          {p.Input},
				"redirect_uri":  {redirectURI},
				"code_verifier": {p.Verifier},
			}, nil)
			if err != nil {
				return Result{}, err
			}
			providerData := map[string]string{}
			if tokens.IDToken != "" {
				providerData["idToken"] = tokens.IDToken
			}
			identity := ""
			if claims := jwtClaims(tokens.IDToken); claims != nil {
				if value, ok := claims["email"].(string); ok {
					identity = value
				}
			}
			if len(providerData) == 0 {
				providerData = nil
			}
			return Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  tokens.AccessToken,
					RefreshToken: tokens.RefreshToken,
					Expiry:       tokens.expiry(p.now()),
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// xaiEndpoints resolves xAI's authorize and token endpoints from discovery,
// falling back to the static values when discovery is unavailable.
func xaiEndpoints(ctx context.Context, client HTTPClient) (string, string, error) {
	const (
		discoveryURL  = "https://auth.x.ai/.well-known/openid-configuration"
		fallbackAuth  = "https://auth.x.ai/oauth2/authorize"
		fallbackToken = "https://auth.x.ai/oauth2/token"
	)
	var document struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := getJSON(ctx, client, discoveryURL, nil, &document); err != nil {
		return fallbackAuth, fallbackToken, nil
	}
	authorize, okAuth := safeEndpoint(document.AuthorizationEndpoint)
	token, okToken := safeEndpoint(document.TokenEndpoint)
	if !okAuth || !okToken {
		return fallbackAuth, fallbackToken, nil
	}
	return authorize, token, nil
}

// safeEndpoint accepts only an absolute HTTPS URL, so a tampered discovery
// document cannot redirect a token exchange to a plaintext or local endpoint.
func safeEndpoint(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", false
	}
	return parsed.String(), true
}

// geminiCLIFlow mirrors src/lib/oauth/providers/gemini-cli.js: Google OAuth
// without PKCE, then a project id from loadCodeAssist.
func geminiCLIFlow() Flow {
	const (
		authorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
		tokenURL     = "https://oauth2.googleapis.com/token"
		userInfoURL  = "https://www.googleapis.com/oauth2/v1/userinfo"
	)
	scope := "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile"
	return Flow{
		Provider:   "gemini-cli",
		Type:       FlowAuthorizationCode,
		Completion: CompletionCode,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			clientID, _, err := googleClient()
			if err != nil {
				return AuthRequest{}, err
			}
			return pkceAuthorize(authorizeURL, url.Values{
				"client_id":     {clientID},
				"response_type": {"code"},
				"redirect_uri":  {p.RedirectURI},
				"scope":         {scope},
				"state":         {p.State},
				"access_type":   {"offline"},
				"prompt":        {"consent"},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			clientID, clientSecret, err := googleClient()
			if err != nil {
				return Result{}, err
			}
			tokens, err := postForm(ctx, p.Client, tokenURL, url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {clientID},
				"client_secret": {clientSecret},
				"code":          {p.Input},
				"redirect_uri":  {p.RedirectURI},
			}, nil)
			if err != nil {
				return Result{}, err
			}
			providerData := map[string]string{}
			identity := ""
			var user struct {
				Email string `json:"email"`
			}
			if err := getJSON(ctx, p.Client, userInfoURL+"?alt=json", map[string]string{
				"Authorization": "Bearer " + tokens.AccessToken,
			}, &user); err == nil {
				identity = user.Email
			}
			if project := cloudCodeProject(ctx, p.Client, tokens.AccessToken, false); project != "" {
				providerData["projectId"] = project
			}
			if len(providerData) == 0 {
				providerData = nil
			}
			return Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  tokens.AccessToken,
					RefreshToken: tokens.RefreshToken,
					Expiry:       tokens.expiry(p.now()),
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// cloudCodeProject resolves the Code Assist project id that the quota and
// inference paths need, so it is captured once at sign-in. Both flows send the
// host client metadata; they differ as the reference's do: gemini-cli adds
// mode 1 (src/lib/oauth/providers/gemini-cli.js:51-63), antigravity sends the
// metadata alone with the IDE user agent and x-request-source
// (antigravity.js:61-67,82-86).
func cloudCodeProject(ctx context.Context, client HTTPClient, accessToken string, antigravity bool) string {
	var body struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
	}
	headers := map[string]string{"Authorization": "Bearer " + accessToken}
	request := `{"metadata":` + cloudcode.ClientMetadata() + `,"mode":1}`
	if antigravity {
		headers["User-Agent"] = cloudcode.AntigravityUserAgent
		headers["x-request-source"] = "local"
		request = `{"metadata":` + cloudcode.ClientMetadata() + `}`
	}
	if err := postJSONTarget(ctx, client, "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", headers, request, &body); err != nil {
		return ""
	}
	return jsonProjectID(body.Project)
}

// jsonProjectID accepts a bare id or an object with an id field.
func jsonProjectID(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err == nil {
		return strings.TrimSpace(id)
	}
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		return strings.TrimSpace(object.ID)
	}
	return ""
}

// antigravityFlow mirrors src/lib/oauth/providers/antigravity.js. The redirect is
// operator-configurable because the Google client is registered per deployment.
func antigravityFlow() Flow {
	const (
		authorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
		tokenURL     = "https://oauth2.googleapis.com/token"
		userInfoURL  = "https://www.googleapis.com/oauth2/v1/userinfo"
	)
	scope := "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs"
	return Flow{
		Provider:   "antigravity",
		Type:       FlowAuthorizationCode,
		Completion: CompletionCode,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			clientID, _, err := googleClient()
			if err != nil {
				return AuthRequest{}, err
			}
			return pkceAuthorize(authorizeURL, url.Values{
				"client_id":     {clientID},
				"response_type": {"code"},
				"redirect_uri":  {p.RedirectURI},
				"scope":         {scope},
				"state":         {p.State},
				"access_type":   {"offline"},
				"prompt":        {"consent"},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			clientID, clientSecret, err := googleClient()
			if err != nil {
				return Result{}, err
			}
			tokens, err := postForm(ctx, p.Client, tokenURL, url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {clientID},
				"client_secret": {clientSecret},
				"code":          {p.Input},
				"redirect_uri":  {p.RedirectURI},
			}, nil)
			if err != nil {
				return Result{}, err
			}
			providerData := map[string]string{}
			identity := ""
			var user struct {
				Email string `json:"email"`
			}
			if err := getJSON(ctx, p.Client, userInfoURL+"?alt=json", map[string]string{
				"Authorization": "Bearer " + tokens.AccessToken,
			}, &user); err == nil {
				identity = user.Email
			}
			if project := cloudCodeProject(ctx, p.Client, tokens.AccessToken, true); project != "" {
				providerData["projectId"] = project
			}
			if len(providerData) == 0 {
				providerData = nil
			}
			return Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  tokens.AccessToken,
					RefreshToken: tokens.RefreshToken,
					Expiry:       tokens.expiry(p.now()),
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// randomToken mints a URL-safe random value of the given byte length.
func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
