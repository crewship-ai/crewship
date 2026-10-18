package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// Test-only real registry prefix. Exported solely for database_test, not API.
func LegacyReceiptOldPrefixForTest(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return err
	}
	for i, m := range migrations {
		if m.name == "pipeline_legacy_head_receipts" {
			return applyRegistry(ctx, db, migrations[:i], logger)
		}
	}
	return fmt.Errorf("legacy receipt storage frontier missing")
}
