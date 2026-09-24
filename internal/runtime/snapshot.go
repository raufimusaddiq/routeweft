// Package runtime owns immutable request configuration and mutable live state.
package runtime

// RuntimeSnapshot is immutable after publication. Accessors return copies of
// mutable values so consumers cannot modify the active configuration.
type RuntimeSnapshot struct {
	version        uint64
	configRevision uint64
	settings       map[string]string
}

func (s *RuntimeSnapshot) Version() uint64        { return s.version }
func (s *RuntimeSnapshot) ConfigRevision() uint64 { return s.configRevision }
func (s *RuntimeSnapshot) Settings() map[string]string {
	return cloneSettings(s.settings)
}
