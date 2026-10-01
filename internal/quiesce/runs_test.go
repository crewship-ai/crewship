package quiesce

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunAdmission(t *testing.T) {
	quick := Options{BusyWait: 20 * time.Millisecond, Poll: time.Millisecond, HoldCap: time.Minute, DrainTimeout: time.Minute}
	cases := []struct {
		name string
		run  func(t *testing.T, c *Controller)
	}{
		{
			name: "admitted with no window, counted until Done, Done is idempotent",
			run: func(t *testing.T, c *Controller) {
				r, ok := c.StartRun(context.Background())
				if !ok {
					t.Fatal("refused with no window")
				}
				if c.Runs() != 1 {
					t.Fatalf("Runs = %d, want 1", c.Runs())
				}
				r.Done()
				r.Done()
				if c.Runs() != 0 {
					t.Fatalf("Runs = %d after Done, want 0", c.Runs())
				}
			},
		},
		{
			name: "refused while held",
			run: func(t *testing.T, c *Controller) {
				w, err := c.Begin(context.Background(), quick)
				if err != nil {
					t.Fatal(err)
				}
				defer w.Release()
				if _, ok := c.StartRun(context.Background()); ok {
					t.Fatal("a run was admitted while the window was held")
				}
			},
		},
		{
			name: "refused while closing, a writer's child admitted",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				res := beginAsync(c, quick)
				waitUntil(t, "closing", c.Holding)
				if _, ok := c.StartRun(context.Background()); ok {
					t.Fatal("a run was admitted while the window was closing")
				}
				child, ok := c.StartRun(wr.Context())
				if !ok {
					t.Fatal("a run started by a writer inside the gate was refused")
				}
				wr.Leave()
				r := <-res
				if r.err == nil {
					r.w.Release()
					t.Fatal("the window held while the writer's run was admitted")
				}
				if !errors.Is(r.err, ErrBusy) {
					t.Fatalf("Begin: %v, want ErrBusy", r.err)
				}
				child.Done()
			},
		},
		{
			name: "a nested run is admitted as its parent's child",
			run: func(t *testing.T, c *Controller) {
				parent, _ := c.StartRun(context.Background())
				// Begin never closes while a run is admitted, so put the
				// controller in the closing state by hand.
				c.mu.Lock()
				c.window = &Window{c: c}
				c.mu.Unlock()
				defer func() {
					c.mu.Lock()
					c.window = nil
					c.mu.Unlock()
				}()
				child, ok := c.StartRun(parent.Context())
				if !ok {
					t.Fatal("a nested run was refused")
				}
				parent.Done()
				if _, ok := c.StartRun(parent.Context()); ok {
					t.Fatal("a finished parent still admits children")
				}
				child.Done()
				if c.Runs() != 0 {
					t.Fatalf("Runs = %d, want 0", c.Runs())
				}
			},
		},
		{
			name: "a run admitted between the busy check and the close keeps the window from holding",
			run: func(t *testing.T, c *Controller) {
				var admitted *Run
				opts := quick
				opts.Busy = func(context.Context) (int, string, error) {
					// The first check reads zero; the run is admitted
					// right after it, before admission closes — the
					// race the re-check after the drain must catch.
					if admitted == nil {
						admitted, _ = c.StartRun(context.Background())
					}
					return 0, "", nil
				}
				w, err := c.Begin(context.Background(), opts)
				if err == nil {
					w.Release()
					t.Fatal("the window held over an admitted run")
				}
				if !errors.Is(err, ErrBusy) {
					t.Fatalf("Begin: %v, want ErrBusy", err)
				}
				admitted.Done()
				w, err = c.Begin(context.Background(), quick)
				if err != nil {
					t.Fatalf("Begin after the run ended: %v", err)
				}
				w.Release()
			},
		},
		{
			name: "StartRunWait waits out the window",
			run: func(t *testing.T, c *Controller) {
				w, err := c.Begin(context.Background(), quick)
				if err != nil {
					t.Fatal(err)
				}
				got := make(chan *Run, 1)
				go func() {
					r, _ := c.StartRunWait(context.Background())
					got <- r
				}()
				select {
				case <-got:
					t.Fatal("StartRunWait returned while the window was held")
				case <-time.After(20 * time.Millisecond):
				}
				w.Release()
				(<-got).Done()
			},
		},
		{
			name: "StartRunWait ends with ctx",
			run: func(t *testing.T, c *Controller) {
				w, err := c.Begin(context.Background(), quick)
				if err != nil {
					t.Fatal(err)
				}
				defer w.Release()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := c.StartRunWait(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("StartRunWait: %v, want context.Canceled", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, New()) })
	}
}
