package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
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
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return false, fmt.Errorf("state provider cannot atomically record recovered absence")
	}
	changed := false
	err := atomic.Update(ctx, "agent_runs", key, func(raw []byte) ([]byte, error) {
		if raw == nil {
			return nil, nil
		}
		var current RunState
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, err
		}
		if current.Status != "running" || !SameRecoveredIdentity(current, expected) {
			return raw, nil
		}
		current.Status, current.LastActivity = "cancelled", time.Now()
		current.StopJournalPending, current.StopOrigin = true, "recovered_absence"
		data, err := json.Marshal(current)
		if err == nil {
			changed = true
		}
		return data, err
	})
	return changed && err == nil, err
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
