package quota

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// Warming spends provider quota, so it is opt-in per account, off by default,
// separate from the quota read, and bounded (#226).

// countingUsage counts reads per account and the most it saw at once.
type countingUsage struct {
	mu       sync.Mutex
	state    func() State
	reads    map[string]int
	inFlight int
	most     int
}

func (c *countingUsage) Usage(_ context.Context, credential Credential) (State, error) {
	c.mu.Lock()
	c.reads[credential.ConnectionID]++
	c.inFlight++
	if c.inFlight > c.most {
		c.most = c.inFlight
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.inFlight--; c.mu.Unlock() }()
	return c.state(), nil
}

func (c *countingUsage) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, reads := range c.reads {
		n += reads
	}
	return n
}

type warmingRig struct {
	usage    *countingUsage
	service  *Service
	pinger   *AutoPinger
	settings map[string]string
	now      *time.Time
	sends    map[string]int
}

func newWarmingRig(provider string, accounts int, window func(now time.Time) Window) *warmingRig {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	rig := &warmingRig{settings: map[string]string{}, now: &now, sends: map[string]int{}}
	rig.usage = &countingUsage{reads: map[string]int{}, state: func() State { return State{Windows: []Window{window(*rig.now)}} }}
	var connections []credentials.Connection
	values := map[string]string{}
	for i := 0; i < accounts; i++ {
		id := fmt.Sprintf("acct-%d", i)
		connections = append(connections, credentials.Connection{ID: id, ProviderID: provider, Enabled: true, AuthKind: credentials.AuthOAuth})
		values[id] = "sealed-fixture"
	}
	rig.service = NewService(fakeConnections{byProvider: map[string][]credentials.Connection{provider: connections}}, fakeCredentials{values: values},
		Observer{Clients: map[string]UsageClient{provider: rig.usage}, Now: func() time.Time { return *rig.now }})
	var sendMu sync.Mutex
	rig.pinger = &AutoPinger{
		Service:  rig.service,
		Settings: func() map[string]string { return rig.settings },
		Send: func(_ context.Context, _ string, credential Credential) error {
			sendMu.Lock()
			defer sendMu.Unlock()
			rig.sends[credential.ConnectionID]++
			return nil
		},
		Save: func(context.Context, map[string]PingRecord) error { return nil },
	}
	return rig
}

func (r *warmingRig) tick(t *testing.T) {
	t.Helper()
	if err := r.pinger.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func claudeSessionAt(reset func(now time.Time) time.Time) func(time.Time) Window {
	return func(now time.Time) Window {
		at := reset(now)
		return Window{Name: "session (5h)", Remaining: ptr(1), ResetAt: &at}
	}
}

func TestWarmingIsOffByDefault(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for name, settings := range map[string]map[string]string{
			"no setting":        {},
			"compiled default":  {provider + "AutoPing": `{"connections":{}}`},
			"every account off": {provider + "AutoPing": `{"connections":{"acct-0":false,"acct-1":false}}`},
			"other provider on": {otherWarmed(provider) + "AutoPing": `{"connections":{"acct-0":true}}`},
		} {
			t.Run(provider+"/"+name, func(t *testing.T) {
				rig := newWarmingRig(provider, 3, claudeSessionAt(func(now time.Time) time.Time { return now }))
				rig.settings = settings
				for i := 0; i < 5; i++ {
					rig.tick(t)
					*rig.now = rig.now.Add(time.Minute)
				}
				if rig.usage.total() != 0 || len(rig.sends) != 0 {
					t.Fatalf("reads %v sends %v without opt-in", rig.usage.reads, rig.sends)
				}
			})
		}
	}
}

func otherWarmed(provider string) string {
	if provider == "claude" {
		return "codex"
	}
	return "claude"
}

// A quota read never warms, even for an opted-in account whose reset is due;
// only the warming tick sends.
func TestQuotaReadNeverWarms(t *testing.T) {
	rig := newWarmingRig("claude", 3, claudeSessionAt(func(now time.Time) time.Time { return now }))
	rig.settings = map[string]string{"claudeAutoPing": `{"connections":{"acct-0":true,"acct-1":true,"acct-2":true}}`}
	for i := 0; i < 3; i++ {
		if err := rig.service.RefreshAll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if rig.usage.total() != 9 || len(rig.sends) != 0 {
		t.Fatalf("refresh read %d and sent %v", rig.usage.total(), rig.sends)
	}
	rig.tick(t)
	if len(rig.sends) != 3 {
		t.Fatalf("the warming tick sent %v", rig.sends)
	}
}

// Only opted-in accounts are touched: of 40 accounts with two opted in, the tick
// reads and warms those two, one at a time.
func TestWarmingFanOutIsTheOptedInAccountsOneAtATime(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			name := "session"
			if provider == "claude" {
				name = "session (5h)"
			}
			reset := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			rig := newWarmingRig(provider, 40, func(time.Time) Window { at := reset; return Window{Name: name, Remaining: ptr(1), ResetAt: &at} })
			rig.settings = map[string]string{provider + "AutoPing": `{"connections":{"acct-3":true,"acct-17":true,"acct-99":true}}`}
			rig.tick(t)
			if provider == "codex" {
				// Codex warms only once its inactive window's reset slides.
				reset = reset.Add(time.Minute)
				rig.tick(t)
			}
			for id := range rig.usage.reads {
				if id != "acct-3" && id != "acct-17" {
					t.Fatalf("read %s, which is not opted in", id)
				}
			}
			if rig.sends["acct-3"] != 1 || rig.sends["acct-17"] != 1 || len(rig.sends) != 2 {
				t.Fatalf("sends %v", rig.sends)
			}
			if rig.usage.most != 1 {
				t.Fatalf("%d reads at once", rig.usage.most)
			}
		})
	}
}

// Claude's reset is fixed, so between resets the warmer does not read usage
// every minute: once per reset, and again only within five minutes of it
// (quotaAutoPing.js:191-193). One send per reset.
func TestClaudeWarmingReadsUsageOncePerReset(t *testing.T) {
	start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	reset := start.Add(3 * time.Hour)
	rig := newWarmingRig("claude", 1, claudeSessionAt(func(time.Time) time.Time { return reset }))
	rig.settings = map[string]string{"claudeAutoPing": `{"connections":{"acct-0":true}}`}
	for minute := 0; minute < 175; minute++ {
		*rig.now = start.Add(time.Duration(minute) * time.Minute)
		rig.tick(t)
	}
	if reads := rig.usage.reads["acct-0"]; reads != 1 || len(rig.sends) != 0 {
		t.Fatalf("three hours before the reset: reads %d sends %v", reads, rig.sends)
	}
	for minute := 175; minute <= 181; minute++ {
		*rig.now = start.Add(time.Duration(minute) * time.Minute)
		rig.tick(t)
	}
	// Minutes 175-181 are all within five minutes of the reset: one read each.
	if reads := rig.usage.reads["acct-0"]; reads != 1+7 {
		t.Fatalf("near the reset: reads %d, want 8", reads)
	}
	if rig.sends["acct-0"] != 1 {
		t.Fatalf("sends %v, want one at the reset", rig.sends)
	}
}
