package appengine

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func historyBytes(t *testing.T, s *Store, page string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT coalesce(sum(length(snapshot)),0)+(SELECT coalesce(sum(length(command)),0) FROM gridcraft_audit WHERE page_id=?) FROM gridcraft_revisions WHERE page_id=?`, page, page).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func bigEdit(i int, input string) Request {
	return Request{MutationID: fmt.Sprintf("s1:%d", i), MandateID: "human-import", SessionID: "s1", BaseRev: int64(i + 1), Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "A1", Input: input}}
}

// Formula text is stored as written: HTML escaping would inflate "<", ">" and
// "&" sixfold in the recorded command and in the history quota.
func TestRecordedCommandIsNotHTMLEscaped(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	r := edit()
	r.Command.Input = `=IF(A1<>B1,"a&b",">")`
	if _, err := s.Execute(t.Context(), k, r, a, e); err != nil {
		t.Fatal(err)
	}
	var command string
	if err := db.QueryRow(`SELECT command FROM gridcraft_audit WHERE rev=2`).Scan(&command); err != nil {
		t.Fatal(err)
	}
	if command != `{"kind":"cells","cells":[{"sheet":"Sheet1","cell":"B2","input":"=IF(A1<>B1,\"a&b\",\">\")"}]}` {
		t.Fatalf("command %s", command)
	}
}

// One recorded command is bounded like a snapshot, before the engine runs, so
// escaping cannot turn a single edit into megabytes of history.
func TestRecordedCommandSizeIsBounded(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	control := strings.Repeat("\x01", 8192) // JSON stores each byte as \u0001
	var ops []CellInput
	for i := 0; i < MaxEditCells; i++ {
		ops = append(ops, CellInput{"Sheet1", fmt.Sprintf("A%d", i+1), control})
	}
	r := Request{MutationID: "s1:1", MandateID: "human-import", SessionID: "s1", BaseRev: 1, Generation: 1, Command: Command{Kind: "cells.set", Cells: ops}}
	calls := e.calls.Load()
	if _, err := s.Execute(t.Context(), k, r, a, e); !errors.Is(err, ErrInvalid) || e.calls.Load() != calls {
		t.Fatalf("oversized command: %v calls=%d", err, e.calls.Load()-calls)
	}
}

// Reaching the history quota is recoverable. History older than the newest
// checkpoint at least MaxRebaseWindow revisions behind the head is not needed
// for rebasing, so it is pruned and editing continues; the audit rows (writer,
// receipt and result hash) remain.
func TestHistoryQuotaPrunesBeyondRebaseWindow(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	input := strings.Repeat("x", 4000)
	s.options.MaxHistoryBytes = 400 * 4100
	const commits = 700
	for i := 0; i < commits; i++ {
		if _, err := s.Execute(t.Context(), k, bigEdit(i, input), a, e); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	head := int64(commits + 1)
	if n := historyBytes(t, s, k.PageID); n > s.options.MaxHistoryBytes {
		t.Fatalf("history %d over quota", n)
	}
	var oldest, kept, audits int64
	if err := db.QueryRow(`SELECT min(rev) FROM gridcraft_revisions WHERE page_id=?`, k.PageID).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest <= 1 || oldest > head-MaxRebaseWindow || oldest%CheckpointInterval != 0 {
		t.Fatalf("oldest kept checkpoint %d", oldest)
	}
	if err := db.QueryRow(`SELECT count(*) FROM gridcraft_audit WHERE page_id=? AND rev>? AND command IS NOT NULL`, k.PageID, oldest).Scan(&kept); err != nil || kept != head-oldest {
		t.Fatalf("commands after the kept checkpoint %d %v", kept, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM gridcraft_audit WHERE page_id=? AND result_hash IS NOT NULL`, k.PageID).Scan(&audits); err != nil || audits != head {
		t.Fatalf("audit rows %d %v", audits, err)
	}
	// The full rebase window is still served.
	late := Request{MutationID: "s2:1", MandateID: "human-import", SessionID: "s2", BaseRev: head - MaxRebaseWindow, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "Z9", Input: "1"}}
	if r, err := s.Execute(t.Context(), k, late, a, e); err != nil || r.Rev != head+1 {
		t.Fatalf("rebase within window after pruning: %+v %v", r, err)
	}
}

// When even the rebase window does not fit the quota, the commit becomes a
// checkpoint and all earlier history is pruned. Stale clients then reread
// (snapshot_required) instead of the workbook refusing every further edit. A
// base just before the forced checkpoint still rebases: the pruned command of
// that base revision is not part of its window.
func TestHistoryQuotaForcesCheckpointWhenWindowDoesNotFit(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	input := strings.Repeat("x", 4000)
	s.options.MaxHistoryBytes = 20 * 4100
	const commits = 100
	for i := 0; i < commits; i++ {
		if _, err := s.Execute(t.Context(), k, bigEdit(i, input), a, e); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	head := int64(commits + 1)
	if n := historyBytes(t, s, k.PageID); n > s.options.MaxHistoryBytes {
		t.Fatalf("history %d over quota", n)
	}
	var forced int64
	if err := db.QueryRow(`SELECT min(rev) FROM gridcraft_revisions WHERE page_id=?`, k.PageID).Scan(&forced); err != nil || forced <= 1 || forced%CheckpointInterval == 0 {
		t.Fatalf("forced checkpoint %d %v", forced, err)
	}
	late := Request{MutationID: "s2:1", MandateID: "human-import", SessionID: "s2", BaseRev: forced - 2, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "Z9", Input: "1"}}
	var precondition *PreconditionError
	if _, err := s.Execute(t.Context(), k, late, a, e); !errors.As(err, &precondition) || !precondition.SnapshotRequired || precondition.HeadRev != head {
		t.Fatalf("stale edit across pruned history: %v", err)
	}
	late.MutationID, late.BaseRev = "s2:2", forced-1
	if r, err := s.Execute(t.Context(), k, late, a, e); err != nil || r.Rev != head+1 {
		t.Fatalf("rebase from the revision before the forced checkpoint: %+v %v", r, err)
	}
}
