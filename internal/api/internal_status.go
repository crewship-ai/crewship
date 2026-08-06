package api

// File: internal_status.go — internal API handlers used by the sidecar on
// behalf of agents for workspace-level operations.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/policy"
)

// nilIfEmpty returns nil for empty strings, otherwise a pointer to the string.
// Used when inserting nullable columns that should hold NULL rather than ”.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListCrews handles GET /api/v1/internal/crews?workspace_id=...
// Used by the sidecar on behalf of agents discovering workspace topology.
func (h *InternalHandler) ListCrews(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	type crewEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name`, wsID)
	if err != nil {
		replyInternalError(w, h.logger, "list crews internal", err)
		return
	}
	defer rows.Close()

	result := []crewEntry{}
	for rows.Next() {
		var c crewEntry
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug); err != nil {
			continue
		}
		result = append(result, c)
	}
	writeJSON(w, http.StatusOK, result)
}

// CreateCrew handles POST /api/v1/internal/crews?workspace_id=...
// Allows LEAD agents (via sidecar) to create a new crew in the workspace.
//
// #1768 — gated on policy.ActionCrewCreate against the CALLING crew's
// autonomy_level. This was the sharpest hole in that issue: the INSERT below
// never set autonomy_level, and crews.autonomy_level carries DEFAULT 'guided'
// (migration v101), so an agent in a strict crew could create a guided one,
// create an agent inside it, and act there — a complete autonomy escape.
//
// Two things close it. The gate refuses outright at strict; and at every
// level the new crew's autonomy_level is now written explicitly instead of
// inherited from the column default:
//
//	held (guided/trusted) → pinned to 'strict'. A strict crew rejects
//	                        agent_create and routine_schedule_create, so the
//	                        new crew cannot be populated or scheduled until
//	                        an operator approves — that is what makes it
//	                        inert. Approving restores the creating crew's
//	                        own level (never more permissive than the
//	                        parent).
//	full                  → inherits the creating crew's level directly.
func (h *InternalHandler) CreateCrew(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	// The subject is the CALLING crew (from the token binding) — a crew
	// being created has no policy of its own yet.
	gate, ok := gateInternalAction(w, r, h.policyResolver, h.logger, "",
		policy.ActionCrewCreate, "Crew creation")
	if !ok {
		return
	}

	var body struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
		Color       string `json:"color"`
	}
	// /api/v1/internal/* bypasses the global BodyCap, so bound the body here.
	// Crew create carries only a handful of small fields — 1 MiB is generous.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Name == "" {
		replyError(w, http.StatusBadRequest, "name is required")
		return
	}
	if body.Slug == "" {
		body.Slug = slugify(body.Name)
	} else {
		body.Slug = slugify(body.Slug)
	}
	if body.Slug == "" {
		replyError(w, http.StatusBadRequest, "slug is required (could not derive from name)")
		return
	}

	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.Slug, wsID).Scan(&existing); err != nil {
		h.logger.Error("check crew slug uniqueness", "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if existing > 0 {
		replyError(w, http.StatusConflict, fmt.Sprintf("crew with slug '%s' already exists", body.Slug))
		return
	}

	crewID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	var icon, color *string
	if body.Icon != "" {
		icon = &body.Icon
	}
	if body.Color != "" {
		color = &body.Color
	}

	// Written explicitly, never left to the v101 column default — see the
	// handler docstring. A held crew is pinned to strict; an allowed one
	// inherits the creating crew's level.
	newLevel := string(gate.Level)
	if gate.held() {
		newLevel = string(policy.AutonomyStrict)
	}
	if newLevel == "" {
		newLevel = string(policy.AutonomyGuided)
	}

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO crews (id, workspace_id, name, slug, description, icon, color, network_mode, autonomy_level, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, wsID, body.Name, body.Slug, body.Description, icon, color, database.DefaultCrewNetworkMode, newLevel, now, now)
	if err != nil {
		h.logger.Error("internal create crew", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to create crew")
		return
	}

	status := http.StatusCreated
	approvalID := ""
	if gate.held() {
		id, herr := writeAutonomyHold(r.Context(), h.db, h.logger, h.journal, gate, autonomyHold{
			WorkspaceID:  wsID,
			CrewID:       crewID,
			Target:       autonomyTargetCrew,
			TargetID:     crewID,
			ReleaseValue: string(gate.Level),
			InboxKind:    inbox.KindWaitpoint,
			Title:        "Crew created by agent: " + body.Name,
			BodyMD: fmt.Sprintf(
				"An agent in a `%s` crew created the crew **%s** (`%s`).\n\n"+
					"It is pinned to `autonomy_level=strict` until you decide, so no agent "+
					"or schedule can be created inside it. Approving restores `%s`.",
				gate.Level, body.Name, body.Slug, gate.Level),
			Reason: "agent created crew " + body.Slug,
		})
		if herr != nil {
			// A sentinel with no release path is a bricked crew. Undo the
			// INSERT and fail loudly so the caller can retry — same
			// compensating-delete rationale as agents_hire.go.
			h.logger.Error("internal create crew: autonomy hold failed — compensating delete",
				"crew_id", crewID, "error", herr)
			if _, derr := h.db.ExecContext(r.Context(),
				`DELETE FROM crews WHERE id = ? AND workspace_id = ?`, crewID, wsID); derr != nil {
				h.logger.Error("internal create crew: compensating delete failed",
					"crew_id", crewID, "error", derr)
			}
			replyError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		approvalID = id
		status = http.StatusAccepted
	} else if gate.wantsInbox() {
		writeAutonomyNotice(r.Context(), h.db, h.logger, gate, wsID, inbox.KindMessage, crewID,
			"Crew created by agent: "+body.Name,
			fmt.Sprintf("An agent created the crew **%s** (`%s`) at `autonomy_level=%s`.",
				body.Name, body.Slug, newLevel))
	}

	audit := gate.auditFields()
	audit["name"] = body.Name
	audit["slug"] = body.Slug
	audit["autonomy_level_assigned"] = newLevel
	audit["pending_review"] = gate.held()
	WriteAuditLog(r.Context(), h.db, h.journal, "crew.created", "CREW", crewID, "", wsID, audit)

	h.logger.Info("crew created via coordinator", "crew_id", crewID, "name", body.Name, "workspace", wsID,
		"decision", string(gate.Decision), "autonomy_level", newLevel)
	writeJSON(w, status, map[string]interface{}{
		"id":             crewID,
		"name":           body.Name,
		"slug":           body.Slug,
		"workspace_id":   wsID,
		"autonomy_level": newLevel,
		"decision":       string(gate.Decision),
		"pending_review": gate.held(),
		"approval_id":    approvalID,
	})
}

// CreateAgent handles POST /api/v1/internal/agents?workspace_id=...
// Allows LEAD agents (via sidecar) to create a new agent within a crew.
//
// #1768 — gated on policy.ActionAgentCreate. A persistent agent differs from
// an ephemeral hire on every axis that made ActionEphemeralSpawn tolerable at
// trusted: no TTL, no template, no max_ephemeral_agents quota, and the
// caller-supplied system_prompt below is persona authorship reached through
// an INSERT rather than the UPDATE that policy.ActionPersonaDirectWrite
// refuses at every level.
//
//	strict          → 403.
//	guided/trusted  → the row is created with status='PENDING_REVIEW'. The
//	                  chatbridge refuses to start an agent in that state
//	                  (internal/chatbridge/bridge.go — the guard is NOT
//	                  ephemeral-scoped), so the agent exists but cannot serve
//	                  a single message until an operator approves. That is
//	                  the same sentinel the guided hire flow uses.
//	full            → live immediately, with a non-blocking inbox notice.
func (h *InternalHandler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	var body struct {
		CrewID       string `json:"crew_id"`
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		RoleTitle    string `json:"role_title"`
		AgentRole    string `json:"agent_role"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		CLIAdapter   string `json:"cli_adapter"`
		LLMProvider  string `json:"llm_provider"`
		LLMModel     string `json:"llm_model"`
		ToolProfile  string `json:"tool_profile"`
	}
	// /api/v1/internal/* bypasses the global BodyCap, so bound the body here.
	// Agent create is small (a system prompt at most) — 1 MiB is generous.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Name == "" || body.CrewID == "" {
		replyError(w, http.StatusBadRequest, "name and crew_id are required")
		return
	}
	// #1186: a crew-bound (crwv1) sidecar token could otherwise hire an
	// agent into any sibling crew in the same workspace by naming it here
	// — this route never consulted the token's crew binding at all. No-op
	// for workspace-bound/master-token callers (unaffected, still
	// workspace-wide by design).
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &body.CrewID) {
		return
	}
	// Runs after the crew-binding check above so the policy we resolve is
	// the one for a crew the caller has been proven to own — otherwise a
	// caller could name a permissive sibling and be judged by its level.
	gate, ok := gateInternalAction(w, r, h.policyResolver, h.logger, body.CrewID,
		policy.ActionAgentCreate, "Agent creation")
	if !ok {
		return
	}
	if body.Slug == "" {
		body.Slug = slugify(body.Name)
	} else {
		body.Slug = slugify(body.Slug)
	}
	if body.Slug == "" {
		replyError(w, http.StatusBadRequest, "slug is required (could not derive from name)")
		return
	}
	if body.AgentRole == "" {
		body.AgentRole = "AGENT"
	}
	if body.CLIAdapter == "" {
		body.CLIAdapter = "CLAUDE_CODE"
	}
	if body.ToolProfile == "" {
		body.ToolProfile = "CODING"
	}

	// Suffix slug with crew slug to prevent workspace-wide UNIQUE conflicts
	var crewSlug string
	if err := h.db.QueryRowContext(r.Context(), `SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, body.CrewID, wsID).Scan(&crewSlug); err != nil {
		h.logger.Warn("lookup crew slug", "crew_id", body.CrewID, "error", err)
	}
	if crewSlug != "" {
		body.Slug = body.Slug + "-" + crewSlug
	}

	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM agents WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.Slug, wsID).Scan(&existing); err != nil {
		h.logger.Error("check agent slug uniqueness", "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if existing > 0 {
		replyError(w, http.StatusConflict, fmt.Sprintf("agent with slug '%s' already exists", body.Slug))
		return
	}

	agentID := generateCUID()
	// #1072/#1029: encrypt the webhook secret at rest (fail-open without a key).
	// This path never returns the secret; it's revealed only via show-once rotate.
	storedWebhookSecret, _, encErr := encryption.EncryptAtRest(generateWebhookSecret())
	if encErr != nil {
		h.logger.Error("internal create agent: encrypt webhook secret", "error", encErr)
		replyError(w, http.StatusInternalServerError, "failed to create agent")
		return
	}
	webhookSecret := storedWebhookSecret
	now := time.Now().UTC().Format(time.RFC3339)

	// The status sentinel — see the handler docstring. Explicit rather than
	// leaning on the agents.status DEFAULT 'IDLE', because the whole point
	// is that a held agent must NOT be idle-and-serviceable.
	initialStatus := "IDLE"
	if gate.held() {
		initialStatus = "PENDING_REVIEW"
	}

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO agents (id, workspace_id, crew_id, name, slug, description, role_title, agent_role,
			cli_adapter, llm_provider, llm_model, tool_profile, system_prompt_legacy, status,
			timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, wsID, body.CrewID, body.Name, body.Slug, body.Description,
		body.RoleTitle, body.AgentRole,
		body.CLIAdapter, nilIfEmpty(body.LLMProvider), nilIfEmpty(body.LLMModel), body.ToolProfile, body.SystemPrompt,
		initialStatus,
		1800, true, webhookSecret, now, now)
	if err != nil {
		h.logger.Error("internal create agent", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to create agent")
		return
	}

	status := http.StatusCreated
	approvalID := ""
	if gate.held() {
		id, herr := writeAutonomyHold(r.Context(), h.db, h.logger, h.journal, gate, autonomyHold{
			WorkspaceID: wsID,
			CrewID:      body.CrewID,
			AgentID:     agentID,
			Target:      autonomyTargetAgent,
			TargetID:    agentID,
			// KindWaitpoint with SourceID=agent_id is the shape
			// ApproveHire's inbox.ResolveBySourceTx already clears, so the
			// legacy `crewship hire approve <id>` path resolves this row
			// too — one waitpoint, two decide surfaces.
			InboxKind: inbox.KindWaitpoint,
			Title:     "Agent created by agent: " + body.Name,
			BodyMD: fmt.Sprintf(
				"An agent in a `%s` crew created the persistent agent **%s** (`%s`) "+
					"with a system prompt it wrote itself.\n\n"+
					"The row is `PENDING_REVIEW` and cannot serve a message until approved.",
				gate.Level, body.Name, body.Slug),
			Reason: "agent created persistent agent " + body.Slug,
		})
		if herr != nil {
			h.logger.Error("internal create agent: autonomy hold failed — compensating delete",
				"agent_id", agentID, "error", herr)
			if _, derr := h.db.ExecContext(r.Context(),
				`DELETE FROM agents WHERE id = ? AND workspace_id = ?`, agentID, wsID); derr != nil {
				h.logger.Error("internal create agent: compensating delete failed",
					"agent_id", agentID, "error", derr)
			}
			replyError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		approvalID = id
		status = http.StatusAccepted
	} else if gate.wantsInbox() {
		writeAutonomyNotice(r.Context(), h.db, h.logger, gate, wsID, inbox.KindMessage, agentID,
			"Agent created by agent: "+body.Name,
			fmt.Sprintf("An agent created the persistent agent **%s** (`%s`) in crew `%s`.",
				body.Name, body.Slug, body.CrewID))
	}

	// Auto-assign workspace AI credentials so the new agent can run once it
	// is live. Done after the hold is durable so a failed hold (which
	// compensating-deletes the agent above) cannot strand an
	// agent_credentials row pointing at a deleted agent.
	autoAssignCredentials(r.Context(), h.db, h.logger, h.journal, wsID, agentID, now)

	audit := gate.auditFields()
	audit["name"] = body.Name
	audit["slug"] = body.Slug
	audit["crew_id"] = body.CrewID
	audit["initial_status"] = initialStatus
	audit["pending_review"] = gate.held()
	WriteAuditLog(r.Context(), h.db, h.journal, "agent.created", "AGENT", agentID, "", wsID, audit)

	h.logger.Info("agent created via coordinator", "agent_id", agentID, "name", body.Name,
		"crew_id", body.CrewID, "decision", string(gate.Decision), "status", initialStatus)
	writeJSON(w, status, map[string]interface{}{
		"id":             agentID,
		"name":           body.Name,
		"slug":           body.Slug,
		"crew_id":        body.CrewID,
		"workspace_id":   wsID,
		"status":         initialStatus,
		"decision":       string(gate.Decision),
		"pending_review": gate.held(),
		"approval_id":    approvalID,
	})
}

// ListCrewConnections handles GET /api/v1/internal/crew-connections?workspace_id=...&crew_id=...
// Used by the sidecar on behalf of agents discovering crew topology.
// When crew_id is provided, only connections involving that crew are returned.
func (h *InternalHandler) ListCrewConnections(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	query := `
		SELECT cc.id, cc.from_crew_id, cc.to_crew_id, cc.direction, cc.status,
		       fc.name, fc.slug, tc.name, tc.slug
		FROM crew_connections cc
		JOIN crews fc ON fc.id = cc.from_crew_id
		JOIN crews tc ON tc.id = cc.to_crew_id
		WHERE cc.workspace_id = ? AND cc.status = 'active'`
	args := []interface{}{wsID}

	// #1186: for a crew-bound (crwv1) token the binding constrains the
	// listing — an omitted ?crew_id returns the token's own crew's
	// connections, not the workspace-wide topology. Unbound callers keep
	// the optional query filter.
	if crewID := effectiveCrewFilter(r); crewID != "" {
		query += " AND (cc.from_crew_id = ? OR cc.to_crew_id = ?)"
		args = append(args, crewID, crewID)
	}

	query += " ORDER BY cc.created_at DESC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list crew connections internal", err)
		return
	}
	defer rows.Close()

	type connEntry struct {
		ID           string `json:"id"`
		FromCrewID   string `json:"from_crew_id"`
		FromCrewName string `json:"from_crew_name"`
		FromCrewSlug string `json:"from_crew_slug"`
		ToCrewID     string `json:"to_crew_id"`
		ToCrewName   string `json:"to_crew_name"`
		ToCrewSlug   string `json:"to_crew_slug"`
		Direction    string `json:"direction"`
		Status       string `json:"status"`
	}

	result := []connEntry{}
	for rows.Next() {
		var c connEntry
		if err := rows.Scan(&c.ID, &c.FromCrewID, &c.ToCrewID, &c.Direction, &c.Status,
			&c.FromCrewName, &c.FromCrewSlug, &c.ToCrewName, &c.ToCrewSlug); err != nil {
			continue
		}
		result = append(result, c)
	}
	writeJSON(w, http.StatusOK, result)
}

// RecordMCPToolCall records an MCP tool call audit entry from the sidecar gateway.
func (h *InternalHandler) RecordMCPToolCall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID    string `json:"workspace_id"`
		AgentID        string `json:"agent_id"`
		CrewID         string `json:"crew_id"`
		MCPServerID    string `json:"mcp_server_id"`
		MCPServerScope string `json:"mcp_server_scope"`
		ToolName       string `json:"tool_name"`
		Status         string `json:"status"`
		DurationMS     int64  `json:"duration_ms"`
		ErrorMessage   string `json:"error_message"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if body.WorkspaceID == "" || body.AgentID == "" || body.MCPServerID == "" || body.ToolName == "" {
		replyError(w, http.StatusBadRequest, "workspace_id, agent_id, mcp_server_id, and tool_name are required")
		return
	}
	// PR-F24 R-1: a bound token may only write audit rows attributed to
	// its own workspace. requireInternal sees only the query string;
	// this guards the body-carried workspace_id (403 on a foreign
	// tenant), same as the F-4 body-workspace writers.
	if !assertInternalTokenWorkspace(w, r, body.WorkspaceID) {
		return
	}
	// PR-F24 foreign-ID closure: crew_id is independent of the workspace_id
	// checked above — prove it belongs to the bound workspace so a ws-A
	// token can't write an audit row attributed to a ws-B crew. For a
	// crew-bound (crwv1) token this also FILLS IN an omitted crew_id with
	// the token's own crew (#1222), so the audit row is never crew-less.
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &body.CrewID) {
		return
	}
	if body.MCPServerScope == "" {
		body.MCPServerScope = "workspace"
	}

	id := generateCUID()
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO mcp_tool_calls (id, workspace_id, crew_id, agent_id, mcp_server_id,
			mcp_server_scope, tool_name, status, duration_ms, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		id, body.WorkspaceID, body.CrewID, body.AgentID, body.MCPServerID, body.MCPServerScope,
		body.ToolName, body.Status, body.DurationMS, body.ErrorMessage)
	if err != nil {
		h.logger.Error("record mcp tool call", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to record")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}
