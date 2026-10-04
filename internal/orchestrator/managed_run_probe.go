package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
)

// The durable launch mode survives restart and is not stored in agent-writable
// /tmp. Losing a managed PID file cannot turn a live direct CLI into an absent
// tmux session. Existing non-pilot legacy probes retain their original path.
func (o *Orchestrator) managedRunProbe(ctx context.Context, location RunLocation, stop bool) (string, error) {
	known := location.Managed
	if value, ok := o.agentRuns.Load(location.RunID); ok {
		owner := value.(*agentRunControl)
		if owner.location.ContainerID == location.ContainerID && owner.location.AgentSlug == location.AgentSlug {
			known = known || owner.location.Managed
		}
	}
	legacy := directRunProbe(location.RunID, stop)
	// Legacy callers do not read durable state or gain new refusal/inspect
	// behavior. Recovery callers carry the host-persisted Managed marker.
	if !known {
		return legacy, nil
	}
	denied := errors.New("managed launch: durable run identity unavailable; runtime absence unconfirmed")
	if o.state == nil {
		return "", denied
	}
	raw, err := o.state.Get(ctx, "agent_runs", location.RunID)
	if err != nil {
		return "", denied
	}
	if len(raw) == 0 {
		return "", denied
	}
	var state RunState
	if json.Unmarshal(raw, &state) != nil {
		return "", denied
	}
	if state.ManagedLaunch == nil {
		return "", denied
	}
	if state.ID != location.RunID || state.ContainerID != location.ContainerID || state.AgentSlug != location.AgentSlug {
		return "", denied
	}
	// A missing or unreadable PID file is UNKNOWN, never tmux ABSENT. A
	// positively inspected stopped/removed container can still prove absence.
	return managedDirectRunProbe(location.RunID, stop) + "echo UNKNOWN; exit; ", nil
}
