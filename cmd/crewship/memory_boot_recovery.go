//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/memory"
)

// recoverMemoryBeforeServe runs after migration and before server construction.
// If an intent cannot be settled, admitting another writer would make the
// filesystem/ledger disagreement harder to repair, so boot fails closed.
func recoverMemoryBeforeServe(ctx context.Context, db *sql.DB, memoryRoot, storageRoot string, logger *slog.Logger) error {
	blobRoot := ""
	if memoryRoot != "" {
		blobRoot = filepath.Join(memoryRoot, "versions")
	}
	recovered, conflicted, err := memory.RecoverPendingUnderRoot(ctx, db, blobRoot, storageRoot)
	if err != nil {
		return fmt.Errorf("recover pending memory mutations before serving: %w", err)
	}
	if recovered > 0 || conflicted > 0 {
		logger.Warn("memory mutation recovery completed before serving",
			"recovered", recovered, "conflicted", conflicted)
	}
	return nil
}
