package orchestrator

import (
	"context"
	"encoding/json"
	"time"
)

// RecordRecoveredAbsence publishes a confirmed probe result only if the same
// recovered identity is still running and no local invocation owns it. The
// caller establishes runtime absence outside the admission lock.
func (o *Orchestrator) RecordRecoveredAbsence(ctx context.Context, key string, expected RunState) (bool, error) {
	o.runRecoveryMu.Lock()
	defer o.runRecoveryMu.Unlock()
	if _, owned := o.agentRuns.Load(expected.ID); owned {
		return false, nil
	}
	raw, err := o.state.Get(ctx, "agent_runs", key)
	if err != nil {
		return false, err
	}
	var current RunState
	if err = json.Unmarshal(raw, &current); err != nil {
		return false, err
	}
	if current.Status != "running" || !SameRecoveredIdentity(current, expected) {
		return false, nil
	}
	current.Status, current.LastActivity = "cancelled", time.Now()
	current.StopJournalPending, current.StopOrigin = true, "recovered_absence"
	raw, err = json.Marshal(current)
	if err != nil {
		return false, err
	}
	if err = o.state.Set(ctx, "agent_runs", key, raw); err != nil {
		return false, err
	}
	return true, nil
}

// SameRecoveredIdentity prevents an old probe from acting on a replaced record.
func SameRecoveredIdentity(a, b RunState) bool {
	return a.ID == b.ID && a.WorkspaceID == b.WorkspaceID && a.AgentID == b.AgentID && a.ContainerID == b.ContainerID &&
		a.AgentSlug == b.AgentSlug && a.StartedAt.Equal(b.StartedAt)
}

// ReconcileAgentActivity keeps admission registration stable while the caller
// projects an agent's status. The callback must also consider durable recovered
// runs and journal traces; locallyActive alone is not an idle proof. It must not
// call methods that register, stop or finish invocations on this orchestrator.
func (o *Orchestrator) ReconcileAgentActivity(agentID string, apply func(locallyActive bool) error) error {
	o.runRecoveryMu.Lock()
	defer o.runRecoveryMu.Unlock()
	active := false
	o.agentRuns.Range(func(_, value any) bool {
		if value.(*agentRunControl).agentID == agentID {
			active = true
			return false
		}
		return true
	})
	return apply(active)
}
