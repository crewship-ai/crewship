package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV148_BackfillsLegacyFreeRows proves the #1366 backfill: a
// grandfathered crew stored as network_mode='free' becomes 'restricted', while
// a crew that was explicitly 'restricted' (with its allowed_domains) is left
// untouched. We run the real migration body against the real schema, seed the
// rows, then re-apply v148 (idempotent) by clearing its _migrations marker —
// which also proves re-running the backfill is safe.
func TestMigrateV148_BackfillsLegacyFreeRows(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "v148.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// A workspace to satisfy the crews FK.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','Work','work')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	// A grandfathered 'free' crew (as a pre-v18 backfill / seed drift would
	// have left it) and an intentional 'restricted' crew with its domains.
	if _, err := db.Exec(`
INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
VALUES ('crew_free','ws1','Legacy','legacy','free', NULL),
       ('crew_restricted','ws1','Locked','locked','restricted','["api.partner.com"]')`); err != nil {
		t.Fatalf("seed crews: %v", err)
	}

	// Re-apply v148 against the seeded rows.
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 148`); err != nil {
		t.Fatalf("clear v148 marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate (v148 backfill): %v", err)
	}

	get := func(id string) (mode string, domains any) {
		if err := db.QueryRow(`SELECT network_mode, allowed_domains FROM crews WHERE id = ?`, id).Scan(&mode, &domains); err != nil {
			t.Fatalf("read crew %s: %v", id, err)
		}
		return
	}

	if mode, _ := get("crew_free"); mode != "restricted" {
		t.Errorf("legacy free crew: network_mode = %q, want restricted (backfill)", mode)
	}
	// The restricted crew must be untouched — same mode, domains preserved.
	if mode, domains := get("crew_restricted"); mode != "restricted" || domains != `["api.partner.com"]` {
		t.Errorf("restricted crew mutated: mode=%q domains=%v, want restricted + preserved domains", mode, domains)
	}
}

// TestMigrateV148_LeavesColumnDefaultAsFree documents the deliberate scope
// boundary: v148 backfills existing rows but does NOT rebuild the crews table
// to flip the column DEFAULT (see the migration's doc comment for why). If a
// future change intentionally flips the default, update this test alongside it.
func TestMigrateV148_LeavesColumnDefaultAsFree(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "v148def.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var def any
	if err := db.QueryRow(`SELECT dflt_value FROM pragma_table_info('crews') WHERE name = 'network_mode'`).Scan(&def); err != nil {
		t.Fatalf("table_info: %v", err)
	}
	// dflt_value is stored quoted in the schema, e.g. 'free'.
	if got, ok := def.(string); !ok || got != "'free'" {
		t.Errorf("crews.network_mode default = %v, want 'free' (v148 is backfill-only, no rebuild)", def)
	}
}
