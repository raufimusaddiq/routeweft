package discovery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fakeClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeClient) Do(request *http.Request) (*http.Response, error) { return f.do(request) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestModelsFetchesBearerCatalogAndTrimsIDs(t *testing.T) {
	var seenPath, seenAuth string
	client := &Client{HTTP: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		seenPath = request.URL.Path
		seenAuth = request.Header.Get("Authorization")
		return response(200, `{"data":[{"id":"m-b"},{"id":"m-a"},{"id":"m-a"}]}`), nil
	}}}
	models, err := client.Models(context.Background(), Request{ProviderID: "p", BaseURL: "https://api.example.com/v1", Credential: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/v1/models" || seenAuth != "Bearer secret" {
		t.Fatalf("path=%q auth=%q", seenPath, seenAuth)
	}
	if len(models) != 2 || models[0] != "m-a" || models[1] != "m-b" {
		t.Fatalf("models=%v", models)
	}
}

func TestModelsSupportsApiKeyHeaderAndGeminiShape(t *testing.T) {
	var seenKey, seenBearer string
	client := &Client{HTTP: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		seenKey = request.Header.Get("x-api-key")
		seenBearer = request.Header.Get("Authorization")
		return response(200, `{"models":[{"name":"models/gemini-2.5-flash"}]}`), nil
	}}}
	models, err := client.Models(context.Background(), Request{ProviderID: "p", BaseURL: "https://gen.example.com/v1beta", AuthStyle: AuthXApiKey, Credential: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if seenKey != "key" || seenBearer != "" {
		t.Fatalf("key=%q bearer=%q", seenKey, seenBearer)
	}
	if len(models) != 1 || models[0] != "gemini-2.5-flash" {
		t.Fatalf("models=%v", models)
	}
}

func TestModelsNoAuthOmitsCredential(t *testing.T) {
	var seenAuth, seenKey string
	client := &Client{HTTP: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		seenAuth = request.Header.Get("Authorization")
		seenKey = request.Header.Get("x-api-key")
		return response(200, `["a","b"]`), nil
	}}}
	models, err := client.Models(context.Background(), Request{ProviderID: "p", BaseURL: "https://free.example.com", AuthStyle: AuthNone, Credential: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if seenAuth != "" || seenKey != "" {
		t.Fatalf("no-auth discovery leaked credential: auth=%q key=%q", seenAuth, seenKey)
	}
	if len(models) != 2 {
		t.Fatalf("models=%v", models)
	}
}

func TestModelsUsesExplicitPathAndRejectsTraversal(t *testing.T) {
	var seenPath string
	client := &Client{HTTP: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		seenPath = request.URL.Path
		return response(200, `{"data":[]}`), nil
	}}}
	if _, err := client.Models(context.Background(), Request{ProviderID: "kilocode", BaseURL: "https://api.kilo.ai/api", Path: "gateway/models"}); err != nil {
		t.Fatal(err)
	}
	if seenPath != "/api/gateway/models" {
		t.Fatalf("path=%q", seenPath)
	}
	if _, err := client.Models(context.Background(), Request{ProviderID: "p", BaseURL: "https://api.example.com", Path: "../evil"}); err == nil {
		t.Fatal("accepted traversal path")
	}
	if _, err := client.Models(context.Background(), Request{ProviderID: "", BaseURL: "https://api.example.com"}); err == nil {
		t.Fatal("accepted missing provider id")
	}
}

func TestModelsReportsHTTPFailureWithoutLeakingBody(t *testing.T) {
	client := &Client{HTTP: fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(401, `{"error":"invalid key sk-secret"}`), nil
	}}}
	_, err := client.Models(context.Background(), Request{ProviderID: "p", BaseURL: "https://api.example.com", Credential: "sk-secret"})
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("error=%v", err)
	}
}

func TestParseModelsRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseModels([]byte("not json")); err == nil {
		t.Fatal("accepted invalid JSON")
	}
}

func TestParseModelsRejectsPartiallyDecodableJSON(t *testing.T) {
	// A valid data[] prefix followed by a syntax error must not yield the partial
	// ID: accepting it would let a truncated catalog replace the durable one.
	partial := `{"data":[{"id":"valid-before-error"}, }`
	if models, err := ParseModels([]byte(partial)); err == nil {
		t.Fatalf("accepted partial decode: %v", models)
	}
	// A truncated bare array is also rejected rather than partially applied.
	if models, err := ParseModels([]byte(`["a",`)); err == nil {
		t.Fatalf("accepted truncated array: %v", models)
	}
}

func TestParseModelsAcceptsCleanShapes(t *testing.T) {
	models, err := ParseModels([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"a"}]}`))
	if err != nil || len(models) != 2 || models[0] != "a" || models[1] != "b" {
		t.Fatalf("models=%v err=%v", models, err)
	}
	models, err = ParseModels([]byte(`["z","y"]`))
	if err != nil || len(models) != 2 || models[0] != "y" || models[1] != "z" {
		t.Fatalf("bare models=%v err=%v", models, err)
	}
}
