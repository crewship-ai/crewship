package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

// ErrAcceptanceBudget means the acceptance path could not commit inside its
// budget. It is retryable by the sender and maps to `503` — never to a `202`.
// Nothing was written; the caller may hand the same delivery back later and it
// will be accepted or deduplicated normally.
var ErrAcceptanceBudget = errors.New("work: acceptance budget exceeded")

// DefaultAcceptanceBudget is §5's: the response is produced within 2 s of the
// body being fully read, under the tested conditions.
const DefaultAcceptanceBudget = 2 * time.Second

// Acceptor runs the acceptance transaction — the one that must answer inside a
// provider's response deadline, or not answer `202` at all.
//
// It owns a SECOND handle to the same database file, and that is not an
// optimisation. A context deadline cannot bound a SQLite write:
// modernc.org/sqlite passes context.Background() into Commit and Rollback
// (tx.go:36,50,58), so only statement execution is interruptible. Measured on
// crewship-dev: a 500 ms context against a write lock held elsewhere for 5 s
// returned after 5044 ms — an order of magnitude past its budget, with the
// context dutifully reporting "deadline exceeded" once it was finally let go.
// The numbers are in docs/prd/ADR-QUEUE-RIVER-SQLITE-2026-09-10.md.
//
// The only thing that actually bounds the wait is busy_timeout, and that pragma
// is per-connection state. Setting it on a connection borrowed from the main
// pool would leave it set when the connection went back, so an unrelated
// dashboard write would inherit a two-second patience it was never designed
// for — the "random switching inside a shared pool" the durability contract
// rules out. Hence a separate handle, whose every connection carries the short
// timeout from its DSN.
//
// It is FULL like the main handle, so this buys a bounded wait and gives up no
// durability. What it does give up is a share of the write lock: two handles on
// one file contend with each other exactly as two connections in one pool do.
//
// Note where the wait actually lands. `_txlock=immediate` takes the writer lock
// at BEGIN, so a contended acceptance is refused before it has written
// anything — measured at 452 ms with a 600 ms budget against a lock held 6 s,
// against 5036 ms for the same case with only a context deadline. busy_timeout
// governs COMMIT as well, so the bound survives a future DSN that drops
// `immediate`; it would merely move the refusal later, after the transaction had
// done work for nothing.
type Acceptor struct {
	db     *database.DB
	store  *Store
	budget time.Duration
}

// OpenAcceptor opens the dedicated acceptance handle for the database at path.
//
// budget is the wall-clock answer time the caller has promised. The handle's
// busy_timeout is set below it, so a contended write fails fast enough for the
// handler to answer honestly instead of blocking past its own deadline.
func OpenAcceptor(path string, budget time.Duration) (*Acceptor, error) {
	if budget <= 0 {
		budget = DefaultAcceptanceBudget
	}
	// Leave room to notice the failure and write a response. A busy_timeout
	// equal to the budget would return exactly as the budget expired.
	busy := budget - budget/4
	if busy <= 0 {
		busy = budget
	}
	db, err := database.Open("file:"+path, database.WithBusyTimeout(busy))
	if err != nil {
		return nil, fmt.Errorf("work: open acceptance handle: %w", err)
	}
	// One connection. The acceptance path is a single short write transaction;
	// more connections would only add contenders for the one write lock, and a
	// second connection is never taken while one is open — see Do, which never
	// reads back through this handle inside its own transaction. The River spike
	// measured what happens when that rule is broken: a pool of one starves for
	// the full context deadline.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &Acceptor{db: db, store: NewStore(db.DB), budget: budget}, nil
}

// Store returns a work store bound to the acceptance handle.
func (a *Acceptor) Store() *Store { return a.store }

// Budget is the answer time this acceptor was built for.
func (a *Acceptor) Budget() time.Duration { return a.budget }

// Close releases the handle.
func (a *Acceptor) Close() error { return a.db.Close() }

// Do runs fn inside one immediate transaction, bounded by the budget.
//
// fn must do database work only. No container start, no HTTP call, no CLI
// launch — those belong after the commit, which is the whole point of accepting
// durably first. It must also not read back through the acceptance handle: the
// pool is one connection and the transaction is holding it.
//
// On ErrAcceptanceBudget nothing was committed, so the caller answers with a
// retryable status and no receipt. A partially-unclear commit is the one case
// this cannot rule out — if the process dies between the commit and the
// response, the work exists and the sender never learned its id. That is why
// the same source delivery id must find it again, which the delivery ledger's
// unique index provides.
func (a *Acceptor) Do(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, a.budget)
	defer cancel()

	start := time.Now()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		if isBusyOrDeadline(err) {
			return fmt.Errorf("%w after %s waiting to begin: %w", ErrAcceptanceBudget, time.Since(start).Round(time.Millisecond), err)
		}
		return fmt.Errorf("work: begin acceptance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		if isBusyOrDeadline(err) {
			return fmt.Errorf("%w after %s at commit: %w", ErrAcceptanceBudget, time.Since(start).Round(time.Millisecond), err)
		}
		return fmt.Errorf("work: commit acceptance: %w", err)
	}
	return nil
}

func isBusyOrDeadline(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return containsAny(msg, "SQLITE_BUSY", "database is locked", "context deadline exceeded")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
