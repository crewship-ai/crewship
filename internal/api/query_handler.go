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
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// QueryHandler handles peer query, standup, and escalation API requests.
type QueryHandler struct {
	db            *sql.DB
	orch          *orchestrator.Orchestrator
	hub           *ws.Hub
	logger        *slog.Logger
	internalToken string

	escalationMu      sync.Mutex
	escalationWaiters map[string]chan escalationResult
}

// NewQueryHandler creates a QueryHandler with the given orchestrator, hub, and internal token.
func NewQueryHandler(db *sql.DB, orch *orchestrator.Orchestrator, hub *ws.Hub, internalToken string, logger *slog.Logger) *QueryHandler {
	return &QueryHandler{
		db:                db,
		orch:              orch,
		hub:               hub,
		logger:            logger,
		internalToken:     internalToken,
		escalationWaiters: make(map[string]chan escalationResult),
	}
}

type createQueryBody struct {
	TargetSlug  string `json:"target_slug"`
	Question    string `json:"question"`
	FromSlug    string `json:"from_slug"`
	CrewID      string `json:"crew_id"`
	WorkspaceID string `json:"workspace_id"`
	ChatID      string `json:"chat_id"`
	Depth       int    `json:"depth"`
}

// Create handles POST /api/v1/internal/queries.
// Called by the sidecar when an agent invokes `curl localhost:9119/query`.
// This is synchronous — it runs the target agent and returns the response.
func (h *QueryHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body createQueryBody
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.TargetSlug == "" || body.Question == "" || body.CrewID == "" || body.WorkspaceID == "" || body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "target_slug, question, crew_id, workspace_id, chat_id required",
		})
		return
	}

	startTime := time.Now()

	// Look up the from agent (for logging/DB)
	var fromAgentID string
	if body.FromSlug != "" {
		err := h.db.QueryRowContext(r.Context(), `
			SELECT id FROM agents WHERE slug = ? AND crew_id = ? AND deleted_at IS NULL
		`, body.FromSlug, body.CrewID).Scan(&fromAgentID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.logger.Error("lookup from agent", "error", err)
		}
	}

	// Look up target agent
	var target targetAgentInfo
	err := h.db.QueryRowContext(r.Context(), `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt,''),
		       a.cli_adapter, COALESCE(a.llm_model,''), a.tool_profile, a.timeout_seconds, a.memory_enabled, c.slug
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.slug = ? AND a.crew_id = ? AND a.deleted_at IS NULL
	`, body.TargetSlug, body.CrewID).Scan(
		&target.ID, &target.Slug, &target.Name, &target.RoleTitle,
		&target.SystemPrompt, &target.CLIAdapter, &target.LLMModel,
		&target.ToolProfile, &target.TimeoutSeconds, &target.MemoryEnabled, &target.CrewSlug,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "target agent not found"})
			return
		}
		h.logger.Error("lookup target agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Load target agent credentials
	creds, err := h.loadAgentCredentials(r.Context(), target.ID)
	if err != nil {
		h.logger.Error("load agent credentials", "agent_id", target.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Create peer_conversations record
	convID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'RUNNING', ?)
	`, convID, body.WorkspaceID, body.CrewID, body.ChatID, fromAgentID, target.ID, body.Question, now)
	if err != nil {
		h.logger.Error("create peer_conversation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Create agent_runs record for dashboard visibility
	runID := generateCUID()
	metadataMap := map[string]string{
		"peer_query_id": convID,
		"from_slug":     body.FromSlug,
		"question":      body.Question,
	}
	metadataBytes, _ := json.Marshal(metadataMap)
	metadata := string(metadataBytes)
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO agent_runs (id, agent_id, chat_id, workspace_id, trigger_type, status, metadata, started_at, created_at)
		VALUES (?, ?, ?, ?, 'PEER_QUERY', 'RUNNING', ?, ?, ?)`,
		runID, target.ID, body.ChatID, body.WorkspaceID, metadata, now, now)
	if err != nil {
		h.logger.Error("create run record for query", "error", err)
		runID = "" // prevent finishQuery from updating a non-existent record
	}

	// Broadcast event
	broadcastChannelEvent(h.hub, "session", body.ChatID, "peer_query_running",
		map[string]string{
			"id":     convID,
			"from":   body.FromSlug,
			"target": body.TargetSlug,
		})

	h.logger.Info("peer query started",
		"query_id", convID,
		"from", body.FromSlug,
		"target", body.TargetSlug,
		"depth", body.Depth,
	)

	if h.orch == nil {
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, "", "orchestrator not available", startTime)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "orchestrator not available"})
		return
	}

	// Ensure crew container is running
	containerID, err := h.orch.GetOrCreateContainer(r.Context(), target.CrewSlug, body.CrewID, body.WorkspaceID)
	if err != nil {
		h.logger.Error("get container for query", "error", err, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, "",
			fmt.Sprintf("container error: %v", err), startTime)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "container error"})
		return
	}

	// Collect agent output
	var outputParts []string
	handler := func(event orchestrator.AgentEvent) {
		if event.Type == "text" && event.Content != "" {
			outputParts = append(outputParts, event.Content)
		}
	}

	// Build the peer query system prompt by prepending the [PEER QUERY] block
	peerQueryBlock := fmt.Sprintf(`[PEER QUERY from @%s]
Answer concisely. This is a quick question, not a task.
Question: %s`, body.FromSlug, body.Question)

	systemPrompt := target.SystemPrompt
	if systemPrompt != "" {
		systemPrompt += "\n\n"
	}
	systemPrompt += peerQueryBlock

	// Build env vars with depth for anti-loop
	req := orchestrator.AgentRunRequest{
		AgentID:         target.ID,
		AgentSlug:       target.Slug,
		AgentRole:       "AGENT",
		CrewID:          body.CrewID,
		CrewSlug:        target.CrewSlug,
		WorkspaceID:     body.WorkspaceID,
		ChatID:          body.ChatID,
		ContainerID:     containerID,
		CLIAdapter:      target.CLIAdapter,
		LLMModel:        target.LLMModel,
		SystemPrompt:    systemPrompt,
		UserMessage:     body.Question,
		ToolProfile:     target.ToolProfile,
		Credentials:     creds,
		TimeoutSecs:     target.TimeoutSeconds,
		MemoryEnabled:   target.MemoryEnabled,
		SkipSidecar:     true,  // Sidecar already running on 9119 in this container
		SkipConvHistory: true,  // Fresh context for peer queries
	}

	if err := h.orch.RunAgentForAssignment(r.Context(), req, handler); err != nil {
		h.logger.Error("peer query execution failed", "error", err, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, "",
			fmt.Sprintf("execution error: %v", err), startTime)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":    "query execution failed",
			"query_id": convID,
		})
		return
	}

	// Build result
	result := strings.Join(outputParts, "")
	if len(result) > 10000 {
		result = result[:10000] + "...(truncated)"
	}

	h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, result, "", startTime)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"query_id": convID,
		"from":     body.FromSlug,
		"target":   body.TargetSlug,
		"question": body.Question,
		"response": result,
		"status":   "COMPLETED",
	})
}

// finishQuery updates peer_conversations and agent_runs records.
func (h *QueryHandler) finishQuery(
	ctx context.Context,
	convID, runID, chatID, fromSlug, targetSlug, workspaceID, result, errMsg string,
	startTime time.Time,
) {
	now := time.Now().UTC().Format(time.RFC3339)
	durationMs := time.Since(startTime).Milliseconds()
	status := "COMPLETED"
	if errMsg != "" {
		status = "FAILED"
	}

	var responseVal interface{}
	if result != "" {
		responseVal = result
	}

	// Update peer_conversations
	if _, err := h.db.ExecContext(ctx,
		`UPDATE peer_conversations SET status=?, response=?, duration_ms=?, finished_at=? WHERE id=?`,
		status, responseVal, durationMs, now, convID); err != nil {
		h.logger.Error("update peer_conversation", "error", err, "id", convID)
	}

	// Update agent_runs
	if runID != "" {
		runStatus := status
		runQuery := `UPDATE agent_runs SET status = ?, finished_at = ?`
		runArgs := []interface{}{runStatus, now}
		if errMsg != "" {
			runQuery += `, error_message = ?`
			runArgs = append(runArgs, errMsg)
		}
		if status == "COMPLETED" {
			runQuery += `, exit_code = 0`
		}
		runQuery += ` WHERE id = ?`
		runArgs = append(runArgs, runID)
		if _, err := h.db.ExecContext(ctx, runQuery, runArgs...); err != nil {
			h.logger.Error("update run record for query", "error", err, "run_id", runID)
		}
	}

	// Broadcast completion
	eventType := "peer_query_completed"
	payload := map[string]string{
		"id":     convID,
		"from":   fromSlug,
		"target": targetSlug,
	}
	if errMsg != "" {
		eventType = "peer_query_failed"
		payload["error"] = errMsg
	} else {
		payload["response"] = result
	}
	broadcastChannelEvent(h.hub, "session", chatID, eventType, payload)
	if workspaceID != "" {
		broadcastWorkspaceEvent(h.hub, workspaceID, "peer_conversation.updated",
			map[string]string{
				"id":     convID,
				"from":   fromSlug,
				"target": targetSlug,
				"status": status,
			})
	}

	h.logger.Info("peer query finished", "query_id", convID, "status", status, "duration_ms", durationMs)
}

// loadAgentCredentials queries and decrypts all credentials for an agent.
func (h *QueryHandler) loadAgentCredentials(ctx context.Context, agentID string) ([]orchestrator.Credential, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT ac.credential_id, ac.env_var_name, ac.priority, c.encrypted_value, c.type
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ? AND c.deleted_at IS NULL
		ORDER BY ac.priority ASC
	`, agentID)
	if err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	defer rows.Close()

	var creds []orchestrator.Credential
	for rows.Next() {
		var c orchestrator.Credential
		var encValue string
		if err := rows.Scan(&c.ID, &c.EnvVarName, &c.Priority, &encValue, &c.Type); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		dec, err := encryption.Decrypt(encValue)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential %s: %w", c.ID, err)
		}
		c.PlainValue = dec
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

// ListPeerConversations handles GET /api/v1/crews/{crewId}/peer-conversations.
func (h *QueryHandler) ListPeerConversations(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	limit, offset := parsePagination(r, 50, 100)

	type peerConvItem struct {
		ID           string  `json:"id"`
		FromName     string  `json:"from_name"`
		FromSlug     string  `json:"from_slug"`
		ToName       string  `json:"to_name"`
		ToSlug       string  `json:"to_slug"`
		Question     string  `json:"question"`
		Response     *string `json:"response"`
		Status       string  `json:"status"`
		DurationMs   *int    `json:"duration_ms"`
		Escalated    bool    `json:"escalated"`
		CreatedAt    string  `json:"created_at"`
		FinishedAt   *string `json:"finished_at"`
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT pc.id, pc.question, pc.response, pc.status, pc.duration_ms,
		       pc.escalated, pc.created_at, pc.finished_at,
		       from_a.name, from_a.slug, to_a.name, to_a.slug
		FROM peer_conversations pc
		JOIN agents from_a ON from_a.id = pc.from_agent_id
		JOIN agents to_a ON to_a.id = pc.to_agent_id
		WHERE pc.crew_id = ? AND pc.workspace_id = ?
		ORDER BY pc.created_at DESC
		LIMIT ? OFFSET ?
	`, crewID, workspaceID, limit, offset)
	if err != nil {
		h.logger.Error("list peer conversations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	items := make([]peerConvItem, 0, limit)
	for rows.Next() {
		var item peerConvItem
		var escalatedInt int
		if err := rows.Scan(
			&item.ID, &item.Question, &item.Response, &item.Status, &item.DurationMs,
			&escalatedInt, &item.CreatedAt, &item.FinishedAt,
			&item.FromName, &item.FromSlug, &item.ToName, &item.ToSlug,
		); err != nil {
			h.logger.Error("scan peer conversation", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		item.Escalated = escalatedInt != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, items)
}
