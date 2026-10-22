package appengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// Cell-level concurrency: a mutation based on an older revision is rebased
// onto the head unless another writer changed one of its target cells, an
// import intervened, or the window exceeds MaxRebaseWindow revisions.
func TestCellLevelConcurrencyAgainstOtherWriters(t *testing.T) {
	agent := func(context.Context, *sql.Tx) (Principal, error) {
		return Principal{AuthorityID: "agent:a:run:r:mandate:m", MandateID: "m", Targets: []string{"Sheet1!B2"}}, nil
	}
	cellSet := func(id, session, cell string, base int64) Request {
		return Request{MutationID: id, MandateID: "human-import", SessionID: session, BaseRev: base, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: cell, Input: id}}
	}
	type step struct {
		auth    string // "owner" or "agent"
		request Request
	}
	for _, test := range []struct {
		name      string
		before    []step
		mutation  step
		wantRev   int64
		conflicts []CellConflict
		snapshot  bool
		wantErr   error
	}{
		{name: "other cell other session is rebased",
			before:   []step{{"owner", cellSet("a", "s1", "C3", 1)}},
			mutation: step{"owner", cellSet("b", "s2", "B2", 1)}, wantRev: 3},
		{name: "same cell other session conflicts",
			before:    []step{{"owner", cellSet("a", "s1", "B2", 1)}},
			mutation:  step{"owner", cellSet("b", "s2", "B2", 1)},
			conflicts: []CellConflict{{Sheet: "Sheet1", Cell: "B2", Input: "a", Rev: 2, AuthorityID: "user:owner"}}, wantErr: ErrPrecondition},
		{name: "same cell own session is rebased",
			before:   []step{{"owner", cellSet("a", "s1", "B2", 1)}},
			mutation: step{"owner", cellSet("b", "s1", "B2", 1)}, wantRev: 3},
		{name: "import inside the window requires a snapshot",
			before:   []step{{"owner", Request{MutationID: "reimport", MandateID: "human-import", BaseRev: 1, Generation: 1, Command: Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[{}]}`)}}}},
			mutation: step{"owner", cellSet("b", "s2", "C3", 1)}, snapshot: true, wantErr: ErrPrecondition},
		{name: "import still requires the current head",
			before:   []step{{"owner", cellSet("a", "s1", "C3", 1)}},
			mutation: step{"owner", Request{MutationID: "stale-import", MandateID: "human-import", BaseRev: 1, Generation: 1, Command: Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[{}]}`)}}}, snapshot: true, wantErr: ErrPrecondition},
		// Retrying the same packet can never succeed in these cases, so the
		// client is told to reread instead of retrying.
		{name: "base ahead of head",
			mutation: step{"owner", cellSet("b", "s2", "C3", 2)}, snapshot: true, wantErr: ErrPrecondition},
		{name: "other generation",
			mutation: step{"owner", func() Request { r := cellSet("b", "s2", "C3", 1); r.Generation = 2; return r }()}, snapshot: true, wantErr: ErrPrecondition},
		// A conflict reports the cell's input in the head, here the later own
		// write, not the other writer's historical input.
		{name: "conflict reports the input in head",
			before:    []step{{"owner", cellSet("a", "s2", "B2", 1)}, {"owner", cellSet("c", "s1", "B2", 2)}},
			mutation:  step{"owner", cellSet("d", "s1", "B2", 1)},
			conflicts: []CellConflict{{Sheet: "Sheet1", Cell: "B2", Input: "c", Rev: 3, AuthorityID: "user:owner"}}, wantErr: ErrPrecondition},
		{name: "agent after a human commit to another cell",
			before:   []step{{"owner", cellSet("a", "s1", "C3", 1)}},
			mutation: step{"agent", Request{MutationID: "agent-1", MandateID: "m", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "99"}}}, wantRev: 3},
		{name: "agent after a human overwrote its target",
			before:    []step{{"owner", cellSet("a", "s1", "B2", 1)}},
			mutation:  step{"agent", Request{MutationID: "agent-1", MandateID: "m", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "99"}}},
			conflicts: []CellConflict{{Sheet: "Sheet1", Cell: "B2", Input: "a", Rev: 2, AuthorityID: "user:owner"}}, wantErr: ErrPrecondition},
		{name: "agent keeps rebasing its own target",
			before:   []step{{"agent", Request{MutationID: "agent-0", MandateID: "m", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "1"}}}},
			mutation: step{"agent", Request{MutationID: "agent-1", MandateID: "m", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "99"}}}, wantRev: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, owner, k := fixture(t)
			e := &fakeEngine{}
			imported(t, s, owner, k, e)
			auth := map[string]Authorize{"owner": owner, "agent": agent}
			for _, b := range test.before {
				if _, err := s.Execute(t.Context(), k, b.request, auth[b.auth], e); err != nil {
					t.Fatalf("setup %s: %v", b.request.MutationID, err)
				}
			}
			calls := e.calls.Load()
			receipt, err := s.Execute(t.Context(), k, test.mutation.request, auth[test.mutation.auth], e)
			if test.wantErr == nil {
				if err != nil || receipt.Rev != test.wantRev {
					t.Fatalf("receipt=%+v err=%v", receipt, err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("got %v want %v", err, test.wantErr)
			}
			if e.calls.Load() != calls {
				t.Fatal("engine ran for a rejected precondition")
			}
			var precondition *PreconditionError
			if !errors.As(err, &precondition) {
				t.Fatalf("no conflict detail: %v", err)
			}
			head, _ := s.Read(t.Context(), k, owner)
			if precondition.HeadRev != head.Rev || precondition.SnapshotRequired != test.snapshot || !reflect.DeepEqual(precondition.Conflicts, test.conflicts) {
				t.Fatalf("detail %+v head %d", precondition, head.Rev)
			}
		})
	}
}

func TestRebaseWindowIsBounded(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	for i := 0; i <= MaxRebaseWindow; i++ {
		r := edit()
		r.MutationID = fmt.Sprintf("fill-%d", i)
		r.BaseRev = int64(i + 1)
		if _, err := s.Execute(t.Context(), k, r, a, e); err != nil {
			t.Fatal(err)
		}
	}
	late := Request{MutationID: "late", MandateID: "human-import", SessionID: "other", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "Z9", Input: "1"}}
	var precondition *PreconditionError
	if _, err := s.Execute(t.Context(), k, late, a, e); !errors.As(err, &precondition) || !precondition.SnapshotRequired {
		t.Fatalf("window: %v", err)
	}
	late.BaseRev = 2 // exactly MaxRebaseWindow revisions behind
	if r, err := s.Execute(t.Context(), k, late, a, e); err != nil || r.Rev != MaxRebaseWindow+3 {
		t.Fatalf("edge: %+v %v", r, err)
	}
}

// The session is part of the request fingerprint: the same mutation ID from
// another session is a conflict, not a replay of someone else's receipt.
func TestSessionIsPartOfPayloadIdentity(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	r := edit()
	r.SessionID = "s1"
	if _, err := s.Execute(t.Context(), k, r, a, e); err != nil {
		t.Fatal(err)
	}
	r.SessionID = "s2"
	if _, err := s.Execute(t.Context(), k, r, a, e); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-session replay: %v", err)
	}
}

// A mutation rejected because the head moved during its candidate leaves no
// blocking operation row: the client retries the same ID and bytes.
func TestPreconditionFailureAllowsExactRetry(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	moved := false
	e.prepare = func(_ context.Context, b Binding, _ State, c Command) (Candidate, error) {
		if !moved {
			moved = true
			// Another writer commits C3 while this candidate is computed.
			if _, err := db.Exec(`UPDATE gridcraft_heads SET head_rev=2 WHERE page_id=?`, k.PageID); err != nil {
				t.Error(err)
			}
			if _, err := db.Exec(`INSERT INTO gridcraft_audit(page_id,rev,authority_id,mutation_id,mandate_id,base_rev,receipt,kind,writer,command) VALUES(?,2,'user:owner','other','human-import',1,'{}','cells','user:owner#session:s2','{"kind":"cells","cells":[{"sheet":"Sheet1","cell":"C3","input":"1"}]}')`, k.PageID); err != nil {
				t.Error(err)
			}
		}
		return NewCandidate(b, json.RawMessage(`{"sheets":[]}`), []string{c.Sheet + "!" + c.Cell}), nil
	}
	r := edit()
	r.SessionID = "s1"
	if _, err := s.Execute(t.Context(), k, r, a, e); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("moved head: %v", err)
	}
	receipt, err := s.Execute(t.Context(), k, r, a, e)
	if err != nil || receipt.Rev != 3 {
		t.Fatalf("retry %+v %v", receipt, err)
	}
}
