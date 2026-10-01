package quiesce

import (
	"context"
	"sync"
)

// Run admission: the quiet window's hold on starting work.
//
// A routine run is not a writer the barrier drains — it can run for an hour,
// and a drain that waited for it would time out. It is running work, which
// Begin waits for with the busy probe instead. That probe used to read the
// database (pipeline_runs.status = 'running'), so a run between its claim and
// that row was invisible to it: the window could close, drain, re-check zero
// and hold, and the run would then write its row and its steps inside the
// consistent copy.
//
// StartRun closes that gap in process. A run is admitted before it writes
// anything and is counted from that moment until Done; Begin counts admitted
// runs as busy on every check, including the re-check after the drain. While
// a window is closing or held StartRun refuses, so nothing new starts once
// admission has closed, and a run admitted a moment before the close is
// still counted by the re-check — the window then does not hold.
//
// A run started inside work that already holds the window up — a writer
// inside the gate (a request that starts a run), or another admitted run (a
// nested routine) — is admitted as its child even while closing, exactly as
// a nested Enter is: refusing it would fail the parent halfway, and it is
// counted, so the window still cannot hold while it runs.

type runKey struct{}

// Run is one admitted run. Call Done once when it ends; extra calls are
// no-ops.
type Run struct {
	c    *Controller
	ctx  context.Context
	once sync.Once

	mu   sync.Mutex
	done bool
}

// StartRun admits a run without waiting. ok is false while a window is
// closing or held, unless ctx carries an admitted run or a writer inside
// the gate of c: the caller must not start, and leaves the work queued
// for after the release.
func (c *Controller) StartRun(ctx context.Context) (*Run, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	child := c.carriesAdmission(ctx)
	c.mu.Lock()
	if c.window != nil && !child {
		c.mu.Unlock()
		return nil, false
	}
	c.runs++
	c.mu.Unlock()
	r := &Run{c: c}
	r.ctx = context.WithValue(ctx, runKey{}, r)
	return r, true
}

// StartRunWait admits a run, waiting out an open window first. It fails only
// when ctx ends.
func (c *Controller) StartRunWait(ctx context.Context) (*Run, error) {
	for {
		if r, ok := c.StartRun(ctx); ok {
			return r, nil
		}
		if err := c.WaitReleased(ctx); err != nil {
			return nil, err
		}
	}
}

// Runs is the number of admitted runs that have not ended.
func (c *Controller) Runs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs
}

// carriesAdmission reports whether ctx carries a live admission of c: an
// admitted run not yet done, or a writer inside the gate.
func (c *Controller) carriesAdmission(ctx context.Context) bool {
	if r, _ := ctx.Value(runKey{}).(*Run); r != nil && r.c == c && r.live() {
		return true
	}
	if w, _ := ctx.Value(writerKey{}).(*Writer); w != nil && w.c == c && w.inside() {
		return true
	}
	return false
}

// StartRun admits a run with the default controller.
func StartRun(ctx context.Context) (*Run, bool) { return Default().StartRun(ctx) }

// StartRunWait admits a run with the default controller, waiting out an
// open window first.
func StartRunWait(ctx context.Context) (*Run, error) { return Default().StartRunWait(ctx) }

// Context carries this admission: pass it to the run, so a nested run is
// admitted as its child.
func (r *Run) Context() context.Context { return r.ctx }

func (r *Run) live() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.done
}

// Done ends the admission. The window's next busy check no longer counts it.
func (r *Run) Done() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.mu.Lock()
		r.done = true
		r.mu.Unlock()
		c := r.c
		c.mu.Lock()
		c.runs--
		c.mu.Unlock()
	})
}
