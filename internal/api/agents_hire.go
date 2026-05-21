package api

// Ephemeral-agent hire endpoint (PRD §6 F5 / PR-D).
//
// POST /api/v1/agents/hire spawns a short-lived "contractor" agent
// against a Crew, bounded by a TTL the operator (or LEAD via sidecar
// /spawn) chose at hire time. The endpoint goes through the per-crew
// autonomy policy (PR-B F2) before doing any DB work so a strict crew
// cannot have ephemerals slipped in by a misconfigured client:
//
//	strict   → 403 with structured reason. Operator must dial down.
//	guided   → 202 + blocking inbox item. Hire is staged; an Approve
//	           click on the inbox flips the agent to live.
//	trusted  → 201 + non-blocking inbox item. Hire goes through.
//	full     → 201 + journal-only entry. No inbox noise.
//
// On top of the policy gate, every hire counts against the crew's
// crews.max_ephemeral_agents quota (default 10, set by v100). Ghosts
// (expired_at IS NOT NULL) do NOT count — the whole point of the
// ghost state is to preserve audit history without consuming quota.
// Hitting the quota returns 429 with a "max reached" message; that
// lets the CLI render a hint to either rehire a ghost or bump
// max_ephemeral_agents via PATCH /api/v1/crews/{id}.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/policy"
)

// hireRequest is the POST /api/v1/agents/hire body. Slug-based
// references (crew_slug, template_slug) are accepted alongside id-based
// references so CLI callers don't need to round-trip a `list` call to
// resolve ids first. CrewID/CrewSlug are mutually exclusive at the
// validator; one of the two is required.
type hireRequest struct {
	CrewID       string `json:"crew_id"`
	CrewSlug     string `json:"crew_slug"`
	TemplateSlug string `json:"template_slug"`
	Model        string `json:"model"`
	TTLMinutes   int    `json:"ttl_minutes"`
	Reason       string `json:"reason"`
	ParentLeadID string `json:"parent_lead_id"`
}

// hireResponse is what the public API returns on the happy path. Mirrors
// the agentResponse shape for the ephemeral-specific fields so a CLI
// that just printed the JSON gets the new lifecycle bits inline.
type hireResponse struct {
	ID            string  `json:"id"`
	CrewID        *string `json:"crew_id"`
	WorkspaceID   string  `json:"workspace_id"`
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	Ephemeral     bool    `json:"ephemeral"`
	ExpiresAt     *string `json:"expires_at"`
	ExpiredAt     *string `json:"expired_at"`
	ParentLeadID  *string `json:"parent_lead_id"`
	HireReason    *string `json:"hire_reason"`
	PendingReview bool    `json:"pending_review"`
	InboxItemID   string  `json:"inbox_item_id,omitempty"`
	Decision      string  `json:"decision"`
}

// defaultHireTTLMinutes is the floor used when the client passes 0 (or
// a negative value). 30 min matches the PRD's "contractor for a single
// task" sizing — long enough for a multi-step ask, short enough that an
// abandoned hire ghosts before consuming meaningful quota.
const defaultHireTTLMinutes = 30

// maxHireTTLMinutes caps a single hire to one work-day so a forgotten
// hire doesn't sit around for weeks burning a quota slot. Operators
// who need a longer-lived agent should either rehire or convert the
// agent to permanent (clear ephemeral via a future admin endpoint).
const maxHireTTLMinutes = 24 * 60

// validHireModels mirrors the LLM-model values we accept. Empty model
// is allowed and falls back to the template's default at provisioning
// time. We do NOT validate against an enum here because new model
// strings ship on every LLM rev — the orchestrator's adapter layer is
// the authoritative validator at first-message time.

// Hire spawns a new ephemeral agent under the named crew. See package
// docstring above for the policy + quota mechanics. Returns:
//
//	201 Created  — live ephemeral (trusted/full)
//	202 Accepted — waiting on inbox approval (guided)
//	403         — strict autonomy rejected the hire
//	404         — crew or template not found
//	429         — per-crew quota reached
//	500         — DB error
func (h *AgentHandler) Hire(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	// MANAGER+ matches the existing Create gate. Human hires from the
	// dashboard / CLI use this path; LEAD-initiated hires arrive via
	// the sidecar /spawn endpoint, which proxies through this same
	// handler but with an internal-token-elevated role so the LEAD's
	// reviewer role (typically MEMBER on its own agent row) doesn't
	// block the call.
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var req hireRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.CrewID == "" && req.CrewSlug == "" {
		replyError(w, http.StatusBadRequest, "crew_id or crew_slug is required")
		return
	}
	if req.CrewID != "" && req.CrewSlug != "" {
		replyError(w, http.StatusBadRequest, "crew_id and crew_slug are mutually exclusive")
		return
	}
	if strings.TrimSpace(req.TemplateSlug) == "" {
		replyError(w, http.StatusBadRequest, "template_slug is required")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		replyError(w, http.StatusBadRequest, "reason is required (audit + history trail)")
		return
	}

	// Clamp TTL into a sane window. We do NOT 400 on out-of-range
	// values because the CLI default is 0 (let the server decide); a
	// hard reject there would punish the happy path.
	ttlMin := req.TTLMinutes
	if ttlMin <= 0 {
		ttlMin = defaultHireTTLMinutes
	}
	if ttlMin > maxHireTTLMinutes {
		ttlMin = maxHireTTLMinutes
	}

	// 1. Resolve crew → enforce workspace scope. Soft-delete check so
	// a deleted crew can't be hired into via a stale slug from a
	// long-running session.
	var (
		crewID      string
		crewSlug    string
		maxAgents   int
		crewLookup  = req.CrewID
		crewLookupB = "id"
	)
	if crewLookup == "" {
		crewLookup = req.CrewSlug
		crewLookupB = "slug"
	}
	query := fmt.Sprintf(
		`SELECT id, slug, max_ephemeral_agents FROM crews
		 WHERE %s = ? AND workspace_id = ? AND deleted_at IS NULL`, crewLookupB)
	err := h.db.QueryRowContext(r.Context(), query, crewLookup, workspaceID).
		Scan(&crewID, &crewSlug, &maxAgents)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Crew not found in this workspace")
		return
	}
	if err != nil {
		h.logger.Error("hire: load crew", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// 2. Resolve parent_lead_id (if provided) — same workspace check
	// as the crew. A LEAD from a different workspace/crew CANNOT
	// parent an ephemeral here; that would let cross-tenant data
	// flow through the parent edge.
	var parentLeadID *string
	if strings.TrimSpace(req.ParentLeadID) != "" {
		var leadID, leadCrewID string
		err := h.db.QueryRowContext(r.Context(), `
			SELECT id, COALESCE(crew_id, '') FROM agents
			WHERE id = ? AND workspace_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL`,
			req.ParentLeadID, workspaceID).Scan(&leadID, &leadCrewID)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "parent_lead_id not found or not a LEAD in this workspace")
			return
		}
		if err != nil {
			h.logger.Error("hire: load parent lead", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if leadCrewID != crewID {
			replyError(w, http.StatusBadRequest, "parent_lead_id belongs to a different crew")
			return
		}
		parentLeadID = &leadID
	}

	// 3. Resolve template → name + default LLM. We accept built-in
	// templates and workspace-owned ones (same predicate as the
	// crew_templates Get handler).
	tmplName, tmplDefaultModel, err := h.lookupCrewTemplate(r, workspaceID, req.TemplateSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Template not found: "+req.TemplateSlug)
			return
		}
		h.logger.Error("hire: load template", "slug", req.TemplateSlug, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = tmplDefaultModel
	}

	// 4. Policy gate. nil resolver path keeps tests compiling against
	// the legacy NewAgentHandler — defaults to "guided" (inbox
	// approve), which is the conservative behavior PR-D ships with
	// until the router calls SetPolicyResolver.
	decision := policy.DecisionInboxApprove
	autonomyLevel := policy.AutonomyGuided
	if h.policyResolver != nil {
		pol, perr := h.policyResolver.Resolve(r.Context(), crewID)
		if perr != nil {
			h.logger.Error("hire: resolve policy", "crew_id", crewID, "error", perr)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		decision = pol.DecideAction(policy.ActionEphemeralSpawn)
		autonomyLevel = pol.AutonomyLevel
	}
	if decision == policy.DecisionRejected {
		// Structured 403 includes the autonomy level so the CLI can
		// suggest the right `crewship policy set` invocation in its
		// error message instead of just bouncing the user.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":          "Ephemeral hire rejected by policy",
			"reason":         "autonomy_level=" + string(autonomyLevel) + " forbids ephemeral_spawn",
			"crew_id":        crewID,
			"autonomy_level": string(autonomyLevel),
		})
		return
	}

	// 5. Per-crew quota + insert, wrapped in a single BEGIN IMMEDIATE
	// transaction so a concurrent hire on the same crew cannot
	// oversubscribe max_ephemeral_agents. Without the lock, the
	// COUNT(*) + INSERT are separate operations: two simultaneous
	// hires at the quota boundary each see (liveCount == max-1) and
	// each commit a row, pushing the crew to (max+1).
	//
	// Counts only LIVE ephemerals (ghosts are preserved for audit
	// and DO NOT consume a slot). Run after the policy gate so a
	// strict-rejected request never touches the quota counter —
	// keeps the 429 surface honest about the budget.
	//
	// sql.LevelSerializable → BEGIN IMMEDIATE on modernc.org/sqlite
	// (same idiom used by internal/backup/lock.go +
	// internal/api/memory_config_handler.go).

	// 6. Compute row identity + lifecycle stamps outside the tx so
	// these are stable across a retry (if a future caller adds one).
	now := time.Now().UTC()
	agentID := generateCUID()
	slug := buildEphemeralSlug(req.TemplateSlug, agentID)
	name := tmplName
	if name == "" {
		name = req.TemplateSlug
	}
	expiresAt := now.Add(time.Duration(ttlMin) * time.Minute).Format(time.RFC3339)
	createdAt := now.Format(time.RFC3339)
	hireReason := buildInitialReason(req.Reason, createdAt)

	// MEMBER role for the row itself — ephemerals never act as a LEAD
	// in their parent crew.
	//
	// Initial status depends on the policy decision:
	//   - DecisionInboxApprove (guided): status='PENDING_REVIEW'. The
	//     chatbridge refuses to start an agent in this state until the
	//     approve-hire endpoint flips it to IDLE. This is what
	//     actually makes "guided" a blocking gate — without the
	//     status sentinel, a client could WS-message the agent the
	//     instant after the 202 lands and the container would spin up
	//     before the operator clicked Approve.
	//   - Everything else: status='IDLE'. First chat message
	//     transitions to RUNNING the same way permanent agents do.
	initialStatus := "IDLE"
	if decision == policy.DecisionInboxApprove {
		initialStatus = "PENDING_REVIEW"
	}

	llmProvider := provideForModel(model)
	var llmProviderArg *string
	if llmProvider != "" {
		llmProviderArg = &llmProvider
	}
	var llmModelArg *string
	if model != "" {
		llmModelArg = &model
	}

	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		h.logger.Error("hire: begin tx", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer func() { _ = tx.Rollback() }()

	var liveCount int
	if err := tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM agents
		WHERE crew_id = ? AND ephemeral = 1
		  AND expired_at IS NULL AND deleted_at IS NULL`,
		crewID).Scan(&liveCount); err != nil {
		h.logger.Error("hire: count live ephemerals", "crew_id", crewID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if liveCount >= maxAgents {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":   "Ephemeral quota reached",
			"reason":  fmt.Sprintf("crew has %d live ephemerals (max %d); rehire a ghost or raise crews.max_ephemeral_agents", liveCount, maxAgents),
			"crew_id": crewID,
			"live":    liveCount,
			"max":     maxAgents,
		})
		return
	}

	if _, err = tx.ExecContext(r.Context(), `
		INSERT INTO agents (
			id, crew_id, workspace_id, name, slug, agent_role, status,
			cli_adapter, llm_provider, llm_model, tool_profile, memory_enabled,
			ephemeral, expires_at, parent_lead_id, hire_reason,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'AGENT', ?,
		        'CLAUDE_CODE', ?, ?, 'CODING', 1,
		        1, ?, ?, ?,
		        ?, ?)`,
		agentID, crewID, workspaceID, name, slug, initialStatus,
		llmProviderArg, llmModelArg,
		expiresAt, parentLeadID, hireReason,
		createdAt, createdAt,
	); err != nil {
		h.logger.Error("hire: insert agent", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if err := tx.Commit(); err != nil {
		h.logger.Error("hire: commit tx", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}

	// 7. Inbox + audit by decision.
	inboxID := ""
	pendingReview := false
	httpStatus := http.StatusCreated
	switch decision {
	case policy.DecisionInboxApprove:
		// Blocking inbox item — the agent row exists in
		// PENDING_REVIEW; the approve-hire handler is the ONLY path
		// to flip it to IDLE. The inbox waitpoint is what surfaces
		// the approve button to the operator, so if the write fails
		// the agent is bricked (status=PENDING_REVIEW with no
		// inbox row to act on). Fail the request loudly so the
		// caller can retry — the deferred Rollback above already
		// undid the agent INSERT.
		inboxID = generateCUID()
		if err := h.writeInboxItem(r, inboxID, workspaceID, agentID, crewID, name,
			"hire", hireInboxTitle(name, ttlMin), hireInboxBody(req.Reason, req.TemplateSlug, ttlMin, model),
			userID, true); err != nil {
			h.logger.Error("hire: required inbox waitpoint write failed",
				"error", err, "agent_id", agentID, "inbox_id", inboxID)
			// Roll back the agent row so the caller's retry doesn't
			// trip the UNIQUE(workspace_id, slug) constraint and so
			// quota stays consistent. The tx was already committed
			// above, so issue a compensating DELETE here.
			if _, derr := h.db.ExecContext(r.Context(),
				`DELETE FROM agents WHERE id = ? AND workspace_id = ?`,
				agentID, workspaceID); derr != nil {
				h.logger.Error("hire: compensating delete after inbox failure",
					"error", derr, "agent_id", agentID)
			}
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		pendingReview = true
		httpStatus = http.StatusAccepted
	case policy.DecisionAutoLogInbox:
		// Non-blocking inbox visibility entry. The agent is already
		// live; we're just letting the operator audit after the fact.
		// A failed inbox write here is a missed audit row, not a
		// bricked agent — log and continue.
		inboxID = generateCUID()
		if err := h.writeInboxItem(r, inboxID, workspaceID, agentID, crewID, name,
			"hire", hireInboxTitle(name, ttlMin), hireInboxBody(req.Reason, req.TemplateSlug, ttlMin, model),
			userID, false); err != nil {
			h.logger.Warn("hire: non-blocking inbox write failed",
				"error", err, "agent_id", agentID)
			inboxID = ""
		}
	case policy.DecisionAutoLogJournal, policy.DecisionAutoJournal:
		// journal-only; the WriteAuditLog below covers it. No inbox
		// noise per PRD §6 F5 (trusted/full crews don't want a hire
		// per inbox row).
	}

	WriteAuditLog(r.Context(), h.db, h.journal, "agent.hired", "AGENT", agentID, userID, workspaceID, map[string]interface{}{
		"crew_id":        crewID,
		"crew_slug":      crewSlug,
		"template_slug":  req.TemplateSlug,
		"ttl_minutes":    ttlMin,
		"reason":         req.Reason,
		"model":          model,
		"parent_lead":    parentLeadID,
		"decision":       string(decision),
		"autonomy_level": string(autonomyLevel),
		"pending_review": pendingReview,
	})

	h.broadcastAgentEvent("agent.hired", workspaceID, map[string]string{
		"id":        agentID,
		"crew_id":   crewID,
		"name":      name,
		"slug":      slug,
		"ephemeral": "true",
	})

	crewIDOut := crewID
	expiresOut := expiresAt
	reasonOut := hireReason
	var parentOut *string
	if parentLeadID != nil {
		parentOut = parentLeadID
	}
	writeJSON(w, httpStatus, hireResponse{
		ID:            agentID,
		CrewID:        &crewIDOut,
		WorkspaceID:   workspaceID,
		Slug:          slug,
		Name:          name,
		Status:        initialStatus,
		Ephemeral:     true,
		ExpiresAt:     &expiresOut,
		ExpiredAt:     nil,
		ParentLeadID:  parentOut,
		HireReason:    &reasonOut,
		PendingReview: pendingReview,
		InboxItemID:   inboxID,
		Decision:      string(decision),
	})
}

// lookupCrewTemplate fetches a template by slug, honoring the built-in
// + workspace-owned predicate so a workspace cannot hire from another
// workspace's private templates.
func (h *AgentHandler) lookupCrewTemplate(r *http.Request, workspaceID, slug string) (name string, defaultModel string, err error) {
	// crew_templates has no first-class default-model column; the
	// model usually lives inside agents_json. For the MVP we leave
	// the model empty and let the orchestrator default kick in at
	// first message — clients can always pass --model on the hire.
	err = h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(name, '') FROM crew_templates
		WHERE slug = ? AND (is_builtin = 1 OR workspace_id = ?)`,
		slug, workspaceID).Scan(&name)
	return name, "", err
}

// buildEphemeralSlug constructs a deterministic, collision-resistant
// slug for an ephemeral agent. Format: "<template-slug>-eph-<6 hex>".
//
// The CUID generator produces "c<base36(ts)><4-hex-counter><8-hex-rand>"
// (see internal/api/cuid.go). The shared `c<ts>` prefix collides between
// hires issued in the same millisecond; the trailing 8 random hex chars
// don't. We pull the suffix from the END of the CUID so two hires from
// the same template don't write the same row slug — that UNIQUE
// (workspace_id, slug) constraint would 500 the second hire otherwise.
//
// 6 hex chars (24 bits) keeps collision probability at ~1 in 16M per
// pair, which is well below the per-crew quota.
func buildEphemeralSlug(templateSlug, agentID string) string {
	suffix := agentID
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	base := strings.ToLower(strings.TrimSpace(templateSlug))
	if base == "" {
		base = "agent"
	}
	return base + "-eph-" + suffix
}

// buildInitialReason composes the first hire_reason entry. Format
// matches what rehire appends so the column reads as a chronological
// log:
//
//	[2026-05-21T11:00:00Z] hire: ship the docs site
//	[2026-05-22T09:00:00Z] rehire: docs need another pass
func buildInitialReason(reason, ts string) string {
	return fmt.Sprintf("[%s] hire: %s", ts, strings.TrimSpace(reason))
}

// provideForModel guesses an LLM provider string from a model name.
// Empty model returns empty (caller stores NULL). We deliberately do
// NOT 400 on unknown model strings — new model releases ship faster
// than we'd refresh this map; the adapter layer rejects at first
// message if the combination is wrong.
func provideForModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude"):
		return "ANTHROPIC"
	case strings.HasPrefix(m, "gpt") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4"):
		return "OPENAI"
	case strings.HasPrefix(m, "gemini"):
		return "GOOGLE"
	default:
		return ""
	}
}

// hireInboxTitle is the inbox row's title. Kept short — the body
// carries the reasoning. The 30/180 mark in parens mirrors how the
// approvals UI surfaces TTL in other flows.
func hireInboxTitle(name string, ttlMin int) string {
	return fmt.Sprintf("Hire ephemeral agent: %s (%dm)", name, ttlMin)
}

// hireInboxBody renders the markdown body the operator sees when they
// expand the inbox row. We include the reason, template, model, and
// TTL so the operator doesn't need to dig through another panel to
// make the approve/deny call.
func hireInboxBody(reason, templateSlug string, ttlMin int, model string) string {
	modelLine := model
	if modelLine == "" {
		modelLine = "(template default)"
	}
	return fmt.Sprintf(
		"**Template:** `%s`\n**Model:** `%s`\n**TTL:** %d minutes\n\n**Reason:** %s",
		templateSlug, modelLine, ttlMin, reason)
}

// rehireRequest is the POST /api/v1/agents/{agentId}/rehire body. The
// agent id is in the path; only the lifecycle bits (new TTL + reason)
// land in the body. Reason is required for the same audit-trail reason
// as Hire — a rehire without a reason loses why the operator extended.
type rehireRequest struct {
	TTLMinutes int    `json:"ttl_minutes"`
	Reason     string `json:"reason"`
}

// Rehire resets the lifecycle on an existing ephemeral agent: clears
// expired_at (the agent stops being a ghost), pushes expires_at
// forward by ttl_minutes, and appends a new reason line to
// hire_reason so the audit history accumulates instead of being
// overwritten.
//
// The container is NOT rebuilt by this handler — Crewship's container
// provider tier (Docker/K8s) is the runtime layer; we only flip DB
// state and emit a "container needs recycle" hint via the WS broadcast.
// The chatbridge auto-provisions a fresh container on the next message
// the way it does for any restarted agent.
//
// Returns:
//
//	200 OK     — rehire succeeded, agent is live again
//	403       — RBAC reject (MEMBER/VIEWER) or policy reject (strict)
//	404       — agent not found in workspace, or not ephemeral
//	500       — DB error
func (h *AgentHandler) Rehire(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	agentID := r.PathValue("agentId")

	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agentId is required")
		return
	}

	var req rehireRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		replyError(w, http.StatusBadRequest, "reason is required (history trail)")
		return
	}

	ttlMin := req.TTLMinutes
	if ttlMin <= 0 {
		ttlMin = defaultHireTTLMinutes
	}
	if ttlMin > maxHireTTLMinutes {
		ttlMin = maxHireTTLMinutes
	}

	// 1. Load the existing row + scope check. Soft-deleted agents are
	// 404'd; permanent (ephemeral=0) agents are 404'd too — rehire is
	// semantically nonsensical on those and we don't want a typo'd id
	// to silently flip a permanent agent into the ephemeral lifecycle.
	//
	// We also read the persisted status so the response can echo it
	// back accurately. Hardcoding "IDLE" here misreports a row that's
	// mid-mission (status=RUNNING) at the moment the operator extends
	// the TTL.
	var (
		crewID        string
		isEphemeral   int
		oldExpiresAt  sql.NullString
		oldExpiredAt  sql.NullString
		oldHireReason sql.NullString
		oldName       string
		oldStatus     string
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(crew_id, ''), ephemeral, expires_at, expired_at,
		       COALESCE(hire_reason, ''), name, status
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).
		Scan(&crewID, &isEphemeral, &oldExpiresAt, &oldExpiredAt, &oldHireReason, &oldName, &oldStatus)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}
	if err != nil {
		h.logger.Error("rehire: load agent", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if isEphemeral != 1 {
		// Contract: 404 "not found or not ephemeral". Returning 400
		// here would imply a client error to fix, but the agent
		// fundamentally cannot be rehired regardless of request
		// shape — same 404 surface as the sql.ErrNoRows branch above
		// keeps caller logic uniform.
		replyError(w, http.StatusNotFound, "Agent not found or not ephemeral")
		return
	}

	// 2. Policy gate (mirrors Hire). Strict crews cannot rehire either
	// — the rejection reason is the same: ephemeral activity is
	// disallowed on this crew. Reuses ActionEphemeralSpawn so the
	// matrix stays single-source-of-truth.
	autonomyLevel := policy.AutonomyGuided
	decision := policy.DecisionInboxApprove
	if h.policyResolver != nil && crewID != "" {
		pol, perr := h.policyResolver.Resolve(r.Context(), crewID)
		if perr != nil {
			h.logger.Error("rehire: resolve policy", "crew_id", crewID, "error", perr)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		decision = pol.DecideAction(policy.ActionEphemeralSpawn)
		autonomyLevel = pol.AutonomyLevel
	}
	if decision == policy.DecisionRejected {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":          "Ephemeral rehire rejected by policy",
			"reason":         "autonomy_level=" + string(autonomyLevel) + " forbids ephemeral_spawn",
			"crew_id":        crewID,
			"autonomy_level": string(autonomyLevel),
		})
		return
	}

	// 3. Quota check + UPDATE wrapped in BEGIN IMMEDIATE so two
	// concurrent rehires of distinct ghosts on the same crew can't
	// both pass the quota gate before either runs the UPDATE. Same
	// rationale as the Hire path: COUNT(*) + UPDATE are separate
	// statements without the tx, leaving a TOCTOU window at the
	// quota boundary.
	//
	// A rehire is only "free" if the agent is currently a ghost (was
	// already counted as not-live). A rehire of a still-live
	// ephemeral (operator extends TTL before it ghosts) doesn't add
	// a slot — same row, same count.
	//
	// CRITICAL: wasGhost is computed INSIDE the tx (below) because a
	// concurrent rehire from another caller can flip expired_at to
	// NULL between the load on line 634 and the BEGIN IMMEDIATE here.
	// Using the pre-tx oldExpiredAt would still treat this request as
	// a ghost rehire — running the quota gate — even though the row
	// is already live, producing a spurious 429.

	// 4. Compute new state. expires_at = now + ttl; expired_at = NULL
	// (un-ghost); hire_reason appended with new timestamp so the
	// column reads as a chronological history (see
	// buildInitialReason).
	now := time.Now().UTC()
	newExpiresAt := now.Add(time.Duration(ttlMin) * time.Minute).Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)
	newReason := appendRehireReason(oldHireReason.String, req.Reason, nowStr)

	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		h.logger.Error("rehire: begin tx", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer func() { _ = tx.Rollback() }()

	// Recompute ghost/live state inside the transaction to close the
	// TOCTOU window described above. If a concurrent rehire flipped
	// expired_at to NULL after our pre-tx load, we now see the live
	// state and skip the quota path.
	var currentExpiredAt sql.NullString
	if err := tx.QueryRowContext(r.Context(), `
		SELECT expired_at
		FROM agents
		WHERE id = ? AND workspace_id = ? AND ephemeral = 1 AND deleted_at IS NULL`,
		agentID, workspaceID,
	).Scan(&currentExpiredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Concurrent soft-delete after the pre-tx load. Race-safe
			// 404 — the caller's view is no longer valid.
			replyError(w, http.StatusNotFound, "Agent not found")
			return
		}
		h.logger.Error("rehire: reload lifecycle state in tx", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	wasGhost := currentExpiredAt.Valid && currentExpiredAt.String != ""

	if wasGhost && crewID != "" {
		var liveCount, maxAgents int
		if err := tx.QueryRowContext(r.Context(),
			`SELECT max_ephemeral_agents FROM crews WHERE id = ?`,
			crewID).Scan(&maxAgents); err != nil {
			h.logger.Error("rehire: load crew quota", "crew_id", crewID, "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if err := tx.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM agents
			WHERE crew_id = ? AND ephemeral = 1
			  AND expired_at IS NULL AND deleted_at IS NULL`,
			crewID).Scan(&liveCount); err != nil {
			h.logger.Error("rehire: count live", "crew_id", crewID, "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if liveCount >= maxAgents {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":   "Ephemeral quota reached",
				"reason":  fmt.Sprintf("crew has %d live ephemerals (max %d); raise crews.max_ephemeral_agents to rehire", liveCount, maxAgents),
				"crew_id": crewID,
				"live":    liveCount,
				"max":     maxAgents,
			})
			return
		}
	}

	if _, err = tx.ExecContext(r.Context(), `
		UPDATE agents
		SET expires_at = ?, expired_at = NULL, hire_reason = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?`,
		newExpiresAt, newReason, nowStr, agentID, workspaceID); err != nil {
		h.logger.Error("rehire: update agent", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if err := tx.Commit(); err != nil {
		h.logger.Error("rehire: commit tx", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}

	priorExpiredAt := ""
	if oldExpiredAt.Valid {
		priorExpiredAt = oldExpiredAt.String
	}
	WriteAuditLog(r.Context(), h.db, h.journal, "agent.rehired", "AGENT", agentID, userID, workspaceID, map[string]interface{}{
		"reason":           req.Reason,
		"ttl_minutes":      ttlMin,
		"prior_expired_at": priorExpiredAt,
		"new_expires_at":   newExpiresAt,
		"was_ghost":        wasGhost,
		"crew_id":          crewID,
		"decision":         string(decision),
		"autonomy_level":   string(autonomyLevel),
	})

	h.broadcastAgentEvent("agent.rehired", workspaceID, map[string]string{
		"id":         agentID,
		"crew_id":    crewID,
		"name":       oldName,
		"expires_at": newExpiresAt,
	})

	crewIDOut := crewID
	expiresOut := newExpiresAt
	reasonOut := newReason
	writeJSON(w, http.StatusOK, hireResponse{
		ID:          agentID,
		CrewID:      &crewIDOut,
		WorkspaceID: workspaceID,
		Slug:        "", // not re-derived; caller can GET to refresh
		Name:        oldName,
		// Echo the persisted status; rehire does not touch the
		// status column, so a row that was RUNNING when the operator
		// extended its TTL stays RUNNING in the response.
		Status:     oldStatus,
		Ephemeral:  true,
		ExpiresAt:  &expiresOut,
		ExpiredAt:  nil,
		HireReason: &reasonOut,
		Decision:   string(decision),
	})
}

// appendRehireReason appends a new rehire reason line to an existing
// hire_reason history. Format matches buildInitialReason so the column
// stays consistently parseable:
//
//	[2026-05-21T11:00:00Z] hire: ship the docs site
//	[2026-05-22T09:00:00Z] rehire: docs need another pass
//
// Empty prior history (column was NULL or never written) falls back to
// a "rehire:" line on its own — better than treating it as a hire,
// which would mis-attribute the original audit trail.
func appendRehireReason(prior, reason, ts string) string {
	line := fmt.Sprintf("[%s] rehire: %s", ts, strings.TrimSpace(reason))
	prior = strings.TrimRight(prior, "\n")
	if prior == "" {
		return line
	}
	return prior + "\n" + line
}

// writeInboxItem inserts an inbox_items row tagged for hire review.
// kind='waitpoint' on blocking; kind='message' on non-blocking
// (informational). Both share source_id=agent_id so the existing inbox
// list query can correlate without a new index. Errors are logged and
// swallowed — the agent row already exists, dropping the inbox row is
// preferable to rolling back the hire on a transient DB hiccup.
func (h *AgentHandler) writeInboxItem(r *http.Request, id, workspaceID, agentID, crewID, agentName, payloadKind, title, body, senderUserID string, blocking bool) error {
	kind := "message"
	blockingFlag := 0
	if blocking {
		kind = "waitpoint"
		blockingFlag = 1
	}
	// json.Marshal does the escaping; a hand-rolled fmt.Sprintf
	// here would have to escape backslashes + quotes manually and is
	// the kind of code that quietly breaks on a name with a `"` in
	// it. Marshal error is unreachable for a string-keyed map of
	// strings, but we fall back to "{}" for belt+suspenders.
	payloadBytes, mErr := json.Marshal(map[string]string{
		"kind":       payloadKind,
		"agent_id":   agentID,
		"crew_id":    crewID,
		"agent_name": agentName,
	})
	payload := "{}"
	if mErr == nil {
		payload = string(payloadBytes)
	}
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO inbox_items (
			id, workspace_id, kind, source_id, sender_type, sender_id, sender_name,
			title, body_md, state, priority, blocking, payload_json)
		VALUES (?, ?, ?, ?, 'user', ?, ?, ?, ?, 'unread', 'medium', ?, ?)`,
		id, workspaceID, kind, agentID, senderUserID, agentName, title, body, blockingFlag, payload)
	if err != nil {
		h.logger.Warn("hire: write inbox item", "error", err, "agent_id", agentID)
	}
	return err
}
