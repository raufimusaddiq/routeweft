package api

import (
	"context"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

// redactedCredential replaces credential material an upstream echoed back.
const redactedCredential = "[redacted]"

// recordValidation persists a connection test's outcome (#220) after removing
// any of the connection's own credential material from its error, so neither the
// answer nor the stored last error can carry it. A test that did not run (a
// disabled connection) or was canceled by its caller records nothing: it says
// nothing about the credential. The record outlives the request that asked.
func (h *Handler) recordValidation(ctx context.Context, c credentials.Connection, result *validationResult) {
	result.Error = h.redactConnectionSecret(ctx, c, result.Error)
	if h.opts.Credentials == nil || c.ID == "" || !c.Enabled || result.Classification == "canceled" {
		return
	}
	// c was read with its credential revision; a relink since then makes the
	// store refuse this result (ErrCredentialChanged).
	_ = h.opts.Credentials.RecordValidation(context.WithoutCancel(ctx), c.ID, c.CredentialRevision, credentials.Validation{OK: result.OK, Error: result.Error, ModelsAvailable: result.ModelsAvailable})
}

// markOnboarded records an onboarded credential's status with its origin. One a
// provider issued or accepted is active, as the reference writes testStatus
// "active" on every sign-in ([provider]/[action]/route.js:69). One stored
// unvalidated (Xiaomi's soft validation) is untested when new and keeps a
// stored duplicate's status (xiaomi-mimo/api-key/route.js:79,115). The
// credential is already saved and live; a failure here leaves the status as it
// was rather than undoing the save.
func (h *Handler) markOnboarded(ctx context.Context, saved map[string]any, origin string, unvalidated bool) {
	id, _ := saved["id"].(string)
	if h.opts.Credentials == nil || id == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	var err error
	switch {
	case !unvalidated:
		err = h.opts.Credentials.MarkCredentialAccepted(ctx, id, origin)
	case saved["outcome"] == onboardedAdded:
		err = h.opts.Credentials.MarkCredentialUntested(ctx, id, origin)
	}
	if err != nil {
		return
	}
	if status, err := h.opts.Credentials.GetConnectionStatus(ctx, id); err == nil {
		saved["testStatus"] = status.TestStatus
		if status.AuthOrigin != "" {
			saved["authOrigin"] = status.AuthOrigin
		}
	}
}

// redactConnectionSecret removes the connection's credential values, as stored
// and as the registry currently holds them after any refresh, from text.
func (h *Handler) redactConnectionSecret(ctx context.Context, c credentials.Connection, text string) string {
	if text == "" {
		return text
	}
	spec, _ := h.providerSpec(c.ProviderID)
	values := secretValues(c.Secret, spec.CredentialName)
	if h.opts.CredentialRegistry != nil && c.ID != "" {
		if current, err := h.opts.CredentialRegistry.Secret(ctx, c.ID); err == nil {
			values = append(values, secretValues(current, spec.CredentialName)...)
		}
	}
	return redactValues(text, values)
}

// secretValues lists every credential value of a secret, whatever its length:
// the access, refresh and ID tokens, the cookie, and each provider-data value
// that is a credential, being the provider's named credential, under a key the
// log redaction treats as one (apiKey, copilotToken, mimoPassToken, wireToken),
// or a proxy URL that may carry a password. Provider metadata (project, region,
// account and user ids, plan, base URL) is not a credential and is left in the
// text.
func secretValues(secret credentials.Secret, credentialName string) []string {
	values := []string{secret.AccessToken, secret.RefreshToken, secret.IDToken, secret.Cookie}
	for key, value := range secret.ProviderData {
		if key == credentialName || telemetry.SensitiveKey(key) || strings.Contains(strings.ToLower(key), "proxy") {
			values = append(values, value)
		}
	}
	return values
}

// redactValues replaces each non-blank value, longest first so a token that
// contains another is removed whole. No length is too short to be a secret.
func redactValues(text string, values []string) string {
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			text = strings.ReplaceAll(text, value, redactedCredential)
		}
	}
	return text
}
