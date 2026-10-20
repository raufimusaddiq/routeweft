package wire

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
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
		if strings.TrimSpace(setting.value) == "" && !(setting.name == "profileArn" && settings.AuthMethod == "api_key") {
			return Target{}, errors.New("kiro connection has no " + setting.name + ": set providerData." + setting.name + " on the connection")
		}
		if strings.IndexFunc(setting.value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return Target{}, errors.New("kiro connection has invalid " + setting.name)
		}
	}
	return Target{Endpoint: kiroOperation}, nil
}

func kiroHeaders(provider routing.ProviderRef) map[string]string {
	token := strings.TrimSpace(provider.APIToken)
	if token == "" {
		return nil
	}
	settings := provider.Connection.Settings
	headers := map[string]string{
		"Content-Type":                    "application/json",
		"Accept":                          "application/vnd.amazon.eventstream",
		"User-Agent":                      "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0",
		"X-Amz-User-Agent":                "aws-sdk-js/3.0.0 kiro-ide/1.0.0",
		"Amz-Sdk-Request":                 "attempt=1; max=3",
		"Amz-Sdk-Invocation-Id":           uuid.NewString(),
		"Authorization":                   "Bearer " + token,
		"x-amz-sso-bearer":                token,
		"x-amzn-kiro-agent-mode":          "spec",
		"x-amzn-codewhisperer-machine-id": "kiro-desktop",
	}
	if strings.TrimSpace(settings.ProfileARN) != "" {
		headers["x-amzn-codewhisperer-profile-arn"] = settings.ProfileARN
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
	profile := provider.Connection.Settings.ProfileARN
	if strings.TrimSpace(profile) == "" {
		return body.delete("profileArn")
	}
	var current string
	if raw, ok := body["profileArn"]; ok && json.Unmarshal(raw, &current) == nil && current == profile {
		return false
	}
	return setJSON(body, "profileArn", profile)
}

var ErrKiroEndpointsExhausted = errors.New("kiro endpoints exhausted")

var kiroRegion = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)

func kiroEndpoint(provider routing.ProviderRef, index int) (Target, error) {
	region := strings.TrimSpace(provider.Connection.Settings.Region)
	if region == "" {
		region = "us-east-1"
	}
	if !kiroRegion.MatchString(region) {
		return Target{}, &TargetError{Err: errors.New("kiro connection has invalid region")}
	}
	bases := []string{
		"https://q." + region + ".amazonaws.com",
		"https://codewhisperer." + region + ".amazonaws.com",
		"https://runtime.us-east-1.kiro.dev",
	}
	configured := strings.TrimRight(strings.TrimSpace(provider.Connection.Settings.BaseURL), "/")
	if configured == "" {
		configured = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
		configured = strings.TrimSuffix(configured, "/"+kiroOperation)
		// The catalog's legacy runtime default must not override the pin's Q-first order.
		if configured == "https://runtime.us-east-1.kiro.dev" {
			configured = ""
		}
	}
	configured = strings.TrimSuffix(configured, "/"+kiroOperation)
	if parsed, err := url.Parse(configured); err == nil {
		for _, service := range []string{"q", "codewhisperer"} {
			host := parsed.Hostname()
			if strings.HasPrefix(host, service+".") && strings.HasSuffix(host, ".amazonaws.com") && kiroRegion.MatchString(strings.TrimSuffix(strings.TrimPrefix(host, service+"."), ".amazonaws.com")) {
				parsed.Host = strings.Replace(parsed.Host, host, service+"."+region+".amazonaws.com", 1)
				configured = parsed.String()
			}
		}
	}
	if configured != "" {
		ordered := []string{configured}
		for _, base := range bases {
			if base != configured {
				ordered = append(ordered, base)
			}
		}
		bases = ordered
	}
	if index < 0 || index >= len(bases) {
		return Target{}, &TargetError{Err: ErrKiroEndpointsExhausted}
	}
	return Target{BaseURL: bases[index], Endpoint: kiroOperation}, nil
}
