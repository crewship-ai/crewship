package quiesce

import (
	"context"
	"sync"
)

// The writer side of the quiet window's barrier.
//
// Every unit of work that changes protected data — a mutating HTTP request,
// one sweep, one dispatcher claim, one scheduler fire — enters the gate
// before it writes and leaves when it is done. While a window is closing or
// held Enter refuses (HTTP answers 503 + Retry-After, a background writer
// skips and retries after release), and Begin waits for the writers already
// inside before it reports the window held. Long units check between batches
// with Yield, so they never hold a drain up for long.
//
// A writer's Context carries its registration. Enter with that context (a
// sweep a request runs inline, a helper that enters on its own) registers a
// child that is admitted even while the window is closing — its parent is
// holding the drain up anyway, and refusing it would fail the parent's work
// halfway. The child still counts, so a child that outlives its parent (a
// goroutine started from a request) is still waited for.

type writerKey struct{}

// Writer is one registration inside the gate. Leave it once when the work is
// done; extra Leaves are no-ops.
type Writer struct {
	c   *Controller
	ctx context.Context

	mu   sync.Mutex
	in   bool // counted in c.writers right now
	done bool // Leave was called: never counted again
}

// Enter registers a writer without waiting. ok is false while a window is
// closing or held (unless ctx carries a writer of c that is inside): the
// caller must not write, and retries after release.
func (c *Controller) Enter(ctx context.Context) (*Writer, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	parent, _ := ctx.Value(writerKey{}).(*Writer)
	child := parent != nil && parent.c == c && parent.inside()
	c.mu.Lock()
	if c.window != nil && !child {
		c.mu.Unlock()
		return nil, false
	}
	c.writers++
	c.mu.Unlock()
	w := &Writer{c: c, in: true}
	w.ctx = context.WithValue(ctx, writerKey{}, w)
	return w, true
}

// EnterWait registers a writer, waiting out an open window first. It fails
// only when ctx ends.
func (c *Controller) EnterWait(ctx context.Context) (*Writer, error) {
	for {
		if w, ok := c.Enter(ctx); ok {
			return w, nil
		}
		if err := c.WaitReleased(ctx); err != nil {
			return nil, err
		}
	}
}

// Enter registers a writer with the default controller.
func Enter(ctx context.Context) (*Writer, bool) { return Default().Enter(ctx) }

// EnterWait registers a writer with the default controller, waiting out an
// open window first.
func EnterWait(ctx context.Context) (*Writer, error) { return Default().EnterWait(ctx) }

// Do runs fn as one writer with the default controller: it waits out an open
// window, enters, runs fn with the writer's context and leaves. The error is
// ctx's when the wait ended, else fn's.
func Do(ctx context.Context, fn func(ctx context.Context) error) error {
	w, err := EnterWait(ctx)
	if err != nil {
		return err
	}
	defer w.Leave()
	return fn(w.Context())
}

// Outside runs fn with the writer ctx carries stepped out of the gate, and
// re-enters it afterwards, waiting out a window that opened meanwhile. It is
// for a stretch inside a writer that is not itself a write the barrier can
// wait for — a routine run a scheduler starts, which the backup's busy probe
// counts instead. With no writer inside in ctx fn simply runs. The error is
// ctx's when the re-entry wait ended; the writer is then outside and its
// Leave is a no-op.
func Outside(ctx context.Context, fn func()) error {
	w := writerIn(ctx)
	if w == nil || !w.stepOut() {
		fn()
		return nil
	}
	fn()
	return w.reenter(ctx)
}

// Yield yields the writer ctx carries (see Writer.Yield) — the batch-loop
// check for code that is handed a writer's Context rather than the Writer.
// With no writer in ctx it returns at once.
func Yield(ctx context.Context) error {
	if w := writerIn(ctx); w != nil {
		return w.Yield(ctx)
	}
	return nil
}

func writerIn(ctx context.Context) *Writer {
	if ctx == nil {
		return nil
	}
	w, _ := ctx.Value(writerKey{}).(*Writer)
	return w
}

// Context carries this registration: pass it to the work, so Yield and
// Outside find the writer and a nested Enter registers as its child.
func (w *Writer) Context() context.Context { return w.ctx }

func (w *Writer) inside() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.in
}

// Leave ends the registration for good. The last writer out of a closing
// window lets Begin hold.
func (w *Writer) Leave() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.done = true
	wasIn := w.in
	w.in = false
	w.mu.Unlock()
	if wasIn {
		w.c.decrement()
	}
}

// Yield is the between-batches check of a long unit of work. With admission
// open it returns at once. While a window is closing or held it steps out,
// waits for the release and enters again, so the drain is not held up by
// this writer. It returns ctx's error (the writer then stays outside) only
// when ctx ends.
func (w *Writer) Yield(ctx context.Context) error {
	if w == nil || !w.c.Holding() || !w.stepOut() {
		return nil
	}
	return w.reenter(ctx)
}

// stepOut uncounts a writer that is inside without ending it. False when it
// was not inside (already stepped out, or left).
func (w *Writer) stepOut() bool {
	w.mu.Lock()
	if !w.in {
		w.mu.Unlock()
		return false
	}
	w.in = false
	w.mu.Unlock()
	w.c.decrement()
	return true
}

// reenter counts a stepped-out writer again, waiting out an open window. A
// writer left meanwhile stays out.
func (w *Writer) reenter(ctx context.Context) error {
	c := w.c
	for {
		c.mu.Lock()
		if c.window == nil {
			w.mu.Lock()
			if !w.done {
				w.in = true
				c.writers++
			}
			w.mu.Unlock()
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		if err := c.WaitReleased(ctx); err != nil {
			return err
		}
	}
}

func (c *Controller) decrement() {
	c.mu.Lock()
	c.writers--
	if c.writers == 0 && c.drained != nil {
		close(c.drained)
		c.drained = nil
	}
	c.mu.Unlock()
}
