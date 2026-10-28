package onboarding

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// The connection fields #299 keeps in the sealed provider data. Neither is a
// credential (telemetry.SensitiveKey matches neither), so setting or clearing
// them does not count as replacing the credential.
const (
	// MimoDesktopRenewalKey holds MimoDesktopRenewalOn while the operator allows
	// quota reads of this connection to read MiMo Desktop's account session.
	MimoDesktopRenewalKey = "mimoDesktopRenewal"
	// MimoDesktopRenewalOn is the one value that allows it.
	MimoDesktopRenewalOn = "on"
	// MimoDesktopReadAtKey is when a quota read last sealed a session read from
	// the host (RFC 3339, UTC). It names the time only, never the values.
	MimoDesktopReadAtKey = "mimoDesktopReadAt"
)

// MimoDesktopRenewalInterval bounds how often one connection's quota reads may
// open MiMo Desktop's cookie store: the pin's session cache lifetime
// (COOKIE_TTL_MS, open-sse/shared/mimoAccount.js:29), which is how often the pin
// re-reads the store for a connection without a stored passToken
// (mimoAccount.js:181-193).
const MimoDesktopRenewalInterval = 30 * time.Minute

// HostMimoDesktop is MiMo Desktop on this host, for the user the server runs as.
func HostMimoDesktop() MimoDesktop {
	home, _ := os.UserHomeDir()
	return MimoDesktop{Home: home, GOOS: runtime.GOOS, AppData: os.Getenv("APPDATA")}
}

// MimoSessionSource reads Desktop's account session; MimoDesktop in production.
type MimoSessionSource interface {
	ReadSession(context.Context) map[string]string
}

// MimoConnectionUpdater revises one connection's credential in a single
// read-modify-write, excluded against every other change of that connection
// and never recreating a deleted one; credentials.Registry in production
// (ReviseSecret), so the renewed session is durable and served by the next read
// in the same step, and an operator's revocation (an Update) is ordered
// against the read.
type MimoConnectionUpdater interface {
	ReviseSecret(ctx context.Context, connectionID string, change func(credentials.Connection) (credentials.Secret, error)) (credentials.Secret, error)
}

// MimoDesktopRenewal is the consented equivalent of the pin's quota-read
// fallback (#299, BDR-028). The pin reads Desktop's cookie store on every quota
// read of a connection without a stored passToken (mimoAccount.js:181-185). Here
// a quota read may read it only when all of these hold at the time of the read:
//
//   - the host allows host credential discovery (Allowed);
//   - the operator turned renewal on for this connection
//     (MimoDesktopRenewalKey, sealed with the credential; turning it off stops
//     the next read);
//   - the connection has no sealed session, or its sealed session just failed
//     the account handshake;
//   - this connection's store was not opened in the last
//     MimoDesktopRenewalInterval.
//
// The consent and session checks are made on the connection as stored at the
// moment of the read, not on the copy the quota read captured, and in the same
// exclusive step (Connections.ReviseSecret) as the read and the seal. Revoking
// is a change of the same connection through the same exclusion, so it either
// commits first, and the store is never opened, or waits until the read is
// sealed and then takes effect for every later read. A session read is sealed
// before it is used, so later reads use it without touching the host; a
// connection deleted while the store was read is not saved again, and nothing
// it read is used.
type MimoDesktopRenewal struct {
	Desktop     MimoSessionSource
	Connections MimoConnectionUpdater
	// Allowed is the host's consent, read at every renewal.
	Allowed bool
	// Now defaults to time.Now.
	Now func() time.Time

	mu       sync.Mutex
	attempts map[string]time.Time
}

// errMimoRenewalStale means the connection as stored now no longer allows the
// read: renewal is off, or its session is not the one the quota read failed
// with. errMimoRenewalNothing means nothing usable was read, or the interval
// has not passed.
var (
	errMimoRenewalStale   = errors.New("xiaomi-mimo connection no longer allows a desktop session read")
	errMimoRenewalNothing = errors.New("xiaomi-mimo desktop session not renewed")
)

// MimoDesktopRenewalEnabled reports whether the operator turned renewal on in these provider data.
func MimoDesktopRenewalEnabled(data map[string]string) bool {
	return data[MimoDesktopRenewalKey] == MimoDesktopRenewalOn
}

// Renew reads Desktop's account session for one connection and seals it, and
// returns the connection's provider data with it, or nil when nothing was read
// or sealed. stored is the provider data the quota read captured; its passToken
// (empty when there is none) is the session the read could not use. The
// captured consent is only a reason not to try: the store is opened on the
// consent stored at that moment. Like the pin's store read, it never fails the
// caller.
func (m *MimoDesktopRenewal) Renew(ctx context.Context, connectionID string, stored map[string]string) map[string]string {
	if m == nil || !m.Allowed || m.Desktop == nil || m.Connections == nil || !MimoDesktopRenewalEnabled(stored) || !m.due(connectionID, false) {
		return nil
	}
	secret, err := m.Connections.ReviseSecret(ctx, connectionID, func(current credentials.Connection) (credentials.Secret, error) {
		data := current.Secret.ProviderData
		if !MimoDesktopRenewalEnabled(data) || data["mimoPassToken"] != stored["mimoPassToken"] {
			return credentials.Secret{}, errMimoRenewalStale
		}
		// Claimed here, where reads of one connection are serialized, so a burst
		// opens the store once and a refused read does not use the interval up.
		if !m.due(connectionID, true) {
			return credentials.Secret{}, errMimoRenewalNothing
		}
		session := m.Desktop.ReadSession(ctx)
		// Absent, locked or unusable; or Desktop holds the session that just failed.
		if session == nil || session["mimoPassToken"] == data["mimoPassToken"] {
			return credentials.Secret{}, errMimoRenewalNothing
		}
		merged := make(map[string]string, len(data)+len(session)+1)
		for key, value := range data {
			merged[key] = value
		}
		// The session's three fields travel together: an identity cookie of the
		// old session must not ride with the new passToken.
		delete(merged, "mimoUserId")
		delete(merged, "mimoCUserId")
		for key, value := range session {
			merged[key] = value
		}
		merged[MimoDesktopReadAtKey] = m.now().UTC().Format(time.RFC3339)
		renewed := current.Secret
		renewed.ProviderData = merged
		return renewed, nil
	})
	if err != nil {
		return nil
	}
	return secret.ProviderData
}

// due reports whether this connection's store may be opened now, at most once
// per MimoDesktopRenewalInterval; claim records the attempt.
func (m *MimoDesktopRenewal) due(connectionID string, claim bool) bool {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if last, ok := m.attempts[connectionID]; ok && now.Sub(last) < MimoDesktopRenewalInterval {
		return false
	}
	if !claim {
		return true
	}
	if m.attempts == nil {
		m.attempts = map[string]time.Time{}
	}
	for id, at := range m.attempts {
		if now.Sub(at) >= MimoDesktopRenewalInterval {
			delete(m.attempts, id)
		}
	}
	m.attempts[connectionID] = now
	return true
}

func (m *MimoDesktopRenewal) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
