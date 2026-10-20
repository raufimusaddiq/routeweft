package wire

import (
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const kiroOperation = "generateAssistantResponse"

func kiroTarget(provider routing.ProviderRef) (Target, error) {
	settings := provider.Connection.Settings
	for _, setting := range []struct{ name, value string }{
		{"profileArn", settings.ProfileARN}, {"authMethod", settings.AuthMethod},
	} {
		if strings.TrimSpace(setting.value) == "" {
			return Target{}, errors.New("kiro connection has no " + setting.name + ": set providerData." + setting.name + " on the connection")
		}
		if strings.IndexFunc(setting.value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return Target{}, errors.New("kiro connection has invalid " + setting.name)
		}
	}
	return Target{Endpoint: kiroOperation}, nil
}

func kiroHeaders(provider routing.ProviderRef) map[string]string {
	if strings.TrimSpace(provider.APIToken) == "" {
		return nil
	}
	settings := provider.Connection.Settings
	headers := map[string]string{
		"Content-Type":                     "application/json",
		"Accept":                           "application/vnd.amazon.eventstream",
		"User-Agent":                       "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0",
		"X-Amz-User-Agent":                 "aws-sdk-js/3.0.0 kiro-ide/1.0.0",
		"Amz-Sdk-Request":                  "attempt=1; max=3",
		"Amz-Sdk-Invocation-Id":            uuid.NewString(),
		"x-amz-sso-bearer":                 provider.APIToken,
		"x-amzn-kiro-agent-mode":           "spec",
		"x-amzn-codewhisperer-machine-id":  "kiro-desktop",
		"x-amzn-codewhisperer-profile-arn": settings.ProfileARN,
	}
	switch settings.AuthMethod {
	case "api_key":
		headers["TokenType"] = "API_KEY"
	case "external_idp":
		headers["TokenType"] = "EXTERNAL_IDP"
	}
	if base, err := url.Parse(provider.BaseURL); err == nil && strings.HasPrefix(base.Hostname(), "codewhisperer.") {
		headers["X-Amz-Target"] = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	}
	return headers
}

func kiroBody(provider routing.ProviderRef, body Body) bool {
	if provider.Protocol != "kiro" {
		return false
	}
	// The connection owns the profile; a client body cannot select another account.
	return setJSON(body, "profileArn", provider.Connection.Settings.ProfileARN)
}
