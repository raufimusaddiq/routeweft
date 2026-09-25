package app

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

// requestIDHeader is the client-visible correlation ID defined for operator logs.
const requestIDHeader = "X-Request-Id"

var fallbackRequestID atomic.Uint64

// withRequestID assigns a stable request ID and echoes any client-provided one.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(requestIDHeader)
		if !validRequestID(requestID) {
			requestID = newRequestID()
		}
		r.Header.Set(requestIDHeader, requestID)
		w.Header().Set(requestIDHeader, requestID)
		next.ServeHTTP(w, r)
	})
}

// withBodyLimit enforces the configurable ingress body limit, defaulting to the
// 128 MB compatibility target from PRD-API-005.
func withBodyLimit(limit int64, next http.Handler) http.Handler {
	if limit <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Admin routes carry their own bounded-upload handling (SPEC §25), so the
		// public inference body limit applies only to inference paths.
		if !isInferencePath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > limit {
			http.Error(w, "request body exceeds configured limit", http.StatusRequestEntityTooLarge)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_.:", char)) {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "req_fallback_" + strconv.FormatUint(fallbackRequestID.Add(1), 10)
	}
	return "req_" + hex.EncodeToString(buf[:])
}
