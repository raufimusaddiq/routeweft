package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The upstream suggestion names a model this route does not list. The client
// must receive a message that only points at the catalogue.
func TestModelUnavailableSuggestionNamesNoModelOutsideCatalogue(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"model_unavailable","message":"The model vendor/model is currently unavailable. Use kenari-free or pick another model from /v1/models.","param":null,"type":"model_unavailable"}}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	got := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"test/vendor/model","messages":[{"role":"user","content":"hi"}]}`)
	if got.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", got.Code, got.Body)
	}
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	const safe = "The requested model is currently unavailable. Pick another model from /v1/models."
	if parsed.Error.Code != "model_unavailable" || parsed.Error.Type != "model_unavailable" || parsed.Error.Message != safe {
		t.Fatalf("message not made catalogue-safe: %+v", parsed.Error)
	}
	if strings.Contains(got.Body.String(), "kenari-free") {
		t.Fatalf("message names an id outside the catalogue: %s", got.Body)
	}
}
