package headroom

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
)

type fakeClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeClient) Do(request *http.Request) (*http.Response, error) { return f.do(request) }
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestHeadroomCompressesWithBoundedRequestAndDiagnostic(t *testing.T) {
	input := transforms.Request{System: []string{"system"}, Messages: []transforms.Message{{Role: "user", Content: "user text"}}}
	var got payload
	var diagnostic Diagnostic
	transform := Transform{URL: "https://headroom.example/api/compress", Timeout: time.Second, CompressUserMessages: true, Client: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://headroom.example/api/compress" || request.Method != http.MethodPost {
			t.Fatalf("request=%s %s", request.Method, request.URL)
		}
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, "{\"system\":[\"compressed system\"],\"messages\":[{\"role\":\"user\",\"content\":\"compressed user\"}]}"), nil
	}}, Diagnostics: func(value Diagnostic) { diagnostic = value }}
	updated, err := transform.Apply(input)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CompressUserMessages || got.Messages[0].Content != "user text" {
		t.Fatalf("payload=%+v", got)
	}
	if updated.System[0] != "compressed system" || updated.Messages[0].Content != "compressed user" {
		t.Fatalf("updated=%+v", updated)
	}
	if input.System[0] != "system" || input.Messages[0].Content != "user text" {
		t.Fatal("input mutated")
	}
	if !diagnostic.Attempted || !diagnostic.Succeeded || diagnostic.Error != "" {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
}

func TestHeadroomFailOpenForPrivateURLStatusMalformedAndLargeBody(t *testing.T) {
	input := transforms.Request{System: []string{"original"}, Messages: []transforms.Message{{Role: "user", Content: "original"}}}
	cases := []struct {
		name, url string
		client    Client
		max       int64
	}{
		{name: "private URL", url: "http://127.0.0.1:8787"},
		{name: "status", url: "https://headroom.example", client: fakeClient{do: func(*http.Request) (*http.Response, error) { return jsonResponse(503, "{}"), nil }}},
		{name: "malformed", url: "https://headroom.example", client: fakeClient{do: func(*http.Request) (*http.Response, error) { return jsonResponse(200, "not-json"), nil }}},
		{name: "too large", url: "https://headroom.example", max: 4, client: fakeClient{do: func(*http.Request) (*http.Response, error) { return jsonResponse(200, strings.Repeat("x", 100)), nil }}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transform := Transform{URL: test.url, Client: test.client, MaxResponseBytes: test.max}
			got := (transforms.Pipeline{Steps: []transforms.Transform{transform}}).Run(input)
			if got.System[0] != "original" || got.Messages[0].Content != "original" {
				t.Fatalf("fail-open changed request: %+v", got)
			}
		})
	}
}

func TestHeadroomTimeoutDiagnosticsRedactContentAndHealth(t *testing.T) {
	input := transforms.Request{System: []string{"secret prompt"}, Messages: []transforms.Message{{Role: "user", Content: "secret body"}}}
	diagnostic := Diagnostic{}
	transform := Transform{URL: "https://headroom.example", Timeout: 10 * time.Millisecond, Diagnostics: func(value Diagnostic) { diagnostic = value }, Client: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}}}
	got := (transforms.Pipeline{Steps: []transforms.Transform{transform}}).Run(input)
	if got.Messages[0].Content != input.Messages[0].Content {
		t.Fatal("timeout did not fail open")
	}
	if diagnostic.Error == "" || strings.Contains(diagnostic.Error, "secret") {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	client := fakeClient{do: func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/health" {
			t.Fatalf("health=%s %s", request.Method, request.URL)
		}
		return jsonResponse(200, "ok"), nil
	}}
	if err := (Transform{URL: "https://headroom.example/api", Client: client}).Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (Transform{URL: "file:///tmp/x"}).Health(context.Background()); err == nil {
		t.Fatal("non-HTTP URL accepted")
	}
}

func TestHeadroomTransportErrorDiagnostic(t *testing.T) {
	var diagnostic Diagnostic
	transform := Transform{URL: "https://headroom.example", Diagnostics: func(value Diagnostic) { diagnostic = value }, Client: fakeClient{do: func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }}}
	_, err := transform.Apply(transforms.Request{System: []string{"secret"}})
	if err == nil || diagnostic.Error != "offline" || strings.Contains(diagnostic.Error, "secret") {
		t.Fatalf("err=%v diagnostic=%+v", err, diagnostic)
	}
}
