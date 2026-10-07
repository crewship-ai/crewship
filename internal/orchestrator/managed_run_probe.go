package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
)

// RetainManagedRunLocation captures admitted mode while the creation gate still
// owns the invocation. Callers retain the result after RunAgent returns; current
// pilot selection is not evidence for an already admitted or recovered run.
// Legacy locations incur no durable state reads or container inspection.
func (o *Orchestrator) RetainManagedRunLocation(ctx context.Context, location RunLocation) (RunLocation, error) {
	known := location.Managed
	if value, ok := o.agentRuns.Load(location.RunID); ok {
		owner := value.(*agentRunControl)
		if owner.location.ContainerID == location.ContainerID && owner.location.AgentSlug == location.AgentSlug {
			known = known || owner.location.Managed
		}
	}
	// HOME-based StopRun/RunIsAlive callers can outlive agentRuns while a
	// detached hold retains their HOME. Its admitted request is authoritative
	// even if the current pilot selector no longer contains the crew.
	if !known {
		o.detachedMu.Lock()
		if hold := o.detached[location.RunID]; hold != nil && hold.containerID == location.ContainerID && hold.agentSlug == location.AgentSlug {
			known = hold.req.managedLaunch != nil
		}
		o.detachedMu.Unlock()
	}
	// Legacy callers do not read durable state or gain new refusal/inspect
	// behavior. Recovery callers carry the host-persisted Managed marker.
	if !known {
		return location, nil
	}
	denied := errors.New("managed launch: durable run identity unavailable; runtime absence unconfirmed")
	if o.state == nil {
		return location, denied
	}
	raw, err := o.state.Get(ctx, "agent_runs", location.RunID)
	if err != nil {
		return location, denied
	}
	if len(raw) == 0 {
		return location, denied
	}
	var state RunState
	if json.Unmarshal(raw, &state) != nil {
		return location, denied
	}
	if state.ManagedLaunch == nil {
		return location, denied
	}
	if state.ID != location.RunID || state.ContainerID != location.ContainerID || state.AgentSlug != location.AgentSlug {
		return location, denied
	}
	location.Managed = true
	return location, nil
}

func (o *Orchestrator) managedRunProbe(ctx context.Context, location RunLocation, stop bool) (string, error) {
	retained, err := o.RetainManagedRunLocation(ctx, location)
	if err != nil {
		return "", err
	}
	if !retained.Managed {
		return directRunProbe(location.RunID, stop), nil
	}
	// A missing or unreadable PID file is UNKNOWN, never tmux ABSENT. A
	// positively inspected stopped/removed container can still prove absence.
	return managedDirectRunProbe(location.RunID, stop) + "echo UNKNOWN; exit; ", nil
}
