package quota

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	rt "github.com/raufimusaddiq/routeweft/internal/runtime"
)

// #231 (O08): a quota refresh reads one account at a time. A refresh of a thousand accounts must
// never have two reads in flight, whatever the provider count, and what it holds afterwards is one
// observation per account; the numbers it measures are logged, not asserted as budgets.

type countingClient struct {
	inFlight, peak, reads atomic.Int64
}

func (c *countingClient) Usage(ctx context.Context, _ Credential) (State, error) {
	now := c.inFlight.Add(1)
	for {
		peak := c.peak.Load()
		if now <= peak || c.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer c.inFlight.Add(-1)
	c.reads.Add(1)
	// A real read yields the processor while it waits on the network.
	runtime.Gosched()
	return State{Windows: []Window{{Name: "weekly", Remaining: ptr(0.5)}}}, ctx.Err()
}

func TestARefreshOfAThousandAccountsReadsOneAtATime(t *testing.T) {
	const accounts = 1000
	providers := []string{"codex", "claude", "kimi", "ollama"}
	connections := fakeConnections{byProvider: map[string][]credentials.Connection{}}
	credential := &fakeCredentials{values: map[string]string{}, known: map[string]bool{}}
	client := &countingClient{}
	clients := map[string]UsageClient{}
	for i := 0; i < accounts; i++ {
		provider := providers[i%len(providers)]
		id := fmt.Sprintf("conn-%04d", i)
		connections.byProvider[provider] = append(connections.byProvider[provider], credentials.Connection{ID: id, ProviderID: provider, Enabled: true})
		credential.values[id] = "token-" + id
		credential.known[id] = true
		clients[provider] = client
	}
	state := rt.NewState()
	observer := Observer{Clients: clients, Now: time.Now, Publisher: state}
	service := NewService(connections, credential, observer)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	if err := service.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)

	if client.reads.Load() != accounts {
		t.Fatalf("reads = %d, want %d", client.reads.Load(), accounts)
	}
	if peak := client.peak.Load(); peak != 1 {
		t.Fatalf("peak concurrent reads = %d, want 1", peak)
	}
	held := 0
	for i := 0; i < accounts; i++ {
		if _, ok := service.LastState(fmt.Sprintf("conn-%04d", i)); ok {
			held++
		}
	}
	if held != accounts {
		t.Fatalf("held %d observations, want one per account (%d)", held, accounts)
	}
	t.Logf("O08 measured: %d accounts over %d providers, peak concurrent reads %d, refresh %s, heap in use %+d KiB after GC (fake client, no network)",
		accounts, len(providers), client.peak.Load(), elapsed.Round(time.Millisecond), (int64(after.HeapInuse)-int64(before.HeapInuse))/1024)
}
