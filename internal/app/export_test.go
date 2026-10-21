package app

import (
	"testing"
	"time"
)

// shortenDrainDeadline lets a test exercise the drain-deadline cut path without
// waiting the production 10 s. The override is restored when the test ends.
func shortenDrainDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	previous := drainDeadline
	drainDeadline = d
	t.Cleanup(func() { drainDeadline = previous })
}
