package appengine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// A principal with a history floor (an agent mandate's base revision) cannot
// read revisions at or before it: the feed answers as if that history were
// pruned, and reports the floor as min_since.
func TestCommitsFeedStopsAtThePrincipalsHistoryFloor(t *testing.T) {
	s, _, owner, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, owner, k, e)
	for i, v := range []string{"secret-old", "public-new"} {
		r := Request{MutationID: "s1:" + v, MandateID: "human-import", SessionID: "s1", BaseRev: int64(i + 1), Generation: 1, Command: Command{Kind: "cells.set", Cells: []CellInput{{"Sheet1", "C3", v}}}}
		if _, err := s.Execute(t.Context(), k, r, owner, e); err != nil {
			t.Fatal(err)
		}
	}
	agent := func(context.Context, *sql.Tx) (Principal, error) {
		return Principal{AuthorityID: "agent:a:run:r:mandate:m", MandateID: "m", Targets: []string{"Sheet1!B2"}, HistoryFloor: 3}, nil
	}
	var gone *HistoryGoneError
	for _, since := range []int64{0, 1, 2} {
		feed, err := s.Commits(t.Context(), k, since, 10, agent)
		if !errors.As(err, &gone) || gone.MinSince != 3 || gone.HeadRev != 3 || feed.MinSince != 3 || len(feed.Commits) != 0 {
			t.Fatalf("since %d below the floor: %+v %v", since, feed, err)
		}
	}
	feed, err := s.Commits(t.Context(), k, 3, 10, agent)
	if err != nil || feed.MinSince != 3 || len(feed.Commits) != 0 {
		t.Fatalf("at the floor: %+v %v", feed, err)
	}
	// Pruned history above the floor still wins.
	if full, err := s.Commits(t.Context(), k, 0, 10, owner); err != nil || full.MinSince != 0 || len(full.Commits) != 3 {
		t.Fatalf("owner without a floor: %+v %v", full, err)
	}
}

// With nothing pending, a claim must not take the database-wide write lock:
// the sweeper runs every few seconds on every server, editor on or off.
func TestClaimCommittedWithNothingPendingTakesNoWriteLock(t *testing.T) {
	s, db, a, k := fixture(t)
	imported(t, s, a, k, &fakeEngine{})
	if _, err := s.ClaimCommitted(t.Context(), "", 10); err != nil {
		t.Fatal(err)
	}
	holder, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err = holder.Exec(`UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	for _, page := range []string{"", k.PageID} {
		if claimed, err := s.ClaimCommitted(ctx, page, 10); err != nil || len(claimed) != 0 {
			t.Fatalf("idle claim %q: %+v %v", page, claimed, err)
		}
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("idle claims waited %v for the write lock", waited)
	}
}
