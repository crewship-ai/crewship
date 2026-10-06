package access

import (
	"database/sql"
	"errors"
	"testing"
)

func TestTxStmtCacheReusesStatementsWithinOneTransaction(t *testing.T) {
	s := fixture(t)
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q := newTxStmtCache(tx)
	defer q.Close()
	for i := 0; i < 3; i++ {
		var n int
		if err := q.QueryRowContext(t.Context(), `SELECT ?+1`, i).Scan(&n); err != nil || n != i+1 {
			t.Fatalf("cached query %d: %d %v", i, n, err)
		}
	}
	if _, err := q.ExecContext(t.Context(), `CREATE TEMP TABLE stmtcache_probe(v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := q.ExecContext(t.Context(), `INSERT INTO stmtcache_probe(v) VALUES(?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := q.QueryContext(t.Context(), `SELECT v FROM stmtcache_probe ORDER BY v`)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	rows.Close()
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("writes through the cache must be visible to later reads in the same transaction: %v", got)
	}
	// One prepared statement per distinct SQL text, however often it runs.
	if len(q.stmts) != 4 {
		t.Fatalf("expected 4 distinct prepared statements, got %d", len(q.stmts))
	}
}

func TestTxStmtCachePropagatesPrepareErrors(t *testing.T) {
	s := fixture(t)
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q := newTxStmtCache(tx)
	defer q.Close()
	var n int
	if err := q.QueryRowContext(t.Context(), `SELECT nope FROM no_such_table`).Scan(&n); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid SQL must surface its error, got %v", err)
	}
	if _, err := q.QueryContext(t.Context(), `SELECT nope FROM no_such_table`); err == nil {
		t.Fatal("invalid SQL must surface its error from QueryContext")
	}
	if _, err := q.ExecContext(t.Context(), `DELETE FROM no_such_table`); err == nil {
		t.Fatal("invalid SQL must surface its error from ExecContext")
	}
	if len(q.stmts) != 0 {
		t.Fatalf("failed prepares must not be cached: %d", len(q.stmts))
	}
}
