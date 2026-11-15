package api

import (
	"context"
	"fmt"
	"net/http"

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

	type humanWait struct{ runID, stepID, token string }
	waiting := make(chan humanWait, 1)
	prev := input.OnHumanWait
	input.OnHumanWait = func(runID, stepID, token string) {
		if prev != nil {
			prev(runID, stepID, token)
		}
		select {
		case waiting <- humanWait{runID, stepID, token}:
		default:
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

	select {
	case done := <-finished:
		return done.result, nil, done.err
	case wait := <-waiting:
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
	case <-r.Context().Done():
		// The client left before anything waited for a person: cancel the
		// run as the request context used to.
		cancelRun()
		done := <-finished
		return done.result, nil, done.err
	}
}
