package api

import (
	"context"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
)

// operatorHash is the self-describing PBKDF2-SHA256 hash of "s3cret" at 1000
// iterations. Production hashes use 600000 (adminauth.HashPassword); the verifier
// reads the iteration count from the hash, so fixtures that only need a valid
// operator login skip the full work factor. Under -race the full factor cost
// ~5s per provisioned admin and pushed this package past Go's ten-minute
// package timeout (#78 C09). The production work factor itself is still
// exercised by internal/adminauth tests and TestChangePassword…, which hashes
// through adminauth.Store.ChangePassword.
const operatorHash = "pbkdf2-sha256$1000$cm91dGV3ZWZ0LXRlc3QwMA$M8yh3I98MqLTrYnNP+7dn5z/piMY8E9Nf7xKhAMfQ8M"

// bootstrapOperator provisions the fixture admin "operator"/"s3cret".
func bootstrapOperator(ctx context.Context, accounts *adminauth.Store) error {
	_, err := accounts.BootstrapWithHash(ctx, "a1", "operator", operatorHash)
	return err
}
