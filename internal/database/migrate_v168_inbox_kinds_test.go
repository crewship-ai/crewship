package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// openMigratedDB brings a fresh database up to head and returns it.
func openMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "inbox_kinds.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db.DB
}

// TestInboxKindsMatchSchema is the totality guard: every kind the product
// writes (inbox.AllKinds) must be admitted by the real inbox_items.kind
// CHECK constraint.
//
// This pins the root cause of the #1405 circuit-breaker alert never
// arriving. internal/pipeline/schedules.go wrote the bare literal
// "schedule_circuit_breaker_tripped", which no migration had ever added to
// the CHECK. inbox.Insert logs its error rather than returning it to a
// user-visible path, so in production the insert failed the constraint and
// the "your routine was auto-disabled after N straight failures" alert was
// silently dropped — the single most important thing to know about a
// routine that stopped running.
//
// The existing unit test for that path (internal/pipeline,
// schedules_circuit_breaker_test.go) passes green because its rig builds a
// hand-rolled inbox_items table with NO CHECK constraint. It proves Insert
// was CALLED, not that it SUCCEEDED. This test closes that gap by using the
// real migrated schema, so the two can never diverge again.
func TestInboxKindsMatchSchema(t *testing.T) {
	db := openMigratedDB(t)
	ctx := context.Background()

	// A real workspace row: inbox_items.workspace_id is an FK, so without
	// one every insert fails on the FK and would mask a CHECK failure.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, created_at, updated_at)
		 VALUES ('ws_kinds', 'Kinds', 'kinds', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	for _, kind := range inbox.AllKinds {
		t.Run(kind, func(t *testing.T) {
			_, err := db.ExecContext(ctx, `
				INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, priority, created_at, updated_at)
				VALUES (?, 'ws_kinds', ?, ?, 'probe', 'unread', 'medium',
				        '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`,
				"ibx_probe_"+kind, kind, "src_"+kind)
			if err != nil {
				if strings.Contains(err.Error(), "CHECK constraint failed") {
					t.Fatalf("kind %q is written by the product but rejected by the inbox_items.kind CHECK — "+
						"the alert it carries reaches nobody. Widen the CHECK in a migration.\n  %v", kind, err)
				}
				t.Fatalf("insert kind %q: %v", kind, err)
			}
		})
	}
}

// TestInboxKindCheckRejectsUnknown guards the other direction: the CHECK
// must stay a real constraint, not be widened into a free-text column. A
// typo'd kind should still be rejected loudly.
func TestInboxKindCheckRejectsUnknown(t *testing.T) {
	db := openMigratedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, created_at, updated_at)
		 VALUES ('ws_unknown', 'U', 'u', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, priority, created_at, updated_at)
		VALUES ('ibx_bogus', 'ws_unknown', 'not_a_real_kind', 'src', 'probe', 'unread', 'medium',
		        '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`)
	if err == nil {
		t.Fatal("an unknown inbox kind was accepted — the CHECK constraint is gone")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("expected a CHECK failure for an unknown kind, got: %v", err)
	}
}
