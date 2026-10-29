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

// A write may never pay for its own storage by deleting confirmed history.
// Exercise both the former forced-checkpoint and old-rebase-window paths.
func TestHistoryQuotaRejectsWriteAndPreservesEveryRevision(t *testing.T) {
	for _, commits := range []int{20, 350} {
		t.Run(fmt.Sprint(commits), func(t *testing.T) {
			s, db, a, k := fixture(t)
			e := mapEngine{}
			initial := Request{MutationID: "import-1", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: []byte(`{}`)}}
			if _, err := s.Execute(t.Context(), k, initial, a, e); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < commits; i++ {
				if _, err := s.Execute(t.Context(), k, bigEdit(i, strings.Repeat("x", 4000)), a, e); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Read(t.Context(), k, a)
			if err != nil {
				t.Fatal(err)
			}
			bytesBefore := historyBytes(t, s, k.PageID)
			s.options.MaxHistoryBytes = bytesBefore
			if _, err = s.Execute(t.Context(), k, bigEdit(commits, "quota-write"), a, e); !errors.Is(err, ErrQuota) {
				t.Fatalf("full history must reject the write, got %v", err)
			}
			after, err := s.Read(t.Context(), k, a)
			if err != nil || after.Rev != before.Rev || string(after.Snapshot) != string(before.Snapshot) {
				t.Fatalf("head changed after rejected write: %+v %v", after, err)
			}
			if got := historyBytes(t, s, k.PageID); got != bytesBefore {
				t.Fatalf("history changed from %d to %d", bytesBefore, got)
			}
			for rev := int64(1); rev <= before.Rev; rev++ {
				replayRevision(t, db.DB, e, k, rev)
			}
			var audits, outbox int64
			if err = db.QueryRow(`SELECT count(*) FROM gridcraft_audit WHERE page_id=?`, k.PageID).Scan(&audits); err != nil || audits != before.Rev {
				t.Fatalf("audit changed: %d %v", audits, err)
			}
			if err = db.QueryRow(`SELECT count(*) FROM gridcraft_outbox WHERE page_id=?`, k.PageID).Scan(&outbox); err != nil || outbox != before.Rev {
				t.Fatalf("outbox changed: %d %v", outbox, err)
			}
			// Refusal is not a receipt: retry can succeed when capacity is explicitly raised.
			s.options.MaxHistoryBytes = bytesBefore + 10000
			if receipt, err := s.Execute(t.Context(), k, bigEdit(commits, "quota-write"), a, e); err != nil || receipt.Rev != before.Rev+1 {
				t.Fatalf("retry after raising quota: %+v %v", receipt, err)
			}
		})
	}
}

func TestCheckpointQuotaRejectsWriteWithoutPruning(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	s.options.MaxRevisions = 1
	request := Request{MutationID: "import-2", MandateID: "human-import", BaseRev: 1, Generation: 1, Command: Command{Kind: "import", Snapshot: []byte(`{"sheets":[{}]}`)}}
	if _, err := s.Execute(t.Context(), k, request, a, e); !errors.Is(err, ErrQuota) {
		t.Fatalf("checkpoint quota: %v", err)
	}
	replayRevision(t, db.DB, e, k, 1)
}
