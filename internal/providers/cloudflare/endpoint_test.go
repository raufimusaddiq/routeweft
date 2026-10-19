package cloudflare

import "testing"

func TestEndpointsKeepTypedAndChatTransportsSeparate(t *testing.T) {
	for _, tc := range []struct{ protocol, model, endpoint string }{
		{"systemone", Clef, "account-a/ai/run/@cf/cloudflare/clef"},
		{"systemone", ClefFlash, "account-a/ai/run/@cf/cloudflare/clef-flash"},
		{"openai-chat", "@cf/meta/llama", "account-a/ai/v1/chat/completions"},
	} {
		got, ok := Endpoint(tc.protocol, "account-a", tc.model)
		if !ok || got != tc.endpoint {
			t.Errorf("endpoint=%s ok=%v want=%s", got, ok, tc.endpoint)
		}
	}
	for _, tc := range []struct{ protocol, model string }{
		{"systemone", "@cf/meta/llama"}, {"systemone", "../escape"},
		{"openai-chat", Clef}, {"openai-responses", Clef},
	} {
		if _, ok := Endpoint(tc.protocol, "account-a", tc.model); ok {
			t.Errorf("unsupported pair accepted: %+v", tc)
		}
	}
	for _, account := range []string{"", ".", "..", "a/b", "a%2fb", "a?b", "a#b", "a b", "a\\b"} {
		if _, ok := Endpoint("systemone", account, Clef); ok {
			t.Errorf("unsafe account accepted: %q", account)
		}
	}
}
