package servicelifecycle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

var ErrBackupMaintenance = errors.New("service backup maintenance requires explicit recovery")

// BeginBackupFence atomically excludes new controller claims and rejects any
// live lease. A failure/crash retains this fence until the administrator has
// verified every writer is stopped and explicitly resumes the service intents.
func BeginBackupFence(ctx context.Context, db *sql.DB, crew, operation string) (string, error) {
	if db == nil || crew == "" || (operation != "backup" && operation != "restore") {
		return "", ErrBackupMaintenance
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(nonce[:])
	now := tsformat.Format(time.Now())
	result, err := db.ExecContext(ctx, `INSERT INTO service_backup_fences(crew_id,workspace_id,token,created_at,operation)
 SELECT id,workspace_id,?,?,? FROM crews WHERE id=? AND deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM service_runtime_intents WHERE crew_id=? AND lease_until>?)`, token, now, operation, crew, crew, now)
	if err != nil {
		return "", ErrBackupMaintenance
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrBackupMaintenance
	}
	return token, nil
}

// EndBackupFence is host-only. The transport calls it only after a successful
// export/remount or complete verified restore; error paths never auto-resume.
func EndBackupFence(ctx context.Context, db *sql.DB, crew, token string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM service_backup_fences WHERE crew_id=? AND token=?`, crew, token)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrBackupMaintenance
	}
	return nil
}
