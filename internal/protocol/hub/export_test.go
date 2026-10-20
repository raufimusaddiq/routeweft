package hub

import "testing"

// WithTestLegs adds synthetic routes only to the test binary; tests using it must not run in parallel.
func WithTestLegs(t *testing.T, extra ...Leg) {
	t.Helper()
	saved := legs
	legs = append(Legs(), extra...)
	t.Cleanup(func() { legs = saved })
}
