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
// inside before it reports the window held. Long units check Closing between
// batches and Yield, so they never hold a drain up for long.
//
// Entering is re-entrant through the context: a writer's Context carries its
// registration, and Enter with that context (a sweep a request runs inline)
// passes without counting again — otherwise a nested writer would be refused
// by the very drain its parent is holding up.

type writerKey struct{}

// Writer is one registration inside the gate. Leave it exactly once when the
// work is done; extra Leaves are no-ops.
type Writer struct {
	c      *Controller
	ctx    context.Context
	nested bool

	mu sync.Mutex
	in bool
}

// Enter registers a writer without waiting. ok is false while a window is
// closing or held: the caller must not write, and retries after release.
func (c *Controller) Enter(ctx context.Context) (*Writer, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if parent, _ := ctx.Value(writerKey{}).(*Writer); parent != nil && parent.c == c && parent.inside() {
		return &Writer{c: c, ctx: ctx, nested: true}, true
	}
	c.mu.Lock()
	if c.window != nil {
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

// Context carries this registration, so nested Enter calls pass through.
func (w *Writer) Context() context.Context { return w.ctx }

func (w *Writer) inside() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.in
}

// Leave ends the registration. The last writer out of a closing window lets
// Begin hold.
func (w *Writer) Leave() {
	if w == nil || w.nested {
		return
	}
	w.mu.Lock()
	if !w.in {
		w.mu.Unlock()
		return
	}
	w.in = false
	w.mu.Unlock()
	c := w.c
	c.mu.Lock()
	c.writers--
	if c.writers == 0 && c.drained != nil {
		close(c.drained)
		c.drained = nil
	}
	c.mu.Unlock()
}

// Yield is the between-batches check of a long unit of work. With admission
// open it returns at once. While a window is closing it leaves, waits for the
// release and enters again, so the drain is not held up by this writer. It
// returns ctx's error (and is left outside) only when ctx ends. A nested
// writer cannot yield its parent's registration and returns at once.
func (w *Writer) Yield(ctx context.Context) error {
	if w == nil || w.nested || !w.c.Holding() {
		return nil
	}
	w.Leave()
	for {
		if err := w.c.WaitReleased(ctx); err != nil {
			return err
		}
		c := w.c
		c.mu.Lock()
		if c.window == nil {
			c.writers++
			c.mu.Unlock()
			w.mu.Lock()
			w.in = true
			w.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
	}
}
