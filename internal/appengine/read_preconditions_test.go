package appengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

// Decode the wire form so the regression reproduces on the pre-read-set host.
func requestWithReadSet(t *testing.T, request Request, set string) Request {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["read_set"] = json.RawMessage(set)
	encoded, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var result Request
	if err = json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func readSetAgent(_ context.Context, _ *sql.Tx) (Principal, error) {
	return Principal{AuthorityID: "agent:test", MandateID: "derived", Targets: []string{"Sheet1!B1"}}, nil
}

func TestReadSetRejectsChangedInputOutsideWriteTarget(t *testing.T) {
	for _, set := range []string{
		`{"cells":[{"sheet":"Sheet1","cell":"A1","rev":1}]}`,
		`{"ranges":[{"sheet":"Sheet1","start":"A1","end":"A5","rev":1,"structure_rev":1}]}`,
	} {
		t.Run(set, func(t *testing.T) {
			s, _, owner, k := fixture(t)
			e := &fakeEngine{}
			imported(t, s, owner, k, e)
			human := bigEdit(0, "changed input")
			if _, err := s.Execute(t.Context(), k, human, owner, e); err != nil {
				t.Fatal(err)
			}
			agent := Request{MutationID: "derived-write", MandateID: "derived", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B1", Input: "stale answer"}}
			agent = requestWithReadSet(t, agent, set)
			calls := e.calls.Load()
			_, err := s.Execute(t.Context(), k, agent, readSetAgent, e)
			var precondition *PreconditionError
			if !errors.As(err, &precondition) {
				t.Fatalf("changed read dependency accepted: %v", err)
			}
			if e.calls.Load() != calls {
				t.Fatal("stale read reached engine")
			}
			state, err := s.Read(t.Context(), k, owner)
			if err != nil || state.Rev != 2 {
				t.Fatalf("rejected write moved head: %+v %v", state, err)
			}
		})
	}
}

func TestAgentWithoutReadSetConservativelyRequiresExactBase(t *testing.T) {
	s, _, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	if _, err := s.Execute(t.Context(), k, bigEdit(0, "changed input"), owner, e); err != nil {
		t.Fatal(err)
	}
	agent := Request{MutationID: "derived-write", MandateID: "derived", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B1", Input: "stale answer"}}
	if _, err := s.Execute(t.Context(), k, agent, readSetAgent, e); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("unspecified stale reads: %v", err)
	}
}

func TestReadSetAllowsUnchangedInputsAcrossIndependentEdit(t *testing.T) {
	s, _, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	human := bigEdit(0, "unrelated")
	human.Command.Cell = "C1"
	if _, err := s.Execute(t.Context(), k, human, owner, e); err != nil {
		t.Fatal(err)
	}
	agent := requestWithReadSet(t, Request{MutationID: "independent", MandateID: "derived", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B1", Input: "answer"}}, `{"cells":[{"sheet":"Sheet1","cell":"A1","rev":1}]}`)
	if receipt, err := s.Execute(t.Context(), k, agent, readSetAgent, e); err != nil || receipt.Rev != 3 {
		t.Fatalf("unchanged dependency: %+v %v", receipt, err)
	}
	// Idempotency includes the read dependency, not just the resulting command.
	changed := requestWithReadSet(t, agent, `{"cells":[{"sheet":"Sheet1","cell":"A2","rev":1}]}`)
	if _, err := s.Execute(t.Context(), k, changed, readSetAgent, e); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed read-set under same ID: %v", err)
	}
	if receipt, err := s.Execute(t.Context(), k, agent, readSetAgent, e); err != nil || receipt.Rev != 3 {
		t.Fatalf("receipt retry: %+v %v", receipt, err)
	}
}

// A structural import is available in the pilot. Row insertion itself is not
// a supported command, so this proves its import boundary rather than UI rows.
func TestReadSetRejectsStructureReplacedByImport(t *testing.T) {
	s, _, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	state, err := s.Read(t.Context(), k, owner)
	if err != nil || state.StructureRev != 1 {
		t.Fatalf("initial structure: %+v %v", state, err)
	}
	request := Request{MutationID: "replace", MandateID: "human-import", BaseRev: 1, Generation: 1, Command: Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[{"rows":["inserted","original"]}]}`)}}
	if _, err = s.Execute(t.Context(), k, request, owner, e); err != nil {
		t.Fatal(err)
	}
	state, err = s.Read(t.Context(), k, owner)
	if err != nil || state.StructureRev != 2 {
		t.Fatalf("replaced structure: %+v %v", state, err)
	}
	agent := requestWithReadSet(t, Request{MutationID: "old-region", MandateID: "derived", BaseRev: 2, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B1", Input: "answer"}}, `{"ranges":[{"sheet":"Sheet1","start":"A1","end":"A5","rev":2,"structure_rev":1}]}`)
	var precondition *PreconditionError
	if _, err = s.Execute(t.Context(), k, agent, readSetAgent, e); !errors.As(err, &precondition) || precondition.Reason != "read_structure_changed" {
		t.Fatalf("replaced structure: %v %+v", err, precondition)
	}
}

func TestReadSetRejectsMalformedAndUnverifiableReads(t *testing.T) {
	for _, set := range []string{
		`{"cells":[{"sheet":"Sheet1","cell":"A1","rev":0}]}`,
		`{"cells":[{"sheet":"Sheet1","cell":"a1","rev":1}]}`,
		`{"ranges":[{"sheet":"Sheet1","start":"A5","end":"A1","rev":1,"structure_rev":1}]}`,
		`{"ranges":[{"sheet":"Sheet1","start":"A1","end":"A5","rev":1,"structure_rev":2}]}`,
	} {
		t.Run(set, func(t *testing.T) {
			s, _, owner, k := fixture(t)
			e := &fakeEngine{}
			imported(t, s, owner, k, e)
			request := requestWithReadSet(t, Request{MutationID: "invalid-reads", MandateID: "derived", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B1", Input: "x"}}, set)
			if _, err := s.Execute(t.Context(), k, request, readSetAgent, e); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid read: %v", err)
			}
		})
	}
	s, _, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	request := requestWithReadSet(t, Request{MutationID: "future-reads", MandateID: "derived", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B1", Input: "x"}}, `{"cells":[{"sheet":"Sheet1","cell":"A1","rev":2}]}`)
	if _, err := s.Execute(t.Context(), k, request, readSetAgent, e); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("future read: %v", err)
	}
}

// Formula recalculation can change a read cell outside the authored command.
// The bounded audit may omit affected addresses, so it must fail closed too.
func TestReadSetIncludesDerivedRecalculation(t *testing.T) {
	s, _, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	e.prepare = func(_ context.Context, b Binding, _ State, command Command) (Candidate, error) {
		candidate := NewCandidate(b, json.RawMessage(`{"sheets":[{"D1":"updated calculation"}]}`), commandTargets(command))
		candidate.Derived = &Derived{Count: 101, Cells: []string{"Sheet1!D1"}}
		return candidate, nil
	}
	if _, err := s.Execute(t.Context(), k, bigEdit(0, "new calculation input"), owner, e); err != nil {
		t.Fatal(err)
	}
	request := requestWithReadSet(t, Request{MutationID: "derived-formula", MandateID: "derived", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B1", Input: "stale formula answer"}}, `{"cells":[{"sheet":"Sheet1","cell":"D1","rev":1}]}`)
	if _, err := s.Execute(t.Context(), k, request, readSetAgent, e); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("recalculated read ignored: %v", err)
	}
}
