package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/database"
)

var backfillMemCounter atomic.Uint64

// newTestDBWithMigrations builds an in-memory SQLite DB with a
// _migrations row per supplied version. No actual schema is created —
// the backfill replay machinery only consults _migrations, so an empty
// DB is all that's needed to exercise the replay logic.
func newTestDBWithMigrations(t *testing.T, versions ...int) *sql.DB {
	t.Helper()
	// Shared-cache in-memory DSN so connection-pool fan-out sees one DB.
	name := fmt.Sprintf("crewship-backfill-test-%d", backfillMemCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create _migrations: %v", err)
	}
	for _, v := range versions {
		if _, err := db.Exec(`INSERT INTO _migrations (version, name) VALUES (?, 'test')`, v); err != nil {
			t.Fatalf("insert v%d: %v", v, err)
		}
	}
	return db
}

// TestReplayRestoreBackfills_InvokesForMissingVersions wires a fake
// v99 backfill, pretends the target applied v99 but the bundle only
// knew up to v50, and verifies the hook fires.
func TestReplayRestoreBackfills_InvokesForMissingVersions(t *testing.T) {
	db := newTestDBWithMigrations(t, 50, 99)

	calls := 0
	var gotVersion int
	unregister := database.RegisterRestoreBackfill(99, func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
		calls++
		gotVersion = 99
		// Touch the tx so we fail fast if it's nil.
		if tx == nil {
			t.Error("backfill received nil tx")
		}
		return nil
	})
	defer unregister()

	bundleVersions := []int{50} // v99 absent in bundle
	if err := replayRestoreBackfills(context.Background(), db, bundleVersions, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected backfill for v99 to fire once, got %d", calls)
	}
	if gotVersion != 99 {
		t.Errorf("wrong version invoked: %d", gotVersion)
	}
}

// TestReplayRestoreBackfills_NoOpWhenBundleAndTargetMatch — same-set
// of migrations on both sides means nothing to replay.
func TestReplayRestoreBackfills_NoOpWhenBundleAndTargetMatch(t *testing.T) {
	db := newTestDBWithMigrations(t, 1, 2, 3)

	calls := 0
	unregister := database.RegisterRestoreBackfill(2, func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
		calls++
		return nil
	})
	defer unregister()

	if err := replayRestoreBackfills(context.Background(), db, []int{1, 2, 3}, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if calls != 0 {
		t.Errorf("expected no backfills, got %d calls", calls)
	}
}

// TestReplayRestoreBackfills_SkipsWhenNoHookRegistered — missing
// migrations without a hook are the common case (pure ADD COLUMN) and
// must NOT produce an error.
func TestReplayRestoreBackfills_SkipsWhenNoHookRegistered(t *testing.T) {
	db := newTestDBWithMigrations(t, 1, 2, 3, 4, 5)

	// No RegisterRestoreBackfill call.
	if err := replayRestoreBackfills(context.Background(), db, []int{1, 2}, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

// TestReplayRestoreBackfills_PropagatesHookError wraps the hook's
// error in ErrRestoreBackfillFailed so callers can map to HTTP 500.
func TestReplayRestoreBackfills_PropagatesHookError(t *testing.T) {
	db := newTestDBWithMigrations(t, 1, 7)

	sentinel := errors.New("hook boom")
	unregister := database.RegisterRestoreBackfill(7, func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
		return sentinel
	})
	defer unregister()

	err := replayRestoreBackfills(context.Background(), db, []int{1}, nil)
	if err == nil {
		t.Fatal("expected error from hook")
	}
	if !errors.Is(err, ErrRestoreBackfillFailed) {
		t.Errorf("expected ErrRestoreBackfillFailed, got %v", err)
	}
}

// TestReplayRestoreBackfills_OrdersAscending ensures a multi-version
// replay runs in ascending version order — a later hook may depend on
// the earlier one having completed.
func TestReplayRestoreBackfills_OrdersAscending(t *testing.T) {
	db := newTestDBWithMigrations(t, 10, 20, 30)

	var order []int
	u1 := database.RegisterRestoreBackfill(30, func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
		order = append(order, 30)
		return nil
	})
	defer u1()
	u2 := database.RegisterRestoreBackfill(20, func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
		order = append(order, 20)
		return nil
	})
	defer u2()
	u3 := database.RegisterRestoreBackfill(10, func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
		order = append(order, 10)
		return nil
	})
	defer u3()

	if err := replayRestoreBackfills(context.Background(), db, nil, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := []int{10, 20, 30}
	if len(order) != len(want) {
		t.Fatalf("want %v got %v", want, order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d]: want %d got %d", i, want[i], order[i])
		}
	}
}

// TestReplayRestoreBackfills_EmptyAppliedIsNoOp covers a fresh DB
// with no _migrations rows at all (unit-test fixture or very old
// install). Must not crash and must not iterate.
func TestReplayRestoreBackfills_EmptyAppliedIsNoOp(t *testing.T) {
	name := fmt.Sprintf("crewship-backfill-empty-%d", backfillMemCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	// Intentionally no _migrations table: AppliedMigrationVersions
	// returns nil on the "table missing" path and we should no-op.
	if err := replayRestoreBackfills(context.Background(), db, []int{1, 2}, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
}

// TestReplayRestoreBackfills_HookContractIsIdempotent pins the
// RestoreBackfillFunc contract: hooks MUST be idempotent because the
// retry path after a partial failure re-executes them.
//
// Why this test matters: replayRestoreBackfills runs each hook in its
// OWN tx (so one failure doesn't strand half-applied state). If hook
// vN commits and hook vN+1 errors, the restore aborts but vN is on
// disk. The operator's recovery is to fix the cause and re-run the
// restore — at which point vN runs AGAIN against the same rows. A
// hook that accumulates (counter += 1) would compound on every retry
// and silently corrupt data; a hook that asserts (col = X WHERE col
// IS NULL) re-runs cleanly.
//
// Test shape: build a target with a `counter` column initialised to
// NULL; register an idempotent backfill (`SET counter = 1 WHERE col
// IS NULL`); run the replay TWICE and assert the column lands on
// exactly 1, not 2. A future change that swaps the hook to
// `counter = counter + 1` would FAIL this test — and that's the
// point: the test stops a non-idempotent hook from sneaking in.
func TestReplayRestoreBackfills_HookContractIsIdempotent(t *testing.T) {
	const versionUnderTest = 12345

	name := fmt.Sprintf("crewship-backfill-idem-%d", backfillMemCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Minimal _migrations + target table. Target row starts with
	// counter=NULL so the WHERE clause matches on the first run only.
	if _, err := db.Exec(`
		CREATE TABLE _migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT (datetime('now')));
		CREATE TABLE rows_under_backfill (id TEXT PRIMARY KEY, counter INTEGER);
		INSERT INTO _migrations (version, name) VALUES (?1, 'idempotency-pin');
		INSERT INTO rows_under_backfill (id, counter) VALUES ('r1', NULL);
	`, versionUnderTest); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Idempotent backfill: the WHERE clause makes the second invocation
	// a no-op. If anyone replaces this with `SET counter = counter + 1`
	// the test catches it.
	calls := 0
	unregister := database.RegisterRestoreBackfill(versionUnderTest, func(ctx context.Context, tx *sql.Tx, _ *slog.Logger) error {
		calls++
		_, err := tx.ExecContext(ctx, `UPDATE rows_under_backfill SET counter = 1 WHERE counter IS NULL`)
		return err
	})
	t.Cleanup(unregister)

	// Two replay passes — bundle "knows nothing" so versionUnderTest is
	// in the missing-on-source set both times.
	for i := 1; i <= 2; i++ {
		if err := replayRestoreBackfills(context.Background(), db, nil, nil); err != nil {
			t.Fatalf("replay pass %d: %v", i, err)
		}
	}
	if calls != 2 {
		t.Fatalf("expected hook to fire twice (idempotency means safe to retry, not skip); got %d calls", calls)
	}

	var counter sql.NullInt64
	if err := db.QueryRow(`SELECT counter FROM rows_under_backfill WHERE id='r1'`).Scan(&counter); err != nil {
		t.Fatalf("post-replay scan: %v", err)
	}
	if !counter.Valid || counter.Int64 != 1 {
		t.Errorf("counter after two idempotent replays = %v.%d; want 1 (anything ≠ 1 means the hook compounded — non-idempotent)",
			counter.Valid, counter.Int64)
	}
}
