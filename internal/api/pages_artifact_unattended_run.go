package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/artifactrun"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Runs without a human (#5317 step 5, #5339), behind the workspace flag
// artifact_agent_access_modes.
//
// A routine, webhook or scheduled run has no human principal. It reaches an
// artifact only when the HOST recorded the run before the agent started
// (artifact_unattended_runs, internal/artifactrun) and the call carries the
// random capability the server minted for that one run. The crew run token
// alone is not enough: the crew container holds the key that derives it for
// any agent and run of the crew, so it proves nothing a container could not
// produce. The capability is not derived from any key a container holds.
//
// Such a run reaches only:
//   - an artifact the agent holds its own live grant on (standing), or
//   - a non-private artifact in a folder whose agents of the owning crew may
//     read or suggest (D2, folder), read only;
//
// never a private (Personal) artifact, never anything through a role (there
// is no human whose role could lend it), and at most run_read_limit distinct
// artifacts per run. It writes only through a live `write` grant on the
// artifact itself that the artifact's owner issued, re-checked on every write;
// never through a folder. It reads (artifact_versions) and publishes
// (artifact_publish); it does not create, share, restore, comment, reply or
// touch dashboard data or slides. Every first read and write per artifact is
// audited as artifact.agent_run_accessed with no user (no human acted).

// unattendedArtifactOperations are the tool operations a run without a human
// may use. Everything else is refused before any artifact is looked up.
var unattendedArtifactOperations = map[string]bool{"artifact_versions": true, "artifact_publish": true}

var errUnattendedRun = errors.New("unattended artifact run not current")

// unattendedArtifactRequest admits a run without a human from a verified crew
// transport and run token plus the host-minted capability. It returns nil for
// anything else, including the flag off, so the caller refuses exactly as it
// did before this mode existed.
func (h *PageHandler) unattendedArtifactRequest(r *http.Request, ws, crew, agent, run string) *artifactRunAuthority {
	capability := r.Header.Get(artifactrun.Header)
	ctx := r.Context()
	if capability == "" || !h.artifactRunModesOn(ctx, ws) {
		return nil
	}
	a := h.lookupUnattendedRun(ctx, ws, crew, agent, run, capability)
	if a == nil || h.checkUnattendedArtifactRun(ctx, a) != nil {
		return nil
	}
	return a
}

// errCodeRunCapabilityLost is the refusal a recorded run gets when its call
// reaches the server without the capability the host handed its sidecar
// (owner decision B, #5339). The capability is never re-issued: nothing in
// the container may fetch one, and the host delivers it once per run.
const errCodeRunCapabilityLost = "run_capability_lost"

const unattendedCapabilityLostMessage = "This run lost its artifact access: the crew sidecar no longer holds the run's artifact capability (it restarted or never received it). Artifact tools stay unavailable for the rest of this run; the next run gets a new capability."

// journalArtifactRunCapabilityLost records, once per run, that a recorded run
// lost its artifact capability. Payload: run_id, trigger, source.
const journalArtifactRunCapabilityLost journal.EntryType = "artifact.run_capability_lost"

// unattendedCapabilityLost reports whether a call that carries no capability
// comes from a run the host recorded that is still live (open, unexpired,
// live in the journal, policy allowing). It never admits anything. The first
// such call per run is journaled.
func (h *PageHandler) unattendedCapabilityLost(r *http.Request, ws, crew, agent, run string) bool {
	ctx := r.Context()
	if r.Header.Get(artifactrun.Header) != "" || !h.artifactRunModesOn(ctx, ws) {
		return false
	}
	a := &artifactRunAuthority{WorkspaceID: ws, CrewID: crew, AgentID: agent, RunID: run, Unattended: true}
	if h.db.QueryRowContext(ctx, `SELECT trigger_type,source_ref FROM artifact_unattended_runs WHERE run_id=? AND workspace_id=? AND crew_id=? AND agent_id=?`, run, ws, crew, agent).Scan(&a.Trigger, &a.Source) != nil {
		return false
	}
	if h.checkUnattendedArtifactRun(ctx, a) != nil {
		return false
	}
	if _, already := h.lostCapabilityJournaled.LoadOrStore(run, true); already {
		return true
	}
	var seen int
	if err := h.db.QueryRowContext(ctx, `SELECT count(*) FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=? AND entry_type=?`, ws, agent, run, string(journalArtifactRunCapabilityLost)).Scan(&seen); err == nil && seen == 0 && h.journal != nil {
		if _, err := h.journal.Emit(ctx, journal.Entry{
			WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run,
			Type: journalArtifactRunCapabilityLost, Severity: journal.SeverityWarn,
			ActorType: journal.ActorAgent, ActorID: agent,
			Summary: "A run lost its artifact access: its sidecar no longer holds the run's capability",
			Payload: map[string]any{"run_id": run, "trigger": a.Trigger, "source": a.Source},
		}); err != nil && h.logger != nil {
			h.logger.Warn("journal entry for a lost run capability not written", "run_id", run, "error", err)
		}
	}
	return true
}

// lookupUnattendedRun finds the host record for exactly this workspace, crew,
// agent and run whose stored digest matches capability, or nil.
func (h *PageHandler) lookupUnattendedRun(ctx context.Context, ws, crew, agent, run, capability string) *artifactRunAuthority {
	a := &artifactRunAuthority{WorkspaceID: ws, CrewID: crew, AgentID: agent, RunID: run, Unattended: true}
	var digest string
	err := h.db.QueryRowContext(ctx, `SELECT capability_sha256,trigger_type,source_ref FROM artifact_unattended_runs WHERE run_id=? AND workspace_id=? AND crew_id=? AND agent_id=?`, run, ws, crew, agent).Scan(&digest, &a.Trigger, &a.Source)
	if err != nil || !artifactrun.Matches(capability, digest) {
		return nil
	}
	return a
}

// checkUnattendedArtifactRun is the run-level gate, read through the caller's
// reader so a check inside the access's transaction sees its snapshot: the
// flag, the recorded run still open and unexpired, its agent and crew live,
// the run live in the journal (started, no terminal entry), its work attempt
// (if any) current, and the workspace policy.
func (h *PageHandler) checkUnattendedArtifactRun(ctx context.Context, a *artifactRunAuthority) error {
	if a == nil || !a.Unattended || a.HumanUserID != "" || !h.artifactRunModesOn(ctx, a.WorkspaceID) || !h.artifactRunFamilyEnabled(ctx, a.WorkspaceID, "") {
		return errUnattendedRun
	}
	q := h.pageAuthReader(ctx)
	var expiry string
	err := q.QueryRowContext(ctx, `SELECT u.expires_at FROM artifact_unattended_runs u
JOIN workspaces w ON w.id=u.workspace_id AND w.deleted_at IS NULL
JOIN crews c ON c.id=u.crew_id AND c.workspace_id=u.workspace_id AND c.deleted_at IS NULL
JOIN agents ag ON ag.id=u.agent_id AND ag.workspace_id=u.workspace_id AND ag.crew_id=u.crew_id AND ag.deleted_at IS NULL AND ag.expired_at IS NULL
WHERE u.run_id=? AND u.workspace_id=? AND u.crew_id=? AND u.agent_id=? AND u.ended_at IS NULL`, a.RunID, a.WorkspaceID, a.CrewID, a.AgentID).Scan(&expiry)
	if err != nil {
		return errUnattendedRun
	}
	limit, err := time.Parse(time.RFC3339Nano, expiry) // tsformat:allow: parsing a stored expiry for comparison; nothing is written
	if err != nil || !time.Now().Before(limit) {
		return errUnattendedRun
	}
	var started, ended int
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN entry_type='run.started' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop') THEN 1 ELSE 0 END),0) FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=?`, a.WorkspaceID, a.AgentID, a.RunID).Scan(&started, &ended); err != nil || started == 0 || ended != 0 {
		return errUnattendedRun
	}
	var state, itemWS, itemAgent, itemCrew, source string
	var generation, attemptGeneration int64
	var endedAt, deadline sql.NullString
	err = q.QueryRowContext(ctx, `SELECT w.state,w.generation,at.generation,at.ended_at,w.workspace_id,coalesce(w.agent_id,''),coalesce(w.crew_id,''),w.source,w.deadline_at FROM work_attempts at JOIN work_items w ON w.id=at.work_id WHERE at.run_id=?`, a.RunID).Scan(&state, &generation, &attemptGeneration, &endedAt, &itemWS, &itemAgent, &itemCrew, &source, &deadline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if (source != "webhook" && source != "schedule" && source != "pipeline_step") || itemWS != a.WorkspaceID || itemAgent != a.AgentID || itemCrew != a.CrewID || !memoryRunStateIsLive(state) || generation != attemptGeneration || endedAt.Valid {
			return errUnattendedRun
		}
		if deadline.Valid {
			d, e := time.Parse(time.RFC3339Nano, deadline.String) // tsformat:allow: parsing a stored deadline for comparison; nothing is written
			if e != nil || !time.Now().Before(d) {
				return errUnattendedRun
			}
		}
	}
	if !h.artifactScopedPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return errUnattendedRun
	}
	return nil
}

// unattendedOwnerWriteGrant reports whether the agent holds a live `write`
// grant on the artifact itself, issued by the artifact's owner. A folder, a
// grant from anyone else, or a lower level never counts.
func (h *PageHandler) unattendedOwnerWriteGrant(ctx context.Context, a *artifactRunAuthority, rec *pageRecord) (bool, error) {
	if rec == nil || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || rec.PrivateAudience {
		return false, nil
	}
	records, err := h.loadPageGrantRecords(ctx, a.WorkspaceID, rec)
	if err != nil {
		return false, err
	}
	grants := liveGrantsIn(records, func(g pageGrantRecord) bool {
		return g.SubjectType == pageSubjectAgent && g.SubjectID == a.AgentID && g.Level == pageGrantWrite && g.GrantedBy == rec.OwnerUserID
	})
	return len(grants) > 0, nil
}

// authorizeUnattendedArtifactRun decides a run without a human's access to
// one artifact inside the operation's own transaction, and returns its actor.
func (h *PageHandler) authorizeUnattendedArtifactRun(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, workspace string, rec *pageRecord, write, share bool) (string, error) {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if a.WorkspaceID != workspace || rec == nil || rec.ArchivedAt != "" {
		return "", errArtifactRefNotFound
	}
	if h.checkUnattendedArtifactRun(ctx, a) != nil {
		return "", errArtifactProjectAccess
	}
	// Never private, whatever grant the agent holds; refused like a miss.
	if rec.PrivateAudience {
		return "", errArtifactRefNotFound
	}
	grants, err := h.agentGrants(ctx, workspace, rec, a.AgentID)
	if err != nil {
		return "", err
	}
	reach := runReachNone
	for _, g := range grants {
		switch g.Level {
		case pageGrantRead, pageGrantPropose, pageGrantWrite:
			reach = runReachStanding
		}
	}
	if reach == runReachNone {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pages p JOIN page_folders f ON f.id=p.folder_id AND f.workspace_id=p.workspace_id
JOIN agents ag ON ag.id=? AND ag.workspace_id=p.workspace_id AND ag.deleted_at IS NULL AND ag.crew_id=f.owner_crew_id AND ag.crew_id=?
JOIN crews c ON c.id=f.owner_crew_id AND c.workspace_id=p.workspace_id AND c.deleted_at IS NULL
WHERE p.id=? AND p.workspace_id=? AND p.private_audience=0 AND f.agent_access IN ('read','propose')`, a.AgentID, a.CrewID, rec.ID, workspace).Scan(&n); err != nil {
			return "", err
		}
		if n != 1 {
			return "", errArtifactRefNotFound
		}
		reach = runReachFolder
	}
	if share {
		return "", errArtifactProjectAccess
	}
	if write {
		owned, err := h.unattendedOwnerWriteGrant(ctx, a, rec)
		if err != nil {
			return "", err
		}
		if reach != runReachStanding || !owned {
			return "", errArtifactProjectAccess
		}
	}
	// Every distinct artifact the run reaches counts against the read cap.
	var seen int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_unattended_run_access WHERE run_id=? AND page_id=?`, a.RunID, rec.ID).Scan(&seen); err != nil {
		return "", err
	}
	if seen == 0 {
		limits, err := loadArtifactRunLimits(ctx, tx, workspace)
		if err != nil {
			return "", err
		}
		var used int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_unattended_run_access WHERE run_id=?`, a.RunID).Scan(&used); err != nil {
			return "", err
		}
		if used >= limits.RunRead {
			return "", errArtifactRunCap
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_unattended_run_access(run_id,workspace_id,page_id,created_at) VALUES(?,?,?,?) ON CONFLICT(run_id,page_id) DO NOTHING`,
			a.RunID, workspace, rec.ID, tsformat.Format(time.Now().UTC())); err != nil {
			return "", err
		}
	}
	operation := "read"
	if write {
		operation = "write"
	}
	if err := recordAgentRunArtifactAccess(ctx, tx, a, rec.ID, reach, operation); err != nil {
		return "", err
	}
	return "agent:" + a.AgentID + ":" + a.RunID, nil
}

// unattendedRunFrom returns the run without a human on ctx, if any.
func unattendedRunFrom(ctx context.Context) *artifactRunAuthority {
	a, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	if a == nil || !a.Unattended {
		return nil
	}
	return a
}

// recordUnattendedArtifactRun is what a host producer of a run without a human
// (webhook, schedule) calls after the run record exists and before the agent
// starts. It returns the capability for AgentRunRequest.ArtifactRunCapability
// and a function that ends it when the run returns. Any failure leaves the run
// without artifact access (fail closed) and is logged; the run itself still
// proceeds, as it did before this mode existed.
func recordUnattendedArtifactRun(ctx context.Context, db *sql.DB, logger interface {
	Warn(string, ...any)
}, run artifactrun.Run, ttl time.Duration) (string, func()) {
	if db == nil || run.CrewID == "" {
		return "", func() {}
	}
	capability, err := artifactrun.Record(ctx, db, run, ttl)
	if err != nil {
		if logger != nil {
			logger.Warn("unattended artifact run not recorded; the run reaches no artifact", "run_id", run.RunID, "error", err)
		}
		return "", func() {}
	}
	if capability == "" {
		return "", func() {}
	}
	return capability, func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := artifactrun.End(endCtx, db, run.RunID); err != nil && logger != nil {
			logger.Warn("unattended artifact run end not recorded; it lapses at its expiry or terminal journal entry", "run_id", run.RunID, "error", err)
		}
	}
}

// The hard run deadlines of the two api-side producers. The artifact
// capability of such a run lives exactly as long (artifactrun.TTLFor), so it
// never ends while the run may still be working.
const (
	webhookAgentRunTimeout   = 10 * time.Minute
	scheduledAgentRunTimeout = 45 * time.Minute
)
