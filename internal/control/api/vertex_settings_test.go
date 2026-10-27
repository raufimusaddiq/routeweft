package api

import (
	"encoding/json"
	"net/http"
	"testing"

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
