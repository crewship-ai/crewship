package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// v161: native outbound notification system, MVP (issue #1412).
//
// Extends the v133 notification_channels table with the columns a
// per-user category × channel preference matrix needs, and adds two new
// tables: user_notification_prefs (the matrix itself) and
// notification_deliveries (a persistent outbox/delivery log so retries
// and anti-storm drops survive a restart instead of living only in
// slog.Warn — see internal/notify/dispatch.go's prior "errors are logged,
// never returned" contract).
//
// Three parts, one transaction:
//
//  1. Widen notification_channels.type's CHECK to admit 'shoutrrr' — the
//     new Slack/Discord/Telegram delivery mechanism riding
//     github.com/nicholas-fedor/shoutrrr — alongside the existing
//     'email'/'webhook'. Uses the writable_schema replace technique v148
//     established (rewriteTableDefaultLiteral's sibling for a CHECK
//     clause) rather than a full table recreate: no rows move, no FK/
//     index/trigger shape changes, only the stored CREATE text's CHECK
//     literal is rewritten.
//  2. ADD COLUMN the new per-channel metadata: provider (slack|discord|
//     telegram; empty for email/webhook), scope (workspace|user — a
//     'user' scoped channel is a member's own personal Telegram/webhook,
//     owner_user_id-gated), owner_user_id, categories_json (admin
//     allowlist of the 9 categories this channel may fan out to; empty =
//     all), min_priority (floor below which this channel is skipped).
//     The webhook/shoutrrr secret continues to ride the EXISTING
//     secret_enc encrypt-at-rest column — no new secret storage path.
//  3. CREATE the two new tables.
//
// See docs/guides/notifications.mdx for the category list, the
// preference matrix semantics, and the CLI surface.
func migrationNotificationPrefs(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	if err := widenNotificationChannelType(ctx, tx, logger); err != nil {
		return err
	}

	for _, ddl := range []string{
		`ALTER TABLE notification_channels ADD COLUMN provider TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE notification_channels ADD COLUMN scope TEXT NOT NULL DEFAULT 'workspace' CHECK (scope IN ('workspace','user'))`,
		`ALTER TABLE notification_channels ADD COLUMN owner_user_id TEXT`,
		`ALTER TABLE notification_channels ADD COLUMN categories_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE notification_channels ADD COLUMN min_priority TEXT NOT NULL DEFAULT 'low' CHECK (min_priority IN ('low','medium','high','urgent'))`,
		`CREATE INDEX IF NOT EXISTS idx_notification_channels_owner
		    ON notification_channels (owner_user_id) WHERE scope = 'user' AND deleted_at IS NULL`,

		// user_notification_prefs: the Linear/Novu-style category × channel
		// matrix. One row per (user, category, channel) cell. Category '*'
		// is the per-channel "mute everything on this channel" master
		// toggle (design note: "global per-channel mute") — checked first
		// by the router before the category-specific cell. `state` already
		// carries the v2 'digest' value so the CHECK/schema never needs to
		// change when digest batching windows ship; MVP only ever writes
		// 'off' or 'immediate'. Absence of a row for a (user, category,
		// channel) triple means 'off' — opt-in by default, never silently
		// push to a channel the user never configured.
		`CREATE TABLE IF NOT EXISTS user_notification_prefs (
		    id            TEXT PRIMARY KEY,
		    workspace_id  TEXT NOT NULL,
		    user_id       TEXT NOT NULL,
		    category      TEXT NOT NULL CHECK (category IN (
		        'approvals','escalations','runs.failed','runs.completed',
		        'chat.replies','security','budget','system','memory','*'
		    )),
		    channel_id    TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
		    state         TEXT NOT NULL DEFAULT 'off' CHECK (state IN ('off','immediate','digest')),
		    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    UNIQUE(user_id, category, channel_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_notification_prefs_ws_user
		    ON user_notification_prefs (workspace_id, user_id)`,

		// notification_deliveries: the outbox/delivery log (issue #1412's
		// "so retries survive restarts"). One row per (channel, dedup_key)
		// — UNIQUE, INSERT-OR-IGNORE'd by the router — so a coalesced
		// re-fire of the same source event never double-delivers to the
		// same channel, and a dropped_rate / dropped_pref verdict is
		// itself an auditable row rather than a lost slog.Warn line.
		`CREATE TABLE IF NOT EXISTS notification_deliveries (
		    id            TEXT PRIMARY KEY,
		    workspace_id  TEXT NOT NULL,
		    channel_id    TEXT NOT NULL,
		    user_id       TEXT,
		    category      TEXT NOT NULL,
		    dedup_key     TEXT NOT NULL,
		    source_kind   TEXT,
		    source_id     TEXT,
		    title         TEXT,
		    status        TEXT NOT NULL DEFAULT 'pending'
		                  CHECK (status IN ('pending','sent','failed','dropped_pref','dropped_rate')),
		    error         TEXT,
		    attempts      INTEGER NOT NULL DEFAULT 0,
		    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    sent_at       TEXT,
		    UNIQUE(channel_id, dedup_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_deliveries_ws_created
		    ON notification_deliveries (workspace_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_notification_deliveries_status
		    ON notification_deliveries (workspace_id, status)`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("notification prefs: %w (ddl: %s)", err, firstLine(ddl))
		}
	}

	if err := checkForeignKeys(ctx, tx); err != nil {
		return fmt.Errorf("notification prefs: post-migration foreign_key_check: %w", err)
	}
	return nil
}

const (
	// Anchor on the full CHECK clause text (unique in the stored CREATE),
	// mirroring v148's anchor-on-full-clause approach so a naive replace
	// can't corrupt an unrelated literal elsewhere in the same DDL.
	notificationChannelTypeCheckOld = `type          TEXT NOT NULL CHECK (type IN ('email','webhook'))`
	notificationChannelTypeCheckNew = `type          TEXT NOT NULL CHECK (type IN ('email','webhook','shoutrrr'))`
)

// widenNotificationChannelType admits the new 'shoutrrr' delivery
// mechanism into notification_channels.type's CHECK constraint in place,
// via the writable_schema rewrite technique (see v148's
// migrateBackfillNetworkModeRestricted for the fuller rationale: safe
// because no table is dropped/renamed/recreated and no rows move).
func widenNotificationChannelType(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	createSQL, err := tableCreateSQL(ctx, tx, "notification_channels")
	if err != nil {
		return fmt.Errorf("read notification_channels schema: %w", err)
	}
	if createSQL == "" || !strings.Contains(createSQL, notificationChannelTypeCheckOld) {
		// Already widened (idempotent re-apply) or the column shape
		// changed — nothing to rewrite.
		return nil
	}

	var schemaVersion int
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA writable_schema = ON`); err != nil {
		return fmt.Errorf("enable writable_schema: %w", err)
	}
	defer func() { _, _ = tx.ExecContext(ctx, `PRAGMA writable_schema = OFF`) }()

	res, err := tx.ExecContext(ctx,
		`UPDATE sqlite_master SET sql = replace(sql, ?, ?) WHERE type='table' AND name='notification_channels'`,
		notificationChannelTypeCheckOld, notificationChannelTypeCheckNew)
	if err != nil {
		return fmt.Errorf("rewrite notification_channels type CHECK: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("rows affected: %w", err)
	} else if n != 1 {
		return fmt.Errorf("expected to rewrite exactly 1 sqlite_master row for notification_channels, rewrote %d", n)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, schemaVersion+1)); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	if logger != nil {
		logger.Info("widened notification_channels.type CHECK to admit 'shoutrrr'")
	}
	return nil
}

// firstLine truncates a multi-line DDL string to its first line for a
// compact error message.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
