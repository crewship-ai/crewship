//go:build !clionly

package main

import (
	"context"
	"os"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Local recovery uses the instance's explicit audit file or app DataDir, never
// the directory of an arbitrary DATABASE_URL. The standalone CLI has no access
// to a running server's in-process revocation notification registry.
func localAuditSigningContext(ctx context.Context) (context.Context, error) {
	configured := os.Getenv(journal.SigningKeysPathEnv)
	if configured != "" {
		keys, err := journal.LoadSigningKeys(configured)
		if err != nil {
			return nil, err
		}
		return journal.WithSigningKeys(ctx, keys), nil
	}
	dataDir, err := database.ResolveDefaultDataDir()
	if err != nil {
		return nil, err
	}
	keys, _, err := journal.EnsureSigningKeys(dataDir.Root, "")
	if err != nil {
		return nil, err
	}
	return journal.WithSigningKeys(ctx, keys), nil
}
