package pxpipe

import (
	"context"
	"encoding/json"
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
func httpResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestPXPIPEThresholdAndSuccessfulTransform(t *testing.T) {
	var called bool
	input := transforms.Request{System: []string{strings.Repeat("s", 12)}, Messages: []transforms.Message{{Role: "user", Content: strings.Repeat("u", 12)}}}
	client := fakeClient{do: func(request *http.Request) (*http.Response, error) {
		called = true
		if request.Method != http.MethodPost || request.URL.String() != "https://pxpipe.example/transform" {
			t.Fatalf("request=%s %s", request.Method, request.URL)
		}
		var sent payload
		if err := json.NewDecoder(request.Body).Decode(&sent); err != nil || len(sent.Messages) != 1 {
			t.Fatalf("payload=%+v err=%v", sent, err)
		}
		return httpResponse(http.StatusOK, `{"system":["short"],"messages":[{"role":"user","content":"short"}]}`), nil
	}}
	var diagnostic Diagnostic
	transform := Transform{Enabled: true, URL: "https://pxpipe.example/transform", MinChars: 25, Client: client, Diagnostics: func(got Diagnostic) { diagnostic = got }}
	got, err := transform.Apply(input)
	if err != nil || called || !diagnostic.Skipped {
		t.Fatalf("threshold: err=%v called=%v diagnostic=%+v", err, called, diagnostic)
	}
	transform.MinChars = 24
	got, err = transform.Apply(input)
	if err != nil || !called || got.System[0] != "short" || got.Messages[0].Content != "short" {
		t.Fatalf("transform: got=%+v err=%v called=%v", got, err, called)
	}
	if input.System[0] != strings.Repeat("s", 12) {
		t.Fatal("input mutated")
	}
}

func TestPXPIPEFailuresFailOpenAndDiagnosticsRedactBody(t *testing.T) {
	input := transforms.Request{System: []string{"private prompt"}, Messages: []transforms.Message{{Role: "user", Content: strings.Repeat("secret", 8)}}}
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{name: "HTTP error", status: http.StatusBadGateway, body: "upstream detail"},
		{name: "invalid JSON", status: http.StatusOK, body: "invalid"},
		{name: "missing fields", status: http.StatusOK, body: `{"messages":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var diagnostic Diagnostic
			transform := Transform{Enabled: true, URL: "https://pxpipe.example", MinChars: 1, Client: fakeClient{do: func(*http.Request) (*http.Response, error) { return httpResponse(test.status, test.body), nil }}, Diagnostics: func(got Diagnostic) { diagnostic = got }}
			got := (transforms.Pipeline{Steps: []transforms.Transform{transform}}).Run(input)
			if got.Messages[0].Content != input.Messages[0].Content || !diagnostic.Attempted || diagnostic.Error == "" || strings.Contains(diagnostic.Error, "secret") {
				t.Fatalf("got=%+v diagnostic=%+v", got, diagnostic)
			}
		})
	}
}

func TestPXPIPEOversizedResponseFailsOpen(t *testing.T) {
	input := transforms.Request{Messages: []transforms.Message{{Role: "user", Content: strings.Repeat("x", 20)}}}
	transform := Transform{Enabled: true, URL: "https://pxpipe.example", MinChars: 1, MaxResponseBytes: 8, Client: fakeClient{do: func(*http.Request) (*http.Response, error) {
		return httpResponse(http.StatusOK, strings.Repeat("x", 64)), nil
	}}}
	got := (transforms.Pipeline{Steps: []transforms.Transform{transform}}).Run(input)
	if got.Messages[0].Content != input.Messages[0].Content {
		t.Fatalf("oversized response changed input: %+v", got)
	}
}

func TestPXPIPEThresholdCountsRunesNotBytes(t *testing.T) {
	input := transforms.Request{Messages: []transforms.Message{{Role: "user", Content: strings.Repeat("界", 5)}}}
	var diagnostic Diagnostic
	transform := Transform{Enabled: true, URL: "https://pxpipe.example", MinChars: 6, Client: fakeClient{do: func(*http.Request) (*http.Response, error) {
		t.Fatal("service called below rune threshold")
		return nil, nil
	}}, Diagnostics: func(got Diagnostic) { diagnostic = got }}
	if _, err := transform.Apply(input); err != nil {
		t.Fatal(err)
	}
	if !diagnostic.Skipped || diagnostic.InputChars != 5 {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
}

func TestPXPIPETimeoutHealthAndServiceStats(t *testing.T) {
	transform := Transform{Enabled: true, URL: "https://pxpipe.example/api", MinChars: 1, Timeout: 10 * time.Millisecond, Client: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			if request.URL.Path != "/api/health" {
				t.Fatalf("health path=%s", request.URL.Path)
			}
			return httpResponse(http.StatusOK, "ok"), nil
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	}}}
	service := NewService(transform)
	input := transforms.Request{Messages: []transforms.Message{{Role: "user", Content: strings.Repeat("x", 5)}}}
	got := (transforms.Pipeline{Steps: []transforms.Transform{service.Transform}}).Run(input)
	if got.Messages[0].Content != input.Messages[0].Content {
		t.Fatal("timeout did not fail open")
	}
	if err := service.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := service.Stats()
	if stats.Attempts != 1 || stats.Failed != 1 || len(service.Logs()) != 1 {
		t.Fatalf("stats=%+v logs=%+v", stats, service.Logs())
	}
	if err := (Transform{URL: "file:///tmp/x"}).Health(context.Background()); err == nil {
		t.Fatal("non-HTTP health URL accepted")
	}
}
