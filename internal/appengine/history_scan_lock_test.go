package appengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The prescan must be a real read transaction: the database runs with
// _txlock=immediate, so an ordinary transaction would take the database-wide
// write lock and an unrelated writer would wait out the busy timeout.
func TestPrescanDoesNotHoldTheWriteLock(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	for i := 0; i < 5; i++ {
		if _, err := s.Execute(t.Context(), k, bigEdit(i, "v"), a, e); err != nil {
			t.Fatal(err)
		}
	}
	var blocked error
	var waited time.Duration
	s.windowScanned = func(writer bool, _ int64) {
		if writer {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, blocked = db.ExecContext(ctx, `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`)
		waited = time.Since(start)
	}
	late := Request{MutationID: "s2:1", MandateID: "human-import", SessionID: "s2", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "Z9", Input: "1"}}
	if _, err := s.Execute(t.Context(), k, late, a, e); err != nil {
		t.Fatalf("stale edit: %v", err)
	}
	if blocked != nil || waited > time.Second {
		t.Fatalf("unrelated write during prescan: err=%v waited=%v", blocked, waited)
	}
}

// A forced checkpoint that prunes the window after the prescan read it must
// still make the stale edit reread, exactly as without the prescan.
func TestPruneAfterPrescanRequiresSnapshot(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	for i := 0; i < 10; i++ {
		if _, err := s.Execute(t.Context(), k, bigEdit(i, strings.Repeat("x", 4000)), a, e); err != nil {
			t.Fatal(err)
		}
	}
	fired := false
	var nested error
	s.windowScanned = func(writer bool, _ int64) {
		if writer || fired {
			return
		}
		fired = true
		s.options.MaxHistoryBytes = 9000
		_, nested = s.Execute(context.Background(), k, bigEdit(10, strings.Repeat("y", 4000)), a, e)
	}
	late := Request{MutationID: "s3:1", MandateID: "human-import", SessionID: "s3", BaseRev: 5, Generation: 1, Command: Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "Z9", Input: "1"}}
	_, err := s.Execute(t.Context(), k, late, a, e)
	if nested != nil {
		t.Fatalf("forced prune: %v", nested)
	}
	var p *PreconditionError
	if !errors.As(err, &p) || !p.SnapshotRequired {
		t.Fatalf("stale edit across a pruned window: %v", err)
	}
}
