package orchestrator

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runoutput"
)

// RunLocallyOwned distinguishes a live controller invocation from a retained
// source whose outcome is available for recovery. Durable duplicate admission
// prevents another invocation acquiring the same identity after this is false.
func (o *Orchestrator) RunLocallyOwned(runID string) bool {
	_, owned := o.agentRuns.Load(runID)
	return owned
}

// MarkRetainedOutputProjected records the scheduling hint only after the source
// confirms its terminal ACK. It is never permission to fabricate a source ACK.
func (o *Orchestrator) MarkRetainedOutputProjected(ctx context.Context, run RunState) error {
	notProjected := errors.New("retained output has not been fully acknowledged")
	snapshot, err := o.ReplayRetainedRun(ctx, run, func(runoutput.Line) error { return notProjected }, func(runoutput.Snapshot) error { return notProjected })
	if err != nil {
		return err
	}
	if !snapshot.Complete {
		return notProjected
	}
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return errors.New("retained output projection requires atomic state")
	}
	return atomic.Update(ctx, "agent_runs", run.ID, func(raw []byte) ([]byte, error) {
		var current RunState
		if len(raw) == 0 || json.Unmarshal(raw, &current) != nil || !SameRecoveredIdentity(current, run) {
			return nil, errors.New("retained output identity changed before projection marker")
		}
		current.Output.Acknowledged = true
		return json.Marshal(current)
	})
}
