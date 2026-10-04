package pipeline

import (
	"testing"
	"time"
)

func TestPendingManagementScopesCancellationAndOrderedListing(t *testing.T) {
	db := newPendingDB(t)
	t.Cleanup(func() { _ = db.Close() })
	s := NewPendingRunStore(db)
	now := time.Now().UTC().Truncate(time.Second)
	pin := 3
	for i, id := range []string{"later", "first", "other", "fired"} {
		ws := "a"
		if id == "other" {
			ws = "b"
		}
		fire := now.Add(time.Hour)
		if id == "first" {
			fire = now
		}
		_, _, err := s.Enqueue(t.Context(), PendingRun{ID: id, WorkspaceID: ws, PipelineID: "recipe", PipelineSlug: "recipe", FireAt: fire, PinnedVersion: &pin, InputsJSON: `{"x":1}`, Priority: i})
		if err != nil {
			t.Fatal(err)
		}
	}
	if fired, err := s.MarkFired(t.Context(), "fired", "run"); err != nil || !fired {
		t.Fatalf("mark fired = %v, %v", fired, err)
	}
	for _, limit := range []int{0, 201, 1, 2} {
		rows, err := s.ListPending(t.Context(), "a", limit)
		want := 2
		if limit == 1 {
			want = 1
		}
		if err != nil || len(rows) != want {
			t.Fatalf("limit %d = %v, %v", limit, rows, err)
		}
		first := rows[0]
		if first.ID != "first" || !first.FireAt.Equal(now) || first.PinnedVersion == nil || *first.PinnedVersion != 3 || first.InputsJSON != `{"x":1}` {
			t.Fatalf("lost pending contract: %+v", first)
		}
	}
	if cancelled, err := s.Cancel(t.Context(), "b", "first"); err != nil || cancelled {
		t.Fatalf("cross-workspace cancel = %v, %v", cancelled, err)
	}
	if cancelled, err := s.Cancel(t.Context(), "a", "fired"); err != nil || cancelled {
		t.Fatalf("fired row cancelled = %v, %v", cancelled, err)
	}
	if cancelled, err := s.Cancel(t.Context(), "a", "first"); err != nil || !cancelled {
		t.Fatalf("cancel = %v, %v", cancelled, err)
	}
	if cancelled, err := s.Cancel(t.Context(), "a", "first"); err != nil || cancelled {
		t.Fatalf("repeat cancel = %v, %v", cancelled, err)
	}
	rows, err := s.ListPending(t.Context(), "a", 50)
	if err != nil || len(rows) != 1 || rows[0].ID != "later" {
		t.Fatalf("cancelled row still listed: %v, %v", rows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListPending(t.Context(), "a", 1); err == nil {
		t.Error("closed DB returned pending rows")
	}
	if _, err := s.Cancel(t.Context(), "a", "later"); err == nil {
		t.Error("closed DB accepted cancellation")
	}
}

func TestPendingAdmissionTransactionRollsBackEnqueue(t *testing.T) {
	db := newPendingDB(t)
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := NewPendingRunStoreTx(tx)
	id, coalesced, err := s.Enqueue(t.Context(), PendingRun{ID: "pending", WorkspaceID: "a", PipelineID: "recipe", PipelineSlug: "recipe", FireAt: time.Now()})
	if err != nil || id != "pending" || coalesced {
		t.Fatalf("enqueue = %q, %v, %v", id, coalesced, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	rows, err := NewPendingRunStore(db).ListPending(t.Context(), "a", 50)
	if err != nil || len(rows) != 0 {
		t.Fatalf("refused admission left pending work: %v, %v", rows, err)
	}
}

func TestRunChainReaderPreservesDepthOriginAndWorkspaceBoundary(t *testing.T) {
	s, db := openRunsTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedRunRow(t, s, "child", "pln_a", RunStatusCompleted)
	if _, err := db.Exec(`UPDATE pipeline_runs SET chain_depth=3,chain_origin='root' WHERE id='child'`); err != nil {
		t.Fatal(err)
	}
	r := NewRunChainReader(db)
	pos, found, err := r.ChainOf(t.Context(), "ws_runs", "child")
	if err != nil || !found || pos.Depth != 3 || pos.Origin != "root" {
		t.Fatalf("chain = %+v, %v, %v", pos, found, err)
	}
	for _, pair := range [][2]string{{"other", "child"}, {"ws_runs", "missing"}, {"", "child"}, {"ws_runs", ""}} {
		if pos, found, err := r.ChainOf(t.Context(), pair[0], pair[1]); err != nil || found || pos != (ChainPos{}) {
			t.Fatalf("unknown chain %v = %+v, %v, %v", pair, pos, found, err)
		}
	}
	for _, reader := range []*RunChainReader{nil, NewRunChainReader(nil)} {
		if _, found, err := reader.ChainOf(t.Context(), "ws_runs", "child"); err != nil || found {
			t.Fatalf("optional reader = %v, %v", found, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ChainOf(t.Context(), "ws_runs", "child"); err == nil {
		t.Fatal("closed DB silently reset chain depth")
	}
}
