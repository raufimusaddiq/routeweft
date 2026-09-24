package fixtures

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedFixturesAreValid(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(set) == 0 {
		t.Fatal("no fixtures embedded")
	}
	byProtocol := make(map[Protocol]int)
	kinds := make(map[Kind]int)
	for _, exchange := range set {
		byProtocol[exchange.Protocol]++
		kinds[exchange.Kind]++
	}
	for _, protocol := range []Protocol{OpenAIChat, OpenAIResponses, Anthropic, Gemini, Ollama, SystemOne} {
		if byProtocol[protocol] == 0 {
			t.Errorf("missing fixture for protocol %s", protocol)
		}
	}
	for _, kind := range []Kind{KindNonStreaming, KindStreaming, KindCancellation, KindAuthError, KindUpstreamError} {
		if kinds[kind] == 0 {
			t.Errorf("missing fixture kind %s", kind)
		}
	}
}

func TestFixtureBodiesAreSyntheticAndParseable(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, exchange := range set {
		for _, body := range []string{exchange.Request.Body, exchange.Response.Body} {
			if !looksLikeSynthetic(body) {
				t.Errorf("fixture %s contains non-fixture content", exchange.ID)
			}
		}
		if exchange.Kind == KindNonStreaming {
			assertValidJSON(t, exchange.ID, exchange.Response.Body)
		}
		assertNoSecretShapes(t, exchange)
	}
}

func TestStreamingFixturesTerminateExactlyOnce(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, exchange := range set {
		if exchange.Kind != KindStreaming {
			if exchange.Terminal != "" {
				t.Errorf("fixture %s has a terminal marker but is %s", exchange.ID, exchange.Kind)
			}
			continue
		}
		if count := strings.Count(exchange.Response.Body, exchange.Terminal); count != 1 {
			t.Errorf("fixture %s terminal %q appears %d times, want exactly once", exchange.ID, exchange.Terminal, count)
		}
	}
}

func assertValidJSON(t *testing.T, id, body string) {
	t.Helper()
	if !json.Valid([]byte(body)) {
		t.Errorf("fixture %s response is not valid JSON", id)
	}
}

func looksLikeSynthetic(body string) bool {
	lower := strings.ToLower(body)
	for _, forbidden := range []string{"sk-", "sk_live", "bearer ey", "refresh_token"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func assertNoSecretShapes(t *testing.T, exchange Exchange) {
	t.Helper()
	if strings.Contains(exchange.Request.Body, "real-api-key") {
		t.Errorf("fixture %s records a real-looking key", exchange.ID)
	}
}
