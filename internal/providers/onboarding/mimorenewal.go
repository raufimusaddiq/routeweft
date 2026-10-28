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

// MimoConnectionUpdater writes one connection in a single read-modify-write;
// credentials.Registry in production, so the renewed session is durable and
// served by the next read in the same step.
type MimoConnectionUpdater interface {
	Update(ctx context.Context, connectionID string, change func(credentials.Connection) (credentials.Connection, error)) (credentials.Connection, error)
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
// A session read is sealed into the connection before it is used, so later
// reads use it without touching the host; if the operator turned renewal off,
// or the session was replaced, while the store was being read, nothing is
// sealed and the read session is dropped.
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

// errMimoRenewalStale means the connection changed while the store was read:
// renewal was turned off, or its session was replaced.
var errMimoRenewalStale = errors.New("xiaomi-mimo connection changed while its desktop session was read")

// MimoDesktopRenewalEnabled reports whether the operator turned renewal on in these provider data.
func MimoDesktopRenewalEnabled(data map[string]string) bool {
	return data[MimoDesktopRenewalKey] == MimoDesktopRenewalOn
}

// Renew reads Desktop's account session for one connection and seals it, and
// returns the connection's provider data with it, or nil when nothing was read
// or sealed. stored is the provider data the quota read holds; its passToken
// (empty when there is none) is the session the read could not use. Like the
// pin's store read, it never fails the caller.
func (m *MimoDesktopRenewal) Renew(ctx context.Context, connectionID string, stored map[string]string) map[string]string {
	if m == nil || !m.Allowed || m.Desktop == nil || m.Connections == nil || !MimoDesktopRenewalEnabled(stored) {
		return nil
	}
	now := m.now()
	m.mu.Lock()
	if last, ok := m.attempts[connectionID]; ok && now.Sub(last) < MimoDesktopRenewalInterval {
		m.mu.Unlock()
		return nil
	}
	// Counted before the read, so a burst of reads opens the store once.
	if m.attempts == nil {
		m.attempts = map[string]time.Time{}
	}
	m.attempts[connectionID] = now
	for id, at := range m.attempts {
		if now.Sub(at) >= MimoDesktopRenewalInterval {
			delete(m.attempts, id)
		}
	}
	m.mu.Unlock()
	session := m.Desktop.ReadSession(ctx)
	// Absent, locked or unusable; or Desktop holds the session that just failed.
	if session == nil || session["mimoPassToken"] == stored["mimoPassToken"] {
		return nil
	}
	updated, err := m.Connections.Update(ctx, connectionID, func(current credentials.Connection) (credentials.Connection, error) {
		data := current.Secret.ProviderData
		if !MimoDesktopRenewalEnabled(data) || data["mimoPassToken"] != stored["mimoPassToken"] {
			return credentials.Connection{}, errMimoRenewalStale
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
		merged[MimoDesktopReadAtKey] = now.UTC().Format(time.RFC3339)
		current.Secret.ProviderData = merged
		// A renewal neither tests nor resets the credential's status.
		current.Lifecycle = credentials.Lifecycle{}
		return current, nil
	})
	if err != nil {
		return nil
	}
	return updated.Secret.ProviderData
}

func (m *MimoDesktopRenewal) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
