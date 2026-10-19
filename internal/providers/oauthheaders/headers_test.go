package oauthheaders

import "testing"

func TestKnownProvidersCarryIdentityHeaders(t *testing.T) {
	if Headers("openai") != nil {
		t.Fatal("unknown provider should add no fingerprint")
	}
	iflow := Headers("iflow")
	if iflow["User-Agent"] != "iFlow-Cli" {
		t.Fatalf("iflow headers=%v", iflow)
	}
	github := Headers("github")
	if github["copilot-integration-id"] != CopilotIntegrationID || github["editor-version"] != CopilotEditorVersion || github[CopilotInitiatorHeader] != CopilotInitiatorValue {
		t.Fatalf("github headers=%v", github)
	}
	// Copilot identity must never smuggle a credential.
	for name := range github {
		if name == "Authorization" || name == "X-Api-Key" {
			t.Fatalf("credential leaked into identity headers: %v", github)
		}
	}
	for _, id := range []string{"cline", "clinepass"} {
		headers := Headers(id)
		if headers[ClineRefererKey] != ClineReferer || headers[ClineTitleKey] != ClineTitle {
			t.Errorf("%s headers=%v", id, headers)
		}
	}
	if Headers("codebuddy-cn")["User-Agent"] != CodeBuddyCNUserAgent || Headers("codebuddy-intl")["User-Agent"] != CodeBuddyIntlUserAgent {
		t.Fatal("codebuddy identity headers missing")
	}
	// Mutating one returned map must not affect the next call.
	aliased := Headers("iflow")
	aliased["User-Agent"] = "mutated"
	if Headers("iflow")["User-Agent"] != "iFlow-Cli" {
		t.Fatal("identity header map leaked")
	}
}
