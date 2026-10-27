package wire

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// An image request's IDE request id is the pin's: built from the model without
// its size suffix and image_gen (executors/antigravity.js:185-186), recorded by
// compat/parity/generate-antigravity-image.mjs.
func TestAntigravityImageRequestIDIsThePins(t *testing.T) {
	raw, err := os.ReadFile("../../protocol/gemini/testdata/antigravity_image_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Now int64 `json:"now"`
		IDs []struct {
			Model         string `json:"model"`
			UpstreamModel string `json:"upstreamModel"`
			Session       string `json:"session"`
			Turns         int    `json:"turns"`
			RequestID     string `json:"requestId"`
		} `json:"ids"`
	}
	if err := json.Unmarshal(raw, &pin); err != nil || len(pin.IDs) == 0 {
		t.Fatalf("fixture: %v", err)
	}
	for _, c := range pin.IDs {
		if got := gemini.AntigravityImageModel(c.Model); got != c.UpstreamModel {
			t.Fatalf("%s: upstream model %q, pin %q", c.Model, got, c.UpstreamModel)
		}
		contents := make([]map[string]any, c.Turns)
		for i := range contents {
			contents[i] = map[string]any{"role": "user", "parts": []any{map[string]any{"text": "x"}}}
		}
		envelope, _ := json.Marshal(map[string]any{
			"project": "p", "model": c.UpstreamModel, "userAgent": "antigravity", "requestType": gemini.AntigravityImageRequestType,
			"requestId": "agent-x", "request": map[string]any{"sessionId": c.Session, "contents": contents},
		})
		var sent struct {
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal(withIDERequestID(envelope, routing.ProviderRef{ConnectionID: "c"}, time.UnixMilli(pin.Now)), &sent)
		if sent.RequestID != c.RequestID {
			t.Errorf("%s: request id %q, pin %q", c.Model, sent.RequestID, c.RequestID)
		}
	}
}

// An image request asks for JSON whatever the client accepts; the agent path's
// headers are unchanged.
func TestAntigravityImageRequestAsksForJSON(t *testing.T) {
	image, _ := antigravityHeaders(routing.ProviderRef{}, []byte(`{"requestType":"image_gen","request":{}}`), "https://x/v1internal:generateContent")
	agent, _ := antigravityHeaders(routing.ProviderRef{}, []byte(`{"request":{}}`), "https://x/v1internal:streamGenerateContent?alt=sse")
	if image["Accept"] != "application/json" || image["User-Agent"] != antigravityUserAgent {
		t.Errorf("image headers %v", image)
	}
	if _, set := agent["Accept"]; set || agent["User-Agent"] != antigravityUserAgent {
		t.Errorf("agent headers %v", agent)
	}
}
