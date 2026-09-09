package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/missionactivity"
)

// Named so Start's 400 body is grep-able and a test can assert exactly
// which one fired, without parsing writeProblem's rendered text (F62,
// PRD-ISSUES-AND-ROUTINES-2026 work package A10). Kept distinct from each
// other on purpose: "nobody was ever delegated" and "something was
// delegated but it can't run work" are different operator actions.
var (
	errIssueStartNoDelegate       = errors.New("Issue must have an agent delegate before starting")
	errIssueStartDelegateNotAgent = errors.New("Issue delegate is not an executable agent")
)

// ── Review — POST /api/v1/crews/{crewId}/issues/{identifier}/review ────────

func (h *IssueHandler) Review(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())

	var req struct {
		Revision      *int    `json:"revision"`
		BriefRevision *int    `json:"brief_revision"`
		Action        string  `json:"action"` // "approve" or "request_changes"
		Comment       string  `json:"comment"`
		ReassignTo    *string `json:"reassign_to"` // agent slug for request_changes
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Action != "approve" && req.Action != "request_changes" {
		writeProblem(w, r, http.StatusBadRequest, "action must be 'approve' or 'request_changes'")
		return
	}

	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		internalError(w, r, h.logger, "review: begin", err)
		return
	}
	defer tx.Rollback()
	// Acquire the write lock before reading the version being approved.
	if _, err = tx.ExecContext(ctx, `UPDATE missions SET updated_at=updated_at WHERE identifier=? AND crew_id=? AND workspace_id=?`, ident, crewID, wsID); err != nil {
		internalError(w, r, h.logger, "review: lock", err)
		return
	}
	var missionID, status, mode string
	var revision, brief int
	var submitted sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT m.id,m.status,w.mode,w.revision,w.brief_revision,w.submitted_brief_revision FROM missions m JOIN issue_work w ON w.mission_id=m.id WHERE m.identifier=? AND m.crew_id=? AND m.workspace_id=?`, ident, crewID, wsID).Scan(&missionID, &status, &mode, &revision, &brief, &submitted)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, 404, "Issue not found")
		return
	}
	if err != nil {
		internalError(w, r, h.logger, "review: load", err)
		return
	}
	if status != "REVIEW" && status != "IN_PROGRESS" {
		writeProblem(w, r, 400, "Issue must be in REVIEW or IN_PROGRESS to review (current: "+status+")")
		return
	}
	if (req.Revision != nil && *req.Revision != revision) || (req.BriefRevision != nil && *req.BriefRevision != brief) {
		writeProblem(w, r, 409, "The issue changed. Refresh before reviewing.")
		return
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE `+assignmentMatch+`) OR EXISTS(SELECT 1 FROM issue_executions WHERE mission_id=? AND stage IN ('working','reviewing'))`, missionID, missionID, missionID, missionID).Scan(&active); err != nil {
		internalError(w, r, h.logger, "review: active work", err)
		return
	}
	if active {
		writeProblem(w, r, 409, "Work or Lead review is still running. Stop it or wait for the result before reviewing.")
		return
	}
	if req.Action == "approve" {
		if mode == "human" && submitted.Valid && int(submitted.Int64) != brief {
			writeProblem(w, r, 409, "The brief changed after submission. Submit an updated result before approving.")
			return
		}
		var executionBrief int
		err = tx.QueryRowContext(ctx, `SELECT brief_revision FROM issue_executions WHERE mission_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, missionID).Scan(&executionBrief)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			internalError(w, r, h.logger, "review: execution", err)
			return
		}
		if mode != "human" && err == nil && executionBrief != brief {
			writeProblem(w, r, 409, "The result belongs to an older brief. Run the updated work before approving.")
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	fromStatus, toStatus := status, "TODO"
	commentBody, activity := "Changes requested", "review_changes_requested"
	if req.Action == "approve" {
		toStatus = "DONE"
		commentBody = "Approved"
		activity = "review_approved"
	}
	if req.Comment != "" {
		commentBody += ": " + req.Comment
	}
	if req.ReassignTo != nil && *req.ReassignTo != "" && req.Action == "request_changes" {
		var agentID string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM agents WHERE slug=? AND workspace_id=? AND deleted_at IS NULL AND status!='PENDING_REVIEW'`, *req.ReassignTo, wsID).Scan(&agentID); err != nil {
			writeProblem(w, r, 400, "Review target is not an available agent in this workspace")
			return
		}
		_, err = tx.ExecContext(ctx, `UPDATE missions SET assignee_type='agent',assignee_id=?,delegate_agent_id=? WHERE id=?`, agentID, agentID, missionID)
		if err != nil {
			internalError(w, r, h.logger, "review: target", err)
			return
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE missions SET status=?,completed_at=CASE WHEN ?='DONE' THEN ? ELSE NULL END,updated_at=? WHERE id=?`, toStatus, toStatus, now, now, missionID)
	if err == nil && req.Action == "request_changes" {
		_, err = tx.ExecContext(ctx, `UPDATE issue_executions SET stage='changes_requested',review_note=?,updated_at=? WHERE mission_id=? AND stage IN ('accepted','needs_human')`, commentBody, now, missionID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,created_at,updated_at) VALUES(?,?,'user',?,?,?,?)`, generateCUID(), missionID, user.ID, commentBody, now, now)
	}
	if err == nil {
		_, err = missionactivity.EmitTx(ctx, tx, missionactivity.Entry{ID: generateCUID(), MissionID: missionID, ActorType: "user", ActorID: user.ID, Action: activity, Details: commentBody})
	}
	if err != nil {
		internalError(w, r, h.logger, "review: persist", err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "review: commit", err)
		return
	}
	if h.journal != nil {
		_, _ = h.journal.Emit(ctx, journal.Entry{WorkspaceID: wsID, CrewID: crewID, MissionID: missionID, Type: journalTypeForIssueAction(issueAction(activity)), Severity: journal.SeverityInfo, ActorType: journal.ActorUser, ActorID: user.ID, Summary: commentBody, Payload: issueEventPayload(issueEvent{MissionID: missionID, Action: issueAction(activity), Details: commentBody, From: fromStatus, To: toStatus})})
	}
	if toStatus == "DONE" {
		emitMissionOutcomeLessonAsync(ctx, h.db, h.storagePath, missionID, "DONE", h.logger)
	}

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": missionID, "identifier": ident})
	h.broadcastIssueEvent(wsID, "issue.status_changed", map[string]string{
		"id": missionID, "identifier": ident, "crew_id": crewID,
		"status": toStatus, "from": fromStatus, "to": toStatus,
	})

	// The status the transition produced (DONE / TODO), not a bare "ok": a
	// pipeline branching on `.status` must not need a second `issue get`.
	writeJSON(w, http.StatusOK, map[string]string{"status": toStatus, "action": req.Action, "identifier": ident})
}

// ── ListActivity — GET /api/v1/crews/{crewId}/issues/{identifier}/activity

func (h *IssueHandler) ListActivity(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Issue not found")
		return
	}

	// Ordered by seq, not created_at.
	//
	// created_at is a second-granularity string, and the rows this endpoint
	// reads are written in bursts — a status change stamps its own row plus
	// the ones its side effects emit, all inside the same second. Ties then
	// broke arbitrarily, so the ORDER BY was decorative: an issue's `created`
	// row could surface AFTER three status_changed rows written later. Two
	// calls could disagree with each other.
	//
	// mission_activity.seq is the monotonic per-mission cursor B11 added, and
	// it is what GET .../events already orders by. Using it here makes the
	// two endpoints agree — they read the same table, and a caller comparing
	// them should not have to reconcile two different histories.
	//
	// The inner query keeps taking the LAST 50 rows (seq DESC) so a busy
	// issue still shows its recent history rather than its first hour; the
	// outer one presents them oldest-first, matching /events.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, mission_id, actor_type, actor_id, action, details, created_at, actor_name FROM (
			SELECT a.id, a.mission_id, a.actor_type, a.actor_id, a.action, a.details, a.created_at, a.seq,
				CASE
					WHEN a.actor_type = 'user' THEN (SELECT full_name FROM users WHERE id = a.actor_id)
					WHEN a.actor_type = 'agent' THEN (SELECT name FROM agents WHERE id = a.actor_id)
					ELSE 'System'
				END AS actor_name
			FROM mission_activity a
			WHERE a.mission_id = ?
			ORDER BY a.seq DESC
			LIMIT 50
		) ORDER BY seq ASC`, missionID)
	if err != nil {
		internalError(w, r, h.logger, "list activity", err)
		return
	}
	defer rows.Close()

	var result []activityResponse
	for rows.Next() {
		var a activityResponse
		if err := rows.Scan(&a.ID, &a.MissionID, &a.ActorType, &a.ActorID, &a.Action, &a.Details, &a.CreatedAt, &a.ActorName); err != nil {
			h.logger.Error("scan activity", "error", err)
			continue
		}
		result = append(result, a)
	}
	if result == nil {
		result = []activityResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 15. Start — POST /api/v1/crews/{crewId}/issues/{identifier}/start ──────

func (h *IssueHandler) Start(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	tx, txErr := h.db.BeginTx(r.Context(), nil)
	if txErr != nil {
		internalError(w, r, h.logger, "start: begin", txErr)
		return
	}
	defer tx.Rollback()

	// 1. Load issue
	var missionID, status, title, leadAgentID string
	var description, delegateAgentID sql.NullString
	err := tx.QueryRowContext(r.Context(), `
		SELECT id, status, title, description, delegate_agent_id, lead_agent_id
		FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status, &title, &description, &delegateAgentID, &leadAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "start issue: load", err)
		return
	}

	var held bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue_work WHERE mission_id=? AND mode='human')`, missionID).Scan(&held); err != nil {
		internalError(w, r, h.logger, "start: work mode", err)
		return
	}
	if held {
		writeProblem(w, r, http.StatusConflict, "This issue is held by a human. Hand it back to an agent delegate before starting.")
		return
	}
	// 2. Validate status
	if status != "BACKLOG" && status != "TODO" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must be in BACKLOG or TODO to start (current: "+status+")")
		return
	}

	var routineID string
	if err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(routine_id,'') FROM missions WHERE id=?`, missionID).Scan(&routineID); err != nil {
		internalError(w, r, h.logger, "start: routine", err)
		return
	}
	if routineID != "" {
		h.startBoundRoutine(w, r, tx, missionID, ident, leadAgentID, routineID)
		return
	}

	// 3. Validate delegate (F62): pre-A10 this checked only that
	// assignee_id EXISTED, which passed for a user-owner with no agent
	// delegate at all, or for an assignee_id whose row had since been
	// hard-deleted or soft-deleted — the mission_task insert below then
	// carried a dangling assigned_agent_id, or the LEAD-planning branch
	// silently ran nothing. delegate_agent_id is a typed FK to agents(id)
	// (ON DELETE SET NULL), so "it exists" is now a real question about an
	// agent specifically, not about either half of the old polymorphic
	// assignee — and existence still isn't executability, so the row is
	// re-checked against deleted_at and PENDING_REVIEW below.
	if !delegateAgentID.Valid || delegateAgentID.String == "" {
		writeProblem(w, r, http.StatusBadRequest, errIssueStartNoDelegate.Error())
		return
	}
	var delegateSlug, delegateStatus, assigneeRole string
	err = tx.QueryRowContext(r.Context(),
		`SELECT slug, COALESCE(status, ''), COALESCE(agent_role, '') FROM agents WHERE id = ? AND deleted_at IS NULL`,
		delegateAgentID.String).Scan(&delegateSlug, &delegateStatus, &assigneeRole)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusBadRequest, errIssueStartDelegateNotAgent.Error())
			return
		}
		internalError(w, r, h.logger, "start issue: validate delegate", err)
		return
	}
	// A HELD agent (PENDING_REVIEW — created or hired by another agent,
	// awaiting operator approval) is not executable either; refuseHeldAgent
	// is the same predicate DispatchMention uses to keep a held agent from
	// being woken by a mention (assignments.go).
	if hErr := refuseHeldAgent(delegateSlug, delegateStatus); hErr != nil {
		writeProblem(w, r, http.StatusBadRequest, hErr.Error())
		return
	}

	// 3b. Create synthetic chat so assignments can reference it (FK on chat_id)
	var chatExists int
	_ = tx.QueryRowContext(r.Context(), `SELECT 1 FROM chats WHERE id = ?`, missionID).Scan(&chatExists)
	if chatExists == 0 {
		chatNow := time.Now().UTC().Format(time.RFC3339)
		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
			missionID, leadAgentID, wsID, "Issue: "+title, chatNow, chatNow, chatNow)
		if err != nil {
			h.logger.Error("start issue: create synthetic chat", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Failed to prepare issue execution")
			return
		}
	}

	// Each start owns a durable review round; historical runs cannot satisfy it.
	executionID := generateCUID()
	executionNow := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(r.Context(), `INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at,updated_at)
 SELECT ?,mission_id,revision,brief_revision,'working',?,?,? FROM issue_work WHERE mission_id=?`, executionID, leadAgentID, executionNow, executionNow, missionID)
	if err != nil {
		internalError(w, r, h.logger, "start: execution", err)
		return
	}

	// 4. Reset existing tasks to PENDING or create new one
	var taskCount int
	_ = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, missionID).Scan(&taskCount)
	if taskCount > 0 {
		// Reset existing tasks for re-run
		resetNow := time.Now().UTC().Format(time.RFC3339)
		_, _ = tx.ExecContext(r.Context(), `
			UPDATE mission_tasks SET status = 'PENDING', started_at = NULL, completed_at = NULL,
			duration_ms = NULL, result_summary = NULL, error_message = NULL, assignment_id = NULL,
			iteration = COALESCE(iteration, 0) + 1, updated_at = ?
			WHERE mission_id = ? AND status NOT IN ('COMPLETED','SKIPPED')`, resetNow, missionID)
	} else {
		// If the delegate is a LEAD agent, skip creating a default task so
		// the mission engine triggers lead planning (with sidecar and crew
		// context for delegation). assigneeRole was already resolved by
		// the delegate validation above (step 3) — no second query needed.
		if assigneeRole == "LEAD" {
			h.logger.Info("start issue: LEAD delegate — skipping default task for lead planning",
				"issue", ident, "delegate", delegateAgentID.String)
		} else {
			taskID := generateCUID()
			now := time.Now().UTC().Format(time.RFC3339)
			desc := ""
			if description.Valid {
				desc = description.String
			}
			_, err = tx.ExecContext(r.Context(), `
				INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 'PENDING', 1, '[]', ?, ?)`,
				taskID, missionID, delegateAgentID.String, title, desc, now, now)
			if err != nil {
				h.logger.Error("start issue: create task", "error", err)
				writeProblem(w, r, http.StatusInternalServerError, "Failed to create task")
				return
			}
		}
	}

	// Returning a previously completed issue creates an actual corrective step.
	var pendingTasks int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM mission_tasks WHERE mission_id=? AND status NOT IN ('COMPLETED','SKIPPED')`, missionID).Scan(&pendingTasks); err != nil {
		internalError(w, r, h.logger, "start: pending tasks", err)
		return
	}
	if taskCount > 0 && pendingTasks == 0 {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,description,status,task_order,depends_on,created_at,updated_at)
 SELECT ?,id,delegate_agent_id,title,COALESCE(description,'') || char(10) || COALESCE((SELECT body FROM mission_comments WHERE mission_id=missions.id ORDER BY created_at DESC,id DESC LIMIT 1),''),'PENDING',COALESCE((SELECT MAX(task_order)+1 FROM mission_tasks WHERE mission_id=missions.id),1),'[]',?,? FROM missions WHERE id=?`, generateCUID(), executionNow, executionNow, missionID)
		if err != nil {
			internalError(w, r, h.logger, "start: corrective task", err)
			return
		}
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET issue_execution_id=? WHERE mission_id=? AND status NOT IN ('COMPLETED','SKIPPED')`, executionID, missionID); err != nil {
		internalError(w, r, h.logger, "start: task execution", err)
		return
	}

	// 5. Update status → IN_PROGRESS (atomic CAS)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(r.Context(), `
		UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND status IN ('BACKLOG', 'TODO')`,
		now, missionID)
	if err != nil {
		internalError(w, r, h.logger, "start issue: update status", err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeProblem(w, r, http.StatusConflict, "Issue was already started by another request")
		return
	}

	if err := tx.Commit(); err != nil {
		internalError(w, r, h.logger, "start: commit", err)
		return
	}
	// 6. Start mission engine (async)
	if h.missionEngine != nil {
		finish := beginBackgroundWork()
		go func() {
			defer finish()
			if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
				h.logger.Error("start issue: engine", "error", err, "issue", ident)
			}
		}()
	}

	// 7. Broadcast
	h.broadcastIssueEvent(wsID, "issue.started", map[string]string{"id": missionID, "identifier": ident, "status": "IN_PROGRESS"})

	h.logger.Info("issue started", "identifier", ident, "agent", delegateAgentID.String)
	writeJSON(w, http.StatusOK, map[string]string{"status": "IN_PROGRESS", "identifier": ident})
}

// ── 16. Stop — POST /api/v1/crews/{crewId}/issues/{identifier}/stop ────────

func (h *IssueHandler) Stop(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var missionID, status string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, status FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "stop issue: load", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// hard opts into Tier 2 (§10.3, work package B7, #2356): after the Tier
	// 1 stamp below lands, resolve each RUNNING target's live exec to a pid
	// and signal it (TERM, then KILL after a grace period) from a new exec
	// into its own container — never a container-level operation, so a
	// sibling agent on the same crew is unaffected. A query flag rather
	// than a new route: it is the same stop, with a bound on how it ends,
	// and both tiers share every guard above this line (role, issue
	// lookup, status check).
	hard := r.URL.Query().Get("hard") == "true"

	// assignmentMatch (issue_handler_hard_stop.go) is the same heuristic in
	// both branches below: mission_id (#2256) is the direct answer and the
	// one preferred — every mission-task run (mission_tasks.go's
	// scheduleTask), the lead-planning run
	// (mission_tasks_planning.go's dispatchLeadPlanning), and a mention
	// dispatch (issue_mentions.go's DispatchMention) stamp it explicitly.
	//
	// chat_id/group_id stay as a FALLBACK, not a redundant belt-and-braces
	// check: a sub-agent that delegates further via /assign while running
	// inside a mission (or a mention-dispatched run doing the same) creates
	// its new assignment row through AssignmentHandler.Create
	// (assignments_run.go), and neither caller of that door threads
	// mission_id through —
	//
	//   - the sidecar's handleAssign (internal/sidecar/assignment.go) never
	//     sets "mission_id" in the body it forwards, only actor_agent_id;
	//   - the routine dispatcher's crewshipBody (crewship_actions.go)
	//     injects workspace_id/crew_id/agent identity but not mission_id
	//     either.
	//
	// That delegated row DOES inherit chat_id = the mission id, though:
	// s.ipc.ChatID for a mission-task or mention-dispatched container is set
	// to the mission id (mission_tasks.go:585, issue_mentions.go's ChatID:
	// req.MissionID), and handleAssign carries that IPC chat_id straight
	// through as the new assignment's chat_id. So a delegated sub-task has
	// mission_id = NULL but chat_id = the mission id, and only the
	// heuristic finds it. Dropping the fallback would make Stop silently
	// stop reaching every delegation hop under a mission or a mention —
	// worse than the heuristic it would replace.
	//
	// NOT IN (...) rather than IN ('PENDING', 'RUNNING') so a QUEUED row
	// (#2312) is reached too, without having to enumerate every non-terminal
	// status by name.

	if status != "IN_PROGRESS" && status != "REVIEW" {
		// #2315: a mention (DispatchMention, issue_mentions.go) can dispatch a
		// run on an issue that was never started — status stays BACKLOG/TODO,
		// and there is no mission_tasks row for Stop to cancel and nothing on
		// the issue itself to move to CANCELLED (that would be option (b),
		// rejected: Stop must not promote the issue to IN_PROGRESS/CANCELLED
		// for a run it never started). But the live assignment IS reachable
		// by the same match used below, and the one door meant to reach every
		// run attributed to an issue (#2295) must not stay closed for exactly
		// the runs a mention starts. So: stamp whatever is live, leave the
		// issue's status alone, and only fall back to the original refusal
		// when nothing was actually reachable.
		targets, err := stampCancelRequested(r.Context(), h.db, now, missionID)
		if err != nil {
			internalError(w, r, h.logger, "stop issue: stamp mention-dispatched runs", err)
			return
		}
		runsStopped := len(targets)
		if runsStopped == 0 {
			writeProblem(w, r, http.StatusBadRequest, "Issue must be IN_PROGRESS or REVIEW to stop (current: "+status+")")
			return
		}
		if hard {
			h.hardStopTargets(r.Context(), wsID, ident, targets)
		}

		h.logger.Info("issue stop: reached mention-dispatched run(s) on an issue that never started",
			"identifier", ident, "status", status, "runs_stopped", runsStopped)
		writeJSON(w, http.StatusOK, map[string]any{"status": status, "identifier": ident, "runs_stopped": runsStopped, "hard": hard})
		return
	}

	var runsStopped int64
	var cancelTargets []cancelTarget

	// One transaction: the DB-visible half of "stop" — mission_tasks,
	// assignments, and missions — moves atomically, so no reader ever
	// observes the issue as CANCELLED while a task or assignment it owns
	// still looks live (or vice versa).
	//
	// This is Tier 1 (cooperative) only: there is no kill primitive for a
	// shared crew container (internal/provider/container.go has
	// Exec/ExecInspect, no Kill), so a running exec is not interrupted here.
	// cancel_requested_at is the cooperative signal the runner checks before
	// starting any further work (assignments_run.go's runAssignment) and
	// consults when an already-in-flight run finishes late (finishAssignment)
	// so it records CANCELLED instead of resurrecting the row as COMPLETED.
	err = database.WithTx(r.Context(), h.db, func(tx *sql.Tx) error {
		if err := cancelParkedIssueRoutinesTx(r.Context(), tx, missionID, now); err != nil {
			return err
		}
		// Cancel running/pending tasks
		if _, err := tx.ExecContext(r.Context(), `
			UPDATE mission_tasks SET status = 'CANCELLED', updated_at = ? WHERE mission_id = ? AND status IN ('PENDING', 'IN_PROGRESS', 'BLOCKED')`,
			now, missionID); err != nil {
			return err
		}

		// Signal the live assignment(s) for this issue — see assignmentMatch
		// (issue_handler_hard_stop.go) for why mission_id is matched
		// directly with a chat_id/group_id fallback.
		var terr error
		cancelTargets, terr = stampCancelRequested(r.Context(), tx, now, missionID)
		if terr != nil {
			return terr
		}
		runsStopped = int64(len(cancelTargets))

		// Update issue status → CANCELLED
		if _, err := tx.ExecContext(r.Context(), `
			UPDATE missions SET status = 'CANCELLED', completed_at = ?, updated_at = ? WHERE id = ?`,
			now, now, missionID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		internalError(w, r, h.logger, "stop issue: update", err)
		return
	}

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": missionID, "identifier": ident, "status": "CANCELLED"})
	h.broadcastIssueEvent(wsID, "issue.status_changed", map[string]string{
		"id": missionID, "identifier": ident, "crew_id": crewID,
		"status": "CANCELLED", "from": status, "to": "CANCELLED",
	})

	// F4.5 mission outcomes → crew memory. CANCELLED maps to neutral.
	emitMissionOutcomeLessonAsync(r.Context(), h.db, h.storagePath, missionID, "CANCELLED", h.logger)

	// Tier 2, after Tier 1 has committed (§10.3): the transaction above is
	// what actually stops this run from starting further work, and it has
	// already landed by this point regardless of what happens next. A hard
	// stop is strictly additional — a run that finishes in the gap, or a
	// signal that misses, still lands CANCELLED via finishAssignment's own
	// cancel_requested_at check, never resurrected as COMPLETED.
	if hard {
		h.hardStopTargets(r.Context(), wsID, ident, cancelTargets)
	}

	h.logger.Info("issue stopped", "identifier", ident)
	writeJSON(w, http.StatusOK, map[string]any{"status": "CANCELLED", "identifier": ident, "runs_stopped": runsStopped, "hard": hard})
}
