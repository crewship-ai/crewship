package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var (
	proposalBase     = strings.Repeat("a", 64)
	proposalProposed = strings.Repeat("b", 64)
	proposalParent   = strings.Repeat("c", 64)
)

// proposalFixture is a schema-4 document whose projection is a list block
// "parent" enclosing paragraph "one", plus an unrelated sibling "two".
func proposalFixture(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	s, db, _ := fixture(t, Options{})
	p := `[{"id":"parent","hash":"` + proposalParent + `"},{"id":"one","hash":"` + proposalBase + `"},{"id":"two","hash":"h2"}]`
	if _, e := db.ExecContext(ctx, `UPDATE documents SET schema_version=4,projection=? WHERE workspace_id=? AND page_id=?`, []byte(p), key.WorkspaceID, key.PageID); e != nil {
		t.Fatal(e)
	}
	return s, db.DB
}

func proposalInput(id string, guard MutationGuard) ProposalInput {
	generation := int64(0)
	return ProposalInput{ID: id, Fingerprint: "f-" + id, RunID: "run-1", Target: "one", BaseHash: proposalBase, ProposedHash: proposalProposed, Text: "New **text**",
		Ancestors: []string{"parent"}, Base: json.RawMessage(`{"type":"paragraph"}`), Proposed: json.RawMessage(`{"type":"paragraph","content":[]}`), IfRev: 0, Generation: &generation, Guard: guard}
}

func pass(context.Context, *sql.Tx, string, State) error { return nil }

func acceptCommit(l Lease, id string, projection string, guard MutationGuard) Commit {
	return Commit{Lease: l, ID: id, Fingerprint: "f-" + id, IfRev: 0, Schema: 4, Update: []byte("u"), CandidateSnapshot: []byte("c"), Projection: json.RawMessage(projection), IfHashes: map[string]string{"one": proposalBase}, Kind: "human", Guard: guard}
}

func TestProposalStoreReplayAndPreconditions(t *testing.T) {
	s, _ := proposalFixture(t)
	r, e := s.Propose(ctx, key, proposalInput("p1", pass), allow)
	if e != nil || r.Kind != "proposal" || r.Rev != 0 || r.Actor != "server-actor" {
		t.Fatalf("propose: %+v %v", r, e)
	}
	again, e := s.Propose(ctx, key, proposalInput("p1", pass), allow)
	if e != nil || again != r {
		t.Fatalf("exact retry: %+v %v", again, e)
	}
	changed := proposalInput("p1", pass)
	changed.Text = "Other"
	if _, e = s.Propose(ctx, key, changed, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("changed retry: %v", e)
	}
	other := func(context.Context, *sql.Tx) (string, error) { return "other-actor", nil }
	if _, e = s.Propose(ctx, key, proposalInput("p1", pass), other); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("other actor replayed: %v", e)
	}
	denied := errors.New("mandate refused")
	if _, e = s.Propose(ctx, key, proposalInput("p2", func(context.Context, *sql.Tx, string, State) error { return denied }), allow); !errors.Is(e, denied) {
		t.Fatalf("guard ignored: %v", e)
	}
	stale := proposalInput("p3", pass)
	stale.IfRev = 1
	if _, e = s.Propose(ctx, key, stale, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale rev: %v", e)
	}
	noGuard := proposalInput("p4", nil)
	if _, e = s.Propose(ctx, key, noGuard, allow); !errors.Is(e, ErrInvalidProposal) {
		t.Fatalf("guardless proposal: %v", e)
	}
	list, e := s.Proposals(ctx, key, allow)
	if e != nil || len(list) != 1 || list[0].ID != "p1" || list[0].Status != "open" || list[0].Base != nil || list[0].Schema != 4 {
		t.Fatalf("list: %+v %v", list, e)
	}
	one, e := s.ProposalRead(ctx, key, "p1", allow)
	if e != nil || string(one.Proposed) != `{"type":"paragraph","content":[]}` || one.Text != "New **text**" {
		t.Fatalf("read: %+v %v", one, e)
	}
	if _, e = s.ProposalRead(ctx, key, "missing", allow); !errors.Is(e, ErrNotFound) {
		t.Fatalf("missing: %v", e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 0 || string(v.Snapshot) != "baseline" {
		t.Fatalf("proposal changed the document: %+v %v", v, e)
	}
}

func TestProposalRequiresSchemaFour(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	if _, e := s.Propose(ctx, key, proposalInput("p1", pass), allow); !errors.Is(e, ErrSchema) {
		t.Fatalf("schema 1 accepted a proposal: %v", e)
	}
}

func TestAcceptProposalGuardBindsCommitToProposal(t *testing.T) {
	good := `[{"id":"parent","hash":"` + strings.Repeat("d", 64) + `"},{"id":"one","hash":"` + proposalProposed + `"},{"id":"two","hash":"h2"}]`
	tests := []struct {
		name       string
		projection string
	}{
		{"wrong proposed hash", `[{"id":"parent","hash":"` + proposalParent + `"},{"id":"one","hash":"` + strings.Repeat("e", 64) + `"},{"id":"two","hash":"h2"}]`},
		{"sibling changed", `[{"id":"parent","hash":"` + proposalParent + `"},{"id":"one","hash":"` + proposalProposed + `"},{"id":"two","hash":"h3"}]`},
		{"block removed", `[{"id":"parent","hash":"` + proposalParent + `"},{"id":"one","hash":"` + proposalProposed + `"}]`},
		{"blocks reordered", `[{"id":"one","hash":"` + proposalProposed + `"},{"id":"parent","hash":"` + proposalParent + `"},{"id":"two","hash":"h2"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, db := proposalFixture(t)
			if _, e := s.Propose(ctx, key, proposalInput("p1", pass), allow); e != nil {
				t.Fatal(e)
			}
			l := acquire(t, s)
			if _, e := s.Commit(ctx, key, acceptCommit(l, "accept", tt.projection, AcceptProposalGuard(key, "p1", "accept", json.RawMessage(tt.projection))), allow); !errors.Is(e, ErrConflict) {
				t.Fatalf("bad candidate accepted: %v", e)
			}
			var status string
			if e := db.QueryRowContext(ctx, `SELECT status FROM document_proposals WHERE id='p1'`).Scan(&status); e != nil || status != "open" {
				t.Fatalf("failed accept decided the proposal: %q %v", status, e)
			}
		})
	}
	t.Run("accepted once", func(t *testing.T) {
		s, _ := proposalFixture(t)
		if _, e := s.Propose(ctx, key, proposalInput("p1", pass), allow); e != nil {
			t.Fatal(e)
		}
		l := acquire(t, s)
		r, e := s.Commit(ctx, key, acceptCommit(l, "accept", good, AcceptProposalGuard(key, "p1", "accept", json.RawMessage(good))), allow)
		if e != nil || r.Rev != 1 || r.Kind != "human" {
			t.Fatalf("accept: %+v %v", r, e)
		}
		p, e := s.ProposalRead(ctx, key, "p1", allow)
		if e != nil || p.Status != "accepted" || p.AcceptedRev == nil || *p.AcceptedRev != 1 || p.DecidedBy != "server-actor" {
			t.Fatalf("accepted state: %+v %v", p, e)
		}
		next := acceptCommit(l, "accept-2", good, AcceptProposalGuard(key, "p1", "accept-2", json.RawMessage(good)))
		next.IfRev = 1
		next.IfHashes = nil
		if _, e = s.Commit(ctx, key, next, allow); !errors.Is(e, ErrConflict) {
			t.Fatalf("second accept: %v", e)
		}
		generation := int64(0)
		if _, e = s.RejectProposal(ctx, key, "reject", "f", "p1", 1, &generation, allow); !errors.Is(e, ErrConflict) {
			t.Fatalf("decided proposal rejected: %v", e)
		}
	})
	t.Run("missing proposal", func(t *testing.T) {
		s, _ := proposalFixture(t)
		l := acquire(t, s)
		if _, e := s.Commit(ctx, key, acceptCommit(l, "accept", good, AcceptProposalGuard(key, "none", "accept", json.RawMessage(good))), allow); !errors.Is(e, ErrNotFound) {
			t.Fatalf("missing proposal: %v", e)
		}
	})
}

func TestRejectProposalClosesOnce(t *testing.T) {
	s, db := proposalFixture(t)
	if _, e := s.Propose(ctx, key, proposalInput("p1", pass), allow); e != nil {
		t.Fatal(e)
	}
	generation := int64(0)
	if _, e := s.RejectProposal(ctx, key, "reject", "f", "p1", 1, &generation, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale rev rejected: %v", e)
	}
	r, e := s.RejectProposal(ctx, key, "reject", "f", "p1", 0, &generation, allow)
	if e != nil || r.Kind != "proposal-reject" {
		t.Fatalf("reject: %+v %v", r, e)
	}
	again, e := s.RejectProposal(ctx, key, "reject", "f", "p1", 0, &generation, allow)
	if e != nil || again != r {
		t.Fatalf("retry: %+v %v", again, e)
	}
	if _, e = s.RejectProposal(ctx, key, "reject-2", "f", "p1", 0, &generation, allow); !errors.Is(e, ErrConflict) {
		t.Fatalf("second reject: %v", e)
	}
	if _, e = db.ExecContext(ctx, `UPDATE document_proposals SET text='tampered' WHERE id='p1'`); e == nil {
		t.Fatal("proposal content is mutable")
	}
	if _, e = db.ExecContext(ctx, `UPDATE document_proposals SET status='open',decided_by=NULL,decision_id=NULL WHERE id='p1'`); e == nil {
		t.Fatal("decided proposal reopened")
	}
}
