package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

func kiroRef() routing.ProviderRef {
	return routing.ProviderRef{
		ProviderID: "kiro", Protocol: "kiro", APIToken: "fixture-token",
		BaseURL: "https://codewhisperer.us-east-1.amazonaws.com",
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{
			ProfileARN: "arn:aws:codewhisperer:us-east-1:123456789012:profile/fixture", AuthMethod: "api_key",
		}},
	}
}

func TestKiroHeadersMatchPin(t *testing.T) {
	for _, auth := range []string{"api_key", "external_idp", "idc", "builder-id", "google", "github", "import"} {
		for _, host := range []string{"codewhisperer.us-east-1.amazonaws.com", "q.eu-west-1.amazonaws.com", "runtime.us-east-1.kiro.dev", "proxy.invalid/codewhisperer.us-east-1.amazonaws.com"} {
			p := kiroRef()
			p.Connection.Settings.AuthMethod = auth
			p.BaseURL = "https://" + host
			got := OpenAIHeaders(p)
			invocation := got.Get("Amz-Sdk-Invocation-Id")
			id, err := uuid.Parse(invocation)
			if err != nil || id.Version() != 4 || id.String() != invocation {
				t.Fatalf("invocation id %q: %v", invocation, err)
			}
			if invocation == OpenAIHeaders(p).Get("Amz-Sdk-Invocation-Id") {
				t.Fatal("invocation id reused")
			}
			want := map[string]string{
				"Authorization": "Bearer fixture-token", "Content-Type": "application/json",
				"Accept": "application/vnd.amazon.eventstream", "User-Agent": "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0",
				"X-Amz-User-Agent": "aws-sdk-js/3.0.0 kiro-ide/1.0.0", "Amz-Sdk-Request": "attempt=1; max=3",
				"Amz-Sdk-Invocation-Id": invocation, "x-amz-sso-bearer": "fixture-token",
				"x-amzn-kiro-agent-mode": "spec", "x-amzn-codewhisperer-machine-id": "kiro-desktop",
				"x-amzn-codewhisperer-profile-arn": p.Connection.Settings.ProfileARN,
			}
			if auth == "api_key" {
				want["TokenType"] = "API_KEY"
			} else if auth == "external_idp" {
				want["TokenType"] = "EXTERNAL_IDP"
			}
			if strings.HasPrefix(host, "codewhisperer.") {
				want["X-Amz-Target"] = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
			}
			if len(got) != len(want) {
				t.Fatalf("%s %s: unexpected header set: %v", auth, host, got)
			}
			for name, value := range want {
				if got.Get(name) != value {
					t.Errorf("%s %s: %s = %q, want %q", auth, host, name, got.Get(name), value)
				}
			}
			if got.Get("Anthropic-Beta") != "" {
				t.Fatal("Kiro executor does not send anthropic-beta")
			}
		}
	}
}

func TestKiroTargetAndStoredProfile(t *testing.T) {
	p := kiroRef()
	target, err := ResolveTarget(p, "chat/completions")
	if err != nil || target.BaseURL != p.BaseURL || target.Endpoint != "generateAssistantResponse" || target.Query != "" {
		t.Fatalf("target %+v: %v", target, err)
	}
	body := []byte(`{"conversationState":{"future":true},"profileArn":"client-selected-profile","future":{"keep":1}}`)
	got := FinalizeBody(p, body)
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	var profile string
	_ = json.Unmarshal(parsed["profileArn"], &profile)
	if profile != p.Connection.Settings.ProfileARN || string(parsed["conversationState"]) != `{"future":true}` || string(parsed["future"]) != `{"keep":1}` {
		t.Fatalf("final body %s", got)
	}
	if again := FinalizeBody(p, got); string(again) != string(got) {
		t.Fatalf("not idempotent: %s", again)
	}
	p.Connection.Settings.ProfileARN = "arn:aws:codewhisperer:us-east-1:123456789012:profile/other"
	if other := FinalizeBody(p, body); string(other) == string(got) {
		t.Fatal("another connection reused the profile")
	}
}

func TestKiroFailsClosed(t *testing.T) {
	for _, value := range []string{"invalid\r\nheader", "invalid\x00header"} {
		p := kiroRef()
		p.Connection.Settings.AuthMethod = "external_idp"
		p.Connection.Settings.ProfileARN = value
		_, err := ResolveTarget(p, "generateAssistantResponse")
		var refused *TargetError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "profileArn") {
			t.Fatalf("profileArn %q: error %v", value, err)
		}
	}
	for _, value := range []string{"", "  ", "invalid\r\nheader", "invalid\x00header"} {
		p := kiroRef()
		p.Connection.Settings.AuthMethod = value
		_, err := ResolveTarget(p, "generateAssistantResponse")
		var refused *TargetError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "authMethod") {
			t.Fatalf("authMethod %q: error %v", value, err)
		}
	}
	for _, token := range []string{"", "  "} {
		p := kiroRef()
		p.APIToken = token
		if !RequiresCredential("kiro") || kiroHeaders(p) != nil {
			t.Fatal("Kiro allowed an unauthenticated request")
		}
	}
}

func TestKiroHeadersDoNotReachOtherProviders(t *testing.T) {
	kiro := OpenAIHeaders(kiroRef())
	for _, id := range providerIDs() {
		if id == "kiro" {
			continue
		}
		p := kiroRef()
		p.ProviderID = id
		for _, headers := range []map[string][]string{OpenAIHeaders(p), MessagesHeaders(p, "", "")} {
			for name := range kiro {
				if name == "Authorization" || name == "Content-Type" {
					continue
				}
				if name == "Accept" || name == "User-Agent" {
					if len(headers[name]) != 0 && headers[name][0] == kiro.Get(name) {
						t.Errorf("%s received Kiro %s", id, name)
					}
					continue
				}
				if _, present := headers[name]; present {
					t.Errorf("%s received Kiro header %s", id, name)
				}
			}
		}
		// Codex derives its cache key; OpenCode Go and GitHub choose streaming per model.
		if want := id == "codex" || id == "opencode-go" || id == "github"; (For(id).FinalizeAccount != nil) != want {
			t.Errorf("%s: account finalizer present=%v, want %v", id, For(id).FinalizeAccount != nil, want)
		}
	}
}

func BenchmarkKiroFinalizeBody(b *testing.B) {
	p := kiroRef()
	body := []byte(`{"conversationState":{"chatTriggerType":"MANUAL","currentMessage":{"userInputMessage":{"content":"hello","modelId":"claude-sonnet-4.5","origin":"AI_EDITOR"}}}}`)
	for _, id := range []string{"unhooked", "kiro"} {
		b.Run(id, func(b *testing.B) {
			p.ProviderID = id
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				FinalizeBody(p, body)
			}
		})
	}
}

func TestKiroCredentialHeadersAreRedacted(t *testing.T) {
	headers := OpenAIHeaders(kiroRef())
	for _, name := range []string{"Authorization", "x-amz-sso-bearer"} {
		if got := telemetry.RedactHeaderValue(name, headers.Get(name)); got != "[redacted]" {
			t.Errorf("credential header %s escaped redaction", name)
		}
	}
}

func TestKiroMatchingProfilePreservesOriginalBytes(t *testing.T) {
	p := kiroRef()
	encoded, err := json.Marshal(p.Connection.Settings.ProfileARN)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{string(encoded), strings.Replace(string(encoded), "arn:", `\u0061rn:`, 1)} {
		original := []byte("{ \n  \"future\": true, \"profileArn\" : " + profile + " }")
		var parsed Body
		if err := json.Unmarshal(original, &parsed); err != nil {
			t.Fatal(err)
		}
		if kiroBody(p, parsed) {
			t.Fatal("matching profile reported a change")
		}
		if got := FinalizeBody(p, original); string(got) != string(original) {
			t.Fatalf("unchanged body was re-encoded: %s", got)
		}
	}
}

func TestKiroAPIKeyMayOmitStoredProfile(t *testing.T) {
	for _, profile := range []string{"", "  "} {
		p := kiroRef()
		p.Connection.Settings.ProfileARN = profile
		if _, err := ResolveTarget(p, "chat/completions"); err != nil {
			t.Fatalf("API key without profile refused: %v", err)
		}
		if _, present := OpenAIHeaders(p)["X-Amzn-Codewhisperer-Profile-Arn"]; present {
			t.Fatal("missing profile produced a header")
		}
		original := []byte(`{ "conversationState" : {"future": true} }`)
		if got := FinalizeBody(p, original); string(got) != string(original) {
			t.Fatalf("absent profile changed body: %s", got)
		}
		var fields Body
		if err := json.Unmarshal(FinalizeBody(p, []byte(`{"profileArn":"client-selected-profile","future":true}`)), &fields); err != nil {
			t.Fatal(err)
		}
		if _, present := fields["profileArn"]; present || string(fields["future"]) != "true" {
			t.Fatalf("client profile survived omission: %s", fields["profileArn"])
		}
	}
}

func TestKiroCredentialHeadersUseOneTrimmedToken(t *testing.T) {
	for _, token := range []string{"", "  ", "\t\r\n", "fixture-token", " \tfixture-token\r\n"} {
		p := kiroRef()
		p.APIToken = token
		headers := OpenAIHeaders(p)
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			if len(headers) != 0 {
				t.Fatal("blank credential emitted headers")
			}
		} else if headers.Get("Authorization") != "Bearer "+trimmed || headers.Get("x-amz-sso-bearer") != trimmed {
			t.Fatal("credential headers disagree with the trimmed token")
		}
	}
}

// A builder-id or social connection that never resolved its own profile serves
// with the shared default ARN the pin uses (open-sse/config/kiroConstants.js:30-42;
// openai-to-kiro.js:325-341). API key, IDC and external-IdP credentials are
// account-bound: they send no shared default, only what the connection resolved.
func TestKiroSharedProfileFallbacks(t *testing.T) {
	for _, c := range []struct {
		auth, want string
	}{
		{"builder-id", kiroDefaultBuilderARN},
		{"google", kiroDefaultSocialARN},
		{"github", kiroDefaultSocialARN},
		{"import", kiroDefaultBuilderARN},
		{"api_key", ""},
		{"idc", ""},
		{"external_idp", ""},
	} {
		p := kiroRef()
		p.Connection.Settings.AuthMethod = c.auth
		p.Connection.Settings.ProfileARN = ""
		if _, err := ResolveTarget(p, "generateAssistantResponse"); err != nil {
			t.Fatalf("%s: %v", c.auth, err)
		}
		if got := kiroHeaders(p)["x-amzn-codewhisperer-profile-arn"]; got != c.want {
			t.Errorf("%s header profile = %q, want %q", c.auth, got, c.want)
		}
		got := FinalizeBody(p, []byte(`{"conversationState":{},"profileArn":"client"}`))
		var parsed map[string]json.RawMessage
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatal(err)
		}
		var profile string
		_ = json.Unmarshal(parsed["profileArn"], &profile)
		if profile != c.want {
			t.Errorf("%s body profile = %q, want %q (%s)", c.auth, profile, c.want, got)
		}
	}
	// The connection's own profile always wins.
	p := kiroRef()
	p.Connection.Settings.AuthMethod = "google"
	p.Connection.Settings.ProfileARN = "arn:aws:codewhisperer:eu-west-1:1:profile/mine"
	if got := kiroHeaders(p)["x-amzn-codewhisperer-profile-arn"]; got != p.Connection.Settings.ProfileARN {
		t.Fatalf("explicit profile lost: %q", got)
	}
}
