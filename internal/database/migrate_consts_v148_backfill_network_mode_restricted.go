package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// v148: fail every crew closed to restricted egress — both the existing
// grandfathered rows AND the column DEFAULT, so a crew is restricted by
// construction no matter which path creates it.
//
// Two parts, one transaction:
//
//  1. Backfill. Every legacy crews.network_mode='free' row → 'restricted'.
//     'free' was never an intentional per-crew choice for those rows (it was
//     the v18 column default + a pre-v18 backfill), so we fail them closed
//     (DECISION 2026-07-23, issue #1366). A restricted crew still reaches every
//     DefaultAllowedDomains host (the LLM/CLI provider APIs) plus whatever an
//     operator adds to that crew's allowed_domains.
//
//  2. Column-DEFAULT flip. The v18 ALTER stored `... DEFAULT 'free' ...` into
//     crews' CREATE statement, so ANY insert that omits network_mode — a raw
//     SQL insert, a seed, a future migration, a writer that forgets — lands on
//     'free'. Flip that default to 'restricted' in place so the safe posture is
//     the default, not something every writer has to remember.
//
// The flip uses the writable_schema technique (mirrors v144's
// rewriteTableDefaultLiteral), NOT a table recreate. This is deliberately safe
// on the wide crews FK parent: no table is dropped/renamed/recreated, no
// rootpage/index/trigger/FK clause changes, and no rows move — so the v89
// recreate-and-swap deferred-FK-at-COMMIT hazard cannot arise. Only the stored
// CREATE text's DEFAULT literal is rewritten, then schema_version is bumped so
// already-open connections recompile INSERTs against the new default.
//
// Operators who deliberately want a specific crew unrestricted can re-open it
// to 'free' (or add the domains it needs) after upgrade — see the changelog
// note shipped with this migration.

const (
	// Anchor on "DEFAULT 'free'" (unique) rather than the bare literal 'free',
	// which also appears inside CHECK(network_mode IN ('free','restricted')) on
	// the same column — a naive replace would corrupt the CHECK.
	crewNetworkModeDefaultOld = "network_mode TEXT NOT NULL DEFAULT 'free'"
	crewNetworkModeDefaultNew = "network_mode TEXT NOT NULL DEFAULT 'restricted'"
)

func migrateBackfillNetworkModeRestricted(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	// 1) Backfill existing grandfathered rows.
	if _, err := tx.ExecContext(ctx,
		`UPDATE crews SET network_mode = 'restricted' WHERE network_mode = 'free'`); err != nil {
		return fmt.Errorf("backfill crews.network_mode: %w", err)
	}

	// 2) Flip the column DEFAULT in place so future omitting-inserts fail safe.
	createSQL, err := tableCreateSQL(ctx, tx, "crews")
	if err != nil {
		return fmt.Errorf("read crews schema: %w", err)
	}
	if createSQL == "" || !strings.Contains(createSQL, crewNetworkModeDefaultOld) {
		// Already flipped (idempotent re-apply) or the column shape changed —
		// nothing to rewrite. Still run the FK integrity gate for parity.
		if err := checkForeignKeys(ctx, tx); err != nil {
			return fmt.Errorf("post-flip foreign_key_check: %w", err)
		}
		return nil
	}

	var schemaVersion int
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA writable_schema = ON`); err != nil {
		return fmt.Errorf("enable writable_schema: %w", err)
	}
	// writable_schema is connection-level, non-transactional state — reset it on
	// every path, including error returns.
	defer func() { _, _ = tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`) }()

	res, err := tx.ExecContext(ctx,
		`UPDATE sqlite_master SET sql = replace(sql, ?, ?) WHERE type='table' AND name='crews'`,
		crewNetworkModeDefaultOld, crewNetworkModeDefaultNew)
	if err != nil {
		return fmt.Errorf("rewrite crews network_mode DEFAULT: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("rows affected: %w", err)
	} else if n != 1 {
		return fmt.Errorf("expected to rewrite exactly 1 sqlite_master row for crews, rewrote %d", n)
	}
	// Bump schema_version so an already-open connection recompiles INSERTs
	// against the new default instead of the cached old one (v144 confirmed this
	// is necessary empirically).
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, schemaVersion+1)); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	if err := checkForeignKeys(ctx, tx); err != nil {
		return fmt.Errorf("post-flip foreign_key_check: %w", err)
	}
	logger.Info("flipped crews.network_mode DEFAULT 'free' -> 'restricted'")
	return nil
}
