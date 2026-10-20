package ingress

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func TestKiroWireRequestConstruction(t *testing.T) {
	parsed, err := openai.ParseChatRequest([]byte(chatBody))
	if err != nil {
		t.Fatal(err)
	}
	translation, err := TranslateChat(parsed, "kiro")
	if err != nil {
		t.Fatal(err)
	}
	provider := routing.ProviderRef{ProviderID: "kiro", Protocol: "kiro", BaseURL: "https://codewhisperer.us-east-1.amazonaws.com", APIToken: "fixture-token",
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{
			ProfileARN: "arn:aws:codewhisperer:us-east-1:123456789012:profile/fixture", AuthMethod: "external_idp",
		}}}
	h := &Handler{}
	inbound := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	inbound.Header.Set("Accept", "text/event-stream")
	inbound.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	request, err := h.newUpstreamRequest(inbound, nil, provider, translation.Endpoint, translation.Body, openAIProviderHeaders(provider))
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	if request.URL.String() != "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse" || request.Method != http.MethodPost {
		t.Fatalf("request %s %s", request.Method, request.URL)
	}
	if request.Header.Get("Accept") != "application/vnd.amazon.eventstream" || request.Header.Get("Authorization") != "Bearer fixture-token" || request.Header.Get("TokenType") != "EXTERNAL_IDP" || request.Header.Get("Anthropic-Beta") != "" {
		t.Fatalf("headers %v", request.Header)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	var profile string
	_ = json.Unmarshal(fields["profileArn"], &profile)
	if profile != provider.Connection.Settings.ProfileARN || len(fields["conversationState"]) == 0 {
		t.Fatalf("body %s", body)
	}
	provider.Connection.Settings.ProfileARN = ""
	request, err = h.newUpstreamRequest(inbound, nil, provider, translation.Endpoint, translation.Body, openAIProviderHeaders(provider))
	var refused *wire.TargetError
	if request != nil || !errors.As(err, &refused) || !strings.Contains(err.Error(), "profileArn") {
		t.Fatalf("missing profile created request: %v, %v", request, err)
	}
}

func TestKiroWithoutCredentialDoesNotDispatch(t *testing.T) {
	up := newRecorder(t)
	ref := routing.ProviderRef{ProviderID: "kiro", Protocol: openai.ChatProtocol, BaseURL: up.srv.URL}
	for _, token := range []string{"", "  "} {
		ref.APIToken = token
		out := send(t, ref, "/v1/chat/completions", chatBody)
		if out.Code != 503 || !strings.Contains(out.Body.String(), "credentials_unavailable") {
			t.Fatalf("status %d %s", out.Code, out.Body)
		}
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.seen) != 0 {
		t.Fatal("unauthenticated Kiro request reached upstream")
	}
}

func TestKiroMissingSettingsDoesNotDispatch(t *testing.T) {
	up := newRecorder(t)
	for _, missing := range []string{"profileArn", "authMethod"} {
		ref := routing.ProviderRef{ProviderID: "kiro", Protocol: openai.ChatProtocol, BaseURL: up.srv.URL, APIToken: "fixture-token",
			Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{ProfileARN: "fixture-profile", AuthMethod: "api_key"}}}
		if missing == "profileArn" {
			ref.Connection.Settings.ProfileARN = ""
		} else {
			ref.Connection.Settings.AuthMethod = ""
		}
		out := send(t, ref, "/v1/chat/completions", chatBody)
		if out.Code != 502 || !strings.Contains(out.Body.String(), missing) {
			t.Fatalf("missing %s: status %d %s", missing, out.Code, out.Body)
		}
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.seen) != 0 {
		t.Fatal("Kiro request with missing settings reached upstream")
	}
}
