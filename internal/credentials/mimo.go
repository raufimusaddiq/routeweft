package credentials

import "strings"

// ValidMimoCookieValue rejects cookie separators and control bytes in account credentials.
func ValidMimoCookieValue(value string) bool {
	for _, c := range value {
		if c < 0x21 || c > 0x7e || strings.ContainsRune("\";,\\", c) {
			return false
		}
	}
	return true
}
