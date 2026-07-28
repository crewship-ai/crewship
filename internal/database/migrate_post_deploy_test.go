package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// postDeployFixture builds a small database with a table to backfill and a
// synthetic post-deploy migration over it, so the runner is tested without
// waiting for a real one to exist.
func postDeployFixture(t *testing.T, rows int, sqlText string) (*sql.DB, migration) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "pd.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, label TEXT)`); err != nil {
		t.Fatalf("create widgets: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < rows; i++ {
		if _, err := tx.Exec(`INSERT INTO widgets (id, label) VALUES (?, NULL)`, i); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	return db, migration{
		version:    20260801000000,
		name:       "backfill_widget_label",
		sql:        sqlText,
		postDeploy: true,
	}
}

// The canonical shape: bounded, and excludes what it has already done.
const convergingBackfill = `UPDATE widgets SET label = 'filled'
	WHERE id IN (SELECT id FROM widgets WHERE label IS NULL LIMIT 500)`

func pdLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func countFilled(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM widgets WHERE label = 'filled'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestPostDeploy_BackfillsInBatchesAndRecordsOnlyWhenDone(t *testing.T) {
	const rows = 1750 // deliberately not a multiple of the batch size
	db, m := postDeployFixture(t, rows, convergingBackfill)
	ctx := context.Background()

	if err := runOnePostDeploy(ctx, db, m, pdLogger()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := countFilled(t, db); got != rows {
		t.Errorf("filled %d of %d rows", got, rows)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM _migrations WHERE version = ?`, m.version).Scan(&name); err != nil {
		t.Fatalf("ledger row missing after completion: %v", err)
	}
	if name != m.name {
		t.Errorf("ledger name = %q, want %q", name, m.name)
	}
}

// The property that makes this lane safe: interrupt it and the finished
// batches stay finished, the migration stays pending, and re-running completes
// it. If the ledger row were written up front, a crash would leave a
// half-backfilled table marked done — permanently.
func TestPostDeploy_ResumesAfterInterruption(t *testing.T) {
	const rows = 2000
	db, m := postDeployFixture(t, rows, convergingBackfill)

	// Cancel after the first batch commits.
	ctx, cancel := context.WithCancel(context.Background())
	n, err := postDeployPass(ctx, db, m)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if n != PostDeployBatchSize {
		t.Fatalf("first pass changed %d rows, want %d", n, PostDeployBatchSize)
	}
	cancel()

	if err := runOnePostDeploy(ctx, db, m, pdLogger()); err != nil {
		t.Fatalf("interrupted run should return cleanly, got: %v", err)
	}

	// Pending, not done.
	var recorded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version = ?`, m.version).Scan(&recorded); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if recorded != 0 {
		t.Error("an interrupted post-deploy migration was recorded as applied — a later " +
			"start would skip the rest of the backfill forever")
	}
	if got := countFilled(t, db); got != PostDeployBatchSize {
		t.Errorf("filled %d rows, want the one committed batch (%d)", got, PostDeployBatchSize)
	}

	// Resume to completion.
	if err := runOnePostDeploy(context.Background(), db, m, pdLogger()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := countFilled(t, db); got != rows {
		t.Errorf("after resume filled %d of %d", got, rows)
	}
}

// A statement that keeps matching the rows it has already handled would loop
// until the process died. The runner has to notice and say why.
func TestPostDeploy_RefusesAStatementThatNeverConverges(t *testing.T) {
	// No WHERE narrowing: every pass rewrites the same rows, forever.
	db, m := postDeployFixture(t, 10, `UPDATE widgets SET label = 'filled'`)

	err := runOnePostDeploy(context.Background(), db, m, pdLogger())
	if err == nil {
		t.Fatal("want an error for a non-converging statement")
	}
	for _, want := range []string{"not converging", "post_deploy/README.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	// And it must not be recorded as applied.
	var recorded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version = ?`, m.version).Scan(&recorded); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if recorded != 0 {
		t.Error("a migration that never converged was recorded as applied")
	}
}

func TestPostDeploy_PendingReportsWhatIsOutstanding(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	if err := Migrate(ctx, db, pdLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pending, err := PostDeployPending(ctx, db)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	// There are none in the tree yet; the contract is "no error, nothing
	// outstanding", not "unimplemented".
	declared := pendingPostDeployDeclared()
	if len(pending) != len(declared) {
		t.Errorf("PostDeployPending returned %d entries for %d declared", len(pending), len(declared))
	}
	for _, p := range pending {
		if p.Applied {
			continue
		}
		t.Logf("outstanding: v%d %s", p.Version, p.Name)
	}
}

// Boot must not run a post-deploy migration, and must not record it either —
// otherwise the background runner would skip it and the backfill would never
// happen.
func TestMigrate_DefersPostDeployWithoutRecordingIt(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "defer.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, label TEXT)`); err != nil {
		t.Fatalf("widgets: %v", err)
	}

	// A two-entry registry: one normal, one deferred.
	reg := []migration{
		{version: 20260801000000, name: "add_widget_note", sql: `ALTER TABLE widgets ADD COLUMN note TEXT`},
		{version: 20260801010000, name: "backfill_widget_note", sql: convergingBackfill, postDeploy: true},
	}
	if err := applyRegistry(ctx, db, reg, pdLogger()); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var applied []int
	rows, err := db.Query(`SELECT version FROM _migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		applied = append(applied, v)
	}
	if fmt.Sprint(applied) != "[20260801000000]" {
		t.Errorf("ledger = %v, want only the non-deferred migration", applied)
	}
}

// --- findings from review ---------------------------------------------------

// A normal shutdown mid-backfill is not a failure. runOnePostDeploy returned
// nil on cancellation, so the caller moved to the NEXT pending migration with
// a dead context, that one failed at BeginTx, and a clean stop was reported as
// "post-deployment migrations did not complete".
func TestPostDeploy_CleanShutdownIsNotReportedAsFailure(t *testing.T) {
	const rows = 2000
	db, m := postDeployFixture(t, rows, convergingBackfill)

	// Two pending migrations, so the "moved on to the next one" bug can show.
	second := m
	second.version = m.version + 10000
	second.name = m.name + "_two"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runPendingPostDeploy(ctx, db, []migration{m, second}, pdLogger()); err != nil {
		t.Fatalf("a cancelled context is a shutdown, not an error: %v", err)
	}
}

// The batch driver reads RowsAffected of the whole Exec. With more than one
// statement in the file, SQLite reports the LAST statement's count — so a
// converging UPDATE followed by a statement that happens to touch no rows
// reports 0, and the backfill is marked complete after one pass with most of
// the table untouched. Verified against this driver before writing the guard.
func TestPostDeploy_RejectsMultipleStatements(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		{"single statement", `UPDATE w SET a = 1 WHERE a IS NULL`, false},
		{"trailing semicolon is still one statement", `UPDATE w SET a = 1 WHERE a IS NULL;`, false},
		{"semicolon inside a string literal", `UPDATE w SET a = 'x;y' WHERE a IS NULL`, false},
		{"comment mentioning a semicolon", "-- ends with ; here\nUPDATE w SET a = 1 WHERE a IS NULL", false},
		{"two statements", `UPDATE w SET a = 1 WHERE a IS NULL;
UPDATE w SET b = 2 WHERE 1 = 0;`, true},
		{"update plus analyze", `UPDATE w SET a = 1 WHERE a IS NULL;
ANALYZE;`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSingleStatement(tc.sql)
			if tc.wantErr && err == nil {
				t.Errorf("want a refusal for:\n%s", tc.sql)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected refusal: %v\nfor:\n%s", err, tc.sql)
			}
		})
	}
}

// A backfill that happens to finish on exactly the pass limit was aborted with
// "not converging" — a claim the runner had no evidence for, since it never
// asked whether anything was left.
func TestPostDeploy_DoesNotAccuseABackfillThatFinishesOnTheLimit(t *testing.T) {
	// Batch size 500, limit lowered for the test via the same code path.
	const rows = 1000
	db, m := postDeployFixture(t, rows, convergingBackfill)

	// Exactly two passes are needed, so a limit of 2 is the boundary case.
	if err := runOnePostDeployWithLimit(context.Background(), db, m, pdLogger(), 2); err != nil {
		t.Fatalf("a backfill that finishes on the last allowed pass is not a failure: %v", err)
	}
	if got := countFilled(t, db); got != rows {
		t.Errorf("filled %d of %d", got, rows)
	}
}

// The limit still has to fire on something genuinely non-converging.
func TestPostDeploy_LimitStillCatchesRunaway(t *testing.T) {
	db, m := postDeployFixture(t, 10, `UPDATE widgets SET label = 'filled'`)
	err := runOnePostDeployWithLimit(context.Background(), db, m, pdLogger(), 3)
	if err == nil || !strings.Contains(err.Error(), "not converging") {
		t.Fatalf("err = %v, want a non-convergence refusal", err)
	}
}

// migration-status is the tool an operator reaches for when migrations are
// misbehaving. If the registry itself failed to build, reporting "nothing
// outstanding" hides the one fact they came for — and migrationRegistryErr is
// unexported, so no caller outside this package can check it themselves.
func TestPostDeployPending_SurfacesABrokenRegistry(t *testing.T) {
	saved := migrationRegistryErr
	migrationRegistryErr = errors.New("synthetic: migration 20260801000000_x.sql is empty")
	t.Cleanup(func() { migrationRegistryErr = saved })

	db, _ := postDeployFixture(t, 1, convergingBackfill)
	_, err := PostDeployPending(context.Background(), db)
	if err == nil {
		t.Fatal("want the registry error surfaced, got nil")
	}
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("error %q should name the registry as the problem", err)
	}
}

// Cancellation between passes was handled; cancellation DURING a pass was not.
// A rolling restart mid-backfill would surface as an ERROR log claiming the
// migrations did not complete, when in fact the batch rolled back safely and
// the next boot resumes.
//
// There is no end-to-end test for it: a context already cancelled is caught by
// the top-of-loop check before any pass starts, and hitting the real window —
// cancellation landing while Exec is in flight — would mean racing the
// scheduler. So the classifier is tested directly, and the loop is verified to
// use it by the control case below.
func TestIsShutdownErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"cancelled", context.Canceled, true},
		{"deadline", context.DeadlineExceeded, true},
		{"wrapped cancellation", fmt.Errorf("batch: %w", context.Canceled), true},
		{"wrapped deadline", fmt.Errorf("commit batch: %w", context.DeadlineExceeded), true},
		{"a real failure", errors.New("no such table: widgets"), false},
		{"a real failure that mentions the word", errors.New("column \"canceled\" does not exist"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isShutdownErr(tc.err); got != tc.want {
				t.Errorf("isShutdownErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Control: a genuine failure inside a pass must still be an error. Without
// this, "treat context errors as a clean stop" could be over-applied into
// "swallow everything".
func TestPostDeploy_AGenuineFailureIsStillAFailure(t *testing.T) {
	db, m := postDeployFixture(t, 10, `UPDATE table_that_does_not_exist SET x = 1`)

	err := runOnePostDeploy(context.Background(), db, m, pdLogger())
	if err == nil {
		t.Fatal("a broken statement must not be reported as a clean stop")
	}
	if isShutdownErr(err) {
		t.Errorf("a SQL error was classified as a shutdown: %v", err)
	}
}
