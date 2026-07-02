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

// GetOrCreateContainerCfg is like GetOrCreateContainer but takes a fully
// resolved CrewConfig (cached/provisioned image, containerEnv, mounts, caps,
// resource limits) so the container is created from the crew's PROVISIONED
// image rather than the bare runtime default. The mission/assignment dispatch
// path uses this (internal/api/assignments_run.go) — passing only {slug, id}
// there is what let a cold crew launch from the base image and fail the agent
// exec with exit 127 (no `claude` in the base image). Stats registration is
// preserved, same as GetOrCreateContainer.
func (o *Orchestrator) GetOrCreateContainerCfg(ctx context.Context, cfg provider.CrewConfig, workspaceID string) (string, error) {
	if o.container == nil {
		return "", fmt.Errorf("container provider not configured")
	}
	containerID, err := o.container.EnsureCrewRuntime(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("ensure crew runtime for crew %s (workspace %s): %w", cfg.ID, workspaceID, err)
	}
	o.mu.RLock()
	reg := o.statsRegister
	o.mu.RUnlock()
	if reg != nil && workspaceID != "" {
		reg(containerID, cfg.ID, workspaceID)
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

// Boot-time crash recovery lives in internal/server/server_lifecycle.go
// (Server.recoverOrphanedRuns, invoked from Server.Start) — it reconciles
// agent_runs against journal_entries rather than against live Docker exec
// state. An earlier orchestrator-level RecoverFromCrash (KV-state +
// ExecInspect reconciliation) was removed as dead code (no production
// caller); see PR history for the gap it covered that the DB/journal-only
// sweep does not (no check of whether the underlying container exec is
// still genuinely alive before marking a run cancelled).

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
