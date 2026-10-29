package runtime

import (
	"sync"
	"testing"
)

// ComboRotation is open-sse/services/combo.js:208-237: the start index moves on
// after sticky consecutive uses and is re-reduced by the current list length.
// The expected sequences are what the oracle's getRotatedModels returned for the
// same lengths and limits (run against fc8dd1fc, first element of each result).
func TestComboRotationFollowsTheOracleStateMachine(t *testing.T) {
	state := NewState()
	var got []int
	for i := 0; i < 7; i++ {
		got = append(got, state.ComboRotation("a", 3, 2))
	}
	if want := []int{0, 0, 1, 1, 2, 2, 0}; !equalInts(got, want) {
		t.Fatalf("sticky 2 over 3: %v, want %v", got, want)
	}
	// A limit below one is one.
	got = nil
	for i := 0; i < 4; i++ {
		got = append(got, state.ComboRotation("b", 2, 0))
	}
	if want := []int{0, 1, 0, 1}; !equalInts(got, want) {
		t.Fatalf("sticky 0 over 2: %v, want %v", got, want)
	}
	// The stored index is re-reduced when the list length changes between requests.
	got = nil
	for _, length := range []int{3, 3, 2, 2, 3} {
		got = append(got, state.ComboRotation("c", length, 1))
	}
	if want := []int{0, 1, 0, 1, 0}; !equalInts(got, want) {
		t.Fatalf("changing length: %v, want %v", got, want)
	}
	if state.ComboRotation("empty", 0, 1) != 0 {
		t.Fatal("an empty list starts at 0")
	}
	// Combos rotate independently.
	if state.ComboRotation("d", 3, 1) != 0 || state.ComboRotation("d", 3, 1) != 1 || state.ComboRotation("e", 3, 1) != 0 {
		t.Fatal("rotation keys are not independent")
	}
}

func TestComboRotationIsSafeUnderConcurrency(t *testing.T) {
	state := NewState()
	var wg sync.WaitGroup
	counts := make([]int, 4)
	var mu sync.Mutex
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := state.ComboRotation("shared", 4, 1)
			mu.Lock()
			counts[start]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	for start, count := range counts {
		if count != 100 {
			t.Fatalf("start %d used %d times, want an even 100: %v", start, count, counts)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
