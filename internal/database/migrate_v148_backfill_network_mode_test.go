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

// TestMigrateV148_NewInsertDefaultsRestricted is the fail-safe-by-construction
// proof: after v148 a raw INSERT that OMITS network_mode lands on 'restricted' —
// no writer (app path, seed, later migration, hand-written SQL) has to remember
// to set it. Red on the backfill-only branch (default still 'free'); green after
// the writable_schema DEFAULT flip. Also asserts the flip is idempotent on
// re-apply so re-running the migration doesn't trip the "rewrote 0 rows" guard.
func TestMigrateV148_NewInsertDefaultsRestricted(t *testing.T) {
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
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','Work','work')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// The load-bearing assertion: omit network_mode entirely.
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c_raw','ws1','Raw','raw')`); err != nil {
		t.Fatalf("insert crew omitting network_mode: %v", err)
	}
	var mode string
	if err := db.QueryRow(`SELECT network_mode FROM crews WHERE id = 'c_raw'`).Scan(&mode); err != nil {
		t.Fatalf("read: %v", err)
	}
	if mode != "restricted" {
		t.Errorf("raw insert omitting network_mode defaulted to %q, want restricted (fail-safe)", mode)
	}

	// The declared column default reflects the flip.
	var def any
	if err := db.QueryRow(`SELECT dflt_value FROM pragma_table_info('crews') WHERE name = 'network_mode'`).Scan(&def); err != nil {
		t.Fatalf("table_info: %v", err)
	}
	if got, _ := def.(string); got != "'restricted'" {
		t.Errorf("crews.network_mode default = %v, want 'restricted'", def)
	}

	// The CHECK constraint must survive the DEFAULT rewrite — both values still valid.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode) VALUES ('c_free','ws1','F','f','free')`); err != nil {
		t.Errorf("CHECK constraint corrupted by the DEFAULT rewrite (explicit 'free' rejected): %v", err)
	}

	// Idempotent re-apply: clearing the marker and re-migrating must not error
	// (the strings.Contains skip handles the already-flipped schema).
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 148`); err != nil {
		t.Fatalf("clear v148 marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-apply v148 after flip must be idempotent: %v", err)
	}
	if err := db.QueryRow(`SELECT dflt_value FROM pragma_table_info('crews') WHERE name = 'network_mode'`).Scan(&def); err != nil {
		t.Fatalf("table_info after re-apply: %v", err)
	}
	if got, _ := def.(string); got != "'restricted'" {
		t.Errorf("default after idempotent re-apply = %v, want 'restricted'", def)
	}
}
