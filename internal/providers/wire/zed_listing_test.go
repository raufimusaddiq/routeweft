package wire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The Zed listing is the pin's customResolver (route.js:293-326) and
// resolveZedModels (zedAuth.js:362-404): exchange the account credential for an
// LLM token, list /models with that token, and drop disabled entries.
func TestZedListingExchangesTheAccountCredential(t *testing.T) {
	u := newZedUpstream(t)
	u.models = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[
			{"id":"claude-sonnet-4.5","display_name":"Claude Sonnet 4.5","provider":"anthropic","max_token_count":200000,"supports_images":true,"supports_thinking":true},
			{"id":"gpt-5","provider":"open_ai","is_disabled":true},
			{"id":["array-id"],"maxTokenCount":64000},
			{"id":{"id":"object-id"},"displayName":"Object"}]}`)
	}
	provider := u.provider()
	models, err := ZedListModels(context.Background(), u.Client(), provider)
	if err != nil {
		t.Fatal(err)
	}
	if got := listingIDs(models); got != "array-id,claude-sonnet-4.5,object-id" {
		t.Fatalf("models = %s", got)
	}
	claude := models[1]
	if claude.Name != "Claude Sonnet 4.5" || claude.ContextWindow != 200000 || strings.Join(claude.Capabilities, ",") != "vision,reasoning" {
		t.Fatalf("claude = %+v", claude)
	}
	if models[0].ContextWindow != 64000 || models[2].Name != "Object" {
		t.Fatalf("spellings = %+v", models)
	}

	tokens := u.seen("/client/llm_tokens")
	if len(tokens) != 1 || tokens[0].Method != http.MethodPost || tokens[0].Header.Get("Authorization") != "user-1 "+zedAccessToken ||
		!strings.Contains(tokens[0].Body, `"organization_id":"org-1"`) {
		t.Fatalf("exchange = %+v", tokens)
	}
	listing := u.seen("/models")
	if len(listing) != 1 || listing[0].Header.Get("Authorization") != "Bearer "+zedLLMToken ||
		listing[0].Header.Get(zedHeaderClientXAI) != "true" || listing[0].Header.Get("Accept") != "application/json" {
		t.Fatalf("models call = %+v", listing)
	}
	if seen := u.seen("/client/users/me"); len(seen) != 0 {
		t.Fatalf("a configured organization still fetched /client/users/me: %+v", seen)
	}
}

// The host calling the LLM token expired costs one more exchange, never a loop
// (zedAuth.js:304-324).
func TestZedListingRefreshesTheLLMTokenOnce(t *testing.T) {
	u := newZedUpstream(t)
	exchanges := 0
	u.tokens = func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		fmt.Fprintf(w, `{"token":"llm-%d"}`, exchanges)
	}
	listings := 0
	u.models = func(w http.ResponseWriter, r *http.Request) {
		listings++
		if listings == 1 {
			w.Header().Set(zedHeaderExpiredToken, "true")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer llm-2" {
			t.Errorf("retry authorization = %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"models":[{"id":"claude-sonnet-4.5"}]}`)
	}
	models, err := ZedListModels(context.Background(), u.Client(), u.provider())
	if err != nil || len(models) != 1 || exchanges != 2 || listings != 2 {
		t.Fatalf("models=%v err=%v exchanges=%d listings=%d", models, err, exchanges, listings)
	}
}

// A refused exchange is a status the caller can act on, with no body echoed.
func TestZedListingReportsARefusedExchange(t *testing.T) {
	u := newZedUpstream(t)
	u.tokens = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"secret-looking text"}`)
	}
	_, err := ZedListModels(context.Background(), u.Client(), u.provider())
	var status *ListingStatusError
	if err == nil || !errors.As(err, &status) || status.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-looking") {
		t.Fatalf("error echoed the provider body: %v", err)
	}
	if seen := u.seen("/models"); len(seen) != 0 {
		t.Fatalf("listed after a refused exchange: %+v", seen)
	}
}

// The cache is reachable in tests without waiting a TTL: a second call inside
// the token TTL reuses the exchange, and an expired token is replaced.
func TestZedListingReusesTheLLMTokenInsideItsTTL(t *testing.T) {
	u := newZedUpstream(t)
	models, err := ZedListModels(context.Background(), u.Client(), u.provider())
	if err != nil || len(models) == 0 {
		t.Fatalf("first listing: %v %v", models, err)
	}
	if _, err := ZedListModels(context.Background(), u.Client(), u.provider()); err != nil {
		t.Fatal(err)
	}
	if n := len(u.seen("/client/llm_tokens")); n != 1 {
		t.Fatalf("exchanges = %d, want 1 inside the TTL", n)
	}
	zedState.mu.Lock()
	zedState.now = func() time.Time { return time.Now().Add(zedTokenTTL + time.Minute) }
	zedState.mu.Unlock()
	if _, err := ZedListModels(context.Background(), u.Client(), u.provider()); err != nil {
		t.Fatal(err)
	}
	if n := len(u.seen("/client/llm_tokens")); n != 2 {
		t.Fatalf("exchanges = %d, want 2 after the TTL", n)
	}
}

func listingIDs(models []ListedModel) string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, model.ID)
	}
	return strings.Join(out, ",")
}
