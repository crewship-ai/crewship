package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// The upgrade path for onboarding_skipped_at.
//
// 20260822203500 added the column with no backfill, and
// OnboardingHandler.Status reads a NULL there as "this completion was
// interrupted, reopen it". On a fresh install that inference is sound. On an
// upgrade every pre-existing completion is NULL by construction, so any user
// whose workspace holds no agents right now — pressed Skip, or finished
// properly and later deleted their crews — is thrown back into the setup
// wizard, and Status PERSISTS the downgrade rather than merely rendering it.
//
// This drives the real migration against the real schema, the way
// migrate_v148_backfill_network_mode_test.go does.
func TestMigrateBackfillsOnboardingSkippedAt(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "skipped.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Three users as an upgraded database really holds them: one completed
	// before the column existed, one still mid-onboarding, and one already
	// carrying an explicit marker that must not be overwritten.
	if _, err := db.Exec(`
INSERT INTO users (id, email, onboarding_completed, onboarding_skipped_at, created_at, updated_at)
VALUES ('u_legacy',    'legacy@x.test',  1, NULL,                   '2026-01-01T00:00:00Z', '2026-02-02T00:00:00Z'),
       ('u_midflight', 'mid@x.test',     0, NULL,                   '2026-01-01T00:00:00Z', '2026-02-02T00:00:00Z'),
       ('u_marked',    'marked@x.test',  1, '2026-03-03T00:00:00Z', '2026-01-01T00:00:00Z', '2026-02-02T00:00:00Z')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260823190000`); err != nil {
		t.Fatalf("clear backfill marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate (backfill): %v", err)
	}

	read := func(id string) (completed int, skipped any) {
		if err := db.QueryRow(
			`SELECT onboarding_completed, onboarding_skipped_at FROM users WHERE id = ?`, id,
		).Scan(&completed, &skipped); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return
	}

	// The whole point: a legacy completion is no longer indistinguishable
	// from an interrupted one.
	if _, skipped := read("u_legacy"); skipped == nil {
		t.Error("a pre-column completion still has NULL skipped_at — Status will reopen their onboarding")
	}

	// A user who has NOT completed must stay untouched. Marking them would
	// suppress the reopen for someone whose onboarding really is unfinished,
	// which is the opposite failure.
	if completed, skipped := read("u_midflight"); completed != 0 || skipped != nil {
		t.Errorf("mid-flight user mutated: completed=%d skipped=%v, want 0 / NULL", completed, skipped)
	}

	// An existing marker is evidence about a real moment; the approximation
	// must never overwrite it.
	if _, skipped := read("u_marked"); skipped != "2026-03-03T00:00:00Z" {
		t.Errorf("existing marker overwritten: %v", skipped)
	}
}
