package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// runSynchronously runs a routine for a synchronous POST /run and returns
// either its final result or, when a step starts blocking on a person, a
// WAITING receipt.
//
// The run used to execute on the request context. A held crewship action or
// `wait: approval` inside foreach blocks until a person decides, so the
// decision had to arrive while the HTTP request stayed open: a client that
// went away (Ctrl-C, a terminal timeout) cancelled the run, and the later
// approval answered "already decided or expired" (#5348).
//
// Now the run gets its own context that keeps the request's values. Until a
// step waits for a person, a client that disconnects still cancels the run,
// as before. Once a step waits, the client gets the receipt a top-level
// approval returns (status WAITING, the token and the step), and the run
// continues without the request; server shutdown still cancels it. The
// run's own row stays `running`: it holds its slot while it waits, unlike a
// parked top-level run.
func (h *PipelineHandler) runSynchronously(r *http.Request, exec *pipeline.Executor, input pipeline.RunInput) (res, receipt *pipeline.RunResult, err error) {
	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(r.Context()))
	stopLifecycle := func() bool { return false }
	if h.lifecycleCtx != nil {
		stopLifecycle = context.AfterFunc(h.lifecycleCtx, cancelRun)
	}

	gate := &syncRunGate{wake: make(chan struct{}, 1)}
	prev := input.OnHumanWait
	input.OnHumanWait = func(runID, stepID, token string) {
		if prev != nil {
			prev(runID, stepID, token)
		}
		if syncRunBeforeWaitSignal != nil {
			syncRunBeforeWaitSignal(runCtx)
		}
		gate.signalWait(humanWait{runID, stepID, token})
		if syncRunAfterWaitSignal != nil {
			syncRunAfterWaitSignal()
		}
	}

	type finishedRun struct {
		result *pipeline.RunResult
		err    error
	}
	finished := make(chan finishedRun, 1)
	finish := beginBackgroundWork()
	go func() {
		defer finish()
		defer cancelRun()
		defer stopLifecycle()
		defer func() {
			if recovered := recover(); recovered != nil {
				finished <- finishedRun{err: fmt.Errorf("routine panicked: %v", recovered)}
			}
		}()
		result, runErr := exec.Run(runCtx, input)
		finished <- finishedRun{result, runErr}
	}()

	receiptOrOutcome := func(wait humanWait) (*pipeline.RunResult, *pipeline.RunResult, error) {
		// A run that finished in the same instant reports its outcome.
		select {
		case done := <-finished:
			return done.result, nil, done.err
		default:
		}
		return nil, &pipeline.RunResult{
			RunID:          wait.runID,
			PipelineID:     input.PipelineID,
			Status:         "WAITING",
			WaitpointToken: wait.token,
			CurrentStep:    wait.stepID,
		}, nil
	}
	select {
	case done := <-finished:
		return done.result, nil, done.err
	case <-gate.wake:
		wait, _ := gate.waiting()
		return receiptOrOutcome(wait)
	case <-r.Context().Done():
		// The client left. If a step already waited for a person, that
		// came first and the run continues without the request (the
		// receipt is still the answer). Otherwise cancel the run as the
		// request context used to; the gate makes sure a wait that
		// signals later cannot revive it.
		if wait, waited := gate.clientLeft(); waited {
			return receiptOrOutcome(wait)
		}
		cancelRun()
		done := <-finished
		return done.result, nil, done.err
	}
}

type humanWait struct{ runID, stepID, token string }

// Test seams: both nil in production.
var (
	syncRunBeforeWaitSignal func(runCtx context.Context)
	syncRunAfterWaitSignal  func()
)

// syncRunGate decides, under one lock, which came first: a step that waits
// for a person or the client leaving. First one wins. Reading a "waiting"
// channel and the request context separately left a gap in which a run that
// already waited was cancelled because select picked the other ready case.
type syncRunGate struct {
	mu    sync.Mutex
	state int // 0 undecided, 1 waiting, 2 client left first
	wait  humanWait
	wake  chan struct{}
}

func (g *syncRunGate) signalWait(w humanWait) {
	g.mu.Lock()
	if g.state == 0 {
		g.state, g.wait = 1, w
	}
	recorded := g.state == 1
	g.mu.Unlock()
	if recorded {
		select {
		case g.wake <- struct{}{}:
		default:
		}
	}
}

func (g *syncRunGate) waiting() (humanWait, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.wait, g.state == 1
}

// clientLeft records that the client left, unless a wait came first.
func (g *syncRunGate) clientLeft() (humanWait, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == 0 {
		g.state = 2
	}
	return g.wait, g.state == 1
}
