package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
)

const deckNativeMigrationVersion = 20261028210747

// Widen only the two closed application CHECKs. Rebuilding the version head
// would cascade-delete dashboard datasets; schema-text changes preserve every
// row, index, trigger, rootpage and FK. This follows the existing v144 approach.
// A dedicated connection permits unconditional pragma cleanup even if the
// caller cancels the transaction. The fnNoTx replay is deliberately idempotent.
func migrationDeckNativeKinds(ctx context.Context, db *sql.DB, logger *slog.Logger) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	var tx *sql.Tx
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanup, `PRAGMA writable_schema=OFF`); err != nil {
			// Never return an unsafe connection to the pool after failed cleanup.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			retErr = errors.Join(retErr, fmt.Errorf("restore protected schema connection: %w", err))
		}
		_ = conn.Close()
	}()
	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	type change struct{ table, before, after string }
	var changes []change
	for _, c := range []struct{ table, old, next string }{
		{"craft_native_documents", "CHECK(application_id IN ('wordcraft','gridcraft'))", "CHECK(application_id IN ('wordcraft','gridcraft','deckcraft'))"},
		{"artifact_version_heads", "CHECK(content_type IN ('artifact_project','wordcraft','gridcraft'))", "CHECK(content_type IN ('artifact_project','wordcraft','gridcraft','deckcraft'))"},
	} {
		original, err := tableCreateSQL(ctx, tx, c.table)
		if err != nil {
			return err
		}
		// Design's append-only migration may already have widened the head.
		// Preserve that closed family rather than replacing its CHECK with the
		// pre-Design shape. Unknown schema variants still fail closed.
		if c.table == "artifact_version_heads" && strings.Contains(original, "'design'") {
			c.old = "CHECK(content_type IN ('artifact_project','wordcraft','gridcraft','design'))"
			c.next = "CHECK(content_type IN ('artifact_project','wordcraft','gridcraft','design','deckcraft'))"
		}
		switch {
		case strings.Count(original, c.next) == 1 && !strings.Contains(original, c.old):
			// Schema work committed but its separate migration-ledger ACK was lost.
		case strings.Count(original, c.old) == 1 && !strings.Contains(original, c.next):
			changes = append(changes, change{c.table, original, strings.Replace(original, c.old, c.next, 1)})
		default:
			return fmt.Errorf("unexpected native family constraint in %s", c.table)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	// Historical databases can contain unrelated, already tolerated orphans.
	// Preserve their exact identities; this schema-only change must introduce
	// neither a new violation nor another occurrence of an existing one.
	beforeFK, err := deckNativeForeignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	var version int64
	if err = tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&version); err != nil {
		return err
	}
	if version < 0 || version >= math.MaxInt32 {
		return errors.New("native family schema version exhausted")
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA writable_schema=ON`); err != nil {
		return err
	}
	for _, c := range changes {
		result, err := tx.ExecContext(ctx, `UPDATE sqlite_master SET sql=? WHERE type='table' AND name=? AND sql=?`, c.after, c.table, c.before)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("native family schema changed during migration")
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA writable_schema=OFF`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version=%d`, version+1)); err != nil {
		return err
	}
	afterFK, err := deckNativeForeignKeyViolations(ctx, tx)
	if err != nil {
		return err
	}
	for violation, count := range afterFK {
		if count > beforeFK[violation] {
			return fmt.Errorf("native family migration introduced foreign key violation: %s", violation)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	tx = nil
	if logger != nil {
		logger.Info("admitted native presentation family without changing existing data", "tables", len(changes))
	}
	return nil
}

func deckNativeForeignKeyViolations(ctx context.Context, tx *sql.Tx) (map[string]uint64, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	violations := make(map[string]uint64)
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var foreignKey int64
		if err := rows.Scan(&table, &rowid, &parent, &foreignKey); err != nil {
			return nil, err
		}
		// WITHOUT ROWID tables report NULL rowids. Keep multiplicity as well
		// as identity so an additional violation cannot hide behind that NULL.
		identity := fmt.Sprintf("table=%q rowid=%v parent=%q fk=%d", table, rowid, parent, foreignKey)
		violations[identity]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return violations, nil
}
