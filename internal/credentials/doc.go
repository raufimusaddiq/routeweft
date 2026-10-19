// Package credentials owns durable provider credential storage and the
// memory-first rotating-token state required by SPEC §19 and PRD-AUTH-003.
//
// Storage keeps one sealed secret blob per provider connection. Sealing is
// authenticated encryption keyed by an operator-supplied 32-byte master key, so
// the SQLite file never contains a plaintext refresh token, cookie or API key.
// The package never chooses where the master key lives; callers resolve it.
package credentials
