package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeDiscoveryAuthStyles(t *testing.T) {
	for _, tc := range []struct {
		style  AuthStyle
		header string
	}{
		{AuthXApiKey, "x-api-key"}, {AuthGoogleAPIKey, "x-goog-api-key"}, {AuthCookie, "Cookie"},
	} {
		t.Run(string(tc.style), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(tc.header) != "synthetic" || r.Header.Get("Authorization") != "" {
					t.Error("wrong discovery authentication")
				}
				io.WriteString(w, `{"data":[{"id":"model"}]}`)
			}))
			defer server.Close()
			client := Client{AllowPrivateUpstreams: true}
			if _, err := client.Models(context.Background(), Request{ProviderID: "provider", BaseURL: server.URL, Credential: "synthetic", AuthStyle: tc.style}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
