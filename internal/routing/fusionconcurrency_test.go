package routing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFusionConcurrentRunsRespectCapAndIsolateResults drives many simultaneous
// Fusion.Run calls with a fixed MaxConcurrent cap and proves the cap is never
// exceeded, every admitted run returns its own answer without cross-talk, the
// excess runs are rejected with ErrFusionBusy, and all admission slots are
// released afterwards (SPEC §16 Fusion concurrency).
func TestFusionConcurrentRunsRespectCapAndIsolateResults(t *testing.T) {
	const (
		cap    = 4
		runs   = 32
		panels = 3
	)
	var inFlight, peak atomic.Int64
	// panelSeen counts one observation per admitted run. Because each admitted
	// run calls Panels once per panel, the run's first panel call marks admission
	// and a run-scoped counter keys on the run's own model prefix.
	seenRuns := sync.Map{}
	release := make(chan struct{})
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: panels, MaxConcurrent: cap, HardTimeout: 5 * time.Second, Grace: 0, MaxPanels: panels}, Panels: func(ctx context.Context, candidate PanelCandidate) (io.ReadCloser, error) {
		if candidate.ModelID == "" {
			return nil, errors.New("missing model")
		}
		runID := candidate.ModelID
		if idx := strings.LastIndex(candidate.ModelID, "-"); idx >= 0 {
			runID = candidate.ModelID[:idx]
		}
		if _, loaded := seenRuns.LoadOrStore(runID, struct{}{}); !loaded {
			current := inFlight.Add(1)
			for {
				observed := peak.Load()
				if current <= observed || peak.CompareAndSwap(observed, current) {
					break
				}
			}
			defer inFlight.Add(-1)
		}
		select {
		case <-release:
			return io.NopCloser(strings.NewReader("panel:" + candidate.ModelID)), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	fusion.Judge = func(_ context.Context, _ string, answers []string) (string, error) {
		return "judged:" + strings.Join(answers, ","), nil
	}

	var wg sync.WaitGroup
	results := make([]string, runs)
	errs := make([]error, runs)
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidates := make([]PanelCandidate, panels)
			for p := 0; p < panels; p++ {
				candidates[p] = PanelCandidate{ProviderID: "p", ModelID: fmt.Sprintf("m-%d-%d", i, p)}
			}
			results[i], errs[i] = fusion.Run(context.Background(), candidates, "judge")
		}(i)
	}

	// Let all goroutines reach the admission gate before releasing panels.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	completed := 0
	busyCount := 0
	for i := range results {
		switch {
		case errs[i] == nil:
			completed++
			// The judge sees this run's own panels; arrival order is not fixed, so
			// assert the exact panel multiset rather than a fixed sequence.
			if !strings.HasPrefix(results[i], "judged:") {
				t.Fatalf("run %d answer %q lacks judge prefix", i, results[i])
			}
			got := strings.Split(strings.TrimPrefix(results[i], "judged:"), ",")
			if len(got) != panels {
				t.Fatalf("run %d got %d panels, want %d", i, len(got), panels)
			}
			wantSet := make(map[string]bool, panels)
			for p := 0; p < panels; p++ {
				wantSet[fmt.Sprintf("panel:m-%d-%d", i, p)] = true
			}
			for _, answer := range got {
				if !wantSet[answer] {
					t.Fatalf("run %d saw foreign panel %q (cross-talk)", i, answer)
				}
				delete(wantSet, answer)
			}
			if len(wantSet) != 0 {
				t.Fatalf("run %d missing panels %v", i, wantSet)
			}
		case errors.Is(errs[i], ErrFusionBusy):
			busyCount++
		default:
			t.Fatalf("run %d unexpected error: %v", i, errs[i])
		}
	}
	if completed == 0 {
		t.Fatal("no Fusion run completed")
	}
	if busyCount == 0 {
		t.Fatalf("concurrency cap was never enforced: %d runs all admitted", runs)
	}
	if completed+busyCount != runs {
		t.Fatalf("run accounting %d completed + %d busy != %d", completed, busyCount, runs)
	}
	if peak.Load() > cap {
		t.Fatalf("peak concurrent runs %d exceeded cap %d", peak.Load(), cap)
	}

	// Slots must be free again: a fresh run after the burst must be admitted.
	after := []PanelCandidate{{ModelID: "after-0"}, {ModelID: "after-1"}, {ModelID: "after-2"}}
	if _, err := fusion.Run(context.Background(), after, "judge"); err != nil {
		t.Fatalf("post-burst run rejected: %v", err)
	}
}

// TestFusionConcurrentRunRaceFree runs many Fusion requests concurrently under
// the race detector to prove first-use config freezing and slot bookkeeping are
// safe when requests arrive together: exactly one effective config is chosen and
// it is honored by every concurrent run (SPEC §16).
func TestFusionConcurrentRunRaceFree(t *testing.T) {
	const maxPanels = 2
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 1, MaxConcurrent: 4, MaxPanels: maxPanels, HardTimeout: time.Second}, Panels: func(ctx context.Context, _ PanelCandidate) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("ok")), nil
	}}
	var wg sync.WaitGroup
	var tooLarge atomic.Int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A fan-out over the frozen MaxPanels must be rejected the same way for
			// every run, proving all goroutines observed one frozen config.
			candidates := []PanelCandidate{{ModelID: "a"}, {ModelID: "b"}, {ModelID: "c"}}
			if _, err := fusion.Run(context.Background(), candidates, ""); errors.Is(err, ErrFusionPanelLimit) {
				tooLarge.Add(1)
			} else if err != nil {
				t.Errorf("unexpected err: %v", err)
			}
		}()
	}
	wg.Wait()
	if tooLarge.Load() != 32 {
		t.Fatalf("only %d/32 runs honored the frozen panel limit", tooLarge.Load())
	}
}
