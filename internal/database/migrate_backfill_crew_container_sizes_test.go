package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// backfillCrewContainerSizesVersion is the migration under test. Named here so
// the marker-clearing re-apply below cannot drift from the filename.
const backfillCrewContainerSizesVersion = 20260802155412

// A crew row written before #1641 can hold container_memory_mb = 0.
//
// The column is NOT NULL DEFAULT 4096, so nothing about the schema stops a 0
// landing in it — and `PATCH /crews/{id}` with container_memory_mb: 0 wrote
// exactly that, because the "0 means use the server default" sentinel was
// resolved on create and not on update. Downstream, every consumer asks
// `<= 0` and substitutes its own default: the docker provider's is 8192, i.e.
// TWICE what the create path, the docs and the schema column all promise
// (#1643). The same crew also gets a concurrency budget of 1 instead of 2,
// because computeCrewBudget divides the stored memory by agentMinMemoryMB and
// treats a non-positive value as "no answer".
//
// Both handlers resolve the sentinel now, so no new row can hold 0. This is
// about the rows that already do.
//
// Values are asserted as numbers, not as "positive" or "changed": the whole
// defect is that a plausible-looking fallback produced the WRONG number, and a
// backfill that landed on 8192 would satisfy any weaker predicate while
// preserving the bug it was written to remove.
func TestMigrateBackfillCrewContainerSizes(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "backfill-sizes.db"))
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
	// Four rows: the sentinel as the old update path wrote it, a stray
	// negative (validation rejects those now, but a row that predates the
	// check is the same class of defect and the same `<= 0` fallback), a crew
	// deliberately sized away from the default, and one already sitting on the
	// default.
	if _, err := db.Exec(`
INSERT INTO crews (id, workspace_id, name, slug, container_memory_mb, container_cpus)
VALUES ('crew_zero','ws1','Zero','zero', 0, 0),
       ('crew_negative','ws1','Neg','neg', -1, -0.5),
       ('crew_sized','ws1','Sized','sized', 2048, 1.5),
       ('crew_default','ws1','Def','def', 4096, 2.0)`); err != nil {
		t.Fatalf("seed crews: %v", err)
	}

	reapply := func() {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM _migrations WHERE version = ?`, backfillCrewContainerSizesVersion); err != nil {
			t.Fatalf("clear migration marker: %v", err)
		}
		if err := Migrate(ctx, db.DB, silent); err != nil {
			t.Fatalf("re-Migrate (backfill): %v", err)
		}
	}
	sizes := func(id string) (int, float64) {
		t.Helper()
		var mem int
		var cpus float64
		if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE id = ?`, id).Scan(&mem, &cpus); err != nil {
			t.Fatalf("read crew %s: %v", id, err)
		}
		return mem, cpus
	}

	reapply()

	for _, id := range []string{"crew_zero", "crew_negative"} {
		mem, cpus := sizes(id)
		if mem != 4096 {
			t.Errorf("%s: container_memory_mb = %d, want 4096 (the documented server default; 8192 is the provider fallback this migration exists to stop reaching)", id, mem)
		}
		if cpus != 2.0 {
			t.Errorf("%s: container_cpus = %g, want 2", id, cpus)
		}
	}
	// A crew that was sized on purpose must come through untouched — a
	// backfill that normalised every row to the default would pass an
	// "is it 4096 now" assertion on the two rows above and silently resize
	// every crew on the instance.
	if mem, cpus := sizes("crew_sized"); mem != 2048 || cpus != 1.5 {
		t.Errorf("crew_sized = %d MiB / %g CPUs, want 2048 / 1.5 — a deliberately sized crew was rewritten", mem, cpus)
	}
	if mem, cpus := sizes("crew_default"); mem != 4096 || cpus != 2.0 {
		t.Errorf("crew_default = %d MiB / %g CPUs, want 4096 / 2", mem, cpus)
	}

	// Idempotent: the migration re-runs on a restore whose ledger was rolled
	// back, and on any target that re-applies it. Running it against rows it
	// has already fixed must be a no-op, not a second rewrite.
	if _, err := db.Exec(`UPDATE crews SET container_memory_mb = 1024, container_cpus = 0.75 WHERE id = 'crew_sized'`); err != nil {
		t.Fatalf("resize crew_sized: %v", err)
	}
	reapply()
	if mem, cpus := sizes("crew_sized"); mem != 1024 || cpus != 0.75 {
		t.Errorf("crew_sized after re-apply = %d MiB / %g CPUs, want 1024 / 0.75 — the backfill is not idempotent", mem, cpus)
	}
}
