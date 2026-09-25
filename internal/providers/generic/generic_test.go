package generic

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

func TestNodeValidationAndNativeRouteSelection(t *testing.T) {
	node := Node{ID: "g1", Name: "Gateway", Prefix: "gw", BaseURL: "https://gateway.example/v1", Transports: []Transport{ChatCompletions, Messages}}
	if err := node.Validate(false); err != nil {
		t.Fatal(err)
	}
	endpoint, ok, err := node.Endpoint("openai-chat")
	if err != nil || !ok || endpoint != "https://gateway.example/v1/chat/completions" {
		t.Fatalf("endpoint=%q ok=%v err=%v", endpoint, ok, err)
	}
	endpoint, ok, err = node.Endpoint("anthropic-messages")
	if err != nil || !ok || endpoint != "https://gateway.example/v1/messages" {
		t.Fatalf("endpoint=%q ok=%v err=%v", endpoint, ok, err)
	}
	if _, ok, _ := node.Endpoint("openai-responses"); ok {
		t.Fatal("unadvertised transport was resolved")
	}
	if _, ok, _ := node.Endpoint("gemini"); ok {
		t.Fatal("unknown protocol was resolved")
	}

	for _, invalid := range []Node{
		{Name: "n", Prefix: "p", BaseURL: "https://x", Transports: []Transport{ChatCompletions}},
		{ID: "g", Name: "n", Prefix: "p", BaseURL: "https://x", Transports: []Transport{"bogus"}},
		{ID: "g", Name: "n", Prefix: "p", BaseURL: "http://127.0.0.1:9000", Transports: []Transport{ChatCompletions}},
		{ID: "g", Name: "n", Prefix: "p", BaseURL: "https://x", Transports: []Transport{ChatCompletions, ChatCompletions}},
	} {
		if err := invalid.Validate(false); err == nil {
			t.Errorf("accepted %+v", invalid)
		}
	}
	priv := Node{ID: "g", Name: "n", Prefix: "p", BaseURL: "http://192.168.1.10:9000", Transports: []Transport{ChatCompletions}}
	if err := priv.Validate(false); err == nil {
		t.Fatal("private URL accepted without trusted-local policy")
	}
	if err := priv.Validate(true); err != nil {
		t.Fatalf("trusted-local URL rejected: %v", err)
	}
}

func TestDiscoverModelsBoundsAndAcceptsManualModels(t *testing.T) {
	var seenURL string
	var seenAuth string
	client := fakeClient{do: func(request *http.Request) (*http.Response, error) {
		seenURL = request.URL.String()
		seenAuth = request.Header.Get("Authorization")
		return response(http.StatusOK, `{"data":[{"id":"a"},{"id":"a"},{"id":"b"},{"id":""}]}`), nil
	}}
	models, err := DiscoverModels(context.Background(), Node{BaseURL: "https://gateway.example/v1"}, Connection{Key: "secret"}, false, client)
	if err != nil || seenURL != "https://gateway.example/v1/models" || seenAuth != "Bearer secret" {
		t.Fatalf("models=%v url=%s auth=%s err=%v", models, seenURL, seenAuth, err)
	}
	if strings.Join(models, ",") != "a,b" {
		t.Fatalf("models=%v", models)
	}
	if err := ValidateModel("manual", models); err == nil {
		t.Fatal("unknown model accepted when discovery succeeded")
	}
	if err := ValidateModel("c", nil); err != nil {
		t.Fatalf("manual model rejected without discovery: %v", err)
	}
	if err := ValidateModel("", models); err == nil {
		t.Fatal("empty model accepted")
	}

	for _, test := range []struct {
		name   string
		client fakeClient
	}{
		{name: "http error", client: fakeClient{do: func(*http.Request) (*http.Response, error) { return response(http.StatusUnauthorized, "denied"), nil }}},
		{name: "malformed", client: fakeClient{do: func(*http.Request) (*http.Response, error) { return response(http.StatusOK, "not-json"), nil }}},
		{name: "too large", client: fakeClient{do: func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, strings.Repeat("x", maxModelsResponseBytes+1)), nil
		}}},
		{name: "network", client: fakeClient{do: func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DiscoverModels(context.Background(), Node{BaseURL: "https://gateway.example"}, Connection{Key: "secret"}, false, test.client); err == nil {
				t.Fatal("discovery error was not reported")
			}
		})
	}
	if _, err := DiscoverModels(context.Background(), Node{BaseURL: "file:///etc/passwd"}, Connection{}, false, client); err == nil {
		t.Fatal("non-HTTP URL accepted")
	}
}
