package api

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
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
	err := h.opts.Credentials.RecordValidation(context.WithoutCancel(ctx), c.ID, c.CredentialRevision, credentials.Validation{OK: result.OK, Error: result.Error, ModelsAvailable: result.ModelsAvailable})
	// A replaced or deleted connection is expected to refuse the result; any
	// other failure leaves the stored status behind the check and is logged.
	if err != nil && !errors.Is(err, credentials.ErrCredentialChanged) && !errors.Is(err, credentials.ErrNotFound) {
		slog.Warn("connection validation status not recorded", "connection", c.ID, "error", err)
	}
}

// onboardedLifecycle is what an onboarding save records with the credential it
// writes, in the same statement (credentials.Lifecycle). A credential a provider
// issued or accepted is active, as the reference writes testStatus "active" on
// every sign-in ([provider]/[action]/route.js:69). One stored unvalidated
// (Xiaomi's soft validation, or an import the flow only parsed) is untested
// when new and keeps a stored duplicate's status (xiaomi-mimo/api-key/route.js:79,115),
// except a failure, which belonged to the credential it replaces.
func onboardedLifecycle(flow oauthFlow, result onboarding.Result, update bool) credentials.Lifecycle {
	origin := credentials.OriginOAuth
	if flow.device {
		origin = credentials.OriginDevice
	} else if flow.imported {
		origin = credentials.OriginImport
	}
	switch {
	case !result.Unvalidated:
		return credentials.Lifecycle{Status: credentials.StatusActive, Origin: origin}
	case update:
		return credentials.Lifecycle{Origin: origin}
	default:
		return credentials.Lifecycle{Status: credentials.StatusUntested, Origin: origin}
	}
}

// bulkLifecycle is a bulk item's. Bulk items are parsed, never sent to the
// provider, so they are imported as unvalidated: active means a provider
// confirmed the credential, which no provider has done for a pasted item.
func bulkLifecycle(update bool) credentials.Lifecycle {
	return onboardedLifecycle(oauthFlow{imported: true}, onboarding.Result{Unvalidated: true}, update)
}

// reportLifecycle adds the saved connection's status and origin to an
// onboarding answer. It only reads what the save wrote.
func (h *Handler) reportLifecycle(ctx context.Context, saved map[string]any) {
	id, _ := saved["id"].(string)
	if h.opts.Credentials == nil || id == "" {
		return
	}
	if status, err := h.opts.Credentials.GetConnectionStatus(context.WithoutCancel(ctx), id); err == nil {
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
