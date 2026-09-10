package work

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// holdWriteLock opens an independent handle to the same file and keeps an
// IMMEDIATE transaction open for d, so the acceptance path meets a real
// contended writer rather than a simulated one.
func holdWriteLock(t *testing.T, path string, d time.Duration) {
	t.Helper()
	holder, err := database.Open("file:" + path)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	holder.SetMaxOpenConns(1)
	tx, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		holder.Close()
		t.Fatalf("begin holder tx: %v", err)
	}
	// _txlock=immediate already took the write lock at BEGIN; writing makes that
	// explicit and survives a future DSN change.
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO work_items (id, workspace_id, source, state, eligible_at, created_at, updated_at, class)
		 VALUES ('lockholder','ws-lock','manual','queued','2026-01-01T00:00:00.000000000Z','2026-01-01T00:00:00.000000000Z','2026-01-01T00:00:00.000000000Z','background')`,
	); err != nil {
		_ = tx.Rollback()
		holder.Close()
		t.Fatalf("holder write: %v", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(d):
		case <-done:
		}
		_ = tx.Rollback()
		holder.Close()
	}()
	t.Cleanup(func() { close(done) })
}

// The measured problem, turned into a guard: a context deadline does NOT bound
// a SQLite write, because modernc passes context.Background() into Commit. An
// acceptance handler that relies on ctx alone answers minutes late under
// contention. The acceptor's short busy_timeout is what actually bounds it.
func TestAcceptor_AnswersInsideItsBudgetUnderAHeldWriteLock(t *testing.T) {
	db := testutil.MigratedDB(t)
	path := db.Path()

	const budget = 600 * time.Millisecond
	const held = 6 * time.Second

	acc, err := OpenAcceptor(path, budget)
	if err != nil {
		t.Fatalf("open acceptor: %v", err)
	}
	defer acc.Close()

	holdWriteLock(t, path, held)

	start := time.Now()
	err = acc.Do(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := acc.Store().AcceptTx(ctx, tx, backgroundReq("agent-jamie"))
		return err
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrAcceptanceBudget) {
		t.Fatalf("error = %v, want ErrAcceptanceBudget", err)
	}
	t.Logf("refused after %s (budget %s, lock held %s)", elapsed.Round(time.Millisecond), budget, held)
	// It must give up on its own deadline rather than on the lock holder's.
	// The ceiling is deliberately a small multiple of the budget and not a
	// fraction of the hold: a bound expressed against `held` would still pass if
	// the busy_timeout silently grew to five seconds.
	if elapsed > 3*budget {
		t.Errorf("acceptance returned after %s with a %s budget and a lock held %s — "+
			"it waited on the lock holder instead of its own deadline", elapsed, budget, held)
	}

	// A rejected acceptance must leave nothing behind. A row without a receipt
	// is worse than a refusal: the sender retries and we run the work twice.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE workspace_id = 'ws1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("work_items after a refused acceptance = %d, want 0 — a refused request must not leave work behind", n)
	}
}

func TestAcceptor_CommitsNormallyWithoutContention(t *testing.T) {
	db := testutil.MigratedDB(t)
	acc, err := OpenAcceptor(db.Path(), DefaultAcceptanceBudget)
	if err != nil {
		t.Fatalf("open acceptor: %v", err)
	}
	defer acc.Close()

	var receipt Receipt
	if err := acc.Do(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		var err error
		receipt, err = acc.Store().AcceptTx(ctx, tx, backgroundReq("agent-jamie"))
		return err
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if receipt.WorkID == "" || receipt.State != StateQueued {
		t.Fatalf("receipt = %+v, want a work id in state queued", receipt)
	}

	// Durable on the shared handle, not just inside the acceptor's own.
	var state string
	if err := db.QueryRow(`SELECT state FROM work_items WHERE id = ?`, receipt.WorkID).Scan(&state); err != nil {
		t.Fatalf("read back through the main handle: %v", err)
	}
	if state != string(StateQueued) {
		t.Errorf("state = %q, want queued", state)
	}
}

// An error from the caller's own function must roll back and must NOT be
// reported as a budget failure — the two mean different things to the sender.
func TestAcceptor_CallerErrorRollsBackAndIsNotABudgetFailure(t *testing.T) {
	db := testutil.MigratedDB(t)
	acc, err := OpenAcceptor(db.Path(), DefaultAcceptanceBudget)
	if err != nil {
		t.Fatalf("open acceptor: %v", err)
	}
	defer acc.Close()

	sentinel := errors.New("policy refused this delivery")
	err = acc.Do(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := acc.Store().AcceptTx(ctx, tx, backgroundReq("agent-jamie")); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the caller's own error", err)
	}
	if errors.Is(err, ErrAcceptanceBudget) {
		t.Error("a caller error was reported as a budget failure; the sender would retry a request we deliberately refused")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("work_items = %d after a rolled-back acceptance, want 0", n)
	}
}

func TestOpenAcceptor_UsesAShorterBusyTimeoutThanItsBudget(t *testing.T) {
	db := testutil.MigratedDB(t)
	acc, err := OpenAcceptor(db.Path(), 2*time.Second)
	if err != nil {
		t.Fatalf("open acceptor: %v", err)
	}
	defer acc.Close()

	var ms int
	if err := acc.db.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if ms >= 2000 {
		t.Errorf("busy_timeout = %dms with a 2000ms budget — it must expire first, or the handler answers after its own deadline", ms)
	}
	// And it must still be FULL: a bounded wait is not worth trading durability
	// for, and the acceptance write is the one that most needs to survive.
	if got := acc.db.Synchronous(); got != database.SynchronousFull {
		t.Errorf("acceptance handle synchronous = %q, want FULL", got)
	}
}
