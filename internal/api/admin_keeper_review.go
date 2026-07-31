package api

// Manual runs for the four Keeper Reviews evaluators (issue #1555).
//
// The evaluators themselves have been reachable since PR-C, but only by two
// callers: the daily scheduler sweeps and a sidecar holding an internal token.
// Both are machine paths. There was no operator-facing route at all, so
// "check my agents' skills now" was not expressible — and the behaviour
// watchdog, which only fires on a real tool call, had never been exercised
// outside its unit tests. A subsystem that ships without ever having run
// outside its tests is the one that breaks quietly.
//
// This adds one admin route:
//
//	POST /api/v1/admin/keeper/review/{slot}/run
//
// It does NOT re-implement any evaluation. It assembles the subject the
// matching Phase 2 handler expects and calls that handler, so a manual run
// takes exactly the path a scheduled one does: same policy resolution, same
// keeper_requests row, same inbox escalation, same realtime push. The response
// is the handler's own — decision, reason, risk and the request id that
// identifies the run in the audit log.
//
// Two things the internal path gets from its transport have to be supplied
// here instead:
//
//   - The workspace. The internal routes wrap the handler in internalWsCtx,
//     which puts ?workspace_id into the request context for
//     assertBodyWorkspaceMatchesCtx to check against the body. Admin routes get
//     the same context value from RequireWorkspace, so the check works
//     unchanged — but the body's workspace_id is filled in from the session
//     here rather than trusted from the caller.
//   - The tenant binding. A sidecar's internal token is bound to a workspace
//     (and often a crew), and assertBoundCrewWorkspaceDB enforces it. An admin
//     session carries no such binding, so any crew or agent id in the body is
//     checked against the session's workspace explicitly below.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// The slot names, spelled the way the internal routes spell them so an
// operator reading router_internal.go or the docs types the same word twice.
const (
	reviewSlotSkillReview      = "skill-review"
	reviewSlotBehavior         = "behavior"
	reviewSlotMemoryHealth     = "memory-health"
	reviewSlotNegativeLearning = "negative-learning"
)

// keeperReviewSlots is the closed set, in the order the docs list them.
var keeperReviewSlots = []string{
	reviewSlotSkillReview,
	reviewSlotBehavior,
	reviewSlotMemoryHealth,
	reviewSlotNegativeLearning,
}

// keeperReviewSlotAliases maps the names an operator is likely to reach for to
// the canonical slot. The evaluator config card and `crewship keeper aux` key
// the same four evaluators by their llm.Slot names (curator, memory_health,
// negative), so somebody who just read that page will type those; underscores
// for hyphens is the other half-remembered spelling. Accepting them costs
// nothing and removes a guessing game that has no discoverable answer.
var keeperReviewSlotAliases = map[string]string{
	"curator":           reviewSlotSkillReview,
	"skill_review":      reviewSlotSkillReview,
	"memory_health":     reviewSlotMemoryHealth,
	"negative":          reviewSlotNegativeLearning,
	"negative_learning": reviewSlotNegativeLearning,
}

// canonicalReviewSlot resolves a caller-supplied slot to its canonical name,
// or "" when it is not one of ours.
func canonicalReviewSlot(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, known := range keeperReviewSlots {
		if s == known {
			return known
		}
	}
	return keeperReviewSlotAliases[s]
}

// manualProbeToolName is the tool call a behaviour run stages when the caller
// names none. It is deliberately self-describing: the watchdog's verdict lands
// in the audit trail like any other, and whoever reads that row later must be
// able to tell "an operator pressed Run" from "an agent really called this".
const manualProbeToolName = "keeper.manual_probe"

// AdminKeeperReviewHandler is the operator-facing trigger for the Reviews
// evaluators. It holds the same *KeeperPhase2Handler the internal routes use —
// not a copy — so there is exactly one implementation of each evaluation.
type AdminKeeperReviewHandler struct {
	db     *sql.DB
	kp2    *KeeperPhase2Handler
	logger *slog.Logger
}

func NewAdminKeeperReviewHandler(db *sql.DB, kp2 *KeeperPhase2Handler, logger *slog.Logger) *AdminKeeperReviewHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AdminKeeperReviewHandler{db: db, kp2: kp2, logger: logger}
}

// reviewRunBody is the union of the fields an operator may pin for a run. Every
// field is optional: what is not supplied is derived from the workspace (see
// the per-slot builders), which is what makes a bare "run it now" possible from
// a button. workspace_id is accepted only so a caller that sends it can be told
// it is not the way to choose a tenant.
type reviewRunBody struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	CrewID      string `json:"crew_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`

	// skill-review
	SkillID string `json:"skill_id,omitempty"`

	// behavior
	ToolName        string   `json:"tool_name,omitempty"`
	ToolArgsSnippet string   `json:"tool_args_snippet,omitempty"`
	RecentToolCalls []string `json:"recent_tool_calls,omitempty"`

	// negative-learning
	Trigger        string `json:"trigger,omitempty"`
	FailureSnippet string `json:"failure_snippet,omitempty"`
	PriorLesson    string `json:"prior_lesson,omitempty"`
}

// Run executes one evaluator now.
// POST /api/v1/admin/keeper/review/{slot}/run
func (h *AdminKeeperReviewHandler) Run(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	slot := canonicalReviewSlot(r.PathValue("slot"))
	if slot == "" {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"unknown review slot %q — valid slots are: %s",
			r.PathValue("slot"), strings.Join(keeperReviewSlots, ", ")))
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		// Not a formality: the workspace is the tenant the LLM spend is
		// billed to, the scope the crew defaults are picked from, and the
		// value the Phase 2 handlers compare the body against.
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if h.db == nil || h.kp2 == nil {
		replyError(w, http.StatusServiceUnavailable, "Keeper reviews are not available on this server")
		return
	}

	var body reviewRunBody
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			replyError(w, http.StatusBadRequest, "could not read request body")
			return
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				replyError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	if body.WorkspaceID != "" && body.WorkspaceID != workspaceID {
		// Silently rewriting it would run the evaluation against a tenant the
		// caller did not mean; the request as written cannot be honoured.
		replyError(w, http.StatusBadRequest,
			"workspace_id is taken from your session, not the body — remove it or switch workspaces")
		return
	}
	body.WorkspaceID = workspaceID

	// The tenant checks an internal token would have made for us.
	if !h.assertOwnsCrew(w, r.Context(), workspaceID, body.CrewID) {
		return
	}
	if !h.assertOwnsAgent(w, r.Context(), workspaceID, &body) {
		return
	}

	payload, dispatch, ok := h.buildSubject(w, r, slot, body)
	if !ok {
		return
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		replyInternalError(w, h.logger, "encode keeper review subject", err)
		return
	}
	// Same handler, same contract — only the transport differs. The context
	// (workspace, deadline) rides along untouched so
	// assertBodyWorkspaceMatchesCtx sees the session's workspace on both sides.
	inner := r.Clone(r.Context())
	inner.Body = io.NopCloser(bytes.NewReader(encoded))
	inner.ContentLength = int64(len(encoded))
	inner.Header.Set("Content-Type", "application/json")

	h.logger.Info("keeper review: manual run",
		"slot", slot, "workspace_id", workspaceID, "actor", actorIDForReview(r))
	dispatch(w, inner)
}

// actorIDForReview names the operator in the run log. Manual runs cost money
// and write to the audit trail, so "who asked" belongs in the server log even
// though the keeper_requests row attributes the decision to Keeper itself.
func actorIDForReview(r *http.Request) string {
	if u := UserFromContext(r.Context()); u != nil {
		return u.ID
	}
	return ""
}

// assertOwnsCrew refuses a crew from another tenant. Empty is fine — the
// per-slot builders fill it in from the workspace when they need one.
func (h *AdminKeeperReviewHandler) assertOwnsCrew(w http.ResponseWriter, ctx context.Context, workspaceID, crewID string) bool {
	if crewID == "" {
		return true
	}
	exists, err := crewExists(ctx, h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve crew for keeper review", err)
		return false
	}
	if !exists {
		replyError(w, http.StatusForbidden,
			fmt.Sprintf("crew %q does not belong to this workspace", crewID))
		return false
	}
	return true
}

// assertOwnsAgent refuses an agent from another tenant, and fills in the
// agent's crew when the caller named an agent but no crew — the evaluators that
// resolve a policy need one, and the agent already implies it.
func (h *AdminKeeperReviewHandler) assertOwnsAgent(w http.ResponseWriter, ctx context.Context, workspaceID string, body *reviewRunBody) bool {
	if body.AgentID == "" {
		return true
	}
	var crewID sql.NullString
	err := h.db.QueryRowContext(ctx,
		`SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.AgentID, workspaceID).Scan(&crewID)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusForbidden,
			fmt.Sprintf("agent %q does not belong to this workspace", body.AgentID))
		return false
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve agent for keeper review", err)
		return false
	}
	if body.CrewID == "" {
		body.CrewID = crewID.String
	}
	return true
}

// buildSubject assembles the payload for one slot and returns the handler that
// consumes it. On a caller-fixable problem it writes the response and returns
// ok=false.
func (h *AdminKeeperReviewHandler) buildSubject(
	w http.ResponseWriter, r *http.Request, slot string, body reviewRunBody,
) (any, func(http.ResponseWriter, *http.Request), bool) {
	switch slot {
	case reviewSlotSkillReview:
		subject, ok := h.skillSubject(w, r, body)
		return subject, h.kp2.HandleSkillReview, ok
	case reviewSlotBehavior:
		subject, ok := h.behaviorSubject(w, r, body)
		return subject, h.kp2.HandleBehavior, ok
	case reviewSlotMemoryHealth:
		subject, ok := h.memoryHealthSubject(w, r, body)
		return subject, h.kp2.HandleMemoryHealth, ok
	case reviewSlotNegativeLearning:
		subject, ok := h.negativeSubject(w, r, body)
		return subject, h.kp2.HandleNegativeLearning, ok
	}
	// canonicalReviewSlot has already rejected everything else.
	replyError(w, http.StatusBadRequest, "unknown review slot")
	return nil, nil, false
}

// skillSubject loads the skill's real state from the catalog rather than
// letting the caller describe it. A review of an operator-supplied fiction
// would land in the audit trail looking exactly like a review of the truth.
func (h *AdminKeeperReviewHandler) skillSubject(
	w http.ResponseWriter, r *http.Request, body reviewRunBody,
) (skillReviewBody, bool) {
	ctx := r.Context()
	var (
		id, name, desc, lifecycle, lastUsed string
	)
	const cols = `s.id, s.name, COALESCE(s.description, ''),
	              COALESCE(s.lifecycle_state, 'active'), COALESCE(s.last_used_at, '')`
	var err error
	if body.SkillID != "" {
		err = h.db.QueryRowContext(ctx,
			`SELECT `+cols+` FROM skills s WHERE s.id = ?`, body.SkillID).
			Scan(&id, &name, &desc, &lifecycle, &lastUsed)
	} else {
		// No skill named: review the stalest one this workspace's agents
		// actually have. That is what "check my agents' skills now" means, and
		// the least-recently-used skill is the one a review is most likely to
		// have something to say about.
		err = h.db.QueryRowContext(ctx, `
			SELECT `+cols+`
			  FROM skills s
			  JOIN agent_skills sk ON sk.skill_id = s.id AND sk.enabled = 1
			  JOIN agents a ON a.id = sk.agent_id
			 WHERE a.workspace_id = ? AND a.deleted_at IS NULL
			 GROUP BY s.id
			 ORDER BY COALESCE(s.last_used_at, '') ASC, s.id ASC
			 LIMIT 1`, body.WorkspaceID).
			Scan(&id, &name, &desc, &lifecycle, &lastUsed)
	}
	if err == sql.ErrNoRows {
		if body.SkillID != "" {
			replyError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", body.SkillID))
		} else {
			replyError(w, http.StatusBadRequest,
				"no skill is assigned to an agent in this workspace — assign one, or name a skill with skill_id")
		}
		return skillReviewBody{}, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load skill for keeper review", err)
		return skillReviewBody{}, false
	}

	// Assignments + agents, scoped to this workspace: the catalog is global but
	// the question being asked is about this tenant's use of the skill.
	var assignments int
	_ = h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_skills sk
		  JOIN agents a ON a.id = sk.agent_id
		 WHERE sk.skill_id = ? AND sk.enabled = 1
		   AND a.workspace_id = ? AND a.deleted_at IS NULL`,
		id, body.WorkspaceID).Scan(&assignments)

	agents := []string{}
	if rows, qerr := h.db.QueryContext(ctx, `
		SELECT a.slug FROM agent_skills sk
		  JOIN agents a ON a.id = sk.agent_id
		 WHERE sk.skill_id = ? AND sk.enabled = 1
		   AND a.workspace_id = ? AND a.deleted_at IS NULL
		 ORDER BY a.slug ASC LIMIT 20`, id, body.WorkspaceID); qerr == nil {
		defer rows.Close()
		for rows.Next() {
			var slug string
			if rows.Scan(&slug) == nil {
				agents = append(agents, slug)
			}
		}
	}

	// The same 30-day window the daily sweep uses, so a manual run and the
	// nightly one are answering the same question.
	const lookbackDays = 30
	cutoff := time.Now().UTC().AddDate(0, 0, -lookbackDays).Format(time.RFC3339)
	stats := gatekeeper.SkillStats{LookbackDays: lookbackDays}
	var lastInvocation sql.NullString
	_ = h.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN exit_code <> 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(MAX(invoked_at), '')
		  FROM skill_invocations
		 WHERE skill_id = ? AND invoked_at >= ?`, id, cutoff).
		Scan(&stats.InvocationCount, &stats.ErrorCount, &lastInvocation)
	if lastInvocation.Valid {
		stats.LastUsedAt = lastInvocation.String
	}

	return skillReviewBody{
		WorkspaceID:      body.WorkspaceID,
		CrewID:           body.CrewID,
		SkillID:          id,
		SkillName:        name,
		SkillDescription: desc,
		LifecycleState:   lifecycle,
		LastUsedAt:       lastUsed,
		Assignments:      assignments,
		AssignedAgents:   agents,
		Stats:            stats,
	}, true
}

// behaviorSubject stages the tool call the watchdog evaluates. With no
// tool_name it stages the self-describing probe — which is the whole point of
// the manual trigger: the watchdog only fires on a tool call, so there was
// previously no way to make it run at all.
func (h *AdminKeeperReviewHandler) behaviorSubject(
	w http.ResponseWriter, r *http.Request, body reviewRunBody,
) (behaviorBody, bool) {
	crewID, crewName, ok := h.resolveCrew(w, r.Context(), body)
	if !ok {
		return behaviorBody{}, false
	}
	tool := body.ToolName
	args := body.ToolArgsSnippet
	if tool == "" {
		tool = manualProbeToolName
		if args == "" {
			args = "operator-triggered watchdog run — no agent tool call was involved"
		}
	}
	return behaviorBody{
		WorkspaceID:     body.WorkspaceID,
		CrewID:          crewID,
		AgentID:         body.AgentID,
		AgentName:       h.agentName(r.Context(), body.AgentID),
		CrewName:        crewName,
		ToolName:        tool,
		ToolArgsSnippet: args,
		RecentToolCalls: body.RecentToolCalls,
	}, true
}

// memoryHealthSubject computes the health snapshot server-side via
// consolidate.ComputeHealth — the same function the daily sweep and the memory
// UI use, so a manual run cannot report a different score than the page next to
// it. A crew is optional here: with none, the snapshot is workspace-wide.
func (h *AdminKeeperReviewHandler) memoryHealthSubject(
	w http.ResponseWriter, r *http.Request, body reviewRunBody,
) (memoryHealthBody, bool) {
	crewID, crewName := body.CrewID, ""
	if crewID == "" {
		// Only auto-pick when the answer is unambiguous. Silently reviewing one
		// of five crews would be a verdict about a scope the operator did not
		// choose.
		if id, name, only := h.onlyCrew(r.Context(), body.WorkspaceID); only {
			crewID, crewName = id, name
		}
	} else {
		crewName = h.crewName(r.Context(), crewID)
	}

	snap, err := consolidate.ComputeHealth(r.Context(), h.db, body.WorkspaceID, crewID)
	if err != nil {
		replyInternalError(w, h.logger, "compute memory health for keeper review", err)
		return memoryHealthBody{}, false
	}

	var contradictions int
	_ = h.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM memory_relations mr
		  JOIN journal_entries je ON je.id = mr.entry_id
		 WHERE je.workspace_id = ? AND (? = '' OR je.crew_id = ?)
		   AND mr.relation_kind = 'refutes'`,
		body.WorkspaceID, crewID, crewID).Scan(&contradictions)

	return memoryHealthBody{
		WorkspaceID:        body.WorkspaceID,
		CrewID:             crewID,
		CrewName:           crewName,
		AgentName:          h.agentName(r.Context(), body.AgentID),
		Snapshot:           snap,
		ContradictionCount: contradictions,
	}, true
}

// negativeSubject builds the failure the lesson extractor reasons about.
//
// There is no placeholder to fall back on here. The evaluator's trigger set is
// closed (run_failed / guardrail_warn / guardrail_error / keeper_execute_deny)
// and its ALLOW path writes a lesson into an agent's memory — inventing a
// failure to have something to run would put fiction there. So a bare run
// learns from the workspace's most recent REAL failure, and a workspace that
// has not failed is told exactly that.
func (h *AdminKeeperReviewHandler) negativeSubject(
	w http.ResponseWriter, r *http.Request, body reviewRunBody,
) (negativeLearningBody, bool) {
	trigger := body.Trigger
	snippet := body.FailureSnippet
	crewID, agentID := body.CrewID, body.AgentID

	if trigger == "" || snippet == "" {
		var (
			summary             string
			lastCrew, lastAgent sql.NullString
		)
		err := h.db.QueryRowContext(r.Context(), `
			SELECT summary, crew_id, agent_id
			  FROM journal_entries
			 WHERE workspace_id = ? AND entry_type = ?
			 ORDER BY ts DESC
			 LIMIT 1`, body.WorkspaceID, string(journal.EntryRunFailed)).
			Scan(&summary, &lastCrew, &lastAgent)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest,
				"nothing has failed in this workspace yet, so there is no lesson to extract — "+
					"pass trigger (run_failed, guardrail_warn, guardrail_error, keeper_execute_deny) "+
					"and failure_snippet to evaluate a specific failure")
			return negativeLearningBody{}, false
		}
		if err != nil {
			replyInternalError(w, h.logger, "load last failure for keeper review", err)
			return negativeLearningBody{}, false
		}
		if trigger == "" {
			trigger = string(gatekeeper.NegTriggerRunFailed)
		}
		if snippet == "" {
			snippet = summary
		}
		// The failure's own crew/agent, so the lesson (if one is written) lands
		// where the failure happened rather than nowhere.
		if crewID == "" {
			crewID = lastCrew.String
		}
		if agentID == "" {
			agentID = lastAgent.String
		}
	}

	return negativeLearningBody{
		WorkspaceID:    body.WorkspaceID,
		CrewID:         crewID,
		AgentID:        agentID,
		AgentName:      h.agentName(r.Context(), agentID),
		CrewName:       h.crewName(r.Context(), crewID),
		Trigger:        trigger,
		ToolName:       body.ToolName,
		FailureSnippet: snippet,
		PriorLesson:    body.PriorLesson,
	}, true
}

// resolveCrew returns the crew a run is scoped to, defaulting to the
// workspace's only crew when there is exactly one. The behaviour evaluator
// resolves policy strictly (a transient failure must not downgrade block to
// warn), so it cannot run without one.
func (h *AdminKeeperReviewHandler) resolveCrew(
	w http.ResponseWriter, ctx context.Context, body reviewRunBody,
) (id, name string, ok bool) {
	if body.CrewID != "" {
		return body.CrewID, h.crewName(ctx, body.CrewID), true
	}
	if id, name, only := h.onlyCrew(ctx, body.WorkspaceID); only {
		return id, name, true
	}
	replyError(w, http.StatusBadRequest,
		"this workspace has more than one crew (or none) — name the one to check with crew_id")
	return "", "", false
}

// onlyCrew returns the workspace's single crew, and whether there was exactly
// one. Two crews is not an error, it is a question only the operator can answer.
func (h *AdminKeeperReviewHandler) onlyCrew(ctx context.Context, workspaceID string) (id, name string, only bool) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, name FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY id ASC LIMIT 2`,
		workspaceID)
	if err != nil {
		return "", "", false
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if n > 1 {
			return "", "", false
		}
		if err := rows.Scan(&id, &name); err != nil {
			return "", "", false
		}
	}
	return id, name, n == 1
}

func (h *AdminKeeperReviewHandler) crewName(ctx context.Context, crewID string) string {
	if crewID == "" {
		return ""
	}
	var name string
	_ = h.db.QueryRowContext(ctx, `SELECT name FROM crews WHERE id = ?`, crewID).Scan(&name)
	return name
}

func (h *AdminKeeperReviewHandler) agentName(ctx context.Context, agentID string) string {
	if agentID == "" {
		return ""
	}
	var name string
	_ = h.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, agentID).Scan(&name)
	return name
}
