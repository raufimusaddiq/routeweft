package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
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
			Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{ProfileARN: "fixture-profile", AuthMethod: "external_idp"}}}
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

type kiroImportClient struct {
	t     *testing.T
	calls int
}

func (c *kiroImportClient) Do(r *http.Request) (*http.Response, error) {
	c.calls++
	// The pin proves a key with GET ListAvailableModels and the API_KEY token type
	// (services/kiro.js:299-325) and refuses a key whose catalog is empty.
	if r.Method != http.MethodGet || r.URL.String() != "https://q.us-east-1.amazonaws.com/ListAvailableModels?origin=AI_EDITOR" ||
		r.Header.Get("Authorization") != "Bearer fixture-api-key" || r.Header.Get("TokenType") != "API_KEY" {
		c.t.Fatalf("unexpected onboarding request: %s %s", r.Method, r.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"models":[{"modelId":"fixture-model"}]}`))}, nil
}

func TestKiroAPIKeyOnboardingResultConstructsRequestWithoutProfile(t *testing.T) {
	client := &kiroImportClient{t: t}
	result, err := onboarding.ManualFlows()["kiro"].Import(context.Background(), onboarding.Params{Input: "fixture-api-key", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	data := result.Secret.ProviderData
	if client.calls != 1 || data["authMethod"] != "api_key" || data["profileArn"] != "" {
		t.Fatal("unexpected API-key onboarding result")
	}
	p := routing.ProviderRef{ProviderID: "kiro", Protocol: "kiro", BaseURL: "https://q.us-east-1.amazonaws.com", APIToken: result.Secret.AccessToken,
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{AuthMethod: data["authMethod"], ProfileARN: data["profileArn"], Region: data["region"]}}}
	h := &Handler{}
	request, err := h.newUpstreamRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), nil, p, "chat/completions", []byte(`{"conversationState":{},"profileArn":"client-selected-profile"}`), openAIProviderHeaders(p))
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	if request.URL.Path != "/generateAssistantResponse" || request.Header.Get("TokenType") != "API_KEY" || request.Header.Get("Authorization") != "Bearer fixture-api-key" {
		t.Fatal("API-key request has wrong target or auth headers")
	}
	if _, present := request.Header["X-Amzn-Codewhisperer-Profile-Arn"]; present {
		t.Fatal("API-key request emitted a profile header")
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["profileArn"]; present {
		t.Fatal("API-key request retained a client-supplied profile")
	}
}
