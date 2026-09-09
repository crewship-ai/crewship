package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

// This also processes soft-deleted rows and is replayed when an older backup
// is restored. Encryption failure rolls back the migration, not a silent skip.
func migrationPrivateServices(ctx context.Context, tx *sql.Tx, _ *slog.Logger) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, services_json FROM crews WHERE services_json IS NOT NULL AND services_json <> ''")
	if err != nil {
		return err
	}
	type change struct{ id, sealed string }
	var changes []change
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		sealed, err := serviceconfig.Seal(raw)
		if err != nil {
			rows.Close()
			return fmt.Errorf("protect stored service configuration: %w", err)
		}
		if sealed != raw {
			changes = append(changes, change{id, sealed})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.ExecContext(ctx, "UPDATE crews SET services_json = ? WHERE id = ?", c.sealed, c.id); err != nil {
			return err
		}
	}
	return nil
}
