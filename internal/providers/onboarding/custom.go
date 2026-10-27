package onboarding

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// awsRegionPattern bounds the region that is interpolated into an AWS endpoint,
// so a region value cannot become an SSRF vector.
var awsRegionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d{1,2}$`)

// kiroFlow mirrors src/lib/oauth/providers/kiro.js: AWS SSO OIDC needs a client
// registration before the device authorization, and the client id/secret must be
// stored for refresh.
func kiroFlow() Flow {
	const (
		clientName = "kiro-oauth-client"
		startURL   = "https://view.awsapps.com/start"
		issuerURL  = "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6"
	)
	scopes := []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"}
	grantTypes := []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"}
	return Flow{
		Provider: "kiro",
		MetaKeys: []string{"region", "startUrl", "authMethod"},
		// clientId/clientSecret are minted by Kiro's own AWS SSO client registration
		// and carried in AuthRequest.Extra; they are never operator input.
		RedirectSource: RedirectUnused,
		Type:           FlowDeviceCode,
		Completion:     CompletionToken,
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			region := metaOr(p, "region", "us-east-1")
			if !awsRegionPattern.MatchString(region) {
				return AuthRequest{}, fmt.Errorf("kiro region is invalid")
			}
			authMethod := "builder-id"
			if p.Meta["authMethod"] == "idc" {
				authMethod = "idc"
			}
			registerBody, err := json.Marshal(map[string]any{
				"clientName": clientName,
				"clientType": "public",
				"scopes":     scopes,
				"grantTypes": grantTypes,
				"issuerUrl":  issuerURL,
			})
			if err != nil {
				return AuthRequest{}, err
			}
			var clientInfo struct {
				ClientID     string `json:"clientId"`
				ClientSecret string `json:"clientSecret"`
			}
			if _, err := postJSONTargetStatus(ctx, p.Client,
				"https://oidc."+region+".amazonaws.com/client/register",
				string(registerBody), &clientInfo); err != nil {
				return AuthRequest{}, fmt.Errorf("kiro client registration failed")
			}
			if clientInfo.ClientID == "" || clientInfo.ClientSecret == "" {
				return AuthRequest{}, fmt.Errorf("kiro client registration returned no credentials")
			}
			deviceBody, err := json.Marshal(map[string]string{
				"clientId":     clientInfo.ClientID,
				"clientSecret": clientInfo.ClientSecret,
				"startUrl":     metaOr(p, "startUrl", startURL),
			})
			if err != nil {
				return AuthRequest{}, err
			}
			var device struct {
				DeviceCode              string `json:"deviceCode"`
				UserCode                string `json:"userCode"`
				VerificationURI         string `json:"verificationUri"`
				VerificationURIComplete string `json:"verificationUriComplete"`
				ExpiresIn               int    `json:"expiresIn"`
				Interval                int    `json:"interval"`
			}
			if _, err := postJSONTargetStatus(ctx, p.Client,
				"https://oidc."+region+".amazonaws.com/device_authorization",
				string(deviceBody), &device); err != nil {
				return AuthRequest{}, fmt.Errorf("kiro device authorization failed")
			}
			interval := device.Interval
			if interval <= 0 {
				interval = 5
			}
			return AuthRequest{
				DeviceCode:      device.DeviceCode,
				UserCode:        device.UserCode,
				VerificationURL: device.VerificationURIComplete,
				Interval:        time.Duration(interval) * time.Second,
				ExpiresIn:       time.Duration(device.ExpiresIn) * time.Second,
				Extra: map[string]string{
					"clientId":     clientInfo.ClientID,
					"clientSecret": clientInfo.ClientSecret,
					"region":       region,
					"authMethod":   authMethod,
					"startUrl":     metaOr(p, "startUrl", startURL),
				},
			}, nil
		},
		Poll: func(ctx context.Context, p Params) (PollResult, error) {
			region := metaOr(p, "region", "us-east-1")
			if !awsRegionPattern.MatchString(region) {
				return PollResult{}, fmt.Errorf("kiro region is invalid")
			}
			body, err := json.Marshal(map[string]string{
				"clientId":     p.Meta["clientId"],
				"clientSecret": p.Meta["clientSecret"],
				"deviceCode":   p.Input,
				"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
			})
			if err != nil {
				return PollResult{}, err
			}
			var document struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresIn    int    `json:"expiresIn"`
				ProfileARN   string `json:"profileArn"`
				Error        string `json:"error"`
				Message      string `json:"message"`
			}
			status, err := postJSONTargetWithHeaders(ctx, p.Client,
				"https://oidc."+region+".amazonaws.com/token", nil, string(body), &document)
			if err != nil {
				return PollResult{}, err
			}
			if document.Error != "" || (status < 200 || status >= 300) {
				if document.Error == "authorization_pending" || document.Error == "slow_down" || document.Error == "" {
					return PollResult{Pending: true}, nil
				}
				return PollResult{Err: fmt.Errorf("kiro authorization failed: %s", document.Error)}, nil
			}
			if document.AccessToken == "" {
				return PollResult{Pending: true}, nil
			}
			providerData := map[string]string{
				"clientId":     p.Meta["clientId"],
				"clientSecret": p.Meta["clientSecret"],
				"region":       region,
				"authMethod":   metaOr(p, "authMethod", "builder-id"),
				"startUrl":     p.Meta["startUrl"],
			}
			if document.ProfileARN != "" {
				providerData["profileArn"] = document.ProfileARN
			}
			identity := ""
			if claims := jwtClaims(document.AccessToken); claims != nil {
				if value, ok := claims["email"].(string); ok {
					identity = value
				}
			}
			return PollResult{Done: true, Result: Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  document.AccessToken,
					RefreshToken: document.RefreshToken,
					Expiry:       p.now().Add(time.Duration(document.ExpiresIn) * time.Second),
					ProviderData: providerData,
				},
			}}, nil
		},
	}
}

// ErrCursorMachineIDRequired means the import has no machine id, which Cursor
// requires on every request and which cannot be read from an IDE on the server.
var ErrCursorMachineIDRequired = errors.New("cursor import requires the machine id from Cursor's local database (meta.machineId)")

// cursorMachineID is the pin's machine id shape (services/cursor.js:110-114): 32 or
// more hex digits once the dashes are removed.
var cursorMachineID = regexp.MustCompile(`^[a-fA-F0-9]{32,}$`)

// cursorFlow mirrors src/app/api/oauth/cursor/import/route.js and
// CursorService.validateImportToken: there is no OAuth flow. The credential is the
// token from Cursor's local database, pasted by the operator, which is why the
// import reads Input like every other import mode.
//
// The pin validates the pair's shape and makes no provider call ("we don't validate
// against API because Cursor uses complex protobuf", services/cursor.js:116-117). An
// earlier version posted JSON to the Connect-protocol model endpoint, which can only
// fail for a genuine token; the token is proven by its first request instead.
//
// The machine id is a second required value the reference reads from the same local
// database (open-sse/executors/cursor.js buildHeaders rejects a credential without
// one). Routeweft cannot read an IDE's SQLite file, so it is supplied as
// meta.machineId and stored with the connection; the import fails with a named
// error rather than storing a credential inference would reject.
func cursorFlow() Flow {
	return Flow{
		Provider: "cursor",
		MetaKeys: []string{"machineId"},
		// accessToken is a legacy alias for Input, which the import body already
		// carries; only the machine id needs an operator field.
		RedirectSource:   RedirectUnused,
		Type:             FlowImportToken,
		Completion:       CompletionToken,
		ResolvesIdentity: true,
		Import: func(_ context.Context, p Params) (Result, error) {
			token := strings.TrimSpace(p.Input)
			if token == "" {
				token = strings.TrimSpace(p.Meta["accessToken"])
			}
			machineID := strings.TrimSpace(p.Meta["machineId"])
			if token == "" {
				return Result{}, fmt.Errorf("cursor import requires an access token")
			}
			if machineID == "" {
				return Result{}, ErrCursorMachineIDRequired
			}
			if len(token) < 50 {
				return Result{}, fmt.Errorf("cursor token appears too short")
			}
			if !cursorMachineID.MatchString(strings.ReplaceAll(machineID, "-", "")) {
				return Result{}, fmt.Errorf("cursor machine id must be a UUID")
			}
			// email, then subject (extractUserInfo, services/cursor.js:131-153).
			identity, userID := "", ""
			if claims := jwtClaims(token); claims != nil {
				sub, _ := claims["sub"].(string)
				userID = sub
				if value, ok := claims["user_id"].(string); ok && userID == "" {
					userID = value
				}
				identity, _ = claims["email"].(string)
				if identity == "" {
					identity = sub
				}
			}
			providerData := map[string]string{
				"machineId":  machineID,
				"authMethod": "imported",
			}
			if userID != "" {
				providerData["userId"] = userID
			}
			return Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken: token,
					// Cursor tokens last about a day and have no public refresh
					// endpoint (services/cursor.js:122).
					Expiry:       p.now().Add(24 * time.Hour),
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// postJSONTargetWithHeadersAuth posts with a bearer token, for provider
// validation calls that take no other credential.
func postJSONTargetWithHeadersAuth(ctx context.Context, client HTTPClient, endpoint, token, body string, target any) error {
	status, err := postJSONTargetWithHeaders(ctx, client, endpoint, map[string]string{
		"Authorization": "Bearer " + token,
	}, body, target)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("validation returned HTTP %d", status)
	}
	return nil
}

// zedFlow mirrors src/lib/oauth/providers/zed.js: an RSA native-app handshake.
// The access token arrives encrypted against the generated public key.
func zedFlow() Flow {
	return Flow{
		Provider: "zed",
		MetaKeys: []string{"nativeAppPort"},
		// /client/users/me names the email when it answers (zed.js:47).
		InfersIdentity: true,
		RedirectSource: RedirectUnused,
		Type:           FlowAuthorizationCode,
		Completion:     CompletionCallback,
		CallbackPath:   "/",
		// Zed's callback is the native app's own loopback port, chosen by
		// native_app_port; no redirect URI is registered or sent.
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			// The keypair is minted here, server-side, and the private half rides in
			// Extra so the callback can decrypt the token. The browser only ever
			// receives the public half, inside the sign-in URL.
			publicKey, privateKey, err := newRSAKeyPair()
			if err != nil {
				return AuthRequest{}, fmt.Errorf("zed keypair could not be generated")
			}
			port := metaOr(p, "nativeAppPort", "58443")
			request, err := pkceAuthorize("https://zed.dev/native_app_signin", url.Values{
				"native_app_port":       {port},
				"native_app_public_key": {publicKey},
			})
			if err != nil {
				return AuthRequest{}, err
			}
			request.Extra = map[string]string{"privateKey": privateKey}
			return request, nil
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			userID, encrypted := parseZedCallback(p.Input)
			if encrypted == "" {
				return Result{}, fmt.Errorf("zed callback carried no access token")
			}
			privateKey := p.Meta["privateKey"]
			if privateKey == "" {
				return Result{}, fmt.Errorf("zed private key is unavailable; restart the sign-in")
			}
			token, err := decryptZedToken(encrypted, privateKey)
			if err != nil {
				return Result{}, fmt.Errorf("zed access token could not be decrypted")
			}
			providerData := map[string]string{
				"authMethod": "oauth",
				"userId":     userID,
			}
			if systemID := p.Meta["systemId"]; systemID != "" {
				providerData["systemId"] = systemID
			}
			identity := ""
			var user struct {
				Email       string `json:"email"`
				Name        string `json:"name"`
				DisplayName string `json:"display_name"`
			}
			if err := getJSON(ctx, p.Client, "https://cloud.zed.dev/client/users/me", map[string]string{
				"Authorization": userID + " " + token,
			}, &user); err == nil {
				identity = user.Email
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

// parseZedCallback extracts the user id and encrypted token from the callback
// URL or query string.
func parseZedCallback(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if index := strings.Index(text, "?"); index >= 0 {
		text = text[index+1:]
	}
	text = strings.TrimPrefix(text, "#")
	values, err := url.ParseQuery(text)
	if err != nil {
		return "", ""
	}
	return values.Get("user_id"), values.Get("access_token")
}

// decryptZedToken reverses the RSA-encrypted access token. The private key is
// URL-safe base64 PKCS#1 DER, as the reference stores it.
func decryptZedToken(encrypted, privateKey string) (string, error) {
	keyDER, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(privateKey, "="))
	if err != nil {
		return "", err
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encrypted, "="))
	if err != nil {
		return "", err
	}
	key, err := parseRSAPrivateKey(keyDER)
	if err != nil {
		return "", err
	}
	plaintext, err := rsaDecryptOAEP(key, payload)
	if err != nil {
		plaintext, err = rsaDecryptPKCS1(key, payload)
		if err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(string(plaintext)), nil
}

// xiaomiFlow mirrors the ECDH handshake in src/lib/oauth/providers/xiaomi-mimo.js
// and the route that drives it: the callback carries ?u=<encrypted payload>.
func xiaomiFlow() Flow {
	return Flow{
		Provider: "xiaomi-mimo",
		MetaKeys: []string{"keyName"},
		// The decrypted payload names its uid, the identity "<uid>@xiaomi".
		InfersIdentity: true,
		Type:           FlowAuthorizationCode,
		Completion:     CompletionCallback,
		CallbackPath:   "/",
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			// The ECDH keypair is minted here and the private half rides in Extra, so
			// the encrypted callback can be decrypted. Only the public half leaves the
			// server, inside the sign-in URL.
			publicKey, privateKey, err := newX25519KeyPair()
			if err != nil {
				return AuthRequest{}, fmt.Errorf("xiaomi-mimo keypair could not be generated")
			}
			values := url.Values{
				"pk":           {publicKey},
				"redirect_uri": {p.RedirectURI},
				"kn":           {"mimocode"},
			}
			if keyName := p.Meta["keyName"]; keyName != "" {
				values.Set("key_name", keyName)
			}
			request, err := pkceAuthorize("https://platform.xiaomimimo.com/authorize", values)
			if err != nil {
				return AuthRequest{}, err
			}
			request.Extra = map[string]string{"privateKey": privateKey}
			return request, nil
		},
		Exchange: func(_ context.Context, p Params) (Result, error) {
			encrypted := callbackParam(p.Input, "u")
			if encrypted == "" {
				return Result{}, fmt.Errorf("xiaomi-mimo callback carried no encrypted payload")
			}
			privateKey := p.Meta["privateKey"]
			if privateKey == "" {
				return Result{}, fmt.Errorf("xiaomi-mimo private key is unavailable; restart the sign-in")
			}
			payload, err := decryptXiaomiPayload(privateKey, encrypted)
			if err != nil {
				return Result{}, fmt.Errorf("xiaomi-mimo callback could not be decrypted")
			}
			identity := payload.UID
			if identity != "" {
				identity += "@xiaomi"
			}
			providerData := map[string]string{
				"authMethod": "oauth",
				"uid":        payload.UID,
			}
			if payload.URL != "" {
				providerData["baseUrl"] = payload.URL
			}
			return Result{
				AuthKind:    credentials.AuthOAuth,
				Identity:    identity,
				DisplayName: "Xiaomi MiMo",
				Secret: credentials.Secret{
					AccessToken:  payload.SK,
					Expiry:       p.now().Add(365 * 24 * time.Hour),
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// xiaomiPayload is the decrypted callback body.
type xiaomiPayload struct {
	UID string `json:"uid"`
	SK  string `json:"sk"`
	URL string `json:"url"`
}

// decryptXiaomiPayload reverses the reference's wire format:
// [12-byte nonce][32-byte ephemeral pubkey][ciphertext][16-byte GCM tag], keyed
// by SHA-256 of the X25519 shared secret.
func decryptXiaomiPayload(privateKeyB64, encryptedB64 string) (xiaomiPayload, error) {
	raw, err := base64.StdEncoding.DecodeString(encryptedB64)
	if err != nil {
		return xiaomiPayload{}, err
	}
	if len(raw) < 12+32+16+1 {
		return xiaomiPayload{}, fmt.Errorf("encrypted payload is too short")
	}
	nonce := raw[:12]
	ephemeral := raw[12:44]
	ciphertextAndTag := raw[44:]
	tag := ciphertextAndTag[len(ciphertextAndTag)-16:]
	ciphertext := ciphertextAndTag[:len(ciphertextAndTag)-16]

	privateDER, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(privateKeyB64, "="))
	if err != nil {
		return xiaomiPayload{}, err
	}
	privateKey, err := ecdh.X25519().NewPrivateKey(pkcs8X25519(privateDER))
	if err != nil {
		return xiaomiPayload{}, err
	}
	publicKey, err := ecdh.X25519().NewPublicKey(ephemeral)
	if err != nil {
		return xiaomiPayload{}, err
	}
	shared, err := privateKey.ECDH(publicKey)
	if err != nil {
		return xiaomiPayload{}, err
	}
	derived := sha256.Sum256(shared)
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		return xiaomiPayload{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return xiaomiPayload{}, err
	}
	sealed := append(append([]byte(nil), ciphertext...), tag...)
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return xiaomiPayload{}, err
	}
	var payload xiaomiPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return xiaomiPayload{}, err
	}
	if payload.SK == "" {
		return xiaomiPayload{}, fmt.Errorf("decrypted payload carried no key")
	}
	return payload, nil
}

// pkcs8X25519 returns the raw 32-byte key from a PKCS#8 DER document, which is
// how the reference stores the private key.
func pkcs8X25519(der []byte) []byte {
	const prefix = 16
	if len(der) == 32 {
		return der
	}
	if len(der) > prefix {
		return der[len(der)-32:]
	}
	return der
}

// callbackParam reads one query parameter from a pasted callback URL.
func callbackParam(raw, key string) string {
	text := strings.TrimSpace(raw)
	if index := strings.Index(text, "?"); index >= 0 {
		text = text[index+1:]
	}
	text = strings.TrimPrefix(text, "#")
	values, err := url.ParseQuery(text)
	if err != nil {
		return ""
	}
	return values.Get(key)
}

// newX25519KeyPair mints the keypair the Xiaomi handshake needs, returning the
// base64 SPKI public key and the base64 PKCS#8 private key for the callback.
func newX25519KeyPair() (string, string, error) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	spki := append([]byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x03, 0x21, 0x00}, privateKey.PublicKey().Bytes()...)
	pkcs8 := append([]byte{0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20}, privateKey.Bytes()...)
	return base64.StdEncoding.EncodeToString(spki), base64.RawURLEncoding.EncodeToString(pkcs8), nil
}

// parseRSAPrivateKey accepts the PKCS#1 DER key the reference stores, falling
// back to PKCS#8 in case the operator pasted the other encoding.
func parseRSAPrivateKey(der []byte) (*rsa.PrivateKey, error) {
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA private key")
	}
	return key, nil
}

// rsaDecryptOAEP is the reference's primary decryption mode.
func rsaDecryptOAEP(key *rsa.PrivateKey, payload []byte) ([]byte, error) {
	return rsa.DecryptOAEP(sha256.New(), nil, key, payload, nil)
}

// rsaDecryptPKCS1 is the reference's fallback for older clients.
func rsaDecryptPKCS1(key *rsa.PrivateKey, payload []byte) ([]byte, error) {
	return rsa.DecryptPKCS1v15(nil, key, payload)
}

// newRSAKeyPair mints the ephemeral RSA-2048 keypair the Zed native-app handshake
// needs, returning the URL-safe base64 public key for the sign-in URL and the
// private key the callback is decrypted with.
func newRSAKeyPair() (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(publicDER),
		base64.RawURLEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key)), nil
}
