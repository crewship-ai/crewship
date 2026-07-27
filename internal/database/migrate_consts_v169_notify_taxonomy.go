package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/notify"
)

// v169: notification category taxonomy v2.
//
// The #1412 MVP shipped 9 categories of which only 5 could ever fire —
// categoryByKind was the only producer anywhere, and it maps inbox kinds, so
// 'runs.completed', 'security', 'budget' and 'system' were switchable rows in
// the preference matrix that nothing could ever deliver against. Issues had no
// coverage at all. See internal/notify/categories.go for the full rationale
// and the replacement vocabulary.
//
// This migration moves the STORED vocabulary onto the new names:
//
//  1. Rewrite user_notification_prefs.category's CHECK to the new set, using
//     the writable_schema technique v148/v161 established — no rows move, no
//     FK/index/trigger shape changes, only the stored CREATE text's CHECK
//     literal is rewritten.
//  2. Rewrite the stored category values in user_notification_prefs and in
//     notification_channels.categories_json (the admin per-channel allowlist).
//
// Rewrite, never drop: a user who opted a cell in stays opted in, and one who
// muted stays muted. 'system' fans out to BOTH new system categories because
// it never had a producer — a pref row for it expresses "tell me about system
// things", and collapsing that onto one of the two would discard half of what
// was asked for. internal/notify.LegacyCategories is the single source of
// truth for the mapping and is unit-tested for totality against the old set.
//
// notification_deliveries.category is deliberately NOT rewritten. It is a
// historical log; rewriting it would falsify what was actually delivered at
// the time. It has no CHECK, so old rows stay readable and the reader maps
// legacy names for display.
func migrationNotifyTaxonomy(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	if err := widenPrefsCategoryCheck(ctx, tx, logger); err != nil {
		return err
	}
	if err := remapPrefRows(ctx, tx, logger); err != nil {
		return err
	}
	if err := remapChannelAllowlists(ctx, tx, logger); err != nil {
		return err
	}
	return nil
}

// prefsCategoryCheckOld is the exact CHECK text v161 wrote. Matched verbatim
// so a schema that has already been rewritten (idempotent re-apply) or that
// diverged is left alone rather than half-edited.
const prefsCategoryCheckOld = `category      TEXT NOT NULL CHECK (category IN (
		        'approvals','escalations','runs.failed','runs.completed',
		        'chat.replies','security','budget','system','memory','*'
		    ))`

// prefsCategoryCheckNew is built from notify.AllCategories so the constraint
// and the Go vocabulary cannot drift — TestPrefsCheckMatchesAllCategories
// pins that the generated text admits exactly the current set.
var prefsCategoryCheckNew = func() string {
	quoted := make([]string, 0, len(notify.AllCategories)+1)
	for _, c := range notify.AllCategories {
		quoted = append(quoted, "'"+c+"'")
	}
	quoted = append(quoted, "'"+notify.CategoryMuteAll+"'")
	return "category      TEXT NOT NULL CHECK (category IN (\n\t\t        " +
		strings.Join(quoted, ",") + "\n\t\t    ))"
}()

func widenPrefsCategoryCheck(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	createSQL, err := tableCreateSQL(ctx, tx, "user_notification_prefs")
	if err != nil {
		return fmt.Errorf("read user_notification_prefs schema: %w", err)
	}
	if createSQL == "" || !strings.Contains(createSQL, prefsCategoryCheckOld) {
		// Already rewritten, or the column shape changed — nothing to do.
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
		`UPDATE sqlite_master SET sql = replace(sql, ?, ?) WHERE type='table' AND name='user_notification_prefs'`,
		prefsCategoryCheckOld, prefsCategoryCheckNew)
	if err != nil {
		return fmt.Errorf("rewrite user_notification_prefs category CHECK: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("rows affected: %w", err)
	} else if n != 1 {
		return fmt.Errorf("expected to rewrite exactly 1 sqlite_master row for user_notification_prefs, rewrote %d", n)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA schema_version = %d`, schemaVersion+1)); err != nil {
		return fmt.Errorf("bump schema_version: %w", err)
	}
	if logger != nil {
		logger.Info("widened user_notification_prefs.category CHECK to the taxonomy-v2 vocabulary",
			"categories", len(notify.AllCategories))
	}
	return nil
}

// remapPrefRows rewrites every stored preference cell onto the new
// vocabulary. A legacy category with two targets ('system') keeps the
// original row for the first target and inserts a sibling for the second, so
// the user's intent survives the split intact.
func remapPrefRows(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	type prefRow struct {
		id, workspaceID, userID, category, channelID, state string
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, workspace_id, user_id, category, channel_id, state FROM user_notification_prefs`)
	if err != nil {
		return fmt.Errorf("read notification prefs: %w", err)
	}
	var all []prefRow
	for rows.Next() {
		var p prefRow
		if err := rows.Scan(&p.id, &p.workspaceID, &p.userID, &p.category, &p.channelID, &p.state); err != nil {
			rows.Close()
			return fmt.Errorf("scan notification pref: %w", err)
		}
		all = append(all, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate notification prefs: %w", err)
	}
	rows.Close()

	updated, inserted := 0, 0
	for _, p := range all {
		if p.category == notify.CategoryMuteAll || notify.ValidCategory(p.category) {
			continue // sentinel, or already on the new vocabulary
		}
		targets, ok := notify.LegacyCategories[p.category]
		if !ok || len(targets) == 0 {
			// An unrecognised value predates or postdates both vocabularies.
			// Leave it: it can no longer match a live category so it is inert,
			// and deleting a user's row on a guess is worse than a dead cell.
			if logger != nil {
				logger.Warn("notification pref carries an unknown category; leaving it untouched",
					"id", p.id, "category", p.category)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE user_notification_prefs SET category = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
			targets[0], p.id); err != nil {
			return fmt.Errorf("remap pref %s (%s -> %s): %w", p.id, p.category, targets[0], err)
		}
		updated++
		for _, extra := range targets[1:] {
			// INSERT OR IGNORE: UNIQUE(user_id, category, channel_id) makes a
			// re-apply a no-op rather than a constraint failure.
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO user_notification_prefs
				    (id, workspace_id, user_id, category, channel_id, state)
				VALUES (?, ?, ?, ?, ?, ?)`,
				newMigrationID("pref"), p.workspaceID, p.userID, extra, p.channelID, p.state); err != nil {
				return fmt.Errorf("split pref %s (%s -> %s): %w", p.id, p.category, extra, err)
			}
			inserted++
		}
	}
	if logger != nil && (updated > 0 || inserted > 0) {
		logger.Info("remapped notification preference cells onto taxonomy v2",
			"updated", updated, "inserted", inserted)
	}
	return nil
}

// remapChannelAllowlists rewrites notification_channels.categories_json — the
// admin per-channel allowlist. An empty array means "every category" and is
// left alone; a non-empty one is remapped entry by entry and de-duplicated
// (two legacy names can now collapse onto the same new one).
func remapChannelAllowlists(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, categories_json FROM notification_channels WHERE categories_json IS NOT NULL AND categories_json NOT IN ('', '[]', 'null')`)
	if err != nil {
		return fmt.Errorf("read channel allowlists: %w", err)
	}
	type chRow struct{ id, raw string }
	var all []chRow
	for rows.Next() {
		var c chRow
		if err := rows.Scan(&c.id, &c.raw); err != nil {
			rows.Close()
			return fmt.Errorf("scan channel allowlist: %w", err)
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate channel allowlists: %w", err)
	}
	rows.Close()

	changed := 0
	for _, c := range all {
		var cats []string
		if err := json.Unmarshal([]byte(c.raw), &cats); err != nil {
			// Unparseable JSON in this column would already break the reader
			// (which ignores the error and treats it as "all categories").
			// Don't fail the migration on pre-existing corruption.
			if logger != nil {
				logger.Warn("channel allowlist is not a JSON array; leaving it untouched", "channel_id", c.id)
			}
			continue
		}
		seen := map[string]bool{}
		out := make([]string, 0, len(cats))
		for _, cat := range cats {
			targets := []string{cat}
			// Only remap a name that is NOT already a live category: the
			// legacy table lists self-mapping entries ('security', 'memory',
			// …) so it reads as a complete statement of the old vocabulary,
			// and following those would be a no-op anyway.
			if mapped, ok := notify.LegacyCategories[cat]; ok && !notify.ValidCategory(cat) {
				targets = mapped
			}
			for _, tgt := range targets {
				if !seen[tgt] {
					seen[tgt] = true
					out = append(out, tgt)
				}
			}
		}
		// Write only when something actually changed. Two legacy names can
		// collapse onto one new name, and a list can carry a pre-existing
		// duplicate, so compare the RESULT rather than tracking a "did we
		// remap" flag — the latter misses de-duplication.
		if equalStrings(cats, out) {
			continue
		}
		b, err := json.Marshal(out)
		if err != nil {
			return fmt.Errorf("marshal remapped allowlist for %s: %w", c.id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE notification_channels SET categories_json = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
			string(b), c.id); err != nil {
			return fmt.Errorf("update allowlist for %s: %w", c.id, err)
		}
		changed++
	}
	if logger != nil && changed > 0 {
		logger.Info("remapped per-channel category allowlists onto taxonomy v2", "channels", changed)
	}
	return nil
}

// equalStrings reports whether two slices hold the same values in the same
// order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newMigrationID mints an id for a row this migration creates. Migrations run
// before any application package is wired, so this does not reach into
// notifyroute's generator.
func newMigrationID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Deterministic fallback: the caller's INSERT OR IGNORE plus the
		// UNIQUE constraint keep a collision harmless.
		return prefix + "_v169fallback"
	}
	return prefix + "_" + hex.EncodeToString(b)
}
