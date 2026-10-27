package wire

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

func vertexRef(token string, settings routing.ProviderSettings) routing.ProviderRef {
	return routing.ProviderRef{ProviderID: "vertex", Protocol: "vertex", BaseURL: "https://aiplatform.googleapis.com", UpstreamModel: "gemini-2.5-flash", APIToken: token,
		Connection: routing.ConnectionMetadata{Settings: settings}}
}

const (
	vertexStream = "publishers/google/models/gemini-2.5-flash:streamGenerateContent"
	vertexBuffer = "publishers/google/models/gemini-2.5-flash:generateContent"
)

var (
	vertexSA  = `{"type":"service_account","client_email":"sa@p.iam.gserviceaccount.com","private_key":"unused","project_id":"sa-project"}`
	vertexADC = `{"type":"authorized_user","client_id":"cid","client_secret":"csecret","refresh_token":"rtok","quota_project_id":"adc-project"}`
)

// executors/vertex.js:83-109 (buildUrl), with the key moved out of the query.
func TestVertexURLs(t *testing.T) {
	for _, c := range []struct {
		name, token string
		settings    routing.ProviderSettings
		endpoint    string
		want        string
	}{
		{"service account", vertexSA, routing.ProviderSettings{}, vertexBuffer, "/v1/projects/sa-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"},
		{"service account stream", vertexSA, routing.ProviderSettings{ProjectID: "ignored"}, vertexStream, "/v1/projects/sa-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent?alt=sse"},
		{"adc quota project", vertexADC, routing.ProviderSettings{Location: "europe-west4"}, vertexBuffer, "/v1/projects/adc-project/locations/europe-west4/publishers/google/models/gemini-2.5-flash:generateContent"},
		{"adc setting project", strings.Replace(vertexADC, `,"quota_project_id":"adc-project"`, "", 1), routing.ProviderSettings{ProjectID: "set-project"}, vertexBuffer, "/v1/projects/set-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"},
		{"key with project", "AIza-fixture", routing.ProviderSettings{ProjectID: "key-project"}, vertexStream, "/v1/projects/key-project/locations/global/publishers/google/models/gemini-2.5-flash:streamGenerateContent?alt=sse"},
		{"key with project and location", "AIza-fixture", routing.ProviderSettings{ProjectID: "key-project", Location: "us-east5"}, vertexBuffer, "/v1/projects/key-project/locations/us-east5/publishers/google/models/gemini-2.5-flash:generateContent"},
		{"key without project", "AIza-fixture", routing.ProviderSettings{}, vertexStream, "/v1/publishers/google/models/gemini-2.5-flash:streamGenerateContent?alt=sse"},
	} {
		target, err := ResolveTarget(vertexRef(c.token, c.settings), c.endpoint)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		u, err := target.URL(transport.ValidatePublicURL)
		if err != nil || u.Host != "aiplatform.googleapis.com" || u.RequestURI() != c.want || strings.Contains(u.String(), "key=") {
			t.Errorf("%s: %v %v, want %s", c.name, u, err, c.want)
		}
	}
}

func TestVertexRefusesWhatItCannotBind(t *testing.T) {
	for name, c := range map[string]struct {
		token    string
		settings routing.ProviderSettings
		endpoint string
	}{
		"oauth without project": {strings.Replace(vertexADC, `,"quota_project_id":"adc-project"`, "", 1), routing.ProviderSettings{}, vertexBuffer},
		"unsafe project":        {"key", routing.ProviderSettings{ProjectID: "../x"}, vertexBuffer},
		"unsafe location":       {"key", routing.ProviderSettings{ProjectID: "p", Location: "a/b"}, vertexBuffer},
		"another endpoint":      {"key", routing.ProviderSettings{}, "chat/completions"},
	} {
		var refused *TargetError
		if _, err := ResolveTarget(vertexRef(c.token, c.settings), c.endpoint); !errors.As(err, &refused) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A raw key travels in a header, and the JSON credentials are never sent as a
// bearer as they stand.
func TestVertexKeyIsAHeaderNotAURLOrABearer(t *testing.T) {
	provider := vertexRef("AIza-fixture", routing.ProviderSettings{ProjectID: "p"})
	if headers := OpenAIHeaders(provider); headers.Get("Authorization") != "" {
		t.Fatalf("a bearer was set: %v", headers)
	}
	var seen *http.Request
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	request, _ := http.NewRequest(http.MethodPost, "https://aiplatform.googleapis.com/v1/x:generateContent", strings.NewReader("{}"))
	response, err := Send(provider, client, request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if seen.Header.Get("X-Goog-Api-Key") != "AIza-fixture" || seen.Header.Get("Authorization") != "" || seen.URL.RawQuery != "" {
		t.Fatalf("request %v %v", seen.Header, seen.URL)
	}
}

func pkcs8(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

type tokenServer struct {
	mu     sync.Mutex
	mints  int
	forms  []url.Values
	status int
	expiry int
}

func (s *tokenServer) client(t *testing.T) (*http.Client, func() *http.Request) {
	var api *http.Request
	return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == vertexTokenURL {
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			s.mu.Lock()
			s.mints++
			s.forms = append(s.forms, form)
			status, expiry := s.status, s.expiry
			s.mu.Unlock()
			if status == 0 {
				status = 200
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"access_token":"ya29.minted","expires_in":` + jsonInt(expiry) + `}`))}, nil
		}
		api = r
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}, func() *http.Request { return api }
}

func jsonInt(n int) string {
	if n == 0 {
		n = 3600
	}
	raw, _ := json.Marshal(n)
	return string(raw)
}

func sendVertex(t *testing.T, provider routing.ProviderRef, client *http.Client) error {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, "https://aiplatform.googleapis.com/v1/x:generateContent", strings.NewReader("{}"))
	response, err := Send(provider, client, request)
	if err == nil {
		response.Body.Close()
	}
	return err
}

// tokenRefresh.js:79-127: a service account is minted into a bearer with an RS256
// JWT for the token endpoint, cached until five minutes before it expires.
func TestVertexMintsAndCachesAServiceAccountToken(t *testing.T) {
	vertexReset()
	defer vertexReset()
	key, pemKey := pkcs8(t)
	// The pin accepts the key with its newlines escaped, as a pasted JSON holds it.
	credential, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "sa@p.iam.gserviceaccount.com", "private_key": strings.ReplaceAll(pemKey, "\n", `\n`), "project_id": "sa-project"})
	provider := vertexRef(string(credential), routing.ProviderSettings{})
	server := &tokenServer{}
	client, api := server.client(t)
	now := time.Unix(1_800_000_000, 0)
	vertexTokens.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if err := sendVertex(t, provider, client); err != nil {
			t.Fatal(err)
		}
		if api().Header.Get("Authorization") != "Bearer ya29.minted" {
			t.Fatalf("request %v", api().Header)
		}
	}
	if server.mints != 1 {
		t.Fatalf("minted %d times", server.mints)
	}
	form := server.forms[0]
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Fatalf("form %v", form)
	}
	parts := strings.Split(form.Get("assertion"), ".")
	if len(parts) != 3 {
		t.Fatalf("assertion %q", form.Get("assertion"))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var claims map[string]any
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(payload, &claims)
	if claims["iss"] != "sa@p.iam.gserviceaccount.com" || claims["aud"] != vertexTokenURL || claims["scope"] != vertexScope ||
		claims["iat"] != float64(now.Unix()) || claims["exp"] != float64(now.Unix()+3600) {
		t.Fatalf("claims %v", claims)
	}
	// Inside the five minutes before expiry the token is minted again.
	now = now.Add(time.Hour - 4*time.Minute)
	if err := sendVertex(t, provider, client); err != nil || server.mints != 2 {
		t.Fatalf("mints %d err %v", server.mints, err)
	}
}

// executors/vertex.js:146-156: an authorized-user credential is refreshed with its
// own client.
func TestVertexRefreshesAnADCToken(t *testing.T) {
	vertexReset()
	defer vertexReset()
	server := &tokenServer{}
	client, api := server.client(t)
	if err := sendVertex(t, vertexRef(vertexADC, routing.ProviderSettings{}), client); err != nil {
		t.Fatal(err)
	}
	form := server.forms[0]
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rtok" || form.Get("client_id") != "cid" || form.Get("client_secret") != "csecret" {
		t.Fatalf("form %v", form)
	}
	if api().Header.Get("Authorization") != "Bearer ya29.minted" {
		t.Fatalf("request %v", api().Header)
	}
}

// vertex.js:142,154: a token that cannot be minted sends nothing, and the
// credential is not in the error.
func TestVertexSendsNothingWhenTheTokenCannotBeMinted(t *testing.T) {
	vertexReset()
	defer vertexReset()
	server := &tokenServer{status: 400}
	client, api := server.client(t)
	err := sendVertex(t, vertexRef(vertexADC, routing.ProviderSettings{}), client)
	var credential *CredentialError
	if !errors.As(err, &credential) || api() != nil || strings.Contains(err.Error(), "csecret") || strings.Contains(err.Error(), "rtok") {
		t.Fatalf("err %v, request %v", err, api())
	}
	_, brokenPEM := "", `{"type":"service_account","client_email":"e","private_key":"not a key","project_id":"p"}`
	if err := sendVertex(t, vertexRef(brokenPEM, routing.ProviderSettings{}), client); !errors.As(err, &credential) {
		t.Fatalf("err %v", err)
	}
}

func partnerRef(token string, settings routing.ProviderSettings) routing.ProviderRef {
	provider := vertexRef(token, settings)
	provider.ProviderID, provider.Protocol, provider.UpstreamModel = "vertex-partner", "openai-chat", "zai-org/glm-5-maas"
	return provider
}

const partnerPath = "/locations/global/endpoints/openapi/chat/completions"

// executors/vertex.js:71-74: the service account's project, else the ADC's quota
// project, else the connection's; a raw key with none is left for the probe.
func TestVertexPartnerProjectPrecedence(t *testing.T) {
	for _, c := range []struct {
		name, token, project, want string
	}{
		{"service account over the setting", vertexSA, "set-project", "sa-project"},
		{"adc quota project over the setting", vertexADC, "set-project", "adc-project"},
		{"adc without one takes the setting", strings.Replace(vertexADC, `,"quota_project_id":"adc-project"`, "", 1), "set-project", "set-project"},
		{"key takes the setting", "AIza-fixture", " set-project ", "set-project"},
		{"key without one is probed", "AIza-fixture", "", vertexUnresolvedProject},
	} {
		target, err := ResolveTarget(partnerRef(c.token, routing.ProviderSettings{ProjectID: c.project}), "chat/completions")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		u, err := target.URL(transport.ValidatePublicURL)
		if err != nil || u.Path != "/v1/projects/"+c.want+partnerPath || u.RawQuery != "" {
			t.Errorf("%s: %v %v", c.name, u, err)
		}
	}
}

type probeServer struct {
	mu      sync.Mutex
	probes  []*http.Request
	reply   string
	api     *http.Request
	apiSeen int
}

func (s *probeServer) client() *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/models/__probe__:generateContent") {
			raw, _ := io.ReadAll(r.Body)
			if string(raw) != "{}" {
				return nil, errors.New("probe body " + string(raw))
			}
			s.probes = append(s.probes, r)
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(s.reply))}, nil
		}
		s.api, s.apiSeen = r, s.apiSeen+1
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
}

func sendPartner(t *testing.T, provider routing.ProviderRef, client *http.Client) error {
	t.Helper()
	target, err := ResolveTarget(provider, "chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	u, err := target.URL(transport.ValidatePublicURL)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(`{"model":"m"}`))
	response, err := Send(provider, client, request)
	if err == nil {
		response.Body.Close()
	}
	return err
}

// executors/vertex.js:35-49,158-164: a partner key with no project is probed with
// a dummy request whose error names the project, and the project is cached for
// the key. The key never enters a URL (see vertex.go).
func TestVertexPartnerProbesTheProjectOfARawKey(t *testing.T) {
	for name, reply := range map[string]string{
		"array":  `[{"error":{"code":404,"message":"Publisher Model ` + "`projects/key-project/locations/us-central1/publishers/google/models/__probe__`" + ` was not found.","status":"NOT_FOUND"}}]`,
		"object": `{"error":{"code":404,"message":"Publisher Model projects/key-project/locations/global/publishers/google/models/__probe__ not found"}}`,
	} {
		vertexReset()
		server := &probeServer{reply: reply}
		client := server.client()
		provider := partnerRef("AIza-"+name, routing.ProviderSettings{})
		for i := 0; i < 2; i++ {
			if err := sendPartner(t, provider, client); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if len(server.probes) != 1 {
			t.Fatalf("%s: probed %d times", name, len(server.probes))
		}
		probe := server.probes[0]
		if probe.Method != http.MethodPost || probe.URL.Host != "aiplatform.googleapis.com" || probe.URL.Path != "/v1/publishers/google/models/__probe__:generateContent" ||
			probe.URL.RawQuery != "" || probe.Header.Get("X-Goog-Api-Key") != "AIza-"+name || probe.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("%s: probe %s %v", name, probe.URL, probe.Header)
		}
		api := server.api
		if api.URL.Path != "/v1/projects/key-project"+partnerPath || api.URL.RawQuery != "" || strings.Contains(api.URL.String(), "AIza") ||
			api.Header.Get("X-Goog-Api-Key") != "AIza-"+name || api.Header.Get("Authorization") != "" {
			t.Fatalf("%s: request %s %v", name, api.URL, api.Header)
		}
	}
	vertexReset()
}

// vertex.js:161: no project in the answer sends nothing, says what to set, and
// is not cached; a project that is not a path segment is not spliced in.
func TestVertexPartnerSendsNothingWhenTheProbeFindsNoProject(t *testing.T) {
	for name, reply := range map[string]string{
		"no project":     `{"error":{"code":403,"message":"API key not valid. Please pass a valid API key."}}`,
		"not json":       `<html>nope</html>`,
		"unsafe project": `{"error":{"message":"projects/a..b/locations/x"}}`,
		"empty":          ``,
	} {
		vertexReset()
		server := &probeServer{reply: reply}
		client := server.client()
		provider := partnerRef("AIza-secret-fixture", routing.ProviderSettings{})
		for i := 0; i < 2; i++ {
			err := sendPartner(t, provider, client)
			if err == nil || !strings.Contains(err.Error(), "Project ID") || strings.Contains(err.Error(), "AIza") {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if server.apiSeen != 0 || len(server.probes) != 2 {
			t.Fatalf("%s: api %d probes %d", name, server.apiSeen, len(server.probes))
		}
	}
	vertexReset()
}

// A client that leaves during the probe ends it; nothing else is sent.
func TestVertexPartnerProbeEndsWithTheRequest(t *testing.T) {
	vertexReset()
	defer vertexReset()
	ctx, cancel := context.WithCancel(context.Background())
	sent := 0
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sent++
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	provider := partnerRef("AIza-fixture", routing.ProviderSettings{})
	target, _ := ResolveTarget(provider, "chat/completions")
	u, _ := target.URL(transport.ValidatePublicURL)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader("{}"))
	if _, err := Send(provider, client, request); !errors.Is(err, context.Canceled) || sent != 1 {
		t.Fatalf("err %v sent %d", err, sent)
	}
}

// vertex.js:112-123,140-156: a partner service account or ADC credential is
// minted into a bearer as for Gemini on Vertex; nothing is probed, and the
// credential is never the bearer as it stands.
func TestVertexPartnerAuthorizesWithAMintedToken(t *testing.T) {
	vertexReset()
	defer vertexReset()
	server := &tokenServer{}
	client, api := server.client(t)
	provider := partnerRef(vertexADC, routing.ProviderSettings{})
	if headers := OpenAIHeaders(provider); headers.Get("Authorization") != "" {
		t.Fatalf("a bearer was set: %v", headers)
	}
	if err := sendPartner(t, provider, client); err != nil {
		t.Fatal(err)
	}
	if got := api(); got.Header.Get("Authorization") != "Bearer ya29.minted" || got.Header.Get("X-Goog-Api-Key") != "" || got.URL.Path != "/v1/projects/adc-project"+partnerPath {
		t.Fatalf("request %s %v", got.URL, got.Header)
	}
}
