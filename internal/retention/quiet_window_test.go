package retention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

// A sweep in flight when a backup's quiet window starts closing steps out of
// the gate at its next batch boundary, so the window holds without waiting
// for the whole sweep — and deletes nothing until the window is released.
func TestSweepStepsAsideForAClosingQuietWindow(t *testing.T) {
	db := newDB(t)
	for i := 0; i < 3; i++ {
		exec(t, db, fmt.Sprintf(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, blocking, created_at, updated_at, read_at, resolved_at)
			VALUES ('old-%d','ws1','escalation','src-%d','t','resolved',1,'%s','%s','%s','%s')`, i, i, ago(40), ago(40), ago(40), ago(40)))
	}
	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	ctrl := quiesce.Default()
	sweeper, ok := ctrl.Enter(context.Background())
	if !ok {
		t.Fatal("admission closed with no window")
	}
	defer sweeper.Leave()

	type result struct {
		w   *quiesce.Window
		err error
	}
	begun := make(chan result, 1)
	go func() {
		w, err := ctrl.Begin(context.Background(), quiesce.Options{HoldCap: time.Minute, DrainTimeout: time.Minute})
		begun <- result{w, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !ctrl.Holding() {
		if time.Now().After(deadline) {
			t.Fatal("the window never started closing")
		}
		time.Sleep(time.Millisecond)
	}

	swept := make(chan int, 1)
	go func() {
		n, _, err := SweepInbox(sweeper.Context(), db, "ws1", 30, time.Now())
		if err != nil {
			t.Error(err)
		}
		swept <- n
	}()

	// The window can only hold once the sweep stepped out at its batch
	// boundary, before its first delete.
	r := <-begun
	if r.err != nil {
		t.Fatalf("the window did not hold with a sweep in flight: %v", r.err)
	}
	if got := count(); got != 3 {
		t.Fatalf("rows = %d while the window holds, want 3 (the sweep deleted inside it)", got)
	}
	select {
	case <-swept:
		t.Fatal("the sweep finished inside the held window")
	default:
	}
	r.w.Release()
	if n := <-swept; n != 3 {
		t.Fatalf("swept %d after release, want 3", n)
	}
	if ctrl.Writers() != 1 {
		t.Fatalf("writers = %d after the sweep resumed, want the sweeper back inside", ctrl.Writers())
	}
}
