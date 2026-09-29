package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
)

// CreateChat creates a new chat session record on behalf of the sidecar.
// POST /api/v1/internal/chats
func (h *InternalHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID      string  `json:"chat_id"`
		AgentID     string  `json:"agent_id"`
		WorkspaceID string  `json:"workspace_id"`
		UserID      *string `json:"user_id"`
		Title       *string `json:"title"`
		// Origin is provenance, and this endpoint not accepting one is
		// why every routine step used to arrive indistinguishable from
		// a person's conversation. Whitelisted through the shared
		// IsChatOrigin — an unrecognised value stores NULL rather than
		// failing the call, because a chat row this endpoint declines
		// to write costs the run its audit trail (the caller treats
		// the failure as non-fatal and continues).
		Origin         string `json:"origin"`
		PipelineRunID  string `json:"pipeline_run_id"`
		PipelineStepID string `json:"pipeline_step_id"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if body.ChatID == "" || body.AgentID == "" || body.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "chat_id, agent_id, workspace_id required")
		return
	}

	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" && scope != body.WorkspaceID {
		replyError(w, http.StatusForbidden, "workspace does not match internal token")
		return
	}

	if body.PipelineRunID != "" {
		// Provenance must point to a live routine in the same tenant as this agent.
		var valid int
		err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pipeline_runs pr
			JOIN pipelines p ON p.id=pr.pipeline_id AND p.workspace_id=pr.workspace_id AND p.deleted_at IS NULL
			JOIN agents a ON a.id=? AND a.workspace_id=pr.workspace_id AND a.deleted_at IS NULL
			WHERE pr.id=? AND pr.workspace_id=?`, body.AgentID, body.PipelineRunID, body.WorkspaceID).Scan(&valid)
		if err != nil {
			replyInternalError(w, h.logger, "validate chat source", err)
			return
		}
		if valid != 1 || body.Origin != "ROUTINE" || body.PipelineStepID == "" {
			replyError(w, http.StatusBadRequest, "invalid routine source")
			return
		}
	}

	// Validate the referenced agent, not just the caller-supplied workspace.
	// A crew token must never create audit records for a sibling crew.
	agentQuery := `SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`
	agentArgs := []any{body.AgentID, body.WorkspaceID}
	if crew := InternalTokenCrewFromContext(r.Context()); crew != "" {
		agentQuery += " AND crew_id = ?"
		agentArgs = append(agentArgs, crew)
	}
	var agentID string
	if err := h.db.QueryRowContext(r.Context(), agentQuery, agentArgs...).Scan(&agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Agent not found")
		} else {
			replyInternalError(w, h.logger, "validate chat agent", err)
		}
		return
	}

	// Idempotency cannot turn a foreign chat ID into a successful create.
	var existingAgent, existingWorkspace string
	var existingRun, existingStep, existingUser sql.NullString
	err := h.db.QueryRowContext(r.Context(), `SELECT agent_id, workspace_id, pipeline_run_id, pipeline_step_id, created_by FROM chats WHERE id = ?`, body.ChatID).
		Scan(&existingAgent, &existingWorkspace, &existingRun, &existingStep, &existingUser)
	if err == nil {
		userID := ""
		if body.UserID != nil {
			userID = *body.UserID
		}
		if existingAgent != body.AgentID || existingWorkspace != body.WorkspaceID || existingRun.String != body.PipelineRunID || existingStep.String != body.PipelineStepID || existingUser.String != userID {
			replyError(w, http.StatusConflict, "chat identity conflict")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": body.ChatID, "status": "already_exists"})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "lookup chat", err)
		return
	}

	var origin sql.NullString
	if IsChatOrigin(body.Origin) {
		origin = sql.NullString{String: body.Origin, Valid: true}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	query := `INSERT INTO chats (id, agent_id, workspace_id, created_by, title, mode, status, origin, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'CHAT', 'ACTIVE', ?, ?, ?)`
	args := []any{body.ChatID, body.AgentID, body.WorkspaceID, body.UserID, body.Title, origin, now, now}
	if body.PipelineRunID != "" {
		query = `INSERT INTO chats (id, agent_id, workspace_id, created_by, title, mode, status, origin, started_at, created_at, pipeline_run_id, pipeline_step_id)
		VALUES (?, ?, ?, ?, ?, 'CHAT', 'ACTIVE', ?, ?, ?, ?, ?)`
		args = append(args, body.PipelineRunID, body.PipelineStepID)
	}
	_, err = h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		replyInternalError(w, h.logger, "create chat", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": body.ChatID, "status": "created"})
}

// ResolveChat looks up a chat's agent and returns the full agent configuration.
// GET /api/v1/internal/chats/{chatId}/resolve
func (h *InternalHandler) ResolveChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")

	// PR-E F6: along with agent_id we read created_by so the resolver
	// can inject the right peer card at session start. created_by is
	// NULL for system-initiated chats (routine dispatch) — empty
	// OpenedByUserID downstream means "no peer card injection",
	// which is what we want for non-human-opened sessions.
	var (
		agentID    string
		openedBy   sql.NullString
		visibility sql.NullString
	)
	// Tenant scope (PR-F24 F-2). A workspace-bound token constrains the
	// chat lookup to its workspace; a foreign chat id then yields the
	// same ErrNoRows → 404 as a missing one (don't leak cross-tenant
	// existence). Master-token (host-side) callers have an empty scope
	// and keep the id-only behavior.
	chatQuery := "SELECT agent_id, created_by, visibility FROM chats WHERE id = ?"
	chatArgs := []any{chatID}
	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" {
		chatQuery += " AND workspace_id = ?"
		chatArgs = append(chatArgs, scope)
	}
	if crew := InternalTokenCrewFromContext(r.Context()); crew != "" {
		chatQuery += " AND EXISTS (SELECT 1 FROM agents a WHERE a.id = chats.agent_id AND a.workspace_id = chats.workspace_id AND a.crew_id = ? AND a.deleted_at IS NULL)"
		chatArgs = append(chatArgs, crew)
	}
	err := h.db.QueryRowContext(r.Context(), chatQuery, chatArgs...).Scan(&agentID, &openedBy, &visibility)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Chat not found")
			return
		}
		replyInternalError(w, h.logger, "resolve chat lookup", err)
		return
	}

	if openedBy.Valid && openedBy.String != "" {
		restricted, err := (access.Store{DB: h.db}).HasRestrictedMembership(r.Context(), openedBy.String)
		if err != nil {
			replyInternalError(w, h.logger, "resolve chat resource authority", err)
			return
		}
		if restricted {
			// The legacy resolver materializes crew-wide prompt, memory and
			// credentials. Never use it for a restricted human conversation.
			replyError(w, http.StatusForbidden, "Restricted runtime authority required")
			return
		}
	}
	h.resolveAgentConfigWithOpener(w, r, agentID, openedBy.String, visibility.String)
}

// ResolveAgent returns the full configuration for a given agent ID.
// GET /api/v1/internal/agents/{agentId}/resolve
func (h *InternalHandler) ResolveAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	h.resolveAgentConfig(w, r, agentID)
}

// IncrementMessageCount increases the message_count on a chat by the given delta.
// POST /api/v1/internal/chats/{chatId}/messages
func (h *InternalHandler) IncrementMessageCount(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	var body struct {
		Delta int `json:"delta"`
	}
	if err := readJSON(r, &body); err != nil || body.Delta <= 0 {
		replyError(w, http.StatusBadRequest, "Invalid delta")
		return
	}
	// Tenant scope (PR-F24 F-5): a bound token may only bump its own
	// workspace's chats. A foreign chat id matches zero rows → 404 below.
	//
	// The bump doubles as the chat's activity heartbeat: every message
	// append flows through here (bridge, scheduler, group-chat no-mention
	// path), so last_activity_at is refreshed in the same statement and
	// the Sessions sidebar orders by "newest message" for free.
	mcQuery := "UPDATE chats SET message_count = message_count + ?, last_activity_at = ? WHERE id = ?"
	mcArgs := []any{body.Delta, isoMillisNow(), chatID}
	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" {
		mcQuery += " AND workspace_id = ?"
		mcArgs = append(mcArgs, scope)
	}
	if crew := InternalTokenCrewFromContext(r.Context()); crew != "" {
		mcQuery += " AND EXISTS (SELECT 1 FROM agents a WHERE a.id = chats.agent_id AND a.workspace_id = chats.workspace_id AND a.crew_id = ? AND a.deleted_at IS NULL)"
		mcArgs = append(mcArgs, crew)
	}
	res, err := h.db.ExecContext(r.Context(), mcQuery, mcArgs...)
	if err != nil {
		replyInternalError(w, h.logger, "increment message count", err)
		return
	}
	// A non-existent chat ID still returned 200 OK — silent no-op masking
	// caller bugs (typo'd ID, race against deletion). Surface it as 404
	// so the caller can either retry resolution or log the broken
	// reference instead of trusting a phantom success.
	n, err := res.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "increment message count rows affected", err)
		return
	}
	if n == 0 {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": chatID})
}

// UpdateChatTitle sets the title on a chat if it has not been set yet.
// PATCH /api/v1/internal/chats/{chatId}/title
func (h *InternalHandler) UpdateChatTitle(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	var body struct {
		Title string `json:"title"`
	}
	if err := readJSON(r, &body); err != nil || body.Title == "" {
		replyError(w, http.StatusBadRequest, "title required")
		return
	}
	// Tenant scope (PR-F24 F-5): a bound token may only title its own
	// workspace's chats; a foreign id matches zero rows → 404 below.
	titleQuery := "UPDATE chats SET title = ? WHERE id = ? AND (title IS NULL OR title = '')"
	titleArgs := []any{body.Title, chatID}
	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" {
		titleQuery += " AND workspace_id = ?"
		titleArgs = append(titleArgs, scope)
	}
	if crew := InternalTokenCrewFromContext(r.Context()); crew != "" {
		titleQuery += " AND EXISTS (SELECT 1 FROM agents a WHERE a.id = chats.agent_id AND a.workspace_id = chats.workspace_id AND a.crew_id = ? AND a.deleted_at IS NULL)"
		titleArgs = append(titleArgs, crew)
	}
	res, err := h.db.ExecContext(r.Context(), titleQuery, titleArgs...)
	if err != nil {
		replyInternalError(w, h.logger, "update chat title", err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "update chat title rows affected", err)
		return
	}
	if n == 0 {
		replyError(w, http.StatusNotFound, "Chat not found or already titled")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": chatID, "title": body.Title})
}
