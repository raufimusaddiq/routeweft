package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

// A connection's validation lifecycle (v13, #220). It is kept beside the
// connection, outside the sealed secret and outside the runtime snapshot:
// recording a test result never recompiles configuration or opens a credential.
const (
	// StatusUntested: stored, and not tested since its credential last changed.
	StatusUntested = "untested"
	// StatusActive: the provider accepted the credential, at sign-in or on test.
	StatusActive = "active"
	// StatusFailed: the last test did not pass.
	StatusFailed = "failed"
)

// How a connection's credential arrived. A label for the operator, never a
// credential.
const (
	OriginManual = "manual"
	OriginOAuth  = "oauth"
	OriginDevice = "device"
	OriginImport = "import"
)

// Validation is the outcome of one connection test. Error must already be free
// of credential material; the store persists it as given.
type Validation struct {
	OK              bool
	Error           string
	ModelsAvailable int
	At              time.Time
}

// ErrCredentialChanged reports a validation result for a credential that has
// since been replaced; it is not recorded.
var ErrCredentialChanged = errors.New("connection credential changed since it was tested")

// RecordValidation persists one connection test's outcome, as the reference's
// testSingleConnection does (testUtils.js:892-899): a pass is active and clears
// the stored error, a failure is failed with its error and time. revision is
// the CredentialRevision the test read with the credential it tested; if the
// credential was replaced meanwhile (a relink), the result describes the old
// one and is refused with ErrCredentialChanged, leaving the new one's status.
func (s *Store) RecordValidation(ctx context.Context, id string, revision int64, v Validation) error {
	at := v.At
	if at.IsZero() {
		at = s.now()
	}
	stamp := statusTime(at)
	status, lastError, lastErrorAt := StatusActive, any(nil), any(nil)
	if !v.OK {
		status, lastError, lastErrorAt = StatusFailed, strings.TrimSpace(v.Error), stamp
	}
	err := requireAffected(s.db.ExecContext(ctx, "UPDATE provider_connections SET test_status=?,last_tested_at=?,last_error=?,last_error_at=?,models_available=? WHERE id=? AND credential_revision=?", status, stamp, lastError, lastErrorAt, v.ModelsAvailable, id, revision))
	if errors.Is(err, ErrNotFound) {
		var exists int
		if s.db.QueryRowContext(ctx, "SELECT 1 FROM provider_connections WHERE id=?", id).Scan(&exists) == nil {
			return ErrCredentialChanged
		}
	}
	return err
}

// ConnectionStatus is the persisted lifecycle of one connection.
type ConnectionStatus struct {
	TestStatus      string
	LastTestedAt    string
	LastError       string
	LastErrorAt     string
	ModelsAvailable *int
	AuthOrigin      string
	ExpiresAt       string
}

// GetConnectionStatus reads one connection's lifecycle without opening its secret.
func (s *Store) GetConnectionStatus(ctx context.Context, id string) (ConnectionStatus, error) {
	var status ConnectionStatus
	var models *int
	err := s.db.QueryRowContext(ctx, "SELECT test_status,COALESCE(last_tested_at,''),COALESCE(last_error,''),COALESCE(last_error_at,''),models_available,auth_origin,COALESCE(credential_expires_at,'') FROM provider_connections WHERE id=?", id).
		Scan(&status.TestStatus, &status.LastTestedAt, &status.LastError, &status.LastErrorAt, &models, &status.AuthOrigin, &status.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectionStatus{}, ErrNotFound
	}
	status.ModelsAvailable = models
	return status, err
}

// credentialMaterial is what a credential is, for Digest: the access, refresh
// and ID tokens, the cookie, and the provider-data values under credential keys
// (apiKey, copilotToken, mimoPassToken, ...). Provider metadata and settings
// (project, region, proxy, plan) are not the credential, so changing them is not
// a replacement. JSON encodes the map in key order, so the result is canonical.
func credentialMaterial(secret Secret) []byte {
	material := struct {
		AccessToken  string            `json:"a,omitempty"`
		RefreshToken string            `json:"r,omitempty"`
		IDToken      string            `json:"i,omitempty"`
		Cookie       string            `json:"c,omitempty"`
		ProviderData map[string]string `json:"p,omitempty"`
	}{AccessToken: secret.AccessToken, RefreshToken: secret.RefreshToken, IDToken: secret.IDToken, Cookie: secret.Cookie}
	for key, value := range secret.ProviderData {
		if value != "" && telemetry.SensitiveKey(key) {
			if material.ProviderData == nil {
				material.ProviderData = map[string]string{}
			}
			material.ProviderData[key] = value
		}
	}
	encoded, _ := json.Marshal(material)
	return encoded
}

func statusTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// expiryColumn is the sealed secret's expiry as stored beside it, or NULL.
func expiryColumn(secret Secret) any {
	if secret.Expiry.IsZero() {
		return nil
	}
	return statusTime(secret.Expiry)
}
