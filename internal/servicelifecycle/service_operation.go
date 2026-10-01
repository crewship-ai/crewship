package servicelifecycle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ServiceOperations provides a two-minute lease for a provider operation that
// the provider independently bounds to ninety seconds. It covers legacy starts
// that do not acquire a controller intent lease. A fence can win only before
// admission or after every admitted bounded operation has drained.
func ServiceOperations(db *sql.DB) provider.ServiceOperationGate {
	return func(ctx context.Context, crew string) (func(context.Context) error, error) {
		if db == nil || crew == "" {
			return nil, ErrBackupMaintenance
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		token := hex.EncodeToString(nonce[:])
		now := time.Now()
		// Expired admission records are operational, never data ownership.
		// Unknown runtime outcomes are still inspected and stopped by capture.
		if _, err := db.ExecContext(ctx, `DELETE FROM service_operation_leases WHERE crew_id=? AND lease_until<=?`, crew, tsformat.Format(now)); err != nil {
			return nil, err
		}
		result, err := db.ExecContext(ctx, `INSERT INTO service_operation_leases(token,crew_id,workspace_id,lease_until)
   SELECT ?,id,workspace_id,? FROM crews WHERE id=? AND deleted_at IS NULL
   AND NOT EXISTS(SELECT 1 FROM service_backup_fences WHERE crew_id=?)`, token, tsformat.Format(now.Add(2*time.Minute)), crew, crew)
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, ErrBackupMaintenance
		}
		return func(ctx context.Context) error {
			_, err := db.ExecContext(ctx, `DELETE FROM service_operation_leases WHERE token=? AND crew_id=?`, token, crew)
			return err
		}, nil
	}
}
