package gemini

import "testing"

func TestAPIKeyProviderMetadata(t *testing.T) {
	if ID != "gemini" || DefaultBaseURL != "https://generativelanguage.googleapis.com/v1beta/models" || APIKeyHeader != "x-goog-api-key" {
		t.Fatalf("metadata=%q %q %q", ID, DefaultBaseURL, APIKeyHeader)
	}
	models := Models()
	if len(models) != 11 {
		t.Fatalf("models=%v", models)
	}
	models[0] = "mutated"
	if Models()[0] == "mutated" {
		t.Fatal("Models leaked slice")
	}
	if len(Headers()) != 0 {
		t.Fatal("unexpected shared auth headers")
	}
}
