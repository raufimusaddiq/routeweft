package claude

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
)

func randomURLSafe(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func sha256Sum(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func parseSeconds(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}
