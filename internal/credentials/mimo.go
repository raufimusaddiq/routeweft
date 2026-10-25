package credentials

import "github.com/raufimusaddiq/routeweft/internal/providers/wire"

// ValidMimoCookieValue rejects cookie separators and control bytes in account credentials.
func ValidMimoCookieValue(value string) bool { return wire.ValidCookieValue(value) }
