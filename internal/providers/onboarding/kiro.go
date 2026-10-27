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

// kiroAuthService is Kiro's own social-login/refresh service
// (open-sse/providers/registry/kiro.js:35,114-117).
const kiroAuthService = "https://prod.us-east-1.auth.desktop.kiro.dev"

// kiroDefaultExpiry is what the pin assumes when a social refresh omits expiresIn
// (src/lib/oauth/services/kiro.js:234).
const kiroDefaultExpiry = 3600 * time.Second

// kiroMicrosoftHosts are the only token endpoints an external_idp credential may
// name (src/lib/oauth/kiroExternalIdp.js:1-5). The endpoint comes from a pasted
// file, so without the allowlist an import could aim the refresh, which carries
// the refresh token, at any host.
var kiroMicrosoftHosts = map[string]bool{
	"login.microsoftonline.com": true,
	"login.microsoft.com":       true,
	"login.windows.net":         true,
}

// refreshKiro mirrors refreshKiroToken (open-sse/services/tokenRefresh/providers.js:326-455):
// three credential shapes share the provider, and each refreshes against its own
// endpoint. Picking by the stored authMethod and client credentials, rather than
// assuming AWS SSO, is what lets an imported social or Microsoft credential rotate
// instead of being rejected as "needs client credentials" at its first expiry.
func refreshKiro(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, data map[string]string) (credentials.Secret, error) {
	if strings.TrimSpace(current.RefreshToken) == "" {
		return credentials.Secret{}, fmt.Errorf("kiro refresh needs a refresh token")
	}
	switch {
	case data["authMethod"] == "external_idp":
		return refreshKiroExternalIdp(ctx, client, now, current, data)
	case data["clientId"] != "" && data["clientSecret"] != "":
		return refreshKiroAWS(ctx, client, now, current, data)
	}
	return refreshKiroSocial(ctx, client, now, current, data)
}

// refreshKiroAWS is the AWS SSO OIDC refresh (:377-418). An IDC session uses its
// own region; every other registration refreshes in us-east-1.
func refreshKiroAWS(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, data map[string]string) (credentials.Secret, error) {
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
		"clientId":     data["clientId"],
		"clientSecret": data["clientSecret"],
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
		ProfileARN   string `json:"profileArn"`
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
	next.ProviderData = kiroProfilePatch(ctx, client, current.ProviderData, document.AccessToken, document.ProfileARN)
	return next, nil
}

// refreshKiroSocial is the Google/GitHub refresh (:421-455): a plain JSON call to
// Kiro's own auth service, with no client registration.
func refreshKiroSocial(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, data map[string]string) (credentials.Secret, error) {
	body, err := json.Marshal(map[string]string{"refreshToken": current.RefreshToken})
	if err != nil {
		return credentials.Secret{}, err
	}
	var document struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileARN   string `json:"profileArn"`
	}
	status, err := postJSONTargetWithHeaders(ctx, client, kiroAuthService+"/refreshToken",
		map[string]string{"User-Agent": "kiro-cli/1.0.0"}, string(body), &document)
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
	lifetime := kiroDefaultExpiry
	if document.ExpiresIn > 0 {
		lifetime = time.Duration(document.ExpiresIn) * time.Second
	}
	next.Expiry = now().Add(lifetime)
	next.ProviderData = kiroProfilePatch(ctx, client, current.ProviderData, document.AccessToken, document.ProfileARN)
	return next, nil
}

// refreshKiroExternalIdp is the Microsoft refresh (:334-375): a form post to the
// token endpoint stored with the credential, which is re-validated here because it
// was persisted from operator input.
func refreshKiroExternalIdp(ctx context.Context, client HTTPClient, now func() time.Time, current credentials.Secret, data map[string]string) (credentials.Secret, error) {
	endpoint, err := validateMicrosoftTokenEndpoint(firstNonEmpty(data["tokenEndpoint"], data["token_endpoint"]))
	if err != nil {
		return credentials.Secret{}, err
	}
	clientID := firstNonEmpty(data["clientId"], data["client_id"])
	scope := firstNonEmpty(data["scope"], data["scopes"])
	if clientID == "" || scope == "" {
		return credentials.Secret{}, fmt.Errorf("kiro external_idp refresh needs a client id and scope")
	}
	tokens, err := postForm(ctx, client, endpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {current.RefreshToken},
		"scope":         {scope},
	}, map[string]string{"Accept": "application/json"})
	if err != nil {
		return credentials.Secret{}, fmt.Errorf("kiro refresh failed")
	}
	if tokens.AccessToken == "" {
		return credentials.Secret{}, fmt.Errorf("kiro refresh returned no token")
	}
	next := current
	next.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		next.RefreshToken = tokens.RefreshToken
	}
	// Without expires_in the old expiry would survive and the registry would
	// refresh on every request; the other branches default to one hour as well.
	next.Expiry = now().Add(kiroDefaultExpiry)
	if expiry := tokens.expiry(now()); !expiry.IsZero() {
		next.Expiry = expiry
	}
	// The pin returns the normalized endpoint, client and scope with the rotation
	// (:148-153), so the next refresh reads the validated values.
	merged := make(map[string]string, len(data)+4)
	for key, value := range data {
		merged[key] = value
	}
	merged["authMethod"], merged["clientId"], merged["tokenEndpoint"], merged["scope"] = "external_idp", clientID, endpoint, scope
	next.ProviderData = merged
	return next, nil
}

// kiroProfilePatch is resolveKiroProfileArnPatch (:316-324): a profile ARN is
// recorded only when the connection has none, and one already stored is never
// replaced. The ARN the refresh reports wins; without one the account's profiles
// are listed, as the pin does. Everything else the connection carries is returned
// untouched.
func kiroProfilePatch(ctx context.Context, client HTTPClient, stored map[string]string, accessToken, reported string) map[string]string {
	if stored["profileArn"] != "" {
		return stored
	}
	arn := strings.TrimSpace(reported)
	if arn == "" {
		arn = kiroProfileARN(ctx, client, accessToken)
	}
	if arn == "" {
		return stored
	}
	patched := make(map[string]string, len(stored)+1)
	for key, value := range stored {
		patched[key] = value
	}
	patched["profileArn"] = arn
	return patched
}

// kiroProfilesEndpoint is where the pin lists an account's CodeWhisperer
// profiles when a token arrives without one (providerHelpers.js:53-71).
const kiroProfilesEndpoint = "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles"

// kiroProfileARN mirrors fetchKiroProfileArn (providerHelpers.js:53-71): the first
// profile with a non-blank ARN, or "" on any failure. It is best effort, as the
// pin's is: a sign-in or refresh never fails because the lookup did.
func kiroProfileARN(ctx context.Context, client HTTPClient, accessToken string) string {
	if strings.TrimSpace(accessToken) == "" {
		return ""
	}
	var document struct {
		Profiles []struct {
			ARN string `json:"arn"`
		} `json:"profiles"`
	}
	status, err := postJSONTargetWithHeaders(ctx, client, kiroProfilesEndpoint,
		map[string]string{"Authorization": "Bearer " + accessToken}, `{"maxResults":10}`, &document)
	if err != nil || status < 200 || status >= 300 {
		return ""
	}
	for _, profile := range document.Profiles {
		if arn := strings.TrimSpace(profile.ARN); arn != "" {
			return arn
		}
	}
	return ""
}

// kiroSocialRedirect is the only redirect Kiro's auth service accepts for a
// social login: the Kiro IDE's own URL scheme (services/kiro.js:133-138,146).
// No browser loads it, so the operator pastes the address back.
const kiroSocialRedirect = "kiro://kiro.kiroAgent/authenticate-success"

// kiroSocialIdPs maps the sign-in method to the idp the login page takes
// (services/kiro.js:134) and the label the pin stores (social-exchange/route.js:50).
var kiroSocialIdPs = map[string]struct{ idp, label string }{
	"google": {idp: "Google", label: "Google"},
	"github": {idp: "Github", label: "Github"},
}

// ErrKiroSocialMethod means a social sign-in was started or completed without
// naming Google or GitHub. It is refused rather than defaulted, so the account
// is never signed in with a provider the operator did not choose.
var ErrKiroSocialMethod = errors.New("kiro social sign-in needs authMethod google or github")

// kiroSocialFlow mirrors social-authorize/route.js and social-exchange/route.js:
// a PKCE login on Kiro's own auth service for a Google or GitHub account, with the
// kiro:// redirect pasted back. It is offered beside the AWS SSO device flow
// (kiroFlow), selected by authMethod; the refresh is refreshKiroSocial.
func kiroSocialFlow() Flow {
	return Flow{
		Provider: "kiro",
		// authMethod is fixed per sign-in method (one for Google, one for GitHub)
		// rather than typed, so it is declared but the dashboard does not render it.
		MetaKeys:       []string{"authMethod"},
		AuthMethods:    []string{"google", "github"},
		RedirectSource: RedirectPinned,
		Type:           FlowAuthorizationCodePKCE,
		Completion:     CompletionRedirectURL,
		// Signing the same account in again renews its row, as the pin's oauth
		// duplicate rule does for every sign-in (connectionsRepo.js:228-283).
		RenewsSameAccount: true,
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			social, ok := kiroSocialIdPs[p.Meta["authMethod"]]
			if !ok {
				return AuthRequest{}, ErrKiroSocialMethod
			}
			return pkceAuthorize(kiroAuthService+"/login", url.Values{
				"idp":                   {social.idp},
				"redirect_uri":          {kiroSocialRedirect},
				"code_challenge":        {challengeFor(p.Verifier)},
				"code_challenge_method": {"S256"},
				"state":                 {p.State},
				"prompt":                {"select_account"},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			method := p.Meta["authMethod"]
			social, ok := kiroSocialIdPs[method]
			if !ok {
				return Result{}, ErrKiroSocialMethod
			}
			body, err := json.Marshal(map[string]string{
				"code":          p.Input,
				"code_verifier": p.Verifier,
				"redirect_uri":  kiroSocialRedirect,
			})
			if err != nil {
				return Result{}, err
			}
			var document struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresIn    int    `json:"expiresIn"`
				ProfileARN   string `json:"profileArn"`
			}
			status, err := postJSONTargetWithHeaders(ctx, p.Client, kiroAuthService+"/oauth/token", nil, string(body), &document)
			if err != nil || status < 200 || status >= 300 {
				return Result{}, fmt.Errorf("kiro social token exchange failed")
			}
			if document.AccessToken == "" {
				return Result{}, fmt.Errorf("kiro social token exchange returned no token")
			}
			lifetime := kiroDefaultExpiry
			if document.ExpiresIn > 0 {
				lifetime = time.Duration(document.ExpiresIn) * time.Second
			}
			// The pin stores whatever ARN the exchange reports and resolves a missing
			// one at the first refresh. Routeweft refuses to serve a Kiro connection
			// without one (wire/kiro.go kiroTarget), so the same lookup runs here, so
			// the new connection is usable before its first refresh.
			providerData := kiroProfilePatch(ctx, p.Client, map[string]string{
				"authMethod": method,
				"provider":   social.label,
			}, document.AccessToken, document.ProfileARN)
			return Result{
				AuthKind: credentials.AuthOAuth,
				Identity: kiroEmail(document.AccessToken),
				Secret: credentials.Secret{
					AccessToken:  document.AccessToken,
					RefreshToken: document.RefreshToken,
					Expiry:       p.now().Add(lifetime),
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// validateMicrosoftTokenEndpoint mirrors kiroExternalIdp.js:14-35.
func validateMicrosoftTokenEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("token_endpoint is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", errors.New("token_endpoint must be a valid URL")
	}
	if parsed.Scheme != "https" {
		return "", errors.New("token_endpoint must use https")
	}
	if !kiroMicrosoftHosts[strings.ToLower(parsed.Hostname())] || parsed.User != nil || parsed.Port() != "" {
		return "", errors.New("token_endpoint must be a Microsoft login endpoint")
	}
	return parsed.String(), nil
}

// kiroImport routes a Kiro import to its mode. The reference has three routes for
// one provider (kiro/api-key, kiro/import, kiro/import-cli-proxy); they share one
// admin endpoint, so the pasted input chooses: a bare string is an API key, a JSON
// object is a token import, and a JSON object with a token endpoint or an access
// token is a CLIProxyAPI export.
//
// JSON carries the secrets (a refresh token, an IDC client secret) in the single
// credential field the dashboard already masks, rather than in extra plain-text
// operator fields.
func kiroImport(ctx context.Context, p Params) (Result, error) {
	input := strings.TrimSpace(p.Input)
	if !strings.HasPrefix(input, "{") {
		return kiroAPIKeyImport(ctx, p)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &document); err != nil {
		return Result{}, errors.New("kiro import expects a JSON object")
	}
	// Only the endpoint or the auth method marks a CLIProxyAPI export. An access
	// token does not: kiro-auth-token.json carries one beside its refresh token, and
	// that file belongs on the token path, which refreshes and so replaces it.
	for _, key := range []string{"token_endpoint", "tokenEndpoint", "auth_method"} {
		if _, ok := document[key]; ok {
			return kiroCLIProxyImport(p, document)
		}
	}
	return kiroTokenImport(ctx, p, document)
}

// jsonString reads one string field, trimmed; a non-string value reads as absent.
func jsonString(document map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := document[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// kiroTokenImport mirrors kiro/import/route.js: a refresh token from the IDE, with
// the client registration when it is an IDC (organization) credential. The token is
// proven by refreshing it, which is also what mints the access token to store.
func kiroTokenImport(ctx context.Context, p Params, document map[string]json.RawMessage) (Result, error) {
	refresh := jsonString(document, "refreshToken", "refresh_token")
	if refresh == "" {
		return Result{}, errors.New("kiro refresh token is required")
	}
	clientID, clientSecret := jsonString(document, "clientId", "client_id"), jsonString(document, "clientSecret", "client_secret")
	idc := clientID != "" && clientSecret != ""
	region := jsonString(document, "region")
	data := map[string]string{}
	if idc {
		if region == "" {
			region = "us-east-1"
		}
		data = map[string]string{"clientId": clientID, "clientSecret": clientSecret, "region": region, "authMethod": "idc"}
	}
	// A pasted profile ARN is the stored one, so the refresh does not look it up
	// again; without one the refresh's own lookup (kiroProfilePatch) supplies it.
	current := credentials.Secret{RefreshToken: refresh}
	if pasted := jsonString(document, "profileArn"); pasted != "" {
		current.ProviderData = map[string]string{"profileArn": pasted}
	}
	rotated, err := refreshKiro(ctx, p.Client, p.now, current, data)
	if err != nil {
		return Result{}, err
	}
	profileARN := firstNonEmpty(jsonString(document, "profileArn"), rotated.ProviderData["profileArn"])
	providerData := map[string]string{"authMethod": "imported"}
	if idc {
		providerData = map[string]string{
			"authMethod": "idc", "clientId": clientID, "clientSecret": clientSecret, "region": region,
		}
	}
	if profileARN != "" {
		providerData["profileArn"] = profileARN
	}
	return Result{
		AuthKind: credentials.AuthOAuth,
		Identity: kiroEmail(rotated.AccessToken),
		Secret: credentials.Secret{
			AccessToken:  rotated.AccessToken,
			RefreshToken: rotated.RefreshToken,
			Expiry:       kiroImportExpiry(rotated.Expiry, p.now()),
			ProviderData: providerData,
		},
	}, nil
}

// kiroImportExpiry keeps the lifetime the refresh reported and falls back to the
// pin's one hour only when the provider reported none (kiro/import/route.js:43).
func kiroImportExpiry(reported, now time.Time) time.Time {
	if !reported.IsZero() {
		return reported
	}
	return now.Add(kiroDefaultExpiry)
}

// kiroEmail reads the account label the pin shows for a Kiro token: email, then
// preferred_username, then subject (services/kiro.js:392-408).
func kiroEmail(accessToken string) string {
	claims := jwtClaims(accessToken)
	for _, key := range []string{"email", "preferred_username", "sub"} {
		if value, ok := claims[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// kiroCLIProxyImport mirrors kiro/import-cli-proxy/route.js and
// normalizeKiroExternalIdpAuth (kiroExternalIdp.js:77-128). It performs no network
// call: the export already carries a token pair, and the refresh endpoint is
// restricted to Microsoft hosts.
func kiroCLIProxyImport(p Params, document map[string]json.RawMessage) (Result, error) {
	if method := jsonString(document, "auth_method", "authMethod"); method != "" && method != "external_idp" {
		return Result{}, errors.New("only external_idp Kiro auth is supported by this importer")
	}
	access := jsonString(document, "access_token", "accessToken")
	refresh := jsonString(document, "refresh_token", "refreshToken")
	clientID := jsonString(document, "client_id", "clientId")
	endpoint, err := validateMicrosoftTokenEndpoint(jsonString(document, "token_endpoint", "tokenEndpoint"))
	if err != nil {
		return Result{}, err
	}
	profileARN := jsonString(document, "profile_arn", "profileArn")
	scope := kiroScope(document)
	switch {
	case access == "":
		return Result{}, errors.New("access_token is required")
	case refresh == "":
		return Result{}, errors.New("refresh_token is required")
	case clientID == "":
		return Result{}, errors.New("client_id is required")
	case scope == "":
		return Result{}, errors.New("scopes is required")
	case profileARN == "":
		return Result{}, errors.New("profile_arn is required")
	}
	identity := jsonString(document, "email")
	if claims := jwtClaims(access); identity == "" && claims != nil {
		for _, key := range []string{"email", "preferred_username", "upn", "sub"} {
			if value, ok := claims[key].(string); ok && value != "" {
				identity = value
				break
			}
		}
	}
	return Result{
		AuthKind: credentials.AuthOAuth,
		Identity: identity,
		Secret: credentials.Secret{
			AccessToken:  access,
			RefreshToken: refresh,
			Expiry:       kiroExternalExpiry(document, access, p.now()),
			ProviderData: map[string]string{
				"profileArn":    profileARN,
				"region":        firstNonEmpty(jsonString(document, "region"), "us-east-1"),
				"authMethod":    "external_idp",
				"clientId":      clientID,
				"tokenEndpoint": endpoint,
				"scope":         scope,
			},
		},
	}, nil
}

// kiroScope accepts the scope list as an array or a space-separated string
// (normalizeScope, kiroExternalIdp.js:37-42).
func kiroScope(document map[string]json.RawMessage) string {
	for _, key := range []string{"scopes", "scope"} {
		raw, ok := document[key]
		if !ok {
			continue
		}
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			kept := list[:0]
			for _, item := range list {
				if item = strings.TrimSpace(item); item != "" {
					kept = append(kept, item)
				}
			}
			if len(kept) > 0 {
				return strings.Join(kept, " ")
			}
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// kiroExternalExpiry resolves the expiry as resolveExpiresAt does
// (kiroExternalIdp.js:57-75): an explicit timestamp, then a lifetime, then the
// token's own exp claim, then one hour.
func kiroExternalExpiry(document map[string]json.RawMessage, access string, now time.Time) time.Time {
	if explicit := jsonString(document, "expired", "expires_at", "expiresAt"); explicit != "" {
		if parsed, err := time.Parse(time.RFC3339, explicit); err == nil {
			return parsed
		}
	}
	for _, key := range []string{"expires_in", "expiresIn"} {
		if seconds, ok := numberFromJSON(document[key]); ok && seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second)
		}
	}
	if claims := jwtClaims(access); claims != nil {
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			return time.Unix(int64(exp), 0)
		}
	}
	return now.Add(kiroDefaultExpiry)
}
