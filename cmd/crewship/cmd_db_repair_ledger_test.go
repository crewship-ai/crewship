//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
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
	// localServerRunning() probes this port. Pin it somewhere nothing
	// listens, so a dev slot on :8080 cannot make the test flap.
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

func openStagedDB(t *testing.T) *sql.DB {
	t.Helper()
	dd, err := database.DefaultDataDir()
	if err != nil {
		t.Fatalf("data dir: %v", err)
	}
	db, err := sql.Open("sqlite", dd.DatabasePath())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
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
	led, err := database.ReadLedger(context.Background(), openStagedDB(t))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if got := led[len(led)-1].Version; got != from {
		t.Errorf("highest applied version = %d, want %d (dry run must not write)", got, from)
	}
	_ = to
}

func TestRepairLedger_RepairsAndLetsTheDatabaseBoot(t *testing.T) {
	_, _, to := stageRenumberedLedger(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Precondition: this database really is unbootable. Without it the test
	// could pass against a database that never needed repairing.
	if err := database.Migrate(context.Background(), openStagedDB(t), quiet); err == nil {
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

	led, err := database.ReadLedger(context.Background(), openStagedDB(t))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if got := led[len(led)-1].Version; got != to {
		t.Errorf("highest applied version = %d, want %d", got, to)
	}

	// The point of the exercise: it boots now, and the migration whose
	// number was freed actually runs rather than being skipped.
	if err := database.Migrate(context.Background(), openStagedDB(t), quiet); err != nil {
		t.Fatalf("still refuses to migrate after the repair: %v", err)
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
