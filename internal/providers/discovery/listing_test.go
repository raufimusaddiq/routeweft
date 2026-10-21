package discovery

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
)

// The reference reads a bare array of objects, and data, models or results, with
// the id taken from id, name or model (route.js:26-29, v1/models/route.js:261).
func TestParseModelEntriesReadsEveryReferenceListShape(t *testing.T) {
	for _, tc := range []struct {
		body string
		want []Model
	}{
		{`[{"id":"a"},{"name":"b"},{"model":"c"}]`, []Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}},
		{`{"results":[{"id":"r"}]}`, []Model{{ID: "r"}}},
		{`{"models":[{"name":"llama3","model":"llama3:latest","modified_at":"x"}]}`, []Model{{ID: "llama3"}}},
		{`{"models":[{"model":"only-model"}]}`, []Model{{ID: "only-model"}}},
		{`{"data":[{"id":"x","name":"Shown"}],"models":[{"id":"x"},{"id":"y"}]}`, []Model{{ID: "x", Name: "Shown"}, {ID: "y"}}},
		{`{}`, []Model{}},
	} {
		got, err := ParseModelEntries([]byte(tc.body))
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, %v; want %+v", tc.body, got, err, tc.want)
		}
	}
	for _, bad := range []string{`{"data":[{"id":"a"}`, `not json`, `{"data":"x"}`} {
		if _, err := ParseModelEntries([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestModelEntriesRunsAProviderListing(t *testing.T) {
	var got *http.Request
	var gotBody string
	client := &Client{HTTP: fakeClient{do: func(r *http.Request) (*http.Response, error) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		return response(200, `{"models":[{"slug":"gpt-5.5"}]}`), nil
	}}}
	models, err := client.ModelEntries(context.Background(), Request{
		ProviderID: "codex", BaseURL: "https://chatgpt.com/backend-api/codex", Credential: "tok",
		Method: "POST", Body: []byte(`{"project":"p"}`), Query: "client_version=0.154.0", Shape: wire.ShapeCodex,
		Headers: map[string]string{"X-Provider": "one"},
		Sign: func(rawURL string, body []byte) (map[string]string, error) {
			return map[string]string{"X-Signed": rawURL + "|" + string(body), "Accept": "signed"}, nil
		},
		Override: map[string]string{"Accept": "application/proto", "Authorization": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	const url = "https://chatgpt.com/backend-api/codex/models?client_version=0.154.0"
	if got.Method != "POST" || got.URL.String() != url || gotBody != `{"project":"p"}` || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %s body %q", got.Method, got.URL, gotBody)
	}
	// The signer sees the final URL and body; Override then wins over it and can drop a header.
	if got.Header.Get("X-Signed") != url+`|{"project":"p"}` || got.Header.Get("Accept") != "application/proto" || got.Header.Get("Authorization") != "" || got.Header.Get("X-Provider") != "one" {
		t.Fatalf("headers %v", got.Header)
	}
	if !strings.HasPrefix(models[0].ID, "gpt-5.5") || len(models) != 2 {
		t.Fatalf("models %+v", models)
	}
}

func TestModelEntriesSignerFailureSendsNothing(t *testing.T) {
	sent := false
	client := &Client{HTTP: fakeClient{do: func(*http.Request) (*http.Response, error) { sent = true; return response(200, `{}`), nil }}}
	_, err := client.ModelEntries(context.Background(), Request{ProviderID: "qoder", BaseURL: "https://api3.qoder.sh", Shape: wire.ShapeQoder,
		Sign: func(string, []byte) (map[string]string, error) { return nil, wire.ErrCosyMachineID }})
	if err == nil || sent {
		t.Fatalf("err=%v sent=%v", err, sent)
	}
}

func TestAnthropicCompatAuthSendsBothHeaders(t *testing.T) {
	var got http.Header
	client := &Client{HTTP: fakeClient{do: func(r *http.Request) (*http.Response, error) {
		got = r.Header
		return response(200, `{"data":[]}`), nil
	}}}
	if _, err := client.ModelEntries(context.Background(), Request{ProviderID: "gw", BaseURL: "https://gw.example.com/v1", Credential: "key", AuthStyle: AuthAnthropicCompat}); err != nil {
		t.Fatal(err)
	}
	if got.Get("x-api-key") != "key" || got.Get("Authorization") != "Bearer key" || got.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("headers %v", got)
	}
}
