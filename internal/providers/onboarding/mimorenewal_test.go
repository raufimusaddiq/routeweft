package onboarding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// #299: a quota read may read MiMo Desktop's session only with the host's and
// the operator's consent, at most once per 30 minutes per connection, and seals
// what it read before using it. Every test uses a fixture home.

// countingSessions counts store reads and can run a hook while one is open.
type countingSessions struct {
	MimoDesktop
	reads  atomic.Int32
	during func()
}

func (c *countingSessions) ReadSession(ctx context.Context) map[string]string {
	c.reads.Add(1)
	if c.during != nil {
		c.during()
	}
	return c.MimoDesktop.ReadSession(ctx)
}

// memoryConnections is one stored connection, revised as the registry does:
// change runs on the row as stored now, excluded against every other change of
// it (setData stands for an operator's Update, such as a revocation).
type memoryConnections struct {
	mu         sync.Mutex
	connection credentials.Connection
	writes     int
	fail       error
	deleted    bool
}

func (m *memoryConnections) ReviseSecret(_ context.Context, id string, change func(credentials.Connection) (credentials.Secret, error)) (credentials.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.connection.ID || m.deleted {
		return credentials.Secret{}, credentials.ErrNotFound
	}
	secret, err := change(m.connection)
	if err != nil {
		return credentials.Secret{}, err
	}
	if m.fail != nil {
		return credentials.Secret{}, m.fail
	}
	m.connection.Secret = secret
	m.writes++
	return secret, nil
}

func (m *memoryConnections) data() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connection.Secret.ProviderData
}

func (m *memoryConnections) setData(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := map[string]string{}
	for k, v := range m.connection.Secret.ProviderData {
		data[k] = v
	}
	if value == "" {
		delete(data, key)
	} else {
		data[key] = value
	}
	m.connection.Secret.ProviderData = data
}

type renewalEnv struct {
	renewal     *MimoDesktopRenewal
	desktop     *countingSessions
	connections *memoryConnections
	now         time.Time
}

func newRenewalEnv(t *testing.T, stored map[string]string) *renewalEnv {
	t.Helper()
	env := &renewalEnv{now: time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)}
	env.desktop = &countingSessions{MimoDesktop: MimoDesktop{Home: t.TempDir(), GOOS: "linux", TempDir: t.TempDir()}}
	env.connections = &memoryConnections{connection: credentials.Connection{ID: "c1", ProviderID: "xiaomi-mimo", Secret: credentials.Secret{AccessToken: "sk-k", ProviderData: stored}}}
	env.renewal = &MimoDesktopRenewal{Desktop: env.desktop, Connections: env.connections, Allowed: true, Now: func() time.Time { return env.now }}
	return env
}

func (env *renewalEnv) writeStore(t *testing.T, pass string) {
	t.Helper()
	path := env.desktop.CookieStorePath()
	_ = os.Remove(path)
	writeMimoCookieStore(t, path, [][3]string{{".account.xiaomi.com", "passToken", pass}, {".account.xiaomi.com", "userId", "desktop-user"}})
}

func renewalOn(extra map[string]string) map[string]string {
	data := map[string]string{"apiKey": "sk-k", MimoDesktopRenewalKey: MimoDesktopRenewalOn}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// The session read is sealed into the connection with the time it was read,
// and the identity cookies of the old session do not ride with the new
// passToken.
func TestMimoRenewalSealsTheDesktopSession(t *testing.T) {
	stored := renewalOn(map[string]string{"mimoPassToken": "expired-pass", "mimoUserId": "old-user", "mimoCUserId": "old-cuser"})
	env := newRenewalEnv(t, stored)
	env.writeStore(t, "desktop-pass")
	got := env.renewal.Renew(context.Background(), "c1", stored)
	if got["mimoPassToken"] != "desktop-pass" || got["mimoUserId"] != "desktop-user" || got["apiKey"] != "sk-k" {
		t.Fatalf("renewed = %v", got)
	}
	if _, kept := got["mimoCUserId"]; kept {
		t.Fatal("the old session's cUserId rode with the new passToken")
	}
	if got[MimoDesktopReadAtKey] != "2026-10-29T08:00:00Z" {
		t.Fatalf("read at = %q", got[MimoDesktopReadAtKey])
	}
	if sealed := env.connections.data(); sealed["mimoPassToken"] != "desktop-pass" || env.connections.writes != 1 {
		t.Fatalf("sealed = %v writes = %d", sealed, env.connections.writes)
	}
	// No private copy of the store is left behind.
	if left, _ := filepath.Glob(filepath.Join(env.desktop.TempDir, "*")); len(left) != 0 {
		t.Fatalf("copies left: %v", left)
	}
}

// Without either consent the store is never opened.
func TestMimoRenewalNeedsBothConsents(t *testing.T) {
	for name, setup := range map[string]func(*renewalEnv) map[string]string{
		"host off": func(env *renewalEnv) map[string]string {
			env.renewal.Allowed = false
			return renewalOn(nil)
		},
		"connection off": func(*renewalEnv) map[string]string { return map[string]string{"apiKey": "sk-k"} },
		"other value":    func(*renewalEnv) map[string]string { return map[string]string{MimoDesktopRenewalKey: "true"} },
	} {
		t.Run(name, func(t *testing.T) {
			env := newRenewalEnv(t, nil)
			env.writeStore(t, "desktop-pass")
			stored := setup(env)
			env.connections.connection.Secret.ProviderData = stored
			if got := env.renewal.Renew(context.Background(), "c1", stored); got != nil || env.desktop.reads.Load() != 0 || env.connections.writes != 0 {
				t.Fatalf("got = %v reads = %d writes = %d", got, env.desktop.reads.Load(), env.connections.writes)
			}
		})
	}
	var unset *MimoDesktopRenewal
	if unset.Renew(context.Background(), "c1", renewalOn(nil)) != nil {
		t.Fatal("a nil renewal read something")
	}
}

// An absent store, one without a passToken, or one that holds the session that
// just failed seals nothing; the store is then not opened again for 30 minutes,
// and is after that.
func TestMimoRenewalAbsentStoreAndTheInterval(t *testing.T) {
	stored := renewalOn(nil)
	env := newRenewalEnv(t, stored)
	if got := env.renewal.Renew(context.Background(), "c1", stored); got != nil || env.connections.writes != 0 {
		t.Fatalf("absent store: %v", got)
	}
	env.writeStore(t, "desktop-pass")
	env.now = env.now.Add(MimoDesktopRenewalInterval - time.Second)
	if got := env.renewal.Renew(context.Background(), "c1", stored); got != nil || env.desktop.reads.Load() != 1 {
		t.Fatalf("within the interval: got = %v reads = %d", got, env.desktop.reads.Load())
	}
	// Another stored connection has its own interval.
	other := &memoryConnections{connection: credentials.Connection{ID: "c2", ProviderID: "xiaomi-mimo", Secret: credentials.Secret{AccessToken: "sk-k", ProviderData: stored}}}
	env.renewal.Connections = other
	if got := env.renewal.Renew(context.Background(), "c2", stored); got["mimoPassToken"] != "desktop-pass" || env.desktop.reads.Load() != 2 {
		t.Fatalf("other connection: got = %v reads = %d", got, env.desktop.reads.Load())
	}
	env.renewal.Connections = env.connections
	env.now = env.now.Add(time.Second)
	if got := env.renewal.Renew(context.Background(), "c1", stored); got["mimoPassToken"] != "desktop-pass" {
		t.Fatalf("after the interval: %v", got)
	}

	failed := renewalOn(map[string]string{"mimoPassToken": "same-pass"})
	same := newRenewalEnv(t, failed)
	same.writeStore(t, "same-pass")
	if got := same.renewal.Renew(context.Background(), "c1", failed); got != nil || same.connections.writes != 0 {
		t.Fatalf("same session: %v", got)
	}

	empty := newRenewalEnv(t, stored)
	writeMimoCookieStore(t, empty.desktop.CookieStorePath(), [][3]string{{".account.xiaomi.com", "userId", "u"}})
	if got := empty.renewal.Renew(context.Background(), "c1", stored); got != nil {
		t.Fatalf("no passToken: %v", got)
	}
}

// Hermes #301 finding: the quota read captured its credential while renewal was
// on, and the operator revoked it (or replaced the session, or deleted the
// connection) before the store was opened. The store is not opened: consent is
// checked on the connection as stored at the read, not on the captured copy.
func TestMimoRenewalUsesTheConsentStoredAtTheRead(t *testing.T) {
	for name, change := range map[string]func(*memoryConnections){
		"revoked":  func(m *memoryConnections) { m.setData(MimoDesktopRenewalKey, "") },
		"replaced": func(m *memoryConnections) { m.setData("mimoPassToken", "operator-pass") },
		"deleted": func(m *memoryConnections) {
			m.mu.Lock()
			m.deleted = true
			m.mu.Unlock()
		},
	} {
		t.Run(name, func(t *testing.T) {
			captured := renewalOn(nil)
			env := newRenewalEnv(t, captured)
			env.writeStore(t, "desktop-pass")
			change(env.connections)
			if got := env.renewal.Renew(context.Background(), "c1", captured); got != nil {
				t.Fatalf("got = %v", got)
			}
			if reads := env.desktop.reads.Load(); reads != 0 {
				t.Fatalf("the store was opened %d times after %s", reads, name)
			}
			if env.connections.writes != 0 {
				t.Fatalf("sealed after %s: %v", name, env.connections.data())
			}
			// A refused read does not use the interval up: once the operator turns
			// renewal on again, the next read may open the store.
			if name == "revoked" {
				env.connections.setData(MimoDesktopRenewalKey, MimoDesktopRenewalOn)
				if got := env.renewal.Renew(context.Background(), "c1", captured); got["mimoPassToken"] != "desktop-pass" {
					t.Fatalf("after turning it on again: %v", got)
				}
			}
		})
	}
}

// A revocation that arrives while the store is open waits for the read to be
// sealed, then takes effect: no later read opens the store.
func TestMimoRenewalRevocationWaitsForTheRead(t *testing.T) {
	captured := renewalOn(nil)
	env := newRenewalEnv(t, captured)
	env.writeStore(t, "desktop-pass")
	revoked := make(chan struct{})
	env.desktop.during = func() {
		go func() {
			env.connections.setData(MimoDesktopRenewalKey, "")
			close(revoked)
		}()
		select {
		case <-revoked:
			t.Error("the revocation landed while the store was open")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if got := env.renewal.Renew(context.Background(), "c1", captured); got["mimoPassToken"] != "desktop-pass" {
		t.Fatalf("got = %v", got)
	}
	<-revoked
	env.desktop.during = nil
	if MimoDesktopRenewalEnabled(env.connections.data()) {
		t.Fatal("the revocation was lost")
	}
	env.now = env.now.Add(MimoDesktopRenewalInterval)
	if got := env.renewal.Renew(context.Background(), "c1", map[string]string{MimoDesktopRenewalKey: MimoDesktopRenewalOn, "mimoPassToken": "desktop-pass"}); got != nil || env.desktop.reads.Load() != 1 {
		t.Fatalf("after revoke: got = %v reads = %d", got, env.desktop.reads.Load())
	}
}

// A write that fails returns nothing read.
func TestMimoRenewalFailedWriteUsesNothing(t *testing.T) {
	captured := renewalOn(nil)
	env := newRenewalEnv(t, captured)
	env.writeStore(t, "desktop-pass")
	env.connections.fail = errors.New("disk full")
	if got := env.renewal.Renew(context.Background(), "c1", captured); got != nil || env.connections.writes != 0 {
		t.Fatalf("got = %v writes = %d", got, env.connections.writes)
	}
}

// A burst of reads for one connection opens the store once.
func TestMimoRenewalBurstReadsOnce(t *testing.T) {
	stored := renewalOn(nil)
	env := newRenewalEnv(t, stored)
	env.writeStore(t, "desktop-pass")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.renewal.Renew(context.Background(), "c1", stored)
		}()
	}
	wg.Wait()
	if reads := env.desktop.reads.Load(); reads != 1 {
		t.Fatalf("reads = %d", reads)
	}
}
