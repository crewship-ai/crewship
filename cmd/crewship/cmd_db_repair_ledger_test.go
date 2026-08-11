//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// stageRenumberedLedger builds a database in the state that took dev3 down on
// 2026-07-27: the newest migration recorded under the PREVIOUS migration's
// version, and that version's real occupant missing. That is what a machine
// looks like after it ran a feature branch whose migration was renumbered
// before the branch merged.
func stageRenumberedLedger(t *testing.T) (dataDir string, from, to int) {
	t.Helper()
	// v152 derives the journal chain key from this; without it the chain
	// seeds from "" and later verification fails for unrelated reasons.
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))

	dataDir = t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dataDir)
	// The guard no longer reads this port — it locks the database file — but
	// pin it anyway so that if a port probe is ever reintroduced, these tests
	// fail on their own terms rather than by noticing a dev slot on :8080.
	t.Setenv("CREWSHIP_PORT", "59237")

	dd, err := database.DefaultDataDir()
	if err != nil {
		t.Fatalf("data dir: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Learn the last two migrations from a throwaway database at head. We need
	// the NAME of the one to leave out before building the real fixture, and
	// the registry is unexported.
	var fromName string
	func() {
		scratch, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "scratch.db"))
		if err != nil {
			t.Fatalf("open scratch: %v", err)
		}
		defer scratch.Close()
		if err := database.Migrate(context.Background(), scratch, quiet); err != nil {
			t.Fatalf("migrate scratch: %v", err)
		}
		led, err := database.ReadLedger(context.Background(), scratch)
		if err != nil {
			t.Fatalf("read ledger: %v", err)
		}
		if len(led) < 2 {
			t.Fatalf("need at least two migrations, got %d", len(led))
		}
		to = led[len(led)-1].Version // where the newest migration belongs
		from = led[len(led)-2].Version
		fromName = led[len(led)-2].Name // …and this one had not merged yet
	}()

	db, err := sql.Open("sqlite", dd.DatabasePath())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// MigrateSkipping, not "migrate to head then delete a ledger row": on dev3
	// the migration that took the branch's number had never run, and deleting
	// its row while leaving its schema change in place would make the final
	// Migrate — the one that applies it for real — fail on an already-existing
	// column, blaming the repair for something the fixture did.
	if err := database.MigrateSkipping(context.Background(), db, quiet, fromName); err != nil {
		t.Fatalf("migrate (without %s): %v", fromName, err)
	}
	if _, err := db.Exec(`UPDATE _migrations SET version = ? WHERE version = ?`, from, to); err != nil {
		t.Fatalf("stage: %v", err)
	}
	return dataDir, from, to
}

// stagedDBPath is the database stageRenumberedLedger built.
func stagedDBPath(t *testing.T) string {
	t.Helper()
	dd, err := database.DefaultDataDir()
	if err != nil {
		t.Fatalf("data dir: %v", err)
	}
	return dd.DatabasePath()
}

// withStagedDB opens the staged database, runs fn, and CLOSES the handle
// before returning.
//
// Closing matters now that repair-ledger refuses to write while anything else
// holds the file open: an assertion helper that parked a *sql.DB in t.Cleanup
// left a pooled connection alive for the rest of the test, and the command
// would — correctly — refuse the repair because of the test's own handle. The
// guard cannot tell a leftover test connection from a crewshipd, and it should
// not try to; the fix belongs here.
func withStagedDB(t *testing.T, fn func(db *sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", stagedDBPath(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close staged db: %v", err)
		}
	}()
	fn(db)
}

// readStagedLedger reads the ledger and lets go of the database.
func readStagedLedger(t *testing.T) []database.LedgerEntry {
	t.Helper()
	var led []database.LedgerEntry
	withStagedDB(t, func(db *sql.DB) {
		var err error
		if led, err = database.ReadLedger(context.Background(), db); err != nil {
			t.Fatalf("read ledger: %v", err)
		}
	})
	return led
}

// migrateStaged runs migrations against the staged database and lets go of it,
// returning what Migrate said.
func migrateStaged(t *testing.T) error {
	t.Helper()
	var err error
	withStagedDB(t, func(db *sql.DB) {
		err = database.Migrate(context.Background(), db,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return err
}

func TestRepairLedger_DryRunShowsThePlanAndChangesNothing(t *testing.T) {
	_, from, to := stageRenumberedLedger(t)

	repairLedgerDryRun, repairLedgerYes = true, false
	t.Cleanup(func() { repairLedgerDryRun, repairLedgerYes = false, false })

	var runErr error
	out := captureStdoutCovCli2(t, func() {
		runErr = repairLedgerCmd.RunE(newFlagCmd(nil, nil), nil)
	})
	if runErr != nil {
		t.Fatalf("RunE: %v\noutput:\n%s", runErr, out)
	}

	if !strings.Contains(out, "Dry run") {
		t.Errorf("output should say it changed nothing:\n%s", out)
	}
	if !strings.Contains(out, "move") {
		t.Errorf("output should show the move:\n%s", out)
	}

	// The ledger is untouched: still the wrong number.
	led := readStagedLedger(t)
	if got := led[len(led)-1].Version; got != from {
		t.Errorf("highest applied version = %d, want %d (dry run must not write)", got, from)
	}
	_ = to
}

func TestRepairLedger_RepairsAndLetsTheDatabaseBoot(t *testing.T) {
	_, from, to := stageRenumberedLedger(t)

	// Precondition: this database really is unbootable. Without it the test
	// could pass against a database that never needed repairing.
	if err := migrateStaged(t); err == nil {
		t.Fatal("staged database migrated cleanly — the collision was not reproduced")
	} else if !strings.Contains(err.Error(), "collision") {
		t.Fatalf("staged database failed for the wrong reason: %v", err)
	}

	repairLedgerDryRun, repairLedgerYes = false, true
	t.Cleanup(func() { repairLedgerDryRun, repairLedgerYes = false, false })

	var runErr error
	out := captureStdoutCovCli2(t, func() {
		runErr = repairLedgerCmd.RunE(newFlagCmd(nil, nil), nil)
	})
	if runErr != nil {
		t.Fatalf("RunE: %v\noutput:\n%s", runErr, out)
	}
	if !strings.Contains(out, "Ledger repaired") {
		t.Errorf("output:\n%s", out)
	}

	led := readStagedLedger(t)
	if got := led[len(led)-1].Version; got != to {
		t.Errorf("highest applied version = %d, want %d", got, to)
	}

	// The point of the exercise: the collision is gone, and the migration
	// whose number was freed is queued to run rather than being skipped.
	//
	// Migrate must return nil, not merely stop complaining about a collision.
	// That is only assertable because staging goes through MigrateSkipping,
	// which produces a database on which the freed migration genuinely never
	// ran. The weaker "no collision in the error" reading was needed while the
	// fixture staged itself by migrating to head and deleting a ledger row —
	// there the freed migration's SQL HAD run, so the final Migrate re-applied
	// it, and that only worked while it happened to be idempotent. The first
	// `ALTER TABLE ... ADD COLUMN` to land in that slot failed the test for a
	// reason that had nothing to do with the repair.
	if err := migrateStaged(t); err != nil {
		t.Fatalf("migrate after repair: %v", err)
	}
	if !strings.Contains(out, "apply") {
		t.Errorf("output should list the freed version as pending, got:\n%s", out)
	}
	if !strings.Contains(out, "v"+strconv.Itoa(from)) {
		t.Errorf("output should name the freed version v%d, got:\n%s", from, out)
	}
}

func TestRepairLedger_SaysSoWhenThereIsNothingToDo(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)
	t.Setenv("CREWSHIP_PORT", "59237")

	db, err := sql.Open("sqlite", filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	dd, _ := database.DefaultDataDir()
	_ = db.Close()
	db, err = sql.Open("sqlite", dd.DatabasePath())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := database.Migrate(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	repairLedgerDryRun, repairLedgerYes = false, true
	t.Cleanup(func() { repairLedgerDryRun, repairLedgerYes = false, false })

	var runErr error
	out := captureStdoutCovCli2(t, func() {
		runErr = repairLedgerCmd.RunE(newFlagCmd(nil, nil), nil)
	})
	if runErr != nil {
		t.Fatalf("RunE: %v\noutput:\n%s", runErr, out)
	}
	if !strings.Contains(out, "already agrees") {
		t.Errorf("a healthy ledger should be reported as such:\n%s", out)
	}
}

// TestRepairLedger_DatabaseInUseGuard pins the write guard to the file being
// rewritten rather than to an HTTP port.
//
// repair-ledger mutates the _migrations ledger, and used to gate that on
// http://localhost:$CREWSHIP_PORT/api/health — a probe with no connection to
// the database path, wrong in both directions:
//
//   - unrelated instance answering on the probed port: the repair was refused
//     on a database nobody had open (false block);
//   - crewshipd on any other port, holding the database: the repair rewrote
//     the ledger under a booted server (false pass, the dangerous one).
//
// The --dry-run subtest guards the property the guard's placement exists for:
// read-only inspection must keep working while the server is up, which is why
// the check sits between the plan and the apply and not at the top of RunE.
func TestRepairLedger_DatabaseInUseGuard(t *testing.T) {
	tests := []struct {
		name string
		// holdDatabaseOpen simulates a live crewshipd: a WAL connection to
		// the staged database that stays open and idle.
		holdDatabaseOpen bool
		// healthServer answers on the port the old guard probed, sharing
		// nothing with the staged database.
		healthServer bool
		dryRun       bool
		wantRefused  bool
		wantOutput   string
		// wantRepaired: did the ledger actually move to the version this
		// binary declares?
		wantRepaired bool
	}{
		{
			name:             "database held open by another connection: refuse",
			holdDatabaseOpen: true,
			wantRefused:      true,
			wantRepaired:     false,
		},
		{
			name:         "unrelated server on the probed port, database free: repair",
			healthServer: true,
			wantRefused:  false,
			wantOutput:   "Ledger repaired",
			wantRepaired: true,
		},
		{
			name:             "--dry-run still works while the database is held open",
			holdDatabaseOpen: true,
			dryRun:           true,
			wantRefused:      false,
			wantOutput:       "Dry run",
			wantRepaired:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, from, to := stageRenumberedLedger(t)

			// After staging: stageRenumberedLedger pins CREWSHIP_PORT itself.
			if tt.healthServer {
				serveHealthOn(t)
			} else {
				pinDeadPort(t)
			}

			if tt.holdDatabaseOpen {
				held, err := database.Open("file:" + stagedDBPath(t))
				if err != nil {
					t.Fatalf("hold db open: %v", err)
				}
				t.Cleanup(func() { _ = held.Close() })
				// One read, then idle — which is the state a live crewshipd
				// is in almost all the time, and the state the guard has to
				// catch. The read is not decoration: measured, a connection
				// that has opened and executed NOTHING does not yet have the
				// WAL index (-shm) mapped when it had to convert the file
				// from rollback-journal mode on connect, and holds no lock
				// for anything to see. One statement materialises the index
				// and the holder becomes visible. stageRenumberedLedger
				// builds its fixture with a plain sql.Open, so it lands in
				// exactly that rollback-mode starting state; a real
				// crewshipd has run its migrations long before an operator
				// gets to repair-ledger.
				var n int
				if err := held.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&n); err != nil {
					t.Fatalf("holder query: %v", err)
				}
			}

			repairLedgerDryRun, repairLedgerYes = tt.dryRun, true
			t.Cleanup(func() { repairLedgerDryRun, repairLedgerYes = false, false })

			var runErr error
			out := captureStdoutCovCli2(t, func() {
				runErr = repairLedgerCmd.RunE(newFlagCmd(nil, nil), nil)
			})

			switch {
			case tt.wantRefused && runErr == nil:
				t.Fatalf("repair was allowed, want refusal; output:\n%s", out)
			case !tt.wantRefused && runErr != nil:
				t.Fatalf("repair refused (%v), want it to proceed; output:\n%s", runErr, out)
			}
			if tt.wantRefused {
				// Name the file at risk. A URL tells the operator nothing
				// about which database is about to be rewritten.
				if !strings.Contains(runErr.Error(), "crewship.db") {
					t.Errorf("error %q does not name the database file", runErr)
				}
				if strings.Contains(runErr.Error(), "http://") {
					t.Errorf("error points at a URL instead of the database file: %v", runErr)
				}
			}
			if tt.wantOutput != "" && !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output should contain %q:\n%s", tt.wantOutput, out)
			}

			// The ledger is the fact of the matter: either the renumber
			// happened or it did not.
			led := readStagedLedger(t)
			got := led[len(led)-1].Version
			want := from
			if tt.wantRepaired {
				want = to
			}
			if got != want {
				t.Errorf("highest applied version = %d, want %d (repair %s)", got, want,
					map[bool]string{true: "should have run", false: "should NOT have run"}[tt.wantRepaired])
			}
		})
	}
}

// An operator running this on a broken box may well have the wrong data
// directory. "no such table: _migrations" is a database internal, not an
// answer, so the command has to recognise "there is no Crewship database
// here" and say that instead.
func TestRepairLedger_ExplainsAnEmptyDataDir(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_PORT", "59237")

	repairLedgerDryRun, repairLedgerYes = true, false
	t.Cleanup(func() { repairLedgerDryRun, repairLedgerYes = false, false })

	var runErr error
	out := captureStdoutCovCli2(t, func() {
		runErr = repairLedgerCmd.RunE(newFlagCmd(nil, nil), nil)
	})
	if runErr == nil {
		t.Fatalf("want an error for a directory with no database\noutput:\n%s", out)
	}
	msg := runErr.Error()
	if strings.Contains(msg, "no such table") {
		t.Errorf("raw SQLite error leaked to the operator: %q", msg)
	}
	for _, want := range []string{"no Crewship database", "CREWSHIP_DATA_DIR"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
}
