package backup

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Hold the SQLite writer lock through publication so explicit recovery cannot
// replace this epoch between its final proof and the archive rename.
func publishServiceSnapshotBundle(ctx context.Context, db *sql.DB, fences []serviceBackupFence, publish func() error) error {
	if len(fences) == 0 {
		return publish()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, f := range fences {
		result, err := tx.ExecContext(ctx, `UPDATE service_backup_fences SET producer_until=producer_until WHERE crew_id=? AND token=? AND producer_until>?`, f.crew, f.token, tsformat.Format(time.Now()))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("backup: service snapshot producer lost its fenced epoch")
		}
	}
	if err = publish(); err != nil {
		return err
	}
	return tx.Commit()
}
