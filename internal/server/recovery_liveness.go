package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// reconcileRecoveredRuntimeAtBoot is called before this server admits work.
// A persisted running flag is not liveness evidence. Only a positively
// inspected container or exact run probe can establish absence; unavailable
// providers, legacy locations and work-owned outcomes remain untouched.
func (s *Server) reconcileRecoveredRuntimeAtBoot(ctx context.Context, key string, run orchestrator.RunState) orchestrator.RunState {
	if run.Status != "running" || run.ID == "" || run.ID == run.AgentID || run.ContainerID == "" || run.AgentSlug == "" || s.container == nil || s.orchestrator == nil || ctx.Err() != nil {
		return run
	}
	var workOwned bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_attempts WHERE run_id=?)`, run.ID).Scan(&workOwned); err != nil || workOwned {
		return run
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := s.container.ContainerStatus(probeCtx, run.ContainerID)
	if err != nil || status == nil || status.ID != run.ContainerID {
		return run
	}
	switch status.State {
	case "stopped":
		// No process can survive inside a positively inspected stopped container.
	case "running", "idle":
		alive, err := s.orchestrator.RunIsAliveAt(probeCtx, orchestrator.RunLocation{ContainerID: run.ContainerID, AgentSlug: run.AgentSlug, RunID: run.ID})
		if err != nil || alive {
			return run
		}
	default:
		return run
	}
	ended := run
	ended.Status, ended.LastActivity = "cancelled", time.Now()
	data, err := json.Marshal(ended)
	if err != nil {
		return run
	}
	if err := s.state.Set(ctx, "agent_runs", key, data); err != nil {
		s.logger.Warn("persist recovered runtime absence", "run_id", run.ID, "error", err)
		return run
	}
	// Existing journal recovery handles this trace, preserving its retry on
	// publication failure. Absence does not establish a successful exit code.
	return ended
}
