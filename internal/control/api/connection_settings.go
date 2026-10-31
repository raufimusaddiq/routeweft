package api

import (
	"errors"
	"fmt"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// Connection settings that change where, or under what name, a request goes (#228). They are
// checked when they are saved, with the outbound policy a request uses, so a setting that could
// never be dialled is refused with its name instead of failing on the first request. Only a
// provider that reads a setting is checked for it; another provider's stored data is not this save's
// business.

// connectionSetting describes the settings one provider's connection holds besides its credential.
type connectionSetting struct {
	// keys are shown by the connection list and changed by the edit form. None is a credential: a
	// URL, a deployment name, an API version, an organization name.
	keys []string
	// urlKey names the setting that holds a URL the router will dial.
	urlKey string
	// required names a setting without which the connection could never be dialled, so a save may
	// not leave it empty.
	required []string
	// trustedLocal is set for a provider whose host is a loopback or LAN address by design. Saving
	// it grants nothing: every request, probe and listing still needs the operator's trusted-local
	// mode.
	trustedLocal bool
}

var connectionSettings = map[string]connectionSetting{
	"azure":        {keys: []string{"azureEndpoint", "deployment", "apiVersion", "organization"}, urlKey: "azureEndpoint", required: []string{"azureEndpoint"}},
	"kimchi":       {keys: []string{"kimchiEndpoint"}, urlKey: "kimchiEndpoint"},
	"ollama-local": {keys: []string{"baseUrl"}, urlKey: "baseUrl", trustedLocal: true},
}

// publicSettings returns the non-empty settings of that list held in a connection's provider data.
func publicSettings(providerID string, data map[string]string) map[string]string {
	out := map[string]string{}
	for _, key := range connectionSettings[providerID].keys {
		if value := strings.TrimSpace(data[key]); value != "" {
			out[key] = value
		}
	}
	return out
}

// mergedProviderData is the provider data a save leaves stored: the edit's keys laid over the stored
// ones, an empty value replacing the stored one, exactly as mergeConnectionSecret does.
func mergedProviderData(stored, edit map[string]string) map[string]string {
	merged := make(map[string]string, len(stored)+len(edit))
	for key, value := range stored {
		merged[key] = value
	}
	for key, value := range edit {
		merged[key] = value
	}
	return merged
}

// validateConnectionSettings refuses data the provider could not use. It is given the data the save
// would leave stored (mergedProviderData), not just the keys the request names, so an edit that
// clears a required setting is refused like a create that omits it. A URL obeys the policy a request
// does (public hosts, or the operator's trusted-local mode), so a private address is never saved just
// because it was typed into a form; local Ollama is the exception the table notes.
func (h *Handler) validateConnectionSettings(providerID string, data map[string]string) error {
	setting, ok := connectionSettings[providerID]
	if !ok {
		return nil
	}
	for _, key := range setting.required {
		if strings.TrimSpace(data[key]) == "" {
			return fmt.Errorf("%s is required: without it the connection could never be dialled", key)
		}
	}
	if raw := strings.TrimSpace(data[setting.urlKey]); raw != "" {
		validate := h.validateOutboundURL
		if setting.trustedLocal {
			validate = transport.ValidateTrustedLocalURL
		}
		if _, err := validate(raw); err != nil {
			return fmt.Errorf("%s: %w", setting.urlKey, err)
		}
	}
	if providerID != "azure" {
		return nil
	}
	if value := strings.TrimSpace(data["apiVersion"]); value != "" && !wire.ValidAzureAPIVersion(value) {
		return errors.New("apiVersion must be letters, digits, dots, hyphens or underscores, such as 2024-10-01-preview")
	}
	if value := strings.TrimSpace(data["deployment"]); value != "" && !wire.ValidAzureDeployment(value) {
		return errors.New("deployment must be a deployment name: letters, digits, dots, hyphens or underscores")
	}
	if strings.ContainsAny(data["organization"], "\r\n") {
		return errors.New("organization must be a single line")
	}
	return nil
}
