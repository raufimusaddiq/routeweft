package cloudflare

const (
	Clef      = "@cf/cloudflare/clef"
	ClefFlash = "@cf/cloudflare/clef-flash"
)

// ValidAccountID reports whether id is safe to use as a path segment. The URL
// itself is built only by wire.ResolveTarget (wire/cloudflare.go).
func ValidAccountID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
