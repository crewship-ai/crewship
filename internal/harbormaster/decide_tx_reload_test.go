package harbormaster

// DecideTx must never report success it cannot describe.
//
// The CAS half of a decision is one conditional UPDATE followed by a
// reload of the row it just won. The reload is not cosmetic: the caller
// gates the whole agent-side transition (POST /approvals/{id}/decide),
// the journal entry and the reward-history row on the returned
// *Request. Swallowing the reload error and returning (nil, nil) told
// the caller "decision applied, nothing to act on", so the caller
// committed the queue CAS and skipped every side effect — a terminal
// approval describing a transition that never happened, which is the
// exact drift #1247 exists to eliminate.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// stubResult is a sql.Result whose RowsAffected is fixed.
type stubResult struct{ n int64 }

func (r stubResult) LastInsertId() (int64, error) { return 0, nil }
func (r stubResult) RowsAffected() (int64, error) { return r.n, nil }

// reloadFailDBTX applies the CAS UPDATE happily and then fails every
// SELECT — the reload is the only SELECT DecideTx issues on the success
// path.
type reloadFailDBTX struct {
	execs   int
	queries int
	db      *sql.DB
}

func (s *reloadFailDBTX) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s.execs++
	return stubResult{n: 1}, nil
}

func (s *reloadFailDBTX) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	s.queries++
	// A *sql.Row can only be produced by database/sql, so route the
	// call at a closed DB: Scan then yields "sql: database is closed",
	// which is exactly the shape of a mid-transaction reload failure.
	return s.db.QueryRowContext(ctx, query, args...)
}

func closedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return db
}

func TestDecideTx_ReloadFailure_ReturnsError(t *testing.T) {
	stub := &reloadFailDBTX{db: closedDB(t)}

	row, err := DecideTx(context.Background(), stub, "ws-1", "appr-1", StatusApproved, "u-1", "ok")
	if err == nil {
		t.Fatalf("DecideTx returned err=nil (row=%v) — a failed reload must fail the "+
			"transaction, not hand the caller a nil row it will silently skip", row)
	}
	if row != nil {
		t.Errorf("row = %v, want nil alongside the error", row)
	}
	// The error must not be mistaken for the two "caller should answer
	// 4xx" sentinels; a reload failure is a 500.
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotPending) {
		t.Errorf("err = %v, want an opaque failure (not a 404/409 sentinel)", err)
	}
	if stub.execs != 1 {
		t.Errorf("CAS UPDATE ran %d times, want 1", stub.execs)
	}
	if stub.queries != 1 {
		t.Errorf("reload SELECT ran %d times, want 1", stub.queries)
	}
}
