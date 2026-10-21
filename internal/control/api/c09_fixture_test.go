package api

import (
	"context"
	"database/sql"
)

// operatorHash is the self-describing PBKDF2-SHA256 hash of "s3cret" at 1000
// iterations. Production hashes use 600000 (adminauth.HashPassword); the verifier
// reads the iteration count from the hash, so fixtures that only need a valid
// operator login skip the full work factor. Under -race the full factor cost
// ~5s per provisioned admin and pushed this package past Go's ten-minute
// package timeout (#78 C09). The fixture inserts the row directly so no
// production API accepts a caller-supplied hash; the production work factor is
// still exercised by internal/adminauth tests and by
// TestChangePasswordRequiresCurrentPasswordAndRevokesSessions.
const operatorHash = "pbkdf2-sha256$1000$cm91dGV3ZWZ0LXRlc3QwMA$M8yh3I98MqLTrYnNP+7dn5z/piMY8E9Nf7xKhAMfQ8M"

// bootstrapOperator provisions the fixture admin "operator"/"s3cret".
func bootstrapOperator(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "INSERT INTO admin_users (id,username,password_hash) VALUES (?,?,?)", "a1", "operator", operatorHash)
	return err
}
