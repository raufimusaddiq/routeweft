package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// Vertex reads providerSpecificData.projectId and .location (executors/
// vertex.js:71-74,94,103), which the pin takes from the create request's
// providerSpecificData (src/app/api/providers/route.js:129) and has no dashboard
// field for. Here the connection API's secret.providerData carries both into the
// serving settings, an edit that names only the location keeps the project and
// the key, and the URL is built from what was stored.
func TestVertexProjectAndLocationTravelThroughTheConnectionAPI(t *testing.T) {
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	t.Cleanup(func() { store.Close() })
	for _, spec := range registry.Builtins() {
		if spec.ID == "vertex" {
			h.opts.Providers = append(h.opts.Providers, spec)
		}
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections",
		`{"providerId":"vertex","name":"v","identity":"v","authKind":"api-key","secret":{"accessToken":"AIza-fixture","providerData":{"projectId":"key-project","location":"us-east5"}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)
	stored := connectionByID(t, creds, id)
	if stored.Settings.ProjectID != "key-project" || stored.Settings.Location != "us-east5" {
		t.Fatalf("settings=%+v", stored.Settings)
	}
	recorder = doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"secret":{"providerData":{"location":"europe-west4"}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	stored = connectionByID(t, creds, id)
	if stored.Settings.ProjectID != "key-project" || stored.Settings.Location != "europe-west4" || stored.Secret.AccessToken != "AIza-fixture" {
		t.Fatalf("settings=%+v", stored.Settings)
	}
	provider := routing.ProviderRef{ProviderID: "vertex", Protocol: "vertex", BaseURL: "https://aiplatform.googleapis.com", APIToken: stored.Secret.AccessToken,
		Connection: routing.ConnectionMetadata{Settings: stored.Settings}}
	target, err := wire.ResolveTarget(provider, "publishers/google/models/gemini-2.5-flash:generateContent")
	if err != nil {
		t.Fatal(err)
	}
	u, err := target.URL(transport.ValidatePublicURL)
	if err != nil || u.Path != "/v1/projects/key-project/locations/europe-west4/publishers/google/models/gemini-2.5-flash:generateContent" || u.RawQuery != "" {
		t.Fatalf("url %v %v", u, err)
	}
}

// A model test resolves the target with the connection's credential loaded, as
// serving does: a vertex-partner raw key with no projectId is probed for its
// project (executors/vertex.js:158-164), and an ADC credential names its own
// (vertex.js:71-74). Before #204's fix the probe resolved with no credential
// and refused both, although inference served them.
func TestVertexPartnerModelTestUsesTheCredentialsProject(t *testing.T) {
	for _, c := range []struct {
		name, token, project, auth string
	}{
		{"raw key, probed project", "AIza-model-test-fixture", "probed-project", ""},
		{"adc quota project", `{"type":"authorized_user","client_id":"cid","client_secret":"csecret","refresh_token":"model-test-rtok","quota_project_id":"adc-project"}`, "adc-project", "Bearer ya29.model-test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mints, probes, chats int
			upstream := probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
				reply := func(status int, body string) (*http.Response, error) {
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				}
				switch {
				case r.URL.String() == "https://oauth2.googleapis.com/token":
					mints++
					return reply(200, `{"access_token":"ya29.model-test","expires_in":3600}`)
				case r.URL.Path == "/v1/publishers/google/models/__probe__:generateContent":
					probes++
					return reply(404, `[{"error":{"message":"Publisher Model `+"`projects/probed-project/locations/us-central1/publishers/google/models/__probe__`"+` was not found."}}]`)
				case r.URL.Path == "/v1/projects/"+c.project+"/locations/global/endpoints/openapi/chat/completions":
					chats++
					if r.URL.RawQuery != "" || r.Header.Get("Authorization") != c.auth || (c.auth == "") != (r.Header.Get("X-Goog-Api-Key") == c.token) {
						return reply(401, `{"error":"credential misplaced"}`)
					}
					return reply(200, `{"choices":[{"message":{"content":"OK"}}]}`)
				}
				return reply(404, `{}`)
			})
			h, mux, store, creds, cookie := newProvidersAPI(t, false, &discovery.Client{HTTP: upstream})
			t.Cleanup(func() { store.Close() })
			for _, spec := range registry.Builtins() {
				if spec.ID == "vertex-partner" {
					h.opts.Providers = append(h.opts.Providers, spec)
				}
			}
			node, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "vertex-partner", Name: "Vertex Partner", BaseURL: "https://aiplatform.googleapis.com", Transports: []string{"openai-chat"}})
			if err != nil {
				t.Fatal(err)
			}
			connection, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "v", Identity: "v", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: c.token}})
			if err != nil {
				t.Fatal(err)
			}
			probed := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/test-models", `{"transport":"openai-chat","modelIds":["zai-org/glm-5-maas"]}`)
			if probed.Code != http.StatusOK || !strings.Contains(probed.Body.String(), `"ok":true`) {
				t.Fatalf("probe: %d %s", probed.Code, probed.Body)
			}
			if chats != 1 || probes+mints != 1 {
				t.Fatalf("chats %d probes %d mints %d", chats, probes, mints)
			}
		})
	}
}
