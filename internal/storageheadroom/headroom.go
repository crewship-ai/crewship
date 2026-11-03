// Package storageheadroom is the free-disk admission shared by every content
// writer that grows the main database: native Craft and artifact versions
// (through internal/artifacts), the legacy document and Slides stores and the
// GridCraft store, which sit below internal/artifacts in the import graph and
// therefore cannot call it there, and the Mooring writers.
//
// Logical admission (workspace and instance byte budgets) is enforced by the
// SQL capacity triggers; this probe only refuses a write that would eat into
// the space reserved for database recovery and backup.
package storageheadroom

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/diskusage"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ErrInsufficient refuses a write because free disk is below the reserve.
// Nothing was written; internal/artifacts maps it to its aggregate storage
// refusal, so every HTTP surface answers with the same storage-full response.
var ErrInsufficient = errors.New("free disk below the database storage reserve")

// ErrInvalid rejects a negative write estimate.
var ErrInvalid = errors.New("invalid storage headroom estimate")

type usageFunc func(string) (diskusage.Stats, error)

var usage atomic.Pointer[usageFunc]

func init() {
	fn := usageFunc(diskusage.Usage)
	usage.Store(&fn)
}

// Check runs inside the caller's writer transaction (under the existing
// database writer fence) before the first row of the write is inserted. It
// keeps the operation reserve, one whole DB copy, and twice the outstanding
// upload allowance plus this write for SQLite pages/WAL. An in-memory
// database (no file) is not probed. Reads must not call this guard.
func Check(ctx context.Context, tx *sql.Tx, writeBytes int64) error {
	if writeBytes < 0 {
		return ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_storage_reservations WHERE julianday(expires_at)<=julianday(?)`, tsformat.Format(time.Now().UTC())); err != nil {
		return err
	}
	var path string
	if err := tx.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	var pages, pageSize, reserved, reserve int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT page_count FROM pragma_page_count),(SELECT page_size FROM pragma_page_size),reserved_bytes,min_free_bytes FROM artifact_storage_global WHERE id=1`).Scan(&pages, &pageSize, &reserved, &reserve); err != nil {
		return err
	}
	if pages < 0 || pageSize <= 0 || reserved < 0 || reserve < 0 {
		return ErrInsufficient
	}
	stats, err := (*usage.Load())(filepath.Dir(path))
	if err != nil {
		return errors.Join(ErrInsufficient, err)
	}
	required := uint64(reserve) + uint64(pages)*uint64(pageSize) + 2*(uint64(reserved)+uint64(writeBytes))
	if stats.FreeBytes < required {
		return ErrInsufficient
	}
	return nil
}
