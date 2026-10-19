package shared

import (
	"net/http"
	"testing"
)

func TestParseErrorNormalizesProviderErrBodies(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   ErrorKind
	}{
		{"openai auth", http.StatusUnauthorized, `{"error":{"message":"bad key","type":"authentication_error","code":"invalid_api_key"}}`, ErrorKindAuth},
		{"openai quota", http.StatusTooManyRequests, `{"error":{"message":"quota","type":"insufficient_quota"}}`, ErrorKindQuota},
		{"openai context", http.StatusBadRequest, `{"error":{"message":"too long","code":"context_length_exceeded"}}`, ErrorKindContextLength},
		{"anthropic overloaded", 529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, ErrorKindOverloaded},
		{"anthropic rate limit", http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, ErrorKindRateLimited},
		{"gemini resource exhausted", http.StatusTooManyRequests, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`, ErrorKindRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseError(FamilyOpenAIChat, tc.status, []byte(tc.body))
			if got.Kind != tc.kind {
				t.Fatalf("kind=%q want=%q", got.Kind, tc.kind)
			}
			if got.Message == "" && tc.kind != ErrorKindUnknown {
				t.Fatal("message not extracted")
			}
		})
	}
}

func TestParseErrorFallsBackToStatusWhenBodySilent(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   ErrorKind
	}{
		{http.StatusUnauthorized, ErrorKindAuth},
		{http.StatusTooManyRequests, ErrorKindRateLimited},
		{http.StatusNotFound, ErrorKindNotFound},
		{http.StatusBadGateway, ErrorKindOverloaded},
	} {
		for _, body := range []string{"", "not json", "{}"} {
			if got := ParseError(FamilyOpenAIChat, tc.status, []byte(body)); got.Kind != tc.kind {
				t.Fatalf("status=%d body=%q kind=%q want=%q", tc.status, body, got.Kind, tc.kind)
			}
		}
	}
}
