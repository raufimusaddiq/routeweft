package capabilities

import "testing"

func TestOutputCeiling(t *testing.T) {
	if got, ok := OutputCeiling("volcengine-ark", "glm-5.1"); !ok || got != 128000 {
		t.Fatalf("glm-5.1 = %d, %v; want 128000, true", got, ok)
	}
	// An unresolved model falls to the catalog floor, which is not a ceiling.
	if got, ok := OutputCeiling("volcengine-ark", "no-such-model"); ok || got != 0 {
		t.Fatalf("unresolved = %d, %v; want 0, false", got, ok)
	}
	if got, ok := OutputCeiling("volcengine-ark", ""); ok || got != 0 {
		t.Fatalf("empty model = %d, %v; want 0, false", got, ok)
	}
}
