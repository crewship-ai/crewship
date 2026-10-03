package server

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// reconcileRecoveredRuntimeAtBoot is called before this server admits work.
// A persisted running flag is not liveness evidence. Only a positively
// inspected container or exact run probe can establish absence; unavailable
// providers, legacy locations and work-owned outcomes remain untouched.
func (s *Server) reconcileRecoveredRuntimeAtBoot(ctx context.Context, key string, run orchestrator.RunState) orchestrator.RunState {
	if !s.recoveredRuntimeAbsent(ctx, run) {
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

func (s *Server) recoveredRuntimeAbsent(ctx context.Context, run orchestrator.RunState) bool {
	if run.Status != "running" || run.ID == "" || run.ID == run.AgentID || run.ContainerID == "" || run.AgentSlug == "" || s.container == nil || s.orchestrator == nil || ctx.Err() != nil {
		return false
	}
	var workOwned bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_attempts WHERE run_id=?)`, run.ID).Scan(&workOwned); err != nil || workOwned {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := s.container.ContainerStatus(probeCtx, run.ContainerID)
	if err != nil || status == nil || status.ID != run.ContainerID {
		return false
	}
	switch status.State {
	case "stopped":
		// No process can survive inside a positively inspected stopped container.
	case "running", "idle":
		alive, err := s.orchestrator.RunIsAliveAt(probeCtx, orchestrator.RunLocation{ContainerID: run.ContainerID, AgentSlug: run.AgentSlug, RunID: run.ID})
		if err != nil || alive {
			return false
		}
	default:
		return false
	}

	return true
}

// Only the boot snapshot is eligible. Never rediscover new runs by scanning
// all journal entries after the server has begun admitting work.
func (s *Server) reconcileRecoveredRuntimes(ctx context.Context) {
	if !s.recoveredRuntimesMu.TryLock() {
		return
	}
	defer s.recoveredRuntimesMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	keys := make([]string, 0, len(s.recoveredRuntimes))
	for key := range s.recoveredRuntimes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	start := sort.Search(len(keys), func(i int) bool { return keys[i] > s.recoveredRuntimesCursor })
	for offset := 0; offset < len(keys) && offset < 100 && ctx.Err() == nil; offset++ {
		key := keys[(start+offset)%len(keys)]
		s.recoveredRuntimesCursor = key
		expected := s.recoveredRuntimes[key]
		raw, err := s.state.Get(ctx, "agent_runs", key)
		if err != nil {
			continue
		}
		var current orchestrator.RunState
		if err = json.Unmarshal(raw, &current); err != nil {
			continue
		}
		if current.Status != "running" || !orchestrator.SameRecoveredIdentity(current, expected) {
			delete(s.recoveredRuntimes, key)
			continue
		}
		if !s.recoveredRuntimeAbsent(ctx, current) {
			continue
		}
		changed, err := s.orchestrator.RecordRecoveredAbsence(ctx, key, current)
		if err != nil {
			s.logger.Warn("record recovered runtime absence", "run_id", current.ID, "error", err)
			continue
		}
		if changed {
			delete(s.recoveredRuntimes, key)
		}
	}
}
