package orchestrator

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
