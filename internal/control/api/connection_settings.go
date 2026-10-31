package api

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// Connection settings that change where, or under what name, a request goes
// (#228). They are checked when they are saved, with the outbound policy a request
// uses, so a setting that could never be dialled is refused with its name instead
// of failing on the first request. Only the key a provider reads is checked for
// it; another provider's stored data is not this save's business.
var (
	azureAPIVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	azureDeployment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// connectionSettingKeys lists, per provider, the settings the connection list shows and the edit form
// changes. None is a credential: a URL, a deployment name, an API version, an organization name.
var connectionSettingKeys = map[string][]string{
	"azure":        {"azureEndpoint", "deployment", "apiVersion", "organization"},
	"kimchi":       {"kimchiEndpoint"},
	"ollama-local": {"baseUrl"},
}

// publicSettings returns the non-empty settings of that list held in a connection's provider data.
func publicSettings(providerID string, data map[string]string) map[string]string {
	out := map[string]string{}
	for _, key := range connectionSettingKeys[providerID] {
		if value := strings.TrimSpace(data[key]); value != "" {
			out[key] = value
		}
	}
	return out
}

// connectionEndpointKeys names, per provider, the settings that hold a URL the
// router will dial.
var connectionEndpointKeys = map[string]string{
	"azure":        "azureEndpoint",
	"kimchi":       "kimchiEndpoint",
	"ollama-local": "baseUrl",
}

// validateConnectionSettings refuses a setting the provider could not use. A URL
// obeys the policy a request does (public hosts, or the operator's explicit
// trusted-local mode), so a private address is never saved just because it was typed
// into a form. Local Ollama is the exception described below.
func (h *Handler) validateConnectionSettings(providerID string, data map[string]string) error {
	if key, ok := connectionEndpointKeys[providerID]; ok {
		if raw := strings.TrimSpace(data[key]); raw != "" {
			validate := h.validateOutboundURL
			if providerID == "ollama-local" {
				// Local Ollama is a loopback or LAN host by design, so the form may name one.
				// Dialling it still needs the operator's trusted-local mode, which every
				// request and probe checks (transport.NewTrustedLocalSSRFProtectedClient);
				// saving the address grants nothing.
				validate = transport.ValidateTrustedLocalURL
			}
			if _, err := validate(raw); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	if providerID != "azure" {
		return nil
	}
	if value := strings.TrimSpace(data["apiVersion"]); value != "" && !azureAPIVersion.MatchString(value) {
		return errors.New("apiVersion must be letters, digits, dots, hyphens or underscores, such as 2024-10-01-preview")
	}
	if value := strings.TrimSpace(data["deployment"]); value != "" && (!azureDeployment.MatchString(value) || strings.Contains(value, "..")) {
		return errors.New("deployment must be a deployment name: letters, digits, dots, hyphens or underscores")
	}
	if value := data["organization"]; strings.ContainsAny(value, "\r\n") {
		return errors.New("organization must be a single line")
	}
	return nil
}
