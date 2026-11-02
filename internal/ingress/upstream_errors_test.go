package ingress

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelUnavailableMessageNamesNoModelOutsideCatalogue(t *testing.T) {
	const upstream = `{"error":{"code":"model_unavailable","message":"The model agnes-2-0-flash:free is currently unavailable. Use kenari-free or pick another model from /v1/models.","param":null,"type":"model_unavailable"}}`
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rewriteModelUnavailable([]byte(upstream)), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Error.Code != "model_unavailable" || parsed.Error.Type != "model_unavailable" {
		t.Fatalf("code/type changed: %+v", parsed.Error)
	}
	if parsed.Error.Message != modelUnavailableMessage {
		t.Fatalf("message = %q", parsed.Error.Message)
	}
	if strings.Contains(parsed.Error.Message, "kenari-free") || strings.Contains(parsed.Error.Message, "agnes") {
		t.Fatalf("message names a model: %q", parsed.Error.Message)
	}
}

func TestRewriteModelUnavailableLeavesOtherAnswersAlone(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"rate_limited","message":"slow down"}}`,
		`{"error":"not an object"}`,
		`{"message":"no error object"}`,
		`upstream is not JSON`,
	} {
		if got := string(rewriteModelUnavailable([]byte(body))); got != body {
			t.Fatalf("changed %q to %q", body, got)
		}
	}
}
