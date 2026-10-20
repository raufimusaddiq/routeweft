package onboarding

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// RequireOracleSecretScanEnv makes the secret scan fail instead of skipping when
// the oracle is absent. The parity CI job sets it, so the enforcing job can never
// silently run the weaker prefix-only check.
const RequireOracleSecretScanEnv = "ROUTEWEFT_REQUIRE_ORACLE_SECRET_SCAN"

// TestNoThirdPartyProviderSecretInTree enforces the divergence recorded in
// docs/SPEC.md 19.1: the reference ships third-party OAuth client secrets in its
// own public tree, and Routeweft does not republish any of them.
//
// This repository is public, so a committed secret is permanent and cannot be
// withdrawn. The check is general on purpose: matching one known prefix let an
// iflow secret ship while the Google tripwire stayed green. It reads the secret
// values from the pinned oracle when ORACLE_DIR is available, and always applies a
// prefix check that needs no oracle.
func TestNoThirdPartyProviderSecretInTree(t *testing.T) {
	root := repoRoot(t)
	secrets := oracleSecretLiterals(t)
	// Without the oracle only prefix markers apply, and a secret like iflow's has no
	// recognizable prefix — that degraded mode is exactly how the iflow literal
	// shipped while this test was green.
	//
	// The enforcing CI job sets RequireOracleSecretScan, so it cannot degrade: a
	// missing oracle there is a hard failure. An ordinary `go test ./...` has no
	// oracle, so it skips loudly rather than passing a weaker check as if it were
	// the full one.
	if len(secrets) == 0 {
		if os.Getenv(RequireOracleSecretScanEnv) != "" {
			t.Fatalf("ORACLE_DIR is unset but %s is required: only known credential prefixes could "+
				"be checked, so a provider secret without one would pass.", RequireOracleSecretScanEnv)
		}
		t.Skip("ORACLE_DIR unset: skipping the full oracle secret scan; the parity CI job enforces it")
	}

	// Prefixes that identify a credential regardless of which values the oracle
	// currently ships, so the check still runs without an oracle.
	markers := []string{"GOC" + "SPX-"}
	for _, secret := range secrets {
		if len(secret) >= 12 {
			markers = append(markers, secret)
		}
	}

	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist", ".vite", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".js", ".mjs", ".ts", ".tsx", ".json", ".md", ".yml", ".yaml", ".sql", ".sh":
		default:
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		text := string(raw)
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				offenders = append(offenders, rel)
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("a third-party provider OAuth secret is committed in: %v\n"+
			"Routeweft does not ship provider client secrets; read them from the "+
			"operator environment (docs/SPEC.md 19.1).", offenders)
	}
}

// oracleSecretLiterals returns the secret values the pinned reference ships, so
// the tripwire covers every provider rather than a hardcoded subset. Without an
// oracle only the prefix markers apply.
func oracleSecretLiterals(t *testing.T) []string {
	t.Helper()
	if os.Getenv("ORACLE_DIR") == "" {
		return nil
	}
	script := filepath.Join(repoRoot(t), "compat", "parity", ".secrets.generated.mjs")
	if err := os.WriteFile(script, []byte(
		"import { providerSecretLiterals } from './providers.mjs';\n"+
			"process.stdout.write(JSON.stringify(providerSecretLiterals()));\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(script) })
	command := exec.Command("node", script)
	command.Dir = repoRoot(t)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("oracle secret literals: %v", err)
	}
	var secrets []string
	for _, item := range strings.Split(strings.Trim(string(output), "[]\n"), ",") {
		item = strings.Trim(item, `" `)
		if item != "" {
			secrets = append(secrets, item)
		}
	}
	return secrets
}

// repoRoot walks up to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}
