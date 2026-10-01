package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

// A dispatch a mission tick starts is admitted by the quiet window before
// its goroutine runs: counted as busy at once, so a window closing while the
// goroutine is being scheduled cannot hold over it.
func TestAdmitDispatchCountsBeforeTheGoroutineRuns(t *testing.T) {
	c := quiesce.Default()
	wr, ok := c.Enter(context.Background())
	if !ok {
		t.Fatal("writer refused with no window")
	}
	begun := make(chan error, 1)
	go func() {
		w, err := c.Begin(context.Background(), quiesce.Options{
			BusyWait: 20 * time.Millisecond, Poll: time.Millisecond,
			HoldCap: time.Minute, DrainTimeout: time.Minute,
		})
		if err == nil {
			w.Release()
		}
		begun <- err
	}()
	for !c.Holding() {
		time.Sleep(time.Millisecond) // wait for "closing"; no assertion depends on it
	}
	admit := admitDispatch(context.WithoutCancel(wr.Context()))
	if c.Runs() != 1 {
		t.Fatalf("Runs = %d right after admitDispatch, want 1", c.Runs())
	}
	wr.Leave()
	if err := <-begun; !errors.Is(err, quiesce.ErrBusy) {
		t.Fatalf("Begin: %v, want ErrBusy (the admitted dispatch keeps the window from holding)", err)
	}
	_, done := admit()
	done()
	if c.Runs() != 0 {
		t.Fatalf("Runs = %d after done, want 0", c.Runs())
	}
}

// Outside a tick (no writer inside) and with a window open, the dispatch
// waits for the release before it starts.
func TestAdmitDispatchWaitsOutAnOpenWindow(t *testing.T) {
	c := quiesce.Default()
	w, err := c.Begin(context.Background(), quiesce.Options{BusyWait: time.Second, HoldCap: time.Minute, DrainTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	admit := admitDispatch(context.Background())
	got := make(chan func(), 1)
	go func() {
		_, done := admit()
		got <- done
	}()
	select {
	case <-got:
		w.Release()
		t.Fatal("a dispatch started while the window was held")
	case <-time.After(20 * time.Millisecond):
	}
	w.Release()
	(<-got)()
}
