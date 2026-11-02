package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// SettleRetainedRunUnprojected ends a retained run whose output can never be
// projected because its ownership cannot be verified. It needs the same
// evidence of termination as AcknowledgeRetainedRunResult: a complete source
// result, whole-runtime absence for a forced end, and no local invocation. A
// run that may still execute is never released.
//
// record runs after that evidence and before the state changes, so durable
// history the caller derives from the settlement exists before the run stops
// being retried. It receives the source result only as termination evidence.
// The run becomes "error" with Unprojected set; the output is not
// acknowledged, collected or replayed.
func (o *Orchestrator) SettleRetainedRunUnprojected(ctx context.Context, run RunState, reason string, record func(runoutput.Snapshot) error) error {
	if reason == "" {
		return errors.New("unprojected retained settlement requires a reason")
	}
	if run.Output == nil || run.Output.Version != 2 {
		return errors.New("unprojected settlement requires retained host output")
	}
	snapshot, err := o.ReadRetainedRunResult(ctx, run)
	if err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if !snapshot.Complete {
		return fmt.Errorf("%w: retained source has not ended", ErrHostCaptureUnconfirmed)
	}
	if snapshot.Result.Reason != "exited" {
		absent, err := o.containerRuntimeAbsent(ctx, run.ContainerID)
		if err != nil || !absent {
			return fmt.Errorf("%w: forced process end lacks whole-runtime absence", ErrHostCaptureUnconfirmed)
		}
	}
	if o.RunLocallyOwned(run.ID) {
		return fmt.Errorf("%w: local invocation still owns outcome", ErrHostCaptureUnconfirmed)
	}
	if record != nil {
		if err := record(snapshot); err != nil {
			return err
		}
	}

	o.runRecoveryMu.Lock()
	defer o.runRecoveryMu.Unlock()
	if _, owned := o.agentRuns.Load(run.ID); owned {
		return fmt.Errorf("%w: local invocation still owns outcome", ErrHostCaptureUnconfirmed)
	}
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return errors.New("unprojected retained settlement requires atomic state")
	}
	return atomic.Update(ctx, "agent_runs", run.ID, func(raw []byte) ([]byte, error) {
		if len(raw) == 0 {
			return nil, errors.New("retained run disappeared before settlement")
		}
		var current RunState
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if !SameRecoveredIdentity(current, run) {
			return nil, errors.New("retained run changed before settlement")
		}
		// A projected or already settled run keeps its recorded outcome.
		if current.Output.Acknowledged || current.Output.Unprojected != "" {
			return raw, nil
		}
		if current.Status == "running" {
			current.Status = "error"
		}
		current.Output.Unprojected = reason
		current.LastActivity = time.Now()
		return json.Marshal(current)
	})
}
