package database

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func led(v int, name string) LedgerEntry { return LedgerEntry{Version: v, Name: name} }

func TestPlanLedgerRepair(t *testing.T) {
	declared := []LedgerEntry{
		led(1, "init"),
		led(167, "journal_append_only_fks"),
		led(168, "rate_limit_overrides"),
		led(169, "account_setup_purpose"),
	}

	t.Run("the dev3 case: our migration renumbered out from under an applied database", func(t *testing.T) {
		// dev3 ran the branch, so it recorded account_setup_purpose as 168.
		// main then shipped rate_limit_overrides as 168 and ours moved to 169.
		applied := []LedgerEntry{led(1, "init"), led(167, "journal_append_only_fks"), led(168, "account_setup_purpose")}

		plan, err := planLedgerRepair(applied, declared)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(plan.Renumbers) != 1 {
			t.Fatalf("renumbers = %+v, want exactly one", plan.Renumbers)
		}
		got := plan.Renumbers[0]
		if got != (Renumber{Name: "account_setup_purpose", From: 168, To: 169}) {
			t.Errorf("renumber = %+v", got)
		}
		// And the operator is told that 168 then runs for real, which is the
		// whole point: the repair does not skip rate_limit_overrides.
		if len(plan.PendingAfter) != 1 || plan.PendingAfter[0].Name != "rate_limit_overrides" {
			t.Errorf("PendingAfter = %+v, want rate_limit_overrides", plan.PendingAfter)
		}
	})

	t.Run("a ledger that already agrees needs no repair", func(t *testing.T) {
		applied := []LedgerEntry{led(1, "init"), led(167, "journal_append_only_fks"), led(168, "rate_limit_overrides")}
		plan, err := planLedgerRepair(applied, declared)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if !plan.Empty() {
			t.Errorf("plan = %+v, want empty", plan)
		}
	})

	t.Run("refuses a migration this binary has never heard of", func(t *testing.T) {
		// The version-skew case: a newer Crewship migrated this database.
		// Renumbering cannot express what it did, so repairing is a lie.
		applied := []LedgerEntry{led(1, "init"), led(200, "something_from_the_future")}
		_, err := planLedgerRepair(applied, declared)
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		for _, want := range []string{"does not declare it", "restore-snapshot"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err, want)
			}
		}
	})

	t.Run("refuses a plan that would put two migrations on one version", func(t *testing.T) {
		// Both applied rows want to land on 169.
		declaredDup := []LedgerEntry{led(169, "a"), led(169, "b")}
		applied := []LedgerEntry{led(10, "a"), led(11, "b")}
		_, err := planLedgerRepair(applied, declaredDup)
		if err == nil || !strings.Contains(err.Error(), "would both end up at version 169") {
			t.Fatalf("err = %v, want a collision refusal", err)
		}
	})

	t.Run("handles two migrations swapping version numbers", func(t *testing.T) {
		declaredSwap := []LedgerEntry{led(10, "b"), led(11, "a")}
		applied := []LedgerEntry{led(10, "a"), led(11, "b")}
		plan, err := planLedgerRepair(applied, declaredSwap)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(plan.Renumbers) != 2 {
			t.Fatalf("renumbers = %+v, want two", plan.Renumbers)
		}
	})
}

// The planner is pure; this exercises the write half against a real SQLite
// file, including the collision the naive UPDATE would hit.
func TestApplyLedgerRepair(t *testing.T) {
	newLedgerDB := func(t *testing.T, rows ...LedgerEntry) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ledger.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`CREATE TABLE _migrations (
			version INTEGER PRIMARY KEY, name TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
			t.Fatalf("create: %v", err)
		}
		for _, r := range rows {
			if _, err := db.Exec(`INSERT INTO _migrations (version, name) VALUES (?, ?)`, r.Version, r.Name); err != nil {
				t.Fatalf("seed %v: %v", r, err)
			}
		}
		return db
	}

	ctx := context.Background()

	t.Run("moves the row and leaves the target version free to apply", func(t *testing.T) {
		db := newLedgerDB(t, led(167, "journal_append_only_fks"), led(168, "account_setup_purpose"))
		plan := RepairPlan{Renumbers: []Renumber{{Name: "account_setup_purpose", From: 168, To: 169}}}

		if err := ApplyLedgerRepair(ctx, db, plan); err != nil {
			t.Fatalf("apply: %v", err)
		}

		after, err := ReadLedger(ctx, db)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if len(after) != 2 || after[1].Version != 169 || after[1].Name != "account_setup_purpose" {
			t.Fatalf("ledger = %+v", after)
		}
		// 168 must now be free — otherwise rate_limit_overrides would be
		// skipped and the schema really would fork.
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version = 168`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Errorf("version 168 still occupied after the repair")
		}
	})

	t.Run("a swap does not trip the primary key", func(t *testing.T) {
		// A direct UPDATE would hit UNIQUE on the first move. This is what
		// the negative parking pass is for.
		db := newLedgerDB(t, led(10, "a"), led(11, "b"))
		plan := RepairPlan{Renumbers: []Renumber{
			{Name: "a", From: 10, To: 11},
			{Name: "b", From: 11, To: 10},
		}}
		if err := ApplyLedgerRepair(ctx, db, plan); err != nil {
			t.Fatalf("apply: %v", err)
		}
		after, _ := ReadLedger(ctx, db)
		if len(after) != 2 || after[0].Name != "b" || after[1].Name != "a" {
			t.Errorf("ledger = %+v, want b@10 and a@11", after)
		}
	})

	t.Run("refuses when the ledger moved since planning", func(t *testing.T) {
		db := newLedgerDB(t, led(168, "something_else"))
		plan := RepairPlan{Renumbers: []Renumber{{Name: "account_setup_purpose", From: 168, To: 169}}}

		err := ApplyLedgerRepair(ctx, db, plan)
		if !errors.Is(err, ErrLedgerChanged) {
			t.Fatalf("err = %v, want ErrLedgerChanged", err)
		}
		// And nothing was written.
		after, _ := ReadLedger(ctx, db)
		if len(after) != 1 || after[0].Version != 168 || after[0].Name != "something_else" {
			t.Errorf("ledger was modified despite the refusal: %+v", after)
		}
	})
}

// End-to-end: a database in the exact state dev3 was in must boot after the
// repair. This is the claim the whole command rests on, so it is checked
// against the real Migrate rather than a stand-in.
func TestRepairThenMigrate_RecoversARenumberedDatabase(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8)) // 64 hex chars, as v152 expects

	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dev3.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	logger := quietLogger()
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("initial migrate: %v", err)
	}

	// Recreate dev3's ledger exactly: the last migration was authored under
	// the previous one's number, so it sits at `other.version` and nothing
	// occupies its declared version.
	victim := migrations[len(migrations)-1] // authored as, and renumbered from…
	other := migrations[len(migrations)-2]  // …this version, which main then took
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = ?`, other.version); err != nil {
		t.Fatalf("stage collision: %v", err)
	}
	if _, err := db.Exec(`UPDATE _migrations SET version = ? WHERE version = ?`,
		other.version, victim.version); err != nil {
		t.Fatalf("stage collision: %v", err)
	}

	if err := Migrate(ctx, db, logger); err == nil {
		t.Fatal("Migrate accepted a colliding ledger — the guard this repair exists for is gone")
	} else if !strings.Contains(err.Error(), "collision") {
		t.Fatalf("unexpected failure: %v", err)
	}

	applied, err := ReadLedger(ctx, db)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	plan, err := PlanLedgerRepair(applied)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Empty() {
		t.Fatal("plan is empty for a ledger Migrate just rejected")
	}
	if err := ApplyLedgerRepair(ctx, db, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("Migrate still refuses after the repair: %v", err)
	}
}

// ApplyLedgerRepair is exported, so a caller can hand it a plan the planner
// would never produce. And even from the planner, the re-check and the writes
// are separate statements. A move that matches nothing must be an error, not a
// silent no-op followed by "Ledger repaired".
func TestApplyLedgerRepair_RefusesAMoveThatChangesNothing(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "noop.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO _migrations (version, name) VALUES (168, 'real')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A plan with two moves off the SAME source row. The re-check passes for
	// both (the row is there, the name matches), the first park consumes it,
	// and the second move then matches nothing — silently, while the repair
	// still commits and reports success.
	//
	// The planner cannot produce this, but ApplyLedgerRepair is exported and a
	// caller can. The simpler "source row is absent" case is already caught by
	// the re-check above the writes, verified separately.
	plan := RepairPlan{Renumbers: []Renumber{
		{Name: "real", From: 168, To: 169},
		{Name: "real", From: 168, To: 170},
	}}
	if err := ApplyLedgerRepair(context.Background(), db, plan); err == nil {
		t.Fatal("want an error for a renumber that matches no row")
	}

	// And the genuine one must not have been left half-applied.
	var version int
	if err := db.QueryRow(`SELECT version FROM _migrations WHERE name = 'real'`).Scan(&version); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if version != 168 {
		t.Errorf("version = %d, want 168 — the failed repair should have rolled back entirely", version)
	}
}

// The plainly-absent case, kept separate so it is clear which guard catches
// which: this one is the re-check, before any write happens.
func TestApplyLedgerRepair_RefusesAMoveForARowThatIsNotThere(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "absent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("create: %v", err)
	}

	plan := RepairPlan{Renumbers: []Renumber{{Name: "ghost", From: 500, To: 501}}}
	if err := ApplyLedgerRepair(context.Background(), db, plan); !errors.Is(err, ErrLedgerChanged) {
		t.Fatalf("err = %v, want ErrLedgerChanged", err)
	}
}
