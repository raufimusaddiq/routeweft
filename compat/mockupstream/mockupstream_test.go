package mockupstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
)

func TestReplayProtocolAndErrorFixturesByteForByte(t *testing.T) {
	set, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	server, err := Start(set)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Second}

	for _, exchange := range set {
		if exchange.Kind == fixtures.KindCancellation {
			continue
		}
		request, err := http.NewRequest(exchange.Request.Method, server.URL()+exchange.Request.Path, strings.NewReader(exchange.Request.Body))
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range exchange.Request.Headers {
			request.Header.Set(key, value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s: request: %v", exchange.ID, err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("%s: read/close response: %v / %v", exchange.ID, readErr, closeErr)
		}
		if response.StatusCode != exchange.Response.Status {
			t.Errorf("%s: status %d, want %d", exchange.ID, response.StatusCode, exchange.Response.Status)
		}
		if string(body) != exchange.Response.Body {
			t.Errorf("%s: response bytes differ\n got: %q\nwant: %q", exchange.ID, body, exchange.Response.Body)
		}
		for key, want := range exchange.Response.Headers {
			if got := response.Header.Get(key); got != want {
				t.Errorf("%s: header %s=%q, want %q", exchange.ID, key, got, want)
			}
		}
	}
	seen := server.Requests()
	if len(seen) != len(set)-1 {
		t.Fatalf("observed %d requests, want %d", len(seen), len(set)-1)
	}
}

func TestClientCancellationReachesDelayedUpstream(t *testing.T) {
	fixtureSet, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	var exchange fixtures.Exchange
	for _, candidate := range fixtureSet {
		if candidate.Kind == fixtures.KindCancellation {
			exchange = candidate
			break
		}
	}
	if exchange.ID == "" {
		t.Fatal("cancellation fixture missing")
	}
	server, err := Start(fixtureSet, WithMatcher(func(r *http.Request, _ string) Decision {
		return Decision{
			ExchangeID: exchange.ID,
			Status:     exchange.Response.Status,
			Headers:    exchange.Response.Headers,
			Body:       exchange.Response.Body,
			Delay:      time.Minute,
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	clientCtx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(clientCtx, exchange.Request.Method, server.URL()+exchange.Request.Path, strings.NewReader(exchange.Request.Body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		cancel()
		_ = server.Close()
		t.Fatal(err)
	}
	partial, err := io.ReadAll(io.LimitReader(response.Body, int64(len(exchange.Response.Body))))
	if err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("read partial response: %v", err)
	}
	if string(partial) != exchange.Response.Body {
		t.Fatalf("partial stream bytes differ: %q", partial)
	}
	cancel()
	_ = response.Body.Close()
	if err := server.Close(); err != nil {
		t.Fatalf("mock upstream did not observe client cancellation: %v", err)
	}
}

func TestUnmatchedPathReturnsActionable404(t *testing.T) {
	server, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response, err := http.Get(server.URL() + "/not-a-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "no fixture for path") {
		t.Fatalf("got %d %s", response.StatusCode, body)
	}
}
