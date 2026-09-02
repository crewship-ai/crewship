package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/ws"
)

// InternalMissionHandler handles mission endpoints called by the sidecar
// on behalf of lead agents. Uses internal token auth, not JWT.
type InternalMissionHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine *orchestrator.MissionEngine
	logger        *slog.Logger
	// policyResolver + journal drive the #1768 autonomy gate on Create /
	// Start. nil resolver → the conservative guided default (mission is
	// staged, not waved through) — see gateInternalAction.
	policyResolver *policy.Resolver
	journal        journal.Emitter
}

// NewInternalMissionHandler creates an InternalMissionHandler for sidecar-facing mission endpoints.
func NewInternalMissionHandler(db *sql.DB, hub *ws.Hub, me *orchestrator.MissionEngine, logger *slog.Logger) *InternalMissionHandler {
	return &InternalMissionHandler{db: db, hub: hub, missionEngine: me, logger: logger}
}

// SetAutonomyGate wires the shared per-crew autonomy resolver and the journal
// emitter the #1768 hold records through.
func (h *InternalMissionHandler) SetAutonomyGate(r *policy.Resolver, j journal.Emitter) {
	h.policyResolver = r
	h.journal = j
}

// Create handles POST /api/v1/internal/missions
// Creates a mission and optionally its tasks in one call.
//
// #1768 — gated on policy.ActionMissionCreate, and bounded by the mission caps
// in mission_limits.go. The two are different questions and are answered in
// that order: the policy decides WHETHER this crew may plan, the caps decide
// HOW MUCH one call may set in motion. The caps exist because the delegation
// depth/fan-out cap that bounds /assign explicitly does NOT reach missions
// (delegation_limits.go:32) — nothing downstream re-bounds what a mission
// dispatches once it starts, so the bound has to be on the task list here.
//
//	strict   → the mission row is written in PLANNING as before, but a hold
//	           is recorded and Start refuses to move it to IN_PROGRESS until
//	           an operator approves. A mission that cannot start dispatches
//	           nothing — that is the inert state, and it is why strict gets
//	           Approve rather than Reject here (a strict crew that cannot
//	           plan work at all would be a usability cliff strict has
//	           nowhere else).
//	guided   → starts freely, with a non-blocking inbox notice. Planning is
//	           ordinary work: no principal is created, the crew is fixed by
//	           the token binding, and every task dispatches to an agent that
//	           was already allowed to run — so nothing here widens what the
//	           caller could already do, and what it may spend is bounded by
//	           mission.max_tasks and mission.max_active_per_crew rather than
//	           by a hold. See policy.ActionMissionCreate for what those caps
//	           still do not cover.
//	trusted  → journal-only, starts freely.
//	full     → journal-only, starts freely.
func (h *InternalMissionHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title            string  `json:"title"`
		Description      *string `json:"description"`
		LeadAgentID      string  `json:"lead_agent_id"`
		CrewID           string  `json:"crew_id"`
		WorkspaceID      string  `json:"workspace_id"`
		Plan             *string `json:"plan"`
		WorkflowTemplate *string `json:"workflow_template"`
		Tasks            []struct {
			Title           string   `json:"title"`
			Description     *string  `json:"description"`
			AssignedAgentID *string  `json:"assigned_agent_id"`
			TaskOrder       int      `json:"task_order"`
			DependsOn       []string `json:"depends_on"`
			MaxIterations   *int     `json:"max_iterations"`
		} `json:"tasks"`
	}
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Title == "" || req.LeadAgentID == "" || req.CrewID == "" || req.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "title, lead_agent_id, crew_id, workspace_id required")
		return
	}
	// PR-F24 F-4: a bound token may only create missions in its own
	// workspace; the body-carried workspace_id is checked here because
	// requireInternal cannot inspect request bodies.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	// #1186: crew_id is body-carried, so requireInternal's ?crew_id gate
	// never sees it. A crew-bound (crwv1) token may only create missions in
	// its OWN crew — the lead-agent check below proves the agent is in the
	// NAMED crew, not that the named crew is the caller's, so a compromised
	// crew sidecar could otherwise plant a mission in a sibling crew. A
	// workspace-bound token's crew must resolve to the bound workspace
	// (PR-F24 foreign-ID closure).
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &req.CrewID) {
		return
	}
	// Runs after the crew-binding check so the policy consulted belongs to a
	// crew the caller has been proven to own.
	gate, ok := gateInternalAction(w, r, h.policyResolver, h.logger, req.CrewID,
		policy.ActionMissionCreate, "Mission creation")
	if !ok {
		return
	}

	// Mission caps (mission_limits.go) — the numeric bound the autonomy
	// decision above assumed existed. Policy decides WHETHER this crew may
	// plan; the caps decide HOW MUCH. Order matters in that direction: a
	// strict crew that is rejected outright should hear about its autonomy
	// level, not about a task count.
	//
	// Before the per-task workspace validation below, which costs one query
	// per task — a runaway list must not buy itself thousands of reads on the
	// way to being refused.
	capLim, capErr := enforceMissionCaps(r.Context(), h.db, req.CrewID, len(req.Tasks))
	if capErr != nil {
		var refusal *missionRefusal
		if errors.As(capErr, &refusal) {
			h.logger.Info("mission refused by instance cap",
				"crew_id", req.CrewID, "lead_agent_id", req.LeadAgentID,
				"task_count", len(req.Tasks), "setting", refusal.Setting,
				"reason", refusal.Error())
			writeMissionCapRefusal(w, req.CrewID, refusal)
			return
		}
		// Fail closed: the cap could not read its own state, so nothing has
		// established that this creation is inside it.
		h.logger.Error("evaluate mission caps", "crew_id", req.CrewID, "error", capErr)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// SECURITY (defense-in-depth): verify the lead agent actually belongs to
	// the supplied crew+workspace. Without this, a compromised agent could
	// create a mission in another crew with itself as lead (cross-crew override).
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM agents WHERE id = ? AND crew_id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		req.LeadAgentID, req.CrewID, req.WorkspaceID).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "lead agent does not belong to the specified crew/workspace")
			return
		}
		h.logger.Error("validate lead agent", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// SECURITY (#1067): each task's assigned_agent_id is agent-supplied and was
	// inserted verbatim. Validate every non-nil one belongs to this workspace —
	// the same isolation guard as lead_agent_id. Cross-crew reachability
	// (crew_connections) is still enforced at dispatch; this closes the
	// cross-workspace stored-bad-state gap before any row is written.
	for _, t := range req.Tasks {
		if t.AssignedAgentID == nil || *t.AssignedAgentID == "" {
			continue
		}
		if err := assertFKInWorkspace(r.Context(), h.db, "agents", *t.AssignedAgentID, req.WorkspaceID); err != nil {
			if errors.Is(err, errFKNotInWorkspace) {
				replyError(w, http.StatusBadRequest, "task assigned_agent_id does not belong to this workspace")
				return
			}
			h.logger.Error("validate task assigned agent", "error", err)
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	id := generateCUID()
	traceID := "mission-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback()

	// The crew's mission budget is re-proved INSIDE the INSERT, not just by the
	// pre-check above: a loop firing /mission/create concurrently is the exact
	// shape the budget exists for, and a read-then-write check admits all of
	// them. Same predicate+write-in-one-statement shape as
	// insertCappedAssignment.
	//
	// authored_via is stamped here (v108 provenance, same 'agent_tool_call'
	// value the internal issue door uses) because it is what the budget
	// counts — see missionAuthoredViaAgent.
	guardSQL, guardArgs := activeMissionGuard(req.CrewID, capLim.MaxActivePerCrew)
	insertArgs := append([]any{
		id, req.WorkspaceID, req.CrewID, req.LeadAgentID, traceID,
		req.Title, req.Description, req.Plan, req.WorkflowTemplate, now, now,
	}, guardArgs...)
	res, err := tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, plan, workflow_template, authored_via, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?, ?, '`+missionAuthoredViaAgent+`', ?, ?
		 WHERE `+guardSQL, insertArgs...)
	if err != nil {
		h.logger.Error("create mission", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to create mission")
		return
	}
	if affected, aerr := res.RowsAffected(); aerr != nil {
		h.logger.Error("create mission rows affected", "error", aerr)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	} else if affected == 0 {
		// Lost the race to a concurrent creation that took the last slot.
		// Answered exactly like the pre-check refusal so a caller cannot read
		// "no row written" as success.
		h.logger.Info("mission refused by instance cap at insert",
			"crew_id", req.CrewID, "setting", SettingMissionMaxActivePerCrew)
		writeMissionCapRefusal(w, req.CrewID, &missionRefusal{
			Setting: SettingMissionMaxActivePerCrew,
			Limit:   capLim.MaxActivePerCrew,
			msg: fmt.Sprintf(
				"mission refused: this crew is at its limit of %d agent-created mission(s) still "+
					"running (instance setting %s). Finish or close one before planning another.",
				capLim.MaxActivePerCrew, SettingMissionMaxActivePerCrew),
		})
		return
	}

	// assignments.chat_id is NOT NULL REFERENCES chats(id), and the mission
	// task dispatcher inserts assignments with chat_id = this mission's id —
	// see mission_handler_mutate.go's Create and issue_handler_workflow.go's
	// Start, which stamp this same synthetic row for the same reason. This
	// door was the one path that didn't: a mission an agent planned here
	// dispatched its first task straight into a FOREIGN KEY constraint
	// failure. In the transaction so a rollback (e.g. a failed task insert
	// below) does not leave an orphaned chat with no mission.
	if err := ensureMissionChat(r.Context(), tx, id, req.WorkspaceID, req.LeadAgentID, req.Title); err != nil {
		h.logger.Error("create synthetic chat for mission", "error", err, "mission_id", id)
		replyError(w, http.StatusInternalServerError, "failed to create mission")
		return
	}

	// Create tasks if provided (batch creation)
	// Task IDs are generated server-side; depends_on references use temp IDs
	// that map to task_order for resolution.
	taskIDs := make(map[int]string) // task_order -> generated ID
	for _, t := range req.Tasks {
		taskID := generateCUID()
		taskIDs[t.TaskOrder] = taskID

		depsJSON := "[]"
		if len(t.DependsOn) > 0 {
			b, _ := json.Marshal(t.DependsOn)
			depsJSON = string(b)
		}

		status := "PENDING"
		if len(t.DependsOn) > 0 {
			status = "BLOCKED"
		}

		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on, max_iterations, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, id, t.AssignedAgentID, t.Title, t.Description, status, t.TaskOrder, depsJSON, t.MaxIterations, now, now)
		if err != nil {
			h.logger.Error("create mission task", "error", err)
			replyError(w, http.StatusInternalServerError, "failed to create task: "+t.Title)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	httpStatus := http.StatusCreated
	approvalID := ""
	if gate.held() {
		aid, herr := writeAutonomyHold(r.Context(), h.db, h.logger, h.journal, gate, autonomyHold{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.LeadAgentID,
			MissionID:   id,
			Target:      autonomyTargetMission,
			TargetID:    id,
			InboxKind:   inbox.KindWaitpoint,
			Title:       "Mission created by agent: " + req.Title,
			BodyMD: fmt.Sprintf(
				"An agent in a `%s` crew planned the mission **%s** with %d task(s).\n\n"+
					"It stays in `PLANNING` and cannot be started until approved.",
				gate.Level, req.Title, len(req.Tasks)),
			Reason:      "agent created mission " + req.Title,
			RequestedBy: req.LeadAgentID,
		})
		if herr != nil {
			// Without the hold there is nothing for Start to consult, so the
			// mission would be startable — i.e. ungated. Undo it.
			h.logger.Error("internal create mission: autonomy hold failed — compensating delete",
				"mission_id", id, "error", herr)
			if _, derr := h.db.ExecContext(r.Context(),
				`DELETE FROM missions WHERE id = ? AND workspace_id = ?`, id, req.WorkspaceID); derr != nil {
				h.logger.Error("internal create mission: compensating delete failed",
					"mission_id", id, "error", derr)
			}
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		approvalID = aid
		httpStatus = http.StatusAccepted
	} else if gate.wantsInbox() {
		// guided → AutoLogInbox. The mission is already live and startable;
		// this is after-the-fact visibility, and it is the whole of what the
		// guided cell buys now that it no longer blocks. A failed write is a
		// missed notice, not a broken mission (writeAutonomyNotice swallows).
		writeAutonomyNotice(r.Context(), h.db, h.logger, gate, req.WorkspaceID,
			inbox.KindMessage, id,
			"Mission created by agent: "+req.Title,
			fmt.Sprintf("An agent planned the mission **%s** with %d task(s). "+
				"It can be started without further approval at `autonomy_level=%s`.",
				req.Title, len(req.Tasks), gate.Level))
	}

	audit := gate.auditFields()
	audit["title"] = req.Title
	audit["crew_id"] = req.CrewID
	audit["task_count"] = len(req.Tasks)
	audit["pending_review"] = gate.held()
	WriteAuditLog(r.Context(), h.db, h.journal, "mission.created", "MISSION", id, "", req.WorkspaceID, audit)

	broadcastChannelEvent(h.hub, "crew", req.CrewID, "mission.created",
		map[string]string{"id": id, "title": req.Title})

	writeJSON(w, httpStatus, map[string]interface{}{
		"id":             id,
		"trace_id":       traceID,
		"status":         "PLANNING",
		"tasks":          taskIDs,
		"decision":       string(gate.Decision),
		"autonomy_level": string(gate.Level),
		"pending_review": gate.held(),
		"approval_id":    approvalID,
	})
}

// Start handles POST /api/v1/internal/missions/{missionId}/start
// Transitions a PLANNING mission to IN_PROGRESS and kicks off the MissionEngine.
func (h *InternalMissionHandler) Start(w http.ResponseWriter, r *http.Request) {
	missionID := r.PathValue("missionId")
	if missionID == "" {
		// Try extracting from URL path directly
		parts := strings.Split(r.URL.Path, "/")
		for i, p := range parts {
			if p == "missions" && i+1 < len(parts) {
				missionID = parts[i+1]
				break
			}
		}
	}

	// SECURITY (defense-in-depth): scope the mission lookup to the caller's
	// workspace (and crew, when supplied). Without this, a compromised sidecar
	// or an agent that enumerated a mission id could start a mission belonging
	// to another crew/workspace. Scope is sourced from the trusted IPC identity
	// the sidecar forwards as query params (workspace_id required, crew_id
	// optional), mirroring InternalIssueHandler.Get.
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	// #1186: for a crew-bound (crwv1) token the binding constrains the
	// lookup — omitting ?crew_id no longer widens it to the whole
	// workspace, so a sibling crew's mission id resolves to 404.
	crewID := effectiveCrewFilter(r)

	selArgs := []any{missionID, wsID}
	selQuery := `SELECT status FROM missions WHERE id = ? AND workspace_id = ?`
	if crewID != "" {
		selQuery = `SELECT status FROM missions WHERE id = ? AND workspace_id = ? AND crew_id = ?`
		selArgs = []any{missionID, wsID, crewID}
	}

	var currentStatus string
	err := h.db.QueryRowContext(r.Context(), selQuery, selArgs...).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "mission not found")
			return
		}
		h.logger.Error("get mission", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if currentStatus != "PLANNING" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("cannot start mission in %s state, must be PLANNING", currentStatus),
		})
		return
	}

	// #1768 — this is where the mission_create hold bites. A mission created
	// under strict/guided autonomy carries an approvals_queue row; until an
	// operator approves it, PLANNING never becomes IN_PROGRESS and the
	// MissionEngine is never handed the mission, so nothing it planned runs.
	//
	// refuseUnlessAutonomyGateApproved is fail-closed on purpose: pending,
	// denied, cancelled and TIMED-OUT rows all answer false. A check phrased
	// as "no pending row blocks me" would have let harbormaster's timeout
	// sweeper turn an unattended hold into a green light. #2258 — the public
	// route (MissionHandler.Start) shares this exact helper now, so it can
	// no longer walk around the hold this route enforces.
	if !refuseUnlessAutonomyGateApproved(w, r, h.db, h.logger, wsID, missionID) {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updQuery := `UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND workspace_id = ?`
	updArgs := []any{now, missionID, wsID}
	if crewID != "" {
		updQuery = `UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND workspace_id = ? AND crew_id = ?`
		updArgs = []any{now, missionID, wsID, crewID}
	}
	if _, err := h.db.ExecContext(r.Context(), updQuery, updArgs...); err != nil {
		h.logger.Error("update mission status", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to start mission")
		return
	}

	// Start the MissionEngine loop for this mission
	if h.missionEngine != nil {
		if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
			h.logger.Error("mission engine start failed", "error", err, "mission_id", missionID)
			// Don't fail the request -- the mission is IN_PROGRESS in DB, engine can catch up
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": missionID, "status": "IN_PROGRESS"})
}

// Get handles GET /api/v1/internal/missions/{missionId}
// Returns mission with tasks and task stats.
func (h *InternalMissionHandler) Get(w http.ResponseWriter, r *http.Request) {
	missionID := r.PathValue("missionId")
	if missionID == "" {
		parts := strings.Split(r.URL.Path, "/")
		for i, p := range parts {
			if p == "missions" && i+1 < len(parts) {
				missionID = parts[i+1]
				break
			}
		}
	}

	// SECURITY (defense-in-depth): scope the mission lookup to the caller's
	// workspace (and crew, when supplied) so an enumerated mission id from
	// another crew/workspace cannot be read. Mirrors InternalIssueHandler.Get.
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	// #1186: same crew-bound constraint as Start — the binding scopes the
	// read, so an enumerated sibling-crew mission id is a 404, not a leak.
	crewID := effectiveCrewFilter(r)

	var m struct {
		ID          string  `json:"id"`
		TraceID     string  `json:"trace_id"`
		Title       string  `json:"title"`
		Description *string `json:"description"`
		Status      string  `json:"status"`
		CreatedAt   string  `json:"created_at"`
	}
	getQuery := `SELECT id, trace_id, title, description, status, created_at FROM missions WHERE id = ? AND workspace_id = ?`
	getArgs := []any{missionID, wsID}
	if crewID != "" {
		getQuery = `SELECT id, trace_id, title, description, status, created_at FROM missions WHERE id = ? AND workspace_id = ? AND crew_id = ?`
		getArgs = []any{missionID, wsID, crewID}
	}
	err := h.db.QueryRowContext(r.Context(), getQuery, getArgs...).Scan(&m.ID, &m.TraceID, &m.Title, &m.Description, &m.Status, &m.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "mission not found")
			return
		}
		h.logger.Error("get mission", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Load tasks
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, title, status, assigned_agent_id, depends_on, result_summary, error_message, task_order
		FROM mission_tasks WHERE mission_id = ? ORDER BY task_order`,
		missionID)
	if err != nil {
		h.logger.Error("get tasks", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type taskSummary struct {
		ID              string  `json:"id"`
		Title           string  `json:"title"`
		Status          string  `json:"status"`
		AssignedAgentID *string `json:"assigned_agent_id"`
		DependsOn       string  `json:"depends_on"`
		ResultSummary   *string `json:"result_summary"`
		ErrorMessage    *string `json:"error_message"`
		TaskOrder       int     `json:"task_order"`
	}
	var tasks []taskSummary
	for rows.Next() {
		var t taskSummary
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.AssignedAgentID, &t.DependsOn, &t.ResultSummary, &t.ErrorMessage, &t.TaskOrder); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mission": m,
		"tasks":   tasks,
	})
}
