package backup

import (
	"fmt"
	"sync"
)

// WorkspaceGuard closes the TOCTOU race between acquiring the
// DB-backed backup_lock and actually starting a backup, AND the
// symmetric race between a mission-start guard check and the mission
// actually being registered with the orchestrator.
//
// The DB row in backup_locks is durable and cross-process, but Crewship
// runs as a single binary, so a process-local synchronisation primitive
// is both sufficient and simpler than a tx-scoped DB-row lock that
// would have to span the full backup duration. If Crewship later goes
// multi-process, this guard must be replaced with a DB-backed advisory
// lock that mission-start and backup-start BOTH contend for inside the
// same transaction that registers the mission row.
//
// Semantics:
//
//   - Many missions may hold the "mission side" simultaneously. This
//     mirrors read locks in a RWMutex.
//   - A backup holds the "backup side" exclusively. While held, no new
//     mission may BeginMission. In-flight missions drain before the
//     backup can proceed (the caller of BeginBackup blocks until they
//     do).
//   - BeginBackup is non-blocking to keep current semantics (the
//     caller refuses a concurrent backup rather than queueing). A
//     second BeginBackup returns ErrGuardBackupInProgress.
//   - BeginMission is non-blocking too: if a backup is active it
//     returns ErrGuardBackupInProgress so the caller can reject the
//     run with the same user-facing message as the DB guard.
//
// Callers MUST invoke the returned release func exactly once. A
// panicking caller's deferred release keeps the guard consistent.
type WorkspaceGuard struct {
	mu    sync.Mutex
	state map[string]*guardState
}

// guardState tracks one workspace.
type guardState struct {
	missionCount int  // number of active mission-side holders
	backupActive bool // a backup holds the exclusive side
}

// NewWorkspaceGuard returns a fresh guard. Tests and the backend wire
// a single instance into both the backup and the API packages.
func NewWorkspaceGuard() *WorkspaceGuard {
	return &WorkspaceGuard{state: map[string]*guardState{}}
}

// defaultGuard is the process-wide singleton. server.go injects it
// into handlers; tests may instantiate their own.
var defaultGuard = NewWorkspaceGuard()

// DefaultGuard returns the process-wide guard. Not meant to be
// replaced; tests that want isolation should create a fresh
// WorkspaceGuard and wire it explicitly.
func DefaultGuard() *WorkspaceGuard { return defaultGuard }

// ErrGuardBackupInProgress is returned when BeginMission sees an
// active backup, or when BeginBackup sees an active backup (two
// backups MUST NOT overlap).
var ErrGuardBackupInProgress = fmt.Errorf("backup: workspace is being backed up; retry after the backup completes (check `crewship backup status`)")

// ErrGuardMissionsInFlight is returned when BeginBackup sees missions
// still running. The caller should surface a "try again when agents
// are idle" message — this mirrors ensureAgentsIdle's behaviour.
var ErrGuardMissionsInFlight = fmt.Errorf("backup: one or more agent runs are in flight; abort the run or wait for it to finish")

// BeginMission claims the mission side for workspaceID. If a backup
// is active the returned error is ErrGuardBackupInProgress and the
// release func is nil.
//
// Hold duration — current semantics
// ---------------------------------
// The release func MUST be called when the caller is done with the
// mission run. The current call sites (assignments.go, webhook.go,
// query_handler.go) defer release until AFTER the full agent run
// has returned — not only the brief "register with orchestrator"
// window. This is the safer of two possible designs:
//
//   - Hold through full execution (current): no missions can slip
//     into the dump between register and execute; cost is that a
//     long-running agent blocks a concurrent backup attempt, which
//     returns ErrGuardMissionsInFlight so the admin can retry.
//   - Hold only through register: backups never starve, but
//     ensureAgentsIdle must still catch in-flight runs via the DB;
//     this requires more careful coupling than we have today.
//
// If backup starvation under long missions becomes a real problem,
// switch to the shorter hold and strengthen ensureAgentsIdle — the
// guard.go API does not have to change.
func (g *WorkspaceGuard) BeginMission(workspaceID string) (release func(), err error) {
	if workspaceID == "" {
		// An empty workspace ID means "no guard" — preserves the
		// legacy behaviour of refuseIfBackupInProgress.
		return func() {}, nil
	}
	g.mu.Lock()
	st := g.stateFor(workspaceID)
	if st.backupActive {
		g.mu.Unlock()
		return nil, ErrGuardBackupInProgress
	}
	st.missionCount++
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		s := g.state[workspaceID]
		if s != nil && s.missionCount > 0 {
			s.missionCount--
			g.maybeGC(workspaceID, s)
		}
		g.mu.Unlock()
	}, nil
}

// BeginBackup claims the backup side for workspaceID. Returns
// ErrGuardBackupInProgress if another backup is already active, or
// ErrGuardMissionsInFlight if any mission is currently registered.
//
// We deliberately do NOT block on in-flight missions. Blocking would
// couple backup latency to unrelated agent runs, and the DB-level
// agent-idle guard (ensureAgentsIdle) already surfaces a clear
// actionable error to the admin. The in-process guard's job is to
// close the TOCTOU window between the admin's first attempt and the
// moment ensureAgentsIdle actually runs its query.
func (g *WorkspaceGuard) BeginBackup(workspaceID string) (release func(), err error) {
	if workspaceID == "" {
		return func() {}, nil
	}
	g.mu.Lock()
	st := g.stateFor(workspaceID)
	if st.backupActive {
		g.mu.Unlock()
		return nil, ErrGuardBackupInProgress
	}
	if st.missionCount > 0 {
		g.mu.Unlock()
		return nil, ErrGuardMissionsInFlight
	}
	st.backupActive = true
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		s := g.state[workspaceID]
		if s != nil {
			s.backupActive = false
			g.maybeGC(workspaceID, s)
		}
		g.mu.Unlock()
	}, nil
}

// stateFor returns the per-workspace entry, creating it on demand.
// Caller holds g.mu.
func (g *WorkspaceGuard) stateFor(workspaceID string) *guardState {
	st, ok := g.state[workspaceID]
	if !ok {
		st = &guardState{}
		g.state[workspaceID] = st
	}
	return st
}

// maybeGC drops an idle map entry to keep memory bounded across many
// one-shot workspaces. Caller holds g.mu.
func (g *WorkspaceGuard) maybeGC(workspaceID string, st *guardState) {
	if st.missionCount == 0 && !st.backupActive {
		delete(g.state, workspaceID)
	}
}
