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
	"io"
	"sync"
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

// SetCrewTTLResolver wires the crews table into the reaper as the authority
// on every crew's TTL. Called once at server bootstrap; nil-safe if never
// called (tests, headless harnesses).
func (o *Orchestrator) SetCrewTTLResolver(fn CrewTTLResolver) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.crewTTL = fn
}

// SetContainerBusyProbe wires an out-of-process occupancy check (port
// exposures) consulted before any stop. Called once at server bootstrap.
func (o *Orchestrator) SetContainerBusyProbe(fn ContainerBusyProbe) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.containerBusy = fn
}

// CrewActivity reports what the reaper currently knows about a crew.
func (o *Orchestrator) CrewActivity(crewID string) (CrewRuntimeActivity, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	cs := o.crews[crewID]
	if cs == nil {
		return CrewRuntimeActivity{}, false
	}
	return CrewRuntimeActivity{
		ContainerID:  cs.containerID,
		TTLHours:     int(cs.ttl / time.Hour),
		LastActivity: cs.lastActivity,
		Holds:        cs.holds,
	}, true
}

// NoteCrewActivity records that a crew's container was just used. Every path
// that calls EnsureCrewRuntime must call this — before #1662 only RunAgent
// did, so a container woken by a script step or a prewarm was tracked by
// nothing and ran until the daemon restarted.
func (o *Orchestrator) NoteCrewActivity(crewID, containerID string, ttlHours int) {
	if crewID == "" {
		return
	}
	o.refreshActivity(crewID, containerID, ttlHours)
}

// SeedCrewActivity records a container discovered at boot, dating its idle
// clock from `lastActivity` — the container's own StartedAt — rather than
// from now.
//
// That distinction is the whole fix for the first defect. The clock used to
// be born in process, so every crewshipd restart handed every surviving
// container a fresh full TTL window; on a host that redeploys more often than
// the TTL (dev1 tracks main and redeploys on merge) nothing would ever be
// reaped. Docker keeps StartedAt across our restarts and no restart of ours
// can reset it, so it is the durable floor the in-process clock lacked.
//
// It is a floor, not a reset: StartedAt is a lower bound on last activity, so
// it can only over-estimate idleness, and only for a crew this process has
// not otherwise heard from. The cost of over-estimating is one container
// start (a few hundred ms) on the next wake; nothing that survives a restart
// is at risk, because the only things that do — a detached tmux session and a
// live port exposure — are both checked before any stop.
//
// Unlike a run's ttl_hours, the hours here are read straight off the crew row,
// so an explicit 0 genuinely means "never stop" and is recorded as such.
func (o *Orchestrator) SeedCrewActivity(crewID, containerID string, ttlHours int, lastActivity time.Time) {
	if crewID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if cs := o.crews[crewID]; cs != nil {
		// This process already has live knowledge of the crew; that beats a
		// start timestamp. Only fill in what is missing.
		if cs.containerID == "" {
			cs.containerID = containerID
		}
		return
	}
	o.crews[crewID] = &crewState{
		lastActivity: lastActivity,
		ttl:          time.Duration(ttlHours) * time.Hour,
		containerID:  containerID,
	}
}

// RetainCrewContainer pins a crew's container against the reaper for as long
// as an occupant is inside it, and returns the release. The returned func is
// idempotent, so a `defer release()` that also runs on an error path cannot
// drive the count negative and unpin a sibling occupant.
//
// Holds rather than polling: three of the four occupants (script step,
// terminal attach, agent run) are in-process events with an obvious
// acquire/release pair, and a hold costs a map increment where a poll costs a
// docker exec per crew per tick — which the capacity work measured at ~42% of
// dockerd's serialized exec capacity at 50 crews. The fourth occupant, a
// detached tmux session, outlives its run and the daemon that started it and
// therefore cannot be held; it is confirmed by a probe at stop time instead.
func (o *Orchestrator) RetainCrewContainer(crewID, containerID string) func() {
	if crewID == "" {
		return func() {}
	}
	o.mu.Lock()
	cs := o.crews[crewID]
	if cs == nil {
		cs = &crewState{}
		o.crews[crewID] = cs
	}
	cs.holds++
	cs.lastActivity = time.Now()
	if containerID != "" {
		cs.containerID = containerID
	}
	o.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			cs := o.crews[crewID]
			if cs == nil {
				return
			}
			if cs.holds > 0 {
				cs.holds--
			}
			// The idle clock starts when the last occupant leaves, not when
			// it arrived — otherwise a run longer than the TTL would leave
			// the container instantly reapable the moment it finished.
			cs.lastActivity = time.Now()
		})
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
	if containerID != "" {
		// An empty id is "I don't know", not "there is none". Blanking it
		// would leave the crew tracked but unstoppable forever.
		cs.containerID = containerID
	}
	// #1662: this used to `else { cs.ttl = 0 }`. routes_agent.go reads
	// ttl_hours off the HTTP body with a default of 0, so "the caller said
	// nothing" and "the operator asked for no TTL" arrived here as the same
	// value and the last one won — one run without a TTL silently disabled
	// the TTL for the crew. A non-positive value carries no information;
	// the crews table (via CrewTTLResolver) is where "never stop" is said.
	if ttlHours > 0 {
		cs.ttl = time.Duration(ttlHours) * time.Hour
	}
}

// tmuxProbeTimeout bounds the stop-time occupancy probe. It runs at most once
// per crew per sweep and only for crews already judged idle, so a hung daemon
// costs the sweep this much per candidate and nothing else.
const tmuxProbeTimeout = 10 * time.Second

// hasLiveTmuxSession asks the container whether any tmux session is still
// alive. secrets_cleanup.go already documents the case: a run whose CLI exec
// outlives RunAgent (a detached session) "keeps its hold forever". Stopping
// the container would kill that agent mid-run.
//
// Exit 0 means at least one session. Exit 1 is tmux's "no server running";
// exit 127 is a BYOI image without tmux, where a session cannot exist. Any
// answer we could not obtain — the exec failed, the inspect failed, the exec
// is somehow still running — is treated as occupied, because failing safe
// here costs one idle container until the next tick while failing open costs
// a killed agent.
func (o *Orchestrator) hasLiveTmuxSession(ctx context.Context, containerID string) bool {
	if o.container == nil || containerID == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, tmuxProbeTimeout)
	defer cancel()

	res, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"tmux", "ls"},
		User:        "1001:1001",
	})
	if err != nil {
		o.logger.Debug("tmux occupancy probe failed; keeping container up",
			"container_id", containerID, "error", err)
		return true
	}
	if res == nil {
		return false
	}
	if res.Reader != nil {
		// Draining to EOF is what makes the following inspect meaningful:
		// the exec has finished by the time the stream closes.
		_, _ = io.Copy(io.Discard, res.Reader)
		_ = res.Reader.Close()
	}
	running, exitCode, err := o.container.ExecInspect(ctx, res.ExecID)
	if err != nil {
		o.logger.Debug("tmux occupancy probe unreadable; keeping container up",
			"container_id", containerID, "error", err)
		return true
	}
	if running {
		return true
	}
	return exitCode == 0
}

// containerOccupied runs the checks that can veto a stop, cheapest first: the
// injected in-memory probe (port exposures) before the docker exec.
func (o *Orchestrator) containerOccupied(ctx context.Context, crewID, containerID string) bool {
	o.mu.RLock()
	probe := o.containerBusy
	o.mu.RUnlock()
	if probe != nil && probe(ctx, crewID, containerID) {
		o.logger.Debug("crew container has a live exposure; not stopping",
			"crew_id", crewID, "container_id", containerID)
		return true
	}
	return o.hasLiveTmuxSession(ctx, containerID)
}

// checkTTLs stops crew containers that have been idle past their TTL.
//
// Scope, deliberately: this stops the CREW container and nothing else. A
// crew's declared services (redis, postgres, …) are separate containers with
// their own names, restart policies and labels, joined to the crew's bridge
// network — see EnsureCrewServices/ensureSidecar in internal/provider/docker.
// They are neighbours of the crew container, not residents of it, so stopping
// the agent runtime does not stop them and this reaper must never reach for
// them. Crewship uses Docker for its own runtime; it is not a general
// infrastructure orchestrator.
//
// The stopped container keeps its writable layer, config and volumes, and
// loses its processes, its tmpfs and its IP. The IP is already handled: the
// port-expose proxy re-resolves it from the container id on every request
// (internal/api/port_expose_list_revoke_serve.go) rather than trusting the
// cached value. The processes are handled by refusing to stop an occupied
// container at all.
func (o *Orchestrator) checkTTLs(ctx context.Context) {
	o.mu.RLock()
	resolver := o.crewTTL
	o.mu.RUnlock()

	var resolved map[string]int
	if resolver != nil {
		resolved = resolver(ctx)
	}

	type candidate struct {
		crewID      string
		containerID string
	}
	var expired []candidate

	o.mu.Lock()
	now := time.Now()
	for crewID, cs := range o.crews {
		if cs.holds > 0 {
			continue // an occupant is inside it
		}
		ttl := cs.ttl
		if resolver != nil {
			hours, known := resolved[crewID]
			if !known {
				// The crew row is gone, or the resolver could not read it.
				// Unknown is not "expired".
				continue
			}
			ttl = time.Duration(hours) * time.Hour
		}
		if ttl <= 0 {
			continue // never stop
		}
		if now.Sub(cs.lastActivity) > ttl {
			expired = append(expired, candidate{crewID: crewID, containerID: cs.containerID})
		}
	}
	o.mu.Unlock()

	for _, c := range expired {
		if c.containerID == "" {
			// Tracked but never associated with a container — nothing to
			// stop, and keeping the entry would re-evaluate it every tick.
			o.forgetCrew(c.crewID)
			continue
		}
		if o.containerOccupied(ctx, c.crewID, c.containerID) {
			// Restart the clock so the probe does not repeat every tick for
			// a container we have just confirmed is busy.
			o.refreshActivity(c.crewID, c.containerID, 0)
			continue
		}
		o.forgetCrew(c.crewID)
		o.logger.Info("stopping idle crew container (TTL expired)", "crew_id", c.crewID, "container_id", c.containerID)
		if err := o.container.StopCrewRuntime(ctx, c.containerID); err != nil {
			o.logger.Error("failed to stop idle crew container", "crew_id", c.crewID, "error", err)
		}
	}
}

// forgetCrew drops a crew from the reaper's tracking map, unless an occupant
// took a hold while we were deciding.
func (o *Orchestrator) forgetCrew(crewID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cs := o.crews[crewID]; cs != nil && cs.holds > 0 {
		return
	}
	delete(o.crews, crewID)
}
