package wire

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Vertex AI, Gemini models (executors/vertex.js). The connection's credential is
// one of three things, told apart by what it parses as (vertex.js:66-72,135-156):
// a service account JSON, minted into a bearer with a signed JWT; an application
// default credentials JSON of an authorized user, refreshed into a bearer; or a
// raw API key.
//
// Divergence from the pin: a raw key travels in X-Goog-Api-Key, as the Gemini
// provider's key does, never as ?key= in the URL (vertex.js:108), so a credential
// is not put where a URL is logged or cached. The pin's already-minted bearer
// (credentials.accessToken, vertex.js:69) has no counterpart: a Routeweft Vertex
// connection holds an API key. The token is cached for the life of the process,
// where the pin caches only the service account's (tokenRefresh.js:76-120).

const (
	vertexTokenURL = "https://oauth2.googleapis.com/token"
	vertexScope    = "https://www.googleapis.com/auth/cloud-platform"
	// vertexExpiryBuffer is the pin's five minutes (tokenRefresh.js:83).
	vertexExpiryBuffer = 5 * time.Minute
	vertexTokenEntries = 256
	vertexReplyLimit   = 64 << 10
	vertexModelPrefix  = "publishers/google/models/"
)

var vertexLocation = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

type vertexCredential struct {
	// kind is "service_account", "authorized_user" or "" for a raw key.
	kind string
	// project is the project the credential names: the service account's
	// project_id or the ADC's quota_project_id.
	project string
	// raw is the credential as given; fields are the parsed JSON.
	raw    string
	fields struct {
		Type         string `json:"type"`
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		ProjectID    string `json:"project_id"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
		QuotaProject string `json:"quota_project_id"`
	}
}

func parseVertexCredential(token string) vertexCredential {
	credential := vertexCredential{raw: strings.TrimSpace(token)}
	if !strings.HasPrefix(credential.raw, "{") || json.Unmarshal([]byte(credential.raw), &credential.fields) != nil {
		return vertexCredential{raw: credential.raw}
	}
	f := credential.fields
	switch {
	case f.Type == "service_account" && f.ClientEmail != "" && f.PrivateKey != "" && f.ProjectID != "":
		credential.kind, credential.project = "service_account", f.ProjectID
	case f.Type == "authorized_user" && f.ClientID != "" && f.ClientSecret != "" && f.RefreshToken != "":
		credential.kind, credential.project = "authorized_user", f.QuotaProject
	default:
		return vertexCredential{raw: credential.raw}
	}
	return credential
}

func (c vertexCredential) oauth() bool { return c.kind != "" }

// vertexRoute is buildUrl (vertex.js:83-109). endpoint is the model's path under
// the publisher; the project and location come from the credential and the
// connection.
func vertexRoute(provider routing.ProviderRef, endpoint string) (Target, error) {
	model, found := strings.CutPrefix(endpoint, vertexModelPrefix)
	action := model[strings.LastIndex(model, ":")+1:]
	if !found || (action != "generateContent" && action != "streamGenerateContent") {
		return Target{}, errors.New("vertex cannot be called at " + endpoint)
	}
	credential := parseVertexCredential(provider.APIToken)
	settings := provider.Connection.Settings
	project := firstNonEmpty(credential.project, settings.ProjectID)
	location := strings.TrimSpace(settings.Location)
	if location == "" {
		location = "global"
		if credential.oauth() {
			location = "us-central1"
		}
	}
	if !vertexLocation.MatchString(location) {
		return Target{}, errors.New("vertex location is not valid")
	}
	path := "v1/" + endpoint
	switch {
	case project != "":
		if !vertexProject.MatchString(project) || strings.Contains(project, "..") {
			return Target{}, errors.New("vertex projectId is not a valid GCP project id")
		}
		path = "v1/projects/" + project + "/locations/" + location + "/" + endpoint
	case credential.oauth():
		// A service account or ADC credential must use the project-scoped path
		// (vertex.js:88-93).
		return Target{}, errors.New("vertex needs a project: add quota_project_id to the ADC JSON or set providerData.projectId on the connection")
	}
	target := Target{Endpoint: path}
	if action == "streamGenerateContent" {
		target.Query = "alt=sse"
	}
	return target, nil
}

func vertexHeaders(_ routing.ProviderRef, _ []byte, requestURL string) (map[string]string, error) {
	return map[string]string{"Accept": cloudCodeAccept(requestURL)}, nil
}

var vertexHook = Hook{
	// The credential is a JSON document or a key, never a bearer as it stands; Do
	// sets the header that carries it.
	NoOpenAIBearer:    true,
	Route:             vertexRoute,
	RequestHeaders:    vertexHeaders,
	Do:                VertexDo,
	Exchange:          true,
	RequireCredential: true,
}

type vertexToken struct {
	value   string
	expires time.Time
}

var vertexTokens = struct {
	mu    sync.Mutex
	byKey map[string]vertexToken
	now   func() time.Time
}{byKey: map[string]vertexToken{}, now: time.Now}

// vertexReset forgets every token, for tests.
func vertexReset() {
	vertexTokens.mu.Lock()
	vertexTokens.byKey, vertexTokens.now = map[string]vertexToken{}, time.Now
	vertexTokens.mu.Unlock()
}

// VertexDo authenticates the request and sends it. A credential that cannot be
// turned into a bearer sends nothing (vertex.js:142,154).
func VertexDo(provider routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	credential := parseVertexCredential(provider.APIToken)
	if !credential.oauth() {
		request.Header.Set("X-Goog-Api-Key", credential.raw)
		return client.Do(request)
	}
	token, err := vertexBearer(client, request, credential)
	if err != nil {
		if request.Body != nil {
			request.Body.Close()
		}
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return client.Do(request)
}

func vertexBearer(client *http.Client, request *http.Request, credential vertexCredential) (string, error) {
	sum := sha256.Sum256([]byte(credential.raw))
	key := hex.EncodeToString(sum[:])
	vertexTokens.mu.Lock()
	cached, ok := vertexTokens.byKey[key]
	now := vertexTokens.now()
	vertexTokens.mu.Unlock()
	if ok && cached.expires.Sub(now) > vertexExpiryBuffer {
		return cached.value, nil
	}
	form := url.Values{}
	if credential.kind == "service_account" {
		assertion, err := vertexAssertion(credential, now)
		if err != nil {
			return "", err
		}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
		form.Set("assertion", assertion)
	} else {
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", credential.fields.RefreshToken)
		form.Set("client_id", credential.fields.ClientID)
		form.Set("client_secret", credential.fields.ClientSecret)
	}
	mint, err := http.NewRequestWithContext(request.Context(), http.MethodPost, vertexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	mint.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mint.Header.Set("Accept", "application/json")
	response, err := client.Do(mint)
	if err != nil {
		return "", &CredentialError{Err: errors.New("vertex: the access token could not be requested")}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &CredentialError{Err: fmt.Errorf("vertex: failed to get an access token from the %s credential (status %d)", credential.kind, response.StatusCode)}
	}
	var reply struct {
		AccessToken string  `json:"access_token"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, vertexReplyLimit)).Decode(&reply); err != nil || strings.TrimSpace(reply.AccessToken) == "" ||
		strings.ContainsAny(reply.AccessToken, " \t\r\n\x00") {
		return "", &CredentialError{Err: errors.New("vertex: the token endpoint returned no usable access token")}
	}
	lifetime := time.Hour
	if reply.ExpiresIn > 0 {
		lifetime = time.Duration(reply.ExpiresIn * float64(time.Second))
	}
	vertexTokens.mu.Lock()
	if len(vertexTokens.byKey) >= vertexTokenEntries {
		for k, entry := range vertexTokens.byKey {
			if !entry.expires.After(vertexTokens.now()) {
				delete(vertexTokens.byKey, k)
			}
		}
		for k := range vertexTokens.byKey {
			if len(vertexTokens.byKey) < vertexTokenEntries {
				break
			}
			delete(vertexTokens.byKey, k)
		}
	}
	vertexTokens.byKey[key] = vertexToken{value: reply.AccessToken, expires: now.Add(lifetime)}
	vertexTokens.mu.Unlock()
	return reply.AccessToken, nil
}

// vertexAssertion is the signed JWT of tokenRefresh.js:90-99: RS256, issued by
// the service account for the token endpoint, valid for an hour.
func vertexAssertion(credential vertexCredential, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(credential.fields.PrivateKey, `\n`, "\n")))
	if block == nil {
		return "", &CredentialError{Err: errors.New("vertex: the service account private key is not PEM")}
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, isRSA := parsed.(*rsa.PrivateKey)
	if err != nil || !isRSA {
		return "", &CredentialError{Err: errors.New("vertex: the service account private key is not an RSA PKCS#8 key")}
	}
	encode := func(value any) string {
		raw, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	issued := now.Unix()
	signing := encode(map[string]string{"alg": "RS256"}) + "." + encode(map[string]any{
		"scope": vertexScope, "iss": credential.fields.ClientEmail, "aud": vertexTokenURL, "iat": issued, "exp": issued + 3600,
	})
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", &CredentialError{Err: errors.New("vertex: the assertion could not be signed")}
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
