package orchestrator

// Lifecycle, container management, tmux, and small helpers extracted from
// orchestrator.go for readability. All public function signatures are
// unchanged; this is a pure file move.
//
// Companion files split out of this one (no behavior change):
//   - orchestrator_exec_env.go — MCP egress domain resolution + tmux exec
//     setup (writes args/env/script files into the crew container).
// Stream JSON parsing and WS event marshaling live in exec_stream.go and
// parser_*.go respectively.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func (o *Orchestrator) GetOrCreateContainer(ctx context.Context, crewSlug, crewID, workspaceID string) (string, error) {
	if o.container == nil {
		return "", fmt.Errorf("container provider not configured")
	}
	containerID, err := o.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID:   crewID,
		Slug: crewSlug,
	})
	if err != nil {
		return "", fmt.Errorf("ensure crew runtime for crew %s (workspace %s): %w", crewID, workspaceID, err)
	}
	// Register for stats streaming. Without this, the direct-run path (server
	// routes.go handleAgentStart) is the only thing that registers containers,
	// which means mission-driven runs (the overwhelming majority) produce no
	// container.stats WS events and the dashboard tile stays empty.
	o.mu.RLock()
	reg := o.statsRegister
	o.mu.RUnlock()
	if reg != nil && workspaceID != "" {
		reg(containerID, crewID, workspaceID)
	}
	return containerID, nil
}

// RunAgentForAssignment runs a sub-agent as part of a mission assignment.
// It skips conversation history injection (each task gets a clean context via the mission brief).
// SkipSidecar is respected from the caller — regular AGENT tasks skip sidecar,
// while LEAD planning tasks need sidecar for mission management API access.

func (o *Orchestrator) StopAccepting() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepting = false
}

// RecoverFromCrash inspects all persisted run states and marks stale runs

func (o *Orchestrator) RecoverFromCrash(ctx context.Context) error {
	runs, err := o.state.List(ctx, "agent_runs")
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	for key, data := range runs {
		var run RunState
		if err := json.Unmarshal(data, &run); err != nil {
			o.logger.Warn("corrupt run state", "key", key, "error", err)
			continue
		}
		if run.Status != "running" {
			continue
		}

		if run.ExecID == "" {
			o.updateRunStatus(ctx, run.ID, "error")
			continue
		}

		running, _, err := o.container.ExecInspect(ctx, run.ExecID)
		if err != nil {
			// Transient inspect failures (Docker daemon briefly unreachable
			// during startup, container being restarted by an external
			// process, etc.) must not be collapsed with "exec finished".
			// Leave the run state alone so the next recovery pass — or the
			// run's own exec loop — can reconcile it.
			o.logger.Warn("inspect failed during crash recovery; leaving run state untouched",
				"run_id", run.ID, "exec_id", run.ExecID, "error", err)
			continue
		}
		if !running {
			o.updateRunStatus(ctx, run.ID, "completed")
			o.logger.Info("recovered stale run", "run_id", run.ID, "agent_id", run.AgentID)
		}
	}
	return nil
}

// wrapScrubHandler returns a handler that scrubs credential patterns from
// event content before forwarding to the real handler.
// When a credential pattern is detected and redacted, a system event is emitted
// so the user can see that the scrubber is active and protecting their secrets.

func (o *Orchestrator) Start(ctx context.Context) error {
	o.logger.Info("starting orchestrator container TTL manager")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.checkTTLs(ctx)
		}
	}
}

func (o *Orchestrator) refreshActivity(crewID, containerID string, ttlHours int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cs := o.crews[crewID]
	if cs == nil {
		cs = &crewState{}
		o.crews[crewID] = cs
	}
	cs.lastActivity = time.Now()
	cs.containerID = containerID
	if ttlHours > 0 {
		cs.ttl = time.Duration(ttlHours) * time.Hour
	} else {
		cs.ttl = 0
	}
}

func (o *Orchestrator) checkTTLs(ctx context.Context) {
	o.mu.Lock()
	var toStop []struct {
		crewID      string
		containerID string
	}
	now := time.Now()
	for crewID, cs := range o.crews {
		if cs.ttl <= 0 {
			continue
		}
		if now.Sub(cs.lastActivity) > cs.ttl {
			toStop = append(toStop, struct {
				crewID      string
				containerID string
			}{crewID: crewID, containerID: cs.containerID})
			delete(o.crews, crewID)
		}
	}
	o.mu.Unlock()

	for _, stop := range toStop {
		if stop.containerID == "" {
			continue
		}
		o.logger.Info("stopping idle crew container (TTL expired)", "crew_id", stop.crewID, "container_id", stop.containerID)
		if err := o.container.StopCrewRuntime(ctx, stop.containerID); err != nil {
			o.logger.Error("failed to stop idle crew container", "crew_id", stop.crewID, "error", err)
		}
	}
}
