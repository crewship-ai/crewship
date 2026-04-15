package backup

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/database"
)

// newTestDBWithMigrations builds an in-memory SQLite DB with a
// _migrations row per supplied version. No actual schema is created —
// the backfill replay machinery only consults _migrations, so an empty
// DB is all that's needed to exercise the replay logic.
func newTestDBWithMigrations(t *testing.T, versions ...int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
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
	db, err := sql.Open("sqlite", ":memory:")
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
