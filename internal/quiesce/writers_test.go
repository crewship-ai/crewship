package quiesce

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitUntil polls cond until it holds. It is a wait for a state another
// goroutine reaches (Begin entering "closing"), never a sleep a result
// depends on: every assertion after it is ordered by channels.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type beginResult struct {
	w   *Window
	err error
}

func beginAsync(c *Controller, opts Options) <-chan beginResult {
	ch := make(chan beginResult, 1)
	go func() {
		w, err := c.Begin(context.Background(), opts)
		ch <- beginResult{w, err}
	}()
	return ch
}

func TestWriterBarrier(t *testing.T) {
	long := Options{HoldCap: time.Minute, DrainTimeout: time.Minute}
	cases := []struct {
		name string
		run  func(t *testing.T, c *Controller)
	}{
		{
			name: "no writers: the window holds at once",
			run: func(t *testing.T, c *Controller) {
				w, err := c.Begin(context.Background(), long)
				if err != nil {
					t.Fatal(err)
				}
				defer w.Release()
				if !c.Held() {
					t.Fatal("Begin returned but the window is not held")
				}
			},
		},
		{
			name: "an in-flight writer is waited for before the window holds",
			run: func(t *testing.T, c *Controller) {
				wr, ok := c.Enter(context.Background())
				if !ok {
					t.Fatal("admission refused with no window")
				}
				res := beginAsync(c, long)
				waitUntil(t, "closing", c.Holding)
				if c.Held() {
					t.Fatal("window held while a writer is still inside")
				}
				select {
				case r := <-res:
					t.Fatalf("Begin returned (%v) before the writer left", r.err)
				default:
				}
				wr.Leave()
				r := <-res
				if r.err != nil {
					t.Fatal(r.err)
				}
				defer r.w.Release()
				if !c.Held() || c.Writers() != 0 {
					t.Fatalf("held=%v writers=%d after drain", c.Held(), c.Writers())
				}
			},
		},
		{
			name: "a new writer during closing is refused and admitted after release",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				res := beginAsync(c, long)
				waitUntil(t, "closing", c.Holding)
				if _, ok := c.Enter(context.Background()); ok {
					t.Fatal("a new writer was admitted while the window was closing")
				}
				waiting := make(chan *Writer, 1)
				go func() {
					w, err := c.EnterWait(context.Background())
					if err != nil {
						t.Error(err)
					}
					waiting <- w
				}()
				wr.Leave()
				r := <-res
				if r.err != nil {
					t.Fatal(r.err)
				}
				if _, ok := c.Enter(context.Background()); ok {
					t.Fatal("a new writer was admitted while the window was held")
				}
				select {
				case <-waiting:
					t.Fatal("EnterWait got in while the window was held")
				default:
				}
				r.w.Release()
				w2 := <-waiting
				if c.Writers() != 1 {
					t.Fatalf("writers = %d after release, want the waiting one", c.Writers())
				}
				w2.Leave()
			},
		},
		{
			name: "a writer that never leaves aborts the window: no copy, admission reopens",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				defer wr.Leave()
				_, err := c.Begin(context.Background(), Options{HoldCap: time.Minute, DrainTimeout: 20 * time.Millisecond})
				if !errors.Is(err, ErrWritesDidNotDrain) || !errors.Is(err, ErrBusy) {
					t.Fatalf("err = %v, want ErrWritesDidNotDrain matching ErrBusy", err)
				}
				if c.Holding() || c.Held() {
					t.Fatal("an abandoned window still holds")
				}
				w2, ok := c.Enter(context.Background())
				if !ok {
					t.Fatal("admission stayed closed after the drain timed out")
				}
				w2.Leave()
			},
		},
		{
			name: "a long sweep yields between batches so the drain finishes",
			run: func(t *testing.T, c *Controller) {
				wr, err := c.EnterWait(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				res := beginAsync(c, long)
				waitUntil(t, "closing", c.Holding)
				// Batch boundary: the sweep sees the window closing.
				yielded := make(chan error, 1)
				go func() { yielded <- wr.Yield(context.Background()) }()
				r := <-res
				if r.err != nil {
					t.Fatal(r.err)
				}
				select {
				case <-yielded:
					t.Fatal("the sweep resumed inside the held window")
				default:
				}
				if c.Writers() != 0 {
					t.Fatalf("writers = %d inside a held window", c.Writers())
				}
				r.w.Release()
				if err := <-yielded; err != nil {
					t.Fatal(err)
				}
				if c.Writers() != 1 {
					t.Fatalf("writers = %d after the sweep resumed", c.Writers())
				}
				wr.Leave()
				wr.Leave() // idempotent
				if c.Writers() != 0 {
					t.Fatalf("writers = %d after leave", c.Writers())
				}
			},
		},
		{
			name: "yield with admission open does not leave",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				if err := wr.Yield(context.Background()); err != nil || c.Writers() != 1 {
					t.Fatalf("yield err=%v writers=%d", err, c.Writers())
				}
				wr.Leave()
			},
		},
		{
			name: "a child writer is admitted while closing and still waited for",
			run: func(t *testing.T, c *Controller) {
				parent, _ := c.Enter(context.Background())
				res := beginAsync(c, long)
				waitUntil(t, "closing", c.Holding)
				child, ok := c.Enter(parent.Context())
				if !ok {
					t.Fatal("a nested writer was refused by the drain its parent holds up")
				}
				if c.Writers() != 2 {
					t.Fatalf("writers = %d, the child must count", c.Writers())
				}
				// The parent finishes first (a goroutine outliving its
				// request): the window still waits for the child.
				parent.Leave()
				select {
				case r := <-res:
					t.Fatalf("Begin returned (%v) while the child was still writing", r.err)
				default:
				}
				if _, ok := c.Enter(parent.Context()); ok {
					t.Fatal("a left parent's context still admits children")
				}
				child.Leave()
				r := <-res
				if r.err != nil {
					t.Fatal(r.err)
				}
				r.w.Release()
			},
		},
		{
			name: "outside steps out for a run and re-enters after the window",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				var res <-chan beginResult
				var held *Window
				// Outside uses the default controller only through the ctx
				// writer, so it works on c too.
				err := Outside(wr.Context(), func() {
					if c.Writers() != 0 {
						t.Errorf("writers = %d during the run, want 0", c.Writers())
					}
					res = beginAsync(c, long)
					r := <-res
					if r.err != nil {
						t.Error(r.err)
						return
					}
					held = r.w
					// Released concurrently with the re-entry: Outside
					// must not count the writer again before it is.
					go held.Release()
				})
				if err != nil {
					t.Fatal(err)
				}
				if c.Holding() {
					t.Fatal("re-entered while the window was still open")
				}
				if c.Writers() != 1 {
					t.Fatalf("writers = %d after Outside, want 1", c.Writers())
				}
				wr.Leave()
				if c.Writers() != 0 {
					t.Fatalf("writers = %d after leave", c.Writers())
				}
			},
		},
		{
			name: "release reopens admission",
			run: func(t *testing.T, c *Controller) {
				w, err := c.Begin(context.Background(), long)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := c.Enter(context.Background()); ok {
					t.Fatal("admitted while held")
				}
				w.Release()
				wr, ok := c.Enter(context.Background())
				if !ok {
					t.Fatal("still refused after release")
				}
				wr.Leave()
			},
		},
		{
			name: "concurrent windows are serialized",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				res := beginAsync(c, long)
				waitUntil(t, "closing", c.Holding)
				if _, err := c.Begin(context.Background(), long); !errors.Is(err, ErrAlreadyHeld) {
					t.Fatalf("second Begin while closing = %v, want ErrAlreadyHeld", err)
				}
				wr.Leave()
				r := <-res
				if r.err != nil {
					t.Fatal(r.err)
				}
				if _, err := c.Begin(context.Background(), long); !errors.Is(err, ErrAlreadyHeld) {
					t.Fatalf("second Begin while held = %v, want ErrAlreadyHeld", err)
				}
				r.w.Release()
				w2, err := c.Begin(context.Background(), long)
				if err != nil {
					t.Fatalf("Begin after release = %v", err)
				}
				w2.Release()
			},
		},
		{
			name: "the hard cap covers the held phase, not the drain",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				res := beginAsync(c, Options{HoldCap: time.Minute, DrainTimeout: time.Minute})
				waitUntil(t, "closing", c.Holding)
				wr.Leave()
				r := <-res
				if r.err != nil {
					t.Fatal(r.err)
				}
				defer r.w.Release()
				if left := c.RetryAfter(); left > time.Minute || left < 50*time.Second {
					t.Fatalf("retry after = %v, want the hold cap counted from the hold", left)
				}
			},
		},
		{
			name: "cancelling Begin during the drain releases the window",
			run: func(t *testing.T, c *Controller) {
				wr, _ := c.Enter(context.Background())
				defer wr.Leave()
				ctx, cancel := context.WithCancel(context.Background())
				res := make(chan error, 1)
				go func() {
					_, err := c.Begin(ctx, long)
					res <- err
				}()
				waitUntil(t, "closing", c.Holding)
				cancel()
				if err := <-res; !errors.Is(err, context.Canceled) {
					t.Fatalf("err = %v", err)
				}
				if c.Holding() {
					t.Fatal("a cancelled Begin left the window closing")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, New()) })
	}
}

func TestGateEnterRegistersTheFire(t *testing.T) {
	h := DefaultHolds()
	t.Cleanup(func() { h.Replace(nil) })
	cases := []struct {
		name   string
		holds  []string
		gate   Gate
		window bool
		wantOK bool
	}{
		{name: "open: the fire is a writer", gate: SchedulerGate(nil), wantOK: true},
		{name: "follower never fires", gate: SchedulerGate(fixedGate(false)), wantOK: false},
		{name: "routines hold", holds: []string{HoldRoutines}, gate: SchedulerGate(nil), wantOK: false},
		{name: "window refuses the fire", window: true, gate: SchedulerGate(nil), wantOK: false},
		{name: "queue gate, window", window: true, gate: QueueGate(nil), wantOK: false},
		{name: "plain gate passes without registering", gate: fixedGate(true), wantOK: true},
		{name: "nil gate passes", gate: nil, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hs []Hold
			for _, k := range tc.holds {
				hs = append(hs, Hold{Key: k})
			}
			h.Replace(hs)
			if tc.window {
				w, err := Default().Begin(context.Background(), Options{HoldCap: time.Minute, DrainTimeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				defer w.Release()
			}
			before := Default().Writers()
			leave, ok := EnterVia(tc.gate)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			_, registers := tc.gate.(EnterGate)
			if ok && registers && Default().Writers() != before+1 {
				t.Fatalf("writers = %d, the fire was not registered", Default().Writers())
			}
			leave()
			if Default().Writers() != before {
				t.Fatalf("writers = %d after leave, want %d", Default().Writers(), before)
			}
		})
	}
}

func TestAdmitQueue(t *testing.T) {
	h := DefaultHolds()
	t.Cleanup(func() { h.Replace(nil) })
	h.Replace([]Hold{{Key: HoldQueue}})
	if _, ok := AdmitQueue(); ok {
		t.Fatal("admitted under the queue hold")
	}
	h.Replace(nil)
	leave, ok := AdmitQueue()
	if !ok || Default().Writers() != 1 {
		t.Fatalf("ok=%v writers=%d", ok, Default().Writers())
	}
	leave()
	w, err := Default().Begin(context.Background(), Options{HoldCap: time.Minute, DrainTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Release()
	if _, ok := AdmitQueue(); ok {
		t.Fatal("admitted during a window")
	}
}
