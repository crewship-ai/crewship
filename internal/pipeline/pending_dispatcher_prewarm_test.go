package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// prewarmExec is a runExecutor + runPrewarmer that records prewarm calls and
// can hold Run open, so a test can prove prewarm fires at claim time —
// concurrently with (and not gated behind) the blocking Run dispatch (#836).
type prewarmExec struct {
	prewarms int32
	runs     int32
	block    chan struct{} // Run blocks until closed (nil = don't block)

	mu       sync.Mutex
	prewarmd [][2]string // (pipelineID, workspaceID) per prewarm
}

func (p *prewarmExec) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	atomic.AddInt32(&p.runs, 1)
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
		}
	}
	return &RunResult{RunID: "r_" + in.TriggeredByID, Status: "COMPLETED"}, nil
}

func (p *prewarmExec) PrewarmForRun(_ context.Context, pipelineID, workspaceID string) {
	p.mu.Lock()
	p.prewarmd = append(p.prewarmd, [2]string{pipelineID, workspaceID})
	p.mu.Unlock()
	atomic.AddInt32(&p.prewarms, 1)
}

// TestPendingDispatcher_PrewarmsOnClaim: a claimed pending row triggers
// PrewarmForRun exactly once with the row's pipeline + workspace, and it
// happens off the critical path — proven by holding Run open and observing the
// prewarm land while Run is still blocked.
func TestPendingDispatcher_PrewarmsOnClaim(t *testing.T) {
	store := enqueueDue(t, 1)
	exec := &prewarmExec{block: make(chan struct{})}
	d := NewPendingRunDispatcher(store, exec, nil)

	ctx := context.Background()
	d.Start(ctx)
	t.Cleanup(func() { close(exec.block); d.Stop() })

	// Prewarm must land even though Run is still blocked (off critical path).
	if !waitFor(t, 2*time.Second, func() bool { return atomic.LoadInt32(&exec.prewarms) == 1 }) {
		t.Fatalf("expected 1 prewarm while Run blocked, got %d (runs=%d)",
			atomic.LoadInt32(&exec.prewarms), atomic.LoadInt32(&exec.runs))
	}

	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.prewarmd) != 1 || exec.prewarmd[0][0] != "pl" || exec.prewarmd[0][1] != "w" {
		t.Fatalf("prewarm should carry the row's pipeline/workspace (pl/w), got %v", exec.prewarmd)
	}
}

// TestPendingDispatcher_PrewarmOncePerRow: N co-due rows each prewarm exactly
// once — no double-fire (the claim gate already guards Run; prewarm rides the
// same claimed goroutine).
func TestPendingDispatcher_PrewarmOncePerRow(t *testing.T) {
	store := enqueueDue(t, 5)
	exec := &prewarmExec{} // Run returns immediately
	d := NewPendingRunDispatcher(store, exec, nil)

	ctx := context.Background()
	d.Start(ctx)
	t.Cleanup(d.Stop)

	if !waitFor(t, 3*time.Second, func() bool { return atomic.LoadInt32(&exec.prewarms) == 5 }) {
		t.Fatalf("expected 5 prewarms (one per row), got %d", atomic.LoadInt32(&exec.prewarms))
	}
	// A following sweep must not re-prewarm an already-claimed row.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&exec.prewarms); got != 5 {
		t.Errorf("prewarm double-fired: got %d, want 5", got)
	}
}
