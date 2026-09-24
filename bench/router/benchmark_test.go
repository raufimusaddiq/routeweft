package router

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
)

// BenchmarkMockUpstreamRoundTrip records fixture HTTP overhead for repeatable
// comparisons. It is a baseline for the router benchmarks added with ingress;
// it intentionally does not claim to measure Routeweft routing, which is not
// implemented by this sprint item.
func BenchmarkMockUpstreamRoundTrip(b *testing.B) {
	set, err := fixtures.Load()
	if err != nil {
		b.Fatal(err)
	}
	var selected fixtures.Exchange
	for _, exchange := range set {
		if exchange.ID == "openai-chat-nonstreaming" {
			selected = exchange
			break
		}
	}
	if selected.ID == "" {
		b.Fatal("OpenAI Chat benchmark fixture missing")
	}
	server, err := mockupstream.Start(set)
	if err != nil {
		b.Fatal(err)
	}
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8}}
	defer client.CloseIdleConnections()
	body := []byte(selected.Request.Body)
	b.SetBytes(int64(len(body) + len(selected.Response.Body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		request, err := http.NewRequest(selected.Request.Method, server.URL()+selected.Request.Path, strings.NewReader(string(body)))
		if err != nil {
			b.Fatal(err)
		}
		for key, value := range selected.Request.Headers {
			request.Header.Set(key, value)
		}
		response, err := client.Do(request)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			b.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			b.Fatal(err)
		}
		if response.StatusCode != selected.Response.Status {
			b.Fatalf("status %d, want %d", response.StatusCode, selected.Response.Status)
		}
	}
}
