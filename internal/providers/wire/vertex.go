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

// Vertex AI: Gemini models ("vertex") and partner models on the OpenAI-compatible
// endpoint ("vertex-partner"), one executor at the pin (executors/vertex.js:61-64).
// The connection's credential is one of three things, told apart by what it parses
// as (vertex.js:66-72,135-156): a service account JSON, minted into a bearer with
// a signed JWT; an application default credentials JSON of an authorized user,
// refreshed into a bearer; or a raw API key. A partner connection with a raw key
// and no project has its project discovered by a probe (vertex.js:35-49,158-164).
//
// Divergences from the pin:
//   - A raw key travels in X-Goog-Api-Key, never as ?key= in the URL
//     (vertex.js:39,80,108). Google documents the header as the way to send a key
//     on REST and the query as the fallback that "includes your API key in the
//     URL, exposing your key to theft through URL scans"
//     (docs.cloud.google.com/docs/authentication/api-keys-use). The key reaches the
//     same API either way; only where it is written differs.
//   - The pin's already-minted bearer (credentials.accessToken, vertex.js:69) is
//     the service account token it persists after a 401 refresh (chatCore.js:
//     466-486, sse/handlers/chat.js:330-335); the next request mints over it
//     (vertex.js:140-144). Routeweft keeps that token in memory instead, so a
//     connection never holds one.
//   - The token is cached for the life of the process for an ADC credential too,
//     where the pin caches only the service account's (tokenRefresh.js:76-120)
//     and refreshes ADC on every request (vertex.js:146-156).
//   - The project probe is sent to the connection's base and through its client
//     (proxy included), where the pin's goes to a fixed host by plain fetch
//     (vertex.js:38-41), and its cache is bounded.

const (
	vertexTokenURL = "https://oauth2.googleapis.com/token"
	vertexScope    = "https://www.googleapis.com/auth/cloud-platform"
	// vertexExpiryBuffer is the pin's five minutes (tokenRefresh.js:83).
	vertexExpiryBuffer = 5 * time.Minute
	vertexTokenEntries = 256
	vertexReplyLimit   = 64 << 10
	vertexModelPrefix  = "publishers/google/models/"
	// vertexUnresolvedProject stands in the partner path for the project a raw key
	// is probed for (vertexPartnerTarget); VertexDo replaces it before anything is
	// sent. An underscore is never in a GCP project id, so it cannot name one.
	vertexUnresolvedProject = "_unresolved_"
	// vertexProbeModel is the pin's dummy model (vertex.js:39).
	vertexProbeModel = "__probe__:generateContent"
)

var vertexProjectPattern = regexp.MustCompile(`projects/([^/]+)/`)

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

// vertexPartnerHook shares the Gemini hook's authentication; its target is the
// partner endpoint, on the OpenAI Chat wire.
var vertexPartnerHook = Hook{
	NoOpenAIBearer:    true,
	Target:            vertexPartnerTarget,
	Do:                VertexDo,
	Exchange:          true,
	RequireCredential: true,
}

type vertexToken struct {
	value   string
	expires time.Time
}

var vertexProjects = struct {
	mu    sync.Mutex
	byKey map[string]string
}{byKey: map[string]string{}}

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
	vertexProjects.mu.Lock()
	vertexProjects.byKey = map[string]string{}
	vertexProjects.mu.Unlock()
}

func vertexCacheKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// vertexResolvePath puts the probed project into a partner path that left it
// unresolved; any other path is left as it is.
func vertexResolvePath(client *http.Client, request *http.Request, key string) error {
	marker := "/v1/projects/" + vertexUnresolvedProject + "/"
	prefix, rest, found := strings.Cut(request.URL.Path, marker)
	if !found {
		return nil
	}
	project, err := vertexProbeProject(client, request, prefix, key)
	if err != nil {
		return err
	}
	request.URL.Path, request.URL.RawPath = prefix+"/v1/projects/"+project+"/"+rest, ""
	return nil
}

// vertexProbeProject is resolveProjectId (vertex.js:35-49): a dummy request
// whose error names the key's project as projects/{id}/. Only a project found is
// cached; whatever the probe answers, the status is not read, as at the pin.
func vertexProbeProject(client *http.Client, request *http.Request, prefix, key string) (string, error) {
	cacheKey := vertexCacheKey(key)
	vertexProjects.mu.Lock()
	project, ok := vertexProjects.byKey[cacheKey]
	vertexProjects.mu.Unlock()
	if ok {
		return project, nil
	}
	refused := errors.New("vertex: could not resolve project_id from the API key; add a Project ID to the connection (providerData.projectId)")
	probeURL := *request.URL
	probeURL.Path, probeURL.RawPath, probeURL.RawQuery, probeURL.Fragment = prefix+"/v1/"+vertexModelPrefix+vertexProbeModel, "", "", ""
	probe, err := http.NewRequestWithContext(request.Context(), http.MethodPost, probeURL.String(), strings.NewReader("{}"))
	if err != nil {
		return "", refused
	}
	probe.Header.Set("Content-Type", "application/json")
	probe.Header.Set("X-Goog-Api-Key", key)
	response, err := client.Do(probe)
	if err != nil {
		if request.Context().Err() != nil {
			return "", request.Context().Err()
		}
		return "", refused
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, vertexReplyLimit))
	type reply struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	// json?.[0]?.error?.message || json?.error?.message (vertex.js:43)
	var message string
	var list []reply
	var single reply
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		message = list[0].Error.Message
	}
	if message == "" && json.Unmarshal(raw, &single) == nil {
		message = single.Error.Message
	}
	match := vertexProjectPattern.FindStringSubmatch(message)
	if match == nil || !vertexProject.MatchString(match[1]) || strings.Contains(match[1], "..") {
		return "", refused
	}
	project = match[1]
	vertexProjects.mu.Lock()
	if len(vertexProjects.byKey) >= vertexTokenEntries {
		for k := range vertexProjects.byKey {
			delete(vertexProjects.byKey, k)
			break
		}
	}
	vertexProjects.byKey[cacheKey] = project
	vertexProjects.mu.Unlock()
	return project, nil
}

// VertexDo authenticates the request and sends it, for both providers. A
// credential that cannot be turned into a bearer, or a partner key whose project
// cannot be discovered, sends nothing (vertex.js:142,154,161).
func VertexDo(provider routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	credential := parseVertexCredential(provider.APIToken)
	if !credential.oauth() {
		if err := vertexResolvePath(client, request, credential.raw); err != nil {
			if request.Body != nil {
				request.Body.Close()
			}
			return nil, err
		}
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
	key := vertexCacheKey(credential.raw)
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
