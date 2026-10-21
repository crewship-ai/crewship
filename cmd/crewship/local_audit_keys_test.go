//go:build !clionly

package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestLocalRecoveryUsesPersistentAppAuditSnapshot(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-local-recovery-root")
	t.Setenv(journal.SigningKeysPathEnv, "")
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)
	keys, _, err := journal.EnsureSigningKeys(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := localAuditSigningContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := journal.SigningKeysForContext(ctx)
	if err != nil || actual.ActiveID() != keys.ActiveID() {
		t.Fatalf("local recovery selected a different app keyring: %v", err)
	}
}
