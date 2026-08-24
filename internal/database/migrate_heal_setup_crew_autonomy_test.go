package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// The upgrade path for the Crewship Guide's autonomy level.
//
// setupCrewAutonomyLevel (internal/api/onboarding_setup_crew.go) moved from
// 'strict' to 'full'. ensureOnboardingSetupCrew creates the crew with INSERT
// OR IGNORE, which only ever applies a constant to a brand-new row, so a
// workspace set up by an older build keeps the stale value unless something
// raises it.
//
// That something used to be an UPDATE on the request path, guarded with
// `AND autonomy_level = 'strict'` so it would "only heal the stale default,
// never an operator's choice". It could not: crew_policy.go writes this same
// column, so an operator's `--level strict` is the same four bytes as the old
// default — and Status calls ensureOnboardingSetupCrew on every poll for any
// workspace holding a credential, onboarding finished or not. The heal was a
// standing re-escalation of the only full-autonomy crew in the workspace.
//
// Here instead, where "once per database" is what the mechanism actually
// provides. Drives the real migration against the real schema, the way
// migrate_backfill_onboarding_skipped_at_test.go does.
func TestMigrateHealsStaleSetupCrewAutonomy(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "healautonomy.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`
INSERT INTO workspaces (id, name, slug) VALUES
  ('ws1', 'One', 'one'), ('ws2', 'Two', 'two'), ('ws3', 'Three', 'three')`); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}

	// Four crews as an upgraded database really holds them: the stale setup
	// crew the migration is for, a setup crew an operator has since moved
	// somewhere else, one already at the current value, and an ordinary crew
	// that happens to be strict and must not be touched at all.
	if _, err := db.Exec(`
INSERT INTO crews (id, workspace_id, name, slug, kind, autonomy_level) VALUES
  ('cr_stale',    'ws1', 'Crewship Guide', '_crewship-setup', 'setup',    'strict'),
  ('cr_operator', 'ws2', 'Crewship Guide', '_crewship-setup', 'setup',    'guided'),
  ('cr_current',  'ws3', 'Crewship Guide', '_crewship-setup', 'setup',    'full'),
  ('cr_ordinary', 'ws1', 'Engineering',    'eng',             'standard', 'strict')`); err != nil {
		t.Fatalf("seed crews: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260824073500`); err != nil {
		t.Fatalf("clear heal marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate (heal): %v", err)
	}

	read := func(id string) string {
		var level string
		if err := db.QueryRow(`SELECT autonomy_level FROM crews WHERE id = ?`, id).Scan(&level); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return level
	}

	for _, tc := range []struct {
		id, want, why string
	}{
		{"cr_stale", "full", "the stale old default is what this migration exists to raise"},
		{"cr_operator", "guided", "an operator's own level is not a stale default"},
		{"cr_current", "full", "already current; nothing to do"},
		{"cr_ordinary", "strict", "an ordinary crew is out of scope even at the matching level"},
	} {
		if got := read(tc.id); got != tc.want {
			t.Errorf("%s: autonomy_level = %q, want %q — %s", tc.id, got, tc.want, tc.why)
		}
	}
}
