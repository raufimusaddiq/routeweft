package runtime

import "sync"

// RuntimeState contains mutable, process-local operational state, separate from
// the immutable request-serving RuntimeSnapshot.
type RuntimeState struct {
	mu sync.Mutex
}

func NewState() *RuntimeState { return &RuntimeState{} }
