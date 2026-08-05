package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// The delegation cap reads its depth off the assignment row, so the row has to
// carry one — on a database that existed before the column did, too. A backfill
// that skipped the ALTER, or a DEFAULT that came out NULL, turns "how deep is
// this chain" into "unknown", and the cap into a scan that never fires.
func TestAssignmentDelegationColumns(t *testing.T) {
	dbh, err := Open("file:" + filepath.Join(t.TempDir(), "delegation.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })
	if err := Migrate(context.Background(), dbh.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db := dbh.DB

	cols := map[string]struct {
		notNull bool
		dflt    sql.NullString
	}{}
	rows, err := db.Query(`PRAGMA table_info(assignments)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		cols[name] = struct {
			notNull bool
			dflt    sql.NullString
		}{notNull == 1, dflt}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}

	depth, ok := cols["depth"]
	if !ok {
		t.Fatal("assignments.depth is missing — the delegation cap has nothing to read")
	}
	if !depth.notNull {
		t.Error("assignments.depth must be NOT NULL: a NULL depth is an unbounded chain that scans as unknown")
	}
	if !depth.dflt.Valid || depth.dflt.String != "0" {
		t.Errorf("assignments.depth default = %v, want 0 so pre-existing rows read as roots", depth.dflt)
	}
	if _, ok := cols["parent_assignment_id"]; !ok {
		t.Fatal("assignments.parent_assignment_id is missing — the chain cannot be reconstructed and fan-out cannot be counted per parent")
	}

	// The fan-out count for a delegated run is a per-parent COUNT on every
	// /assign; unindexed it degrades into a table scan of every assignment ever.
	var idx int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_assignment_parent'`,
	).Scan(&idx); err != nil {
		t.Fatalf("read index: %v", err)
	}
	if idx != 1 {
		t.Error("idx_assignment_parent is missing")
	}
}
