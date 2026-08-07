package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

// The `notifications` table was created in v42 for an in-app bell that was
// never wired: no code path outside a test ever ran `INSERT INTO notifications`
// in any released version, and there was never a create route. Five read/clear
// endpoints stood over a table that could not fill (#1751).
//
// These tests are the tripwire for the half-wired revival — a table with no
// writer is exactly what got shipped the first time. The companion guards are
// TestNotificationsSurfaceStaysRemoved (internal/api — no routes) and
// TestNotificationCommandStaysRemoved (cmd/crewship — no CLI group). Bringing
// any one layer back on its own now fails a test that names the missing half.

var dropNotificationsCounter atomic.Int64

// dropNotificationsVersion finds the migration by name in the merged registry.
// Hard-coding the timestamp would silently stop testing the thing it names the
// moment the file is renamed.
func dropNotificationsVersion() int {
	for _, m := range migrations {
		if m.name == "drop_dead_notifications" {
			return m.version
		}
	}
	return 0
}

func openDropNotificationsDB(t *testing.T) (*sql.DB, context.Context, *slog.Logger) {
	t.Helper()
	name := fmt.Sprintf("crewship-drop-notifications-%d", dropNotificationsCounter.Add(1))
	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil))
}

func tableExists(t *testing.T, db *sql.DB, ctx context.Context, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&found)
	switch err {
	case nil:
		return true
	case sql.ErrNoRows:
		return false
	default:
		t.Fatalf("query sqlite_master for %q: %v", name, err)
		return false
	}
}

// A fresh install must not carry the table at all. If someone re-adds a
// CREATE TABLE notifications in a later migration without a writer, this is
// what catches it.
func TestDropDeadNotifications_FreshInstallHasNoTable(t *testing.T) {
	db, ctx, logger := openDropNotificationsDB(t)
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if tableExists(t, db, ctx, "notifications") {
		t.Error("table `notifications` exists after a full migration — it is the dead " +
			"entity-scoped feed removed in #1751. If it is genuinely wanted again it needs a " +
			"writer, a create route and a backup classification, not just a CREATE TABLE.")
	}
	// The live outbound-notification tables share the prefix and must survive.
	for _, keep := range []string{"notification_channels", "user_notification_prefs", "notification_templates"} {
		if !tableExists(t, db, ctx, keep) {
			t.Errorf("table %q is missing — the drop took a live outbound-notification table with it", keep)
		}
	}
}

// The upgrade path: an instance that already ran v42 has the table (empty, but
// present). The migration must remove it there too, and must not fall over on
// a row — the CHECK-constrained shape is seeded here so a stray row from a
// hand-written INSERT cannot wedge an operator's upgrade.
func TestDropDeadNotifications_UpgradeDropsAnExistingTable(t *testing.T) {
	db, ctx, logger := openDropNotificationsDB(t)

	version := dropNotificationsVersion()
	if version == 0 {
		t.Fatal("no migration named `drop_dead_notifications` in the registry")
	}
	if err := applyMigrationsUpTo(ctx, db, version-1, logger); err != nil {
		t.Fatalf("migrate to the version before the drop: %v", err)
	}
	if !tableExists(t, db, ctx, "notifications") {
		t.Fatal("pre-drop schema has no `notifications` table — the fixture no longer " +
			"reproduces the upgrade this migration exists for")
	}

	mustExecCtx(t, db, ctx, `INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('u1','u@ex.com','U')`)
	mustExecCtx(t, db, ctx, `INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`)
	mustExecCtx(t, db, ctx, `INSERT INTO notifications
		(id, workspace_id, user_id, actor_type, actor_id, action, entity_type, entity_id, entity_title)
		VALUES ('n1','ws1','u1','system','sys','created','issue','iss-1','T')`)

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if tableExists(t, db, ctx, "notifications") {
		t.Error("`notifications` survived the upgrade — an existing install keeps a table " +
			"nothing reads and nothing writes")
	}
}
