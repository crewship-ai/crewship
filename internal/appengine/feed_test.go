package appengine

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

// The commits feed serves confirmed revisions after a revision in ascending
// order. Session and mutation IDs are returned only for the caller's own
// revisions; other writers stay internal authorities the host renders by label.
func TestCommitsFeedServesRetainedHistoryInOrder(t *testing.T) {
	s, db, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	agent := func(context.Context, *sql.Tx) (Principal, error) {
		return Principal{AuthorityID: "agent:a:run:r:mandate:m", MandateID: "m", Targets: []string{"Sheet1!C3"}}, nil
	}
	human := Request{MutationID: "s1:1", MandateID: "human-import", SessionID: "s1", BaseRev: 1, Generation: 1, Command: Command{Kind: "cells.set", Cells: []CellInput{{"Sheet1", "B2", "7"}}}}
	if _, err := s.Execute(t.Context(), k, human, owner, e); err != nil {
		t.Fatal(err)
	}
	// This feed fixture writes a constant independent of previous cell values.
	byAgent := Request{ReadSet: &ReadSet{}, MutationID: "agent-1", MandateID: "m", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "C3", Input: "9"}}
	if _, err := s.Execute(t.Context(), k, byAgent, agent, e); err != nil {
		t.Fatal(err)
	}
	feed, err := s.Commits(t.Context(), k, 0, MaxFeedLimit, owner)
	if err != nil {
		t.Fatal(err)
	}
	want := Feed{HeadRev: 3, Generation: 1, Commits: []FeedCommit{
		{Rev: 1, Kind: "snapshot", Own: true, AuthorityID: "user:owner", MutationID: "import-1"},
		{Rev: 2, Kind: "cells", Own: true, AuthorityID: "user:owner", SessionID: "s1", MutationID: "s1:1", Cells: []CellInput{{"Sheet1", "B2", "7"}}},
		{Rev: 3, Kind: "cells", AuthorityID: "agent:a:run:r:mandate:m", Cells: []CellInput{{"Sheet1", "C3", "9"}}},
	}}
	if !reflect.DeepEqual(feed, want) {
		t.Fatalf("owner feed\n got %+v\nwant %+v", feed, want)
	}
	asAgent, err := s.Commits(t.Context(), k, 1, MaxFeedLimit, agent)
	if err != nil {
		t.Fatal(err)
	}
	if c := asAgent.Commits; len(c) != 2 || c[0].Own || c[0].SessionID != "" || c[0].MutationID != "" || !c[1].Own || c[1].MutationID != "agent-1" {
		t.Fatalf("own is per caller: %+v", c)
	}
	page, err := s.Commits(t.Context(), k, 1, 1, owner)
	if err != nil || len(page.Commits) != 1 || page.Commits[0].Rev != 2 || page.HeadRev != 3 {
		t.Fatalf("limited page %+v %v", page, err)
	}
	if empty, err := s.Commits(t.Context(), k, 3, 10, owner); err != nil || empty.Commits == nil || len(empty.Commits) != 0 {
		t.Fatalf("caught up %+v %v", empty, err)
	}
	for name, args := range map[string][2]int64{"negative since": {-1, 10}, "zero limit": {0, 0}, "limit over max": {0, MaxFeedLimit + 1}} {
		if _, err := s.Commits(t.Context(), k, args[0], int(args[1]), owner); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var gone *HistoryGoneError
	if _, err := s.Commits(t.Context(), k, 4, 10, owner); !errors.As(err, &gone) || gone.HeadRev != 3 || gone.MinSince != 0 || !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("since after head: %v", err)
	}
	denied := func(context.Context, *sql.Tx) (Principal, error) { return Principal{}, ErrForbidden }
	if _, err := s.Commits(t.Context(), k, 0, 10, denied); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized feed: %v", err)
	}

	// Pruned history (and rows written before command history) cannot be
	// replayed: the feed reports the oldest since it still serves.
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = pruneHistory(t.Context(), tx, k.PageID, 2); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, since := range []int64{0, 1} {
		feed, err := s.Commits(t.Context(), k, since, 10, owner)
		if !errors.As(err, &gone) || gone.MinSince != 2 || gone.HeadRev != 3 || gone.Generation != 1 || feed.MinSince != 2 {
			t.Fatalf("since %d inside pruned history: %+v %v", since, feed, err)
		}
	}
	retained, err := s.Commits(t.Context(), k, 2, 10, owner)
	if err != nil || retained.MinSince != 2 || len(retained.Commits) != 1 || retained.Commits[0].Rev != 3 {
		t.Fatalf("retained window %+v %v", retained, err)
	}
}

// Every committed revision is claimed for one live push and its outbox row is
// marked sent; a second claim returns nothing. Revisions of a Page that is no
// longer a personal workbook Page are marked sent without a push.
func TestClaimCommittedMarksOutboxSentOnce(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	cells := Request{MutationID: "s1:1", MandateID: "human-import", SessionID: "s1", BaseRev: 1, Generation: 1, Command: Command{Kind: "cells.set", Cells: []CellInput{{"Sheet1", "B2", "1"}, {"Sheet1", "B3", "2"}}}}
	if _, err := s.Execute(t.Context(), k, cells, a, e); err != nil {
		t.Fatal(err)
	}
	if other, err := s.ClaimCommitted(t.Context(), "other-page", 10); err != nil || len(other) != 0 {
		t.Fatalf("other page claim %+v %v", other, err)
	}
	claimed, err := s.ClaimCommitted(t.Context(), k.PageID, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []Committed{
		{WorkspaceID: "ws", PageID: "page", Rev: 1, Generation: 1, Kind: "snapshot", Deliverable: true},
		{WorkspaceID: "ws", PageID: "page", Rev: 2, Generation: 1, Kind: "cells", Cells: 2, Deliverable: true},
	}
	if !reflect.DeepEqual(claimed, want) {
		t.Fatalf("claimed\n got %+v\nwant %+v", claimed, want)
	}
	if again, err := s.ClaimCommitted(t.Context(), "", 10); err != nil || len(again) != 0 {
		t.Fatalf("second claim %+v %v", again, err)
	}
	var pending int
	if err = db.QueryRow(`SELECT count(*) FROM gridcraft_outbox WHERE state<>'sent'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending=%d %v", pending, err)
	}
	status, err := s.Status(t.Context(), k, "s1:1", a)
	if err != nil || status.Delivery != "sent" {
		t.Fatalf("status %+v %v", status, err)
	}
	next := edit()
	next.BaseRev = 2
	if _, err = s.Execute(t.Context(), k, next, a, e); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pages SET private_audience=0 WHERE id='page'`); err != nil {
		t.Fatal(err)
	}
	// A personal workbook its owner published to named editors still pushes.
	claimed, err = s.ClaimCommitted(t.Context(), "", 10)
	if err != nil || len(claimed) != 1 || claimed[0].Rev != 3 || !claimed[0].Deliverable {
		t.Fatalf("published page claim %+v %v", claimed, err)
	}
	// A Page that is no longer personal is marked sent without a push.
	last := edit()
	last.MutationID, last.BaseRev = "s1:3", 3
	if _, err = s.Execute(t.Context(), k, last, a, e); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','ws','Crew','crew')`,
		`UPDATE pages SET owner_user_id=NULL, owner_crew_id='crew' WHERE id='page'`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err = s.ClaimCommitted(t.Context(), "", 10)
	if err != nil || len(claimed) != 1 || claimed[0].Rev != 4 || claimed[0].Deliverable {
		t.Fatalf("crew page claim %+v %v", claimed, err)
	}
	disabled := New(db.DB, Options{})
	if _, err = disabled.ClaimCommitted(t.Context(), "", 10); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled claim: %v", err)
	}
}
