package api

import (
	"context"
	"database/sql"
	"errors"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
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
}

func NewQueryHandler(db *sql.DB, orch *orchestrator.Orchestrator, hub *ws.Hub, internalToken string, logger *slog.Logger) *QueryHandler {
	return &QueryHandler{
		db:            db,
		orch:          orch,
		hub:           hub,
		logger:        logger,
		internalToken: internalToken,
	}
}

// PendingEscalationCount returns the number of unresolved escalations workspace-wide.
func (h *QueryHandler) PendingEscalationCount(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	var count int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM escalations e
		 JOIN crews c ON c.id = e.crew_id
		 WHERE c.workspace_id = ? AND e.status = 'PENDING' AND c.deleted_at IS NULL`,
		workspaceID).Scan(&count)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
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
	if h.hub != nil {
		h.hub.Broadcast("session:"+body.ChatID, ws.ServerMessage{
			Type:    "peer_query_running",
			Channel: "session:" + body.ChatID,
			Payload: map[string]string{
				"id":     convID,
				"from":   body.FromSlug,
				"target": body.TargetSlug,
			},
		})
	}

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
	containerID, err := h.orch.GetOrCreateContainer(r.Context(), target.CrewSlug, body.CrewID)
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
	if h.hub != nil {
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
		h.hub.Broadcast("session:"+chatID, ws.ServerMessage{
			Type:    eventType,
			Channel: "session:" + chatID,
			Payload: payload,
		})
		// Broadcast to workspace for global visibility
		if workspaceID != "" {
			wsChannel := "workspace:" + workspaceID
			h.hub.Broadcast(wsChannel, ws.ServerMessage{
				Type:    "peer_conversation.updated",
				Channel: wsChannel,
				Payload: map[string]string{
					"id":     convID,
					"from":   fromSlug,
					"target": targetSlug,
					"status": status,
				},
			})
		}
	}

	h.logger.Info("peer query finished", "query_id", convID, "status", status, "duration_ms", durationMs)
}

// loadAgentCredentials queries and decrypts all credentials for an agent.
func (h *QueryHandler) loadAgentCredentials(ctx context.Context, agentID string) ([]orchestrator.Credential, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT ac.credential_id, ac.env_var_name, ac.priority, c.encrypted_value, c.type
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?
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

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

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

	items := make([]peerConvItem, 0)
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

// ResolveEscalation handles PATCH /api/v1/escalations/{escalationId}/resolve.
func (h *QueryHandler) ResolveEscalation(w http.ResponseWriter, r *http.Request) {
	escalationID := r.PathValue("escalationId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var body struct {
		Resolution string `json:"resolution"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.Resolution == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "resolution required"})
		return
	}

	var status, chatID, crewID, fromSlug, escalationType string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT e.status, e.chat_id, e.crew_id, a.slug, e.type
		FROM escalations e
		JOIN agents a ON a.id = e.from_agent_id
		WHERE e.id = ? AND e.workspace_id = ?
	`, escalationID, workspaceID).Scan(&status, &chatID, &crewID, &fromSlug, &escalationType)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "escalation not found"})
			return
		}
		h.logger.Error("resolve escalation lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if status != "PENDING" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "escalation already resolved"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// For CREDENTIAL escalations encrypt the value at rest; for others store as-is.
	storedResolution := body.Resolution
	if escalationType == "CREDENTIAL" {
		enc, encErr := encryption.Encrypt(body.Resolution)
		if encErr != nil {
			h.logger.Error("encrypt credential resolution", "error", encErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		storedResolution = enc
	}

	result, err := h.db.ExecContext(r.Context(), `
		UPDATE escalations SET status = 'RESOLVED', resolution = ?, resolved_at = ?, resolved_by = 'user'
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING'
	`, storedResolution, now, escalationID, workspaceID)
	if err != nil {
		h.logger.Error("resolve escalation update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "escalation already resolved"})
		return
	}

	if h.hub != nil {
		broadcastResolution := body.Resolution
		if escalationType == "CREDENTIAL" {
			broadcastResolution = "[credential submitted]"
		}
		h.hub.Broadcast("session:"+chatID, ws.ServerMessage{
			Type:    "escalation_resolved",
			Channel: "session:" + chatID,
			Payload: map[string]string{
				"id":         escalationID,
				"resolution": broadcastResolution,
			},
		})
		h.hub.Broadcast("workspace:"+workspaceID, ws.ServerMessage{
			Type:    "escalation.resolved",
			Channel: "workspace:" + workspaceID,
			Payload: map[string]string{
				"id":        escalationID,
				"crew_id":   crewID,
				"from_slug": fromSlug,
			},
		})
	}

	h.logger.Info("escalation resolved",
		"escalation_id", escalationID,
		"crew_id", crewID,
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"id":     escalationID,
		"status": "RESOLVED",
	})
}
// Standup handles GET /api/v1/internal/standup (internal) and GET /api/v1/crews/{crewId}/standup (public).
func (h *QueryHandler) Standup(w http.ResponseWriter, r *http.Request) {
	crewID := r.URL.Query().Get("crew_id")
	if crewID == "" {
		crewID = r.PathValue("crewId")
	}
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_id required"})
		return
	}

	since := r.URL.Query().Get("since")
	if since == "" {
		// Default to last 24 hours
		since = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	}

	// Query peer conversations
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT pc.question, pc.response, pc.status, pc.escalated, pc.created_at,
		       from_a.name, from_a.slug, to_a.name, to_a.slug
		FROM peer_conversations pc
		JOIN agents from_a ON from_a.id = pc.from_agent_id
		JOIN agents to_a ON to_a.id = pc.to_agent_id
		WHERE pc.crew_id = ? AND pc.created_at >= ?
		ORDER BY pc.created_at ASC
	`, crewID, since)
	if err != nil {
		h.logger.Error("standup query conversations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var b strings.Builder
	b.WriteString("[CREW STANDUP]\n")

	type convEntry struct {
		question, response, status, createdAt string
		fromName, fromSlug, toName, toSlug    string
		escalated                             int
	}
	var convs []convEntry
	for rows.Next() {
		var c convEntry
		var nullResp sql.NullString
		if err := rows.Scan(&c.question, &nullResp, &c.status, &c.escalated, &c.createdAt,
			&c.fromName, &c.fromSlug, &c.toName, &c.toSlug); err != nil {
			h.logger.Error("scan standup conversation", "error", err)
			continue
		}
		c.response = nullResp.String
		convs = append(convs, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (standup convs)", "error", err)
	}

	if len(convs) > 0 {
		b.WriteString(fmt.Sprintf("Peer interactions (%d):\n\n", len(convs)))
		for i, c := range convs {
			ts := c.createdAt
			if t, err := time.Parse(time.RFC3339, c.createdAt); err == nil {
				ts = t.Format("15:04")
			}
			b.WriteString(fmt.Sprintf("%d. %s -> %s: \"%s\"\n", i+1, c.fromName, c.toName, c.question))
			if c.response != "" {
				resp := c.response
				if len(resp) > 200 {
					resp = resp[:200] + "..."
				}
				b.WriteString(fmt.Sprintf("   %s: \"%s\"\n", c.toName, resp))
			}
			suffix := ""
			if c.escalated != 0 {
				suffix = ", ESCALATED"
			}
			b.WriteString(fmt.Sprintf("   (%s%s)\n\n", ts, suffix))
		}
	} else {
		b.WriteString("No peer interactions in this period.\n\n")
	}

	// Query escalations
	var pending, resolved int
	escRows, err := h.db.QueryContext(r.Context(), `
		SELECT e.reason, e.status, e.created_at, from_a.name, from_a.slug
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		WHERE e.crew_id = ? AND e.created_at >= ?
		ORDER BY e.created_at ASC
	`, crewID, since)
	if err != nil {
		h.logger.Error("standup query escalations", "error", err)
	} else {
		defer escRows.Close()
		type escEntry struct {
			reason, status, createdAt, fromName, fromSlug string
		}
		var escs []escEntry
		for escRows.Next() {
			var e escEntry
			if err := escRows.Scan(&e.reason, &e.status, &e.createdAt, &e.fromName, &e.fromSlug); err != nil {
				h.logger.Error("scan standup escalation", "error", err)
				continue
			}
			escs = append(escs, e)
			if e.status == "PENDING" {
				pending++
			} else {
				resolved++
			}
		}
		if err := escRows.Err(); err != nil {
			h.logger.Error("rows iteration (standup escalations)", "error", err)
		}

		if len(escs) > 0 {
			b.WriteString(fmt.Sprintf("Escalations (%d pending, %d resolved):\n", pending, resolved))
			for _, e := range escs {
				ts := e.createdAt
				if t, err := time.Parse(time.RFC3339, e.createdAt); err == nil {
					ts = t.Format("15:04")
				}
				b.WriteString(fmt.Sprintf("- %s [%s]: \"%s\" (%s)\n", e.fromName, e.status, e.reason, ts))
			}
		}
	}

	queryCount := len(convs)
	escalationCount := pending + resolved

	b.WriteString(fmt.Sprintf("\nSummary: %d queries", queryCount))
	if escalationCount > 0 {
		b.WriteString(fmt.Sprintf(", %d escalations", escalationCount))
	}
	b.WriteString("\n[END CREW STANDUP]")

	writeJSON(w, http.StatusOK, map[string]string{
		"standup": b.String(),
		"crew_id": crewID,
		"since":   since,
	})
}

// CreateEscalation handles POST /api/v1/internal/escalations.
func (h *QueryHandler) CreateEscalation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromSlug    string `json:"from_slug"`
		Reason      string `json:"reason"`
		Context     string `json:"context"`
		Type        string `json:"type"`
		Metadata    string `json:"metadata"`
		CrewID      string `json:"crew_id"`
		WorkspaceID string `json:"workspace_id"`
		ChatID      string `json:"chat_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.FromSlug == "" || body.Reason == "" || body.CrewID == "" || body.WorkspaceID == "" || body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "from_slug, reason, crew_id, workspace_id, chat_id required",
		})
		return
	}

	// Look up the from agent
	var fromAgentID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id FROM agents WHERE slug = ? AND crew_id = ? AND deleted_at IS NULL
	`, body.FromSlug, body.CrewID).Scan(&fromAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "from agent not found"})
			return
		}
		h.logger.Error("lookup from agent for escalation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	escalationID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	var contextVal interface{}
	if body.Context != "" {
		contextVal = body.Context
	}

	escalationType := body.Type
	if escalationType == "" {
		escalationType = "TEXT"
	}
	if escalationType != "TEXT" && escalationType != "CREDENTIAL" && escalationType != "LINK" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be TEXT, CREDENTIAL, or LINK"})
		return
	}

	if escalationType == "LINK" {
		if body.Metadata == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "metadata (https URL) required for LINK type"})
			return
		}
		u, parseErr := url.ParseRequestURI(body.Metadata)
		if parseErr != nil || u.Scheme != "https" || u.Host == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "metadata must be a valid https URL"})
			return
		}
	}

	var metadataVal interface{}
	if body.Metadata != "" {
		metadataVal = body.Metadata
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, context, type, metadata, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', ?)
	`, escalationID, body.WorkspaceID, body.CrewID, body.ChatID, fromAgentID, body.Reason, contextVal, escalationType, metadataVal, now)
	if err != nil {
		h.logger.Error("create escalation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Broadcast escalation event
	if h.hub != nil {
		h.hub.Broadcast("session:"+body.ChatID, ws.ServerMessage{
			Type:    "escalation_created",
			Channel: "session:" + body.ChatID,
			Payload: map[string]string{
				"id":     escalationID,
				"from":   body.FromSlug,
				"reason": body.Reason,
			},
		})
		// Broadcast to workspace for global visibility
		wsChannel := "workspace:" + body.WorkspaceID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "escalation.created",
			Channel: wsChannel,
			Payload: map[string]string{
				"id":        escalationID,
				"crew_id":   body.CrewID,
				"from_slug": body.FromSlug,
				"reason":    body.Reason,
			},
		})
	}

	h.logger.Info("escalation created",
		"escalation_id", escalationID,
		"from", body.FromSlug,
		"crew_id", body.CrewID,
	)

	writeJSON(w, http.StatusCreated, map[string]string{
		"escalation_id": escalationID,
		"status":        "PENDING",
	})
}

// ListAllActivity handles GET /api/v1/activity.
// Returns a unified feed of assignments, peer conversations, and escalations across all crews.
func (h *QueryHandler) ListAllActivity(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	type activityItem struct {
		ID        string  `json:"id"`
		Type      string  `json:"type"`
		Status    string  `json:"status"`
		Summary   string  `json:"summary"`
		Detail    *string `json:"detail"`
		FromName  string  `json:"from_name"`
		FromSlug  string  `json:"from_slug"`
		ToName    *string `json:"to_name"`
		ToSlug    *string `json:"to_slug"`
		CrewName  string  `json:"crew_name"`
		CrewSlug  string  `json:"crew_slug"`
		CrewColor *string `json:"crew_color"`
		CreatedAt string  `json:"created_at"`
	}

	var items []activityItem

	// 1. Assignments
	aRows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.task, a.status, a.result_summary, a.created_at,
		       by_a.name, by_a.slug, to_a.name, to_a.slug,
		       c.name, c.slug, c.color
		FROM assignments a
		JOIN agents by_a ON by_a.id = a.assigned_by_id
		JOIN agents to_a ON to_a.id = a.assigned_to_id
		JOIN crews c ON c.id = by_a.crew_id
		WHERE a.workspace_id = ?
		ORDER BY a.created_at DESC LIMIT 20
	`, workspaceID)
	if err != nil {
		h.logger.Error("list activity: assignments", "error", err)
	} else {
		defer aRows.Close()
		for aRows.Next() {
			var item activityItem
			var resultSummary *string
			if err := aRows.Scan(
				&item.ID, &item.Summary, &item.Status, &resultSummary, &item.CreatedAt,
				&item.FromName, &item.FromSlug, &item.ToName, &item.ToSlug,
				&item.CrewName, &item.CrewSlug, &item.CrewColor,
			); err != nil {
				h.logger.Error("scan activity: assignment", "error", err)
				continue
			}
			item.Type = "assignment"
			item.Detail = resultSummary
			items = append(items, item)
		}
	}

	// 2. Peer conversations
	pcRows, err := h.db.QueryContext(r.Context(), `
		SELECT pc.id, pc.question, pc.status, pc.response, pc.created_at,
		       from_a.name, from_a.slug, to_a.name, to_a.slug,
		       c.name, c.slug, c.color
		FROM peer_conversations pc
		JOIN agents from_a ON from_a.id = pc.from_agent_id
		JOIN agents to_a ON to_a.id = pc.to_agent_id
		JOIN crews c ON c.id = pc.crew_id
		WHERE pc.workspace_id = ?
		ORDER BY pc.created_at DESC LIMIT 20
	`, workspaceID)
	if err != nil {
		h.logger.Error("list activity: peer_conversations", "error", err)
	} else {
		defer pcRows.Close()
		for pcRows.Next() {
			var item activityItem
			if err := pcRows.Scan(
				&item.ID, &item.Summary, &item.Status, &item.Detail, &item.CreatedAt,
				&item.FromName, &item.FromSlug, &item.ToName, &item.ToSlug,
				&item.CrewName, &item.CrewSlug, &item.CrewColor,
			); err != nil {
				h.logger.Error("scan activity: peer_conversation", "error", err)
				continue
			}
			item.Type = "peer_conversation"
			items = append(items, item)
		}
	}

	// 3. Escalations
	eRows, err := h.db.QueryContext(r.Context(), `
		SELECT e.id, e.reason, e.status, e.context, e.created_at,
		       from_a.name, from_a.slug,
		       c.name, c.slug, c.color
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		JOIN crews c ON c.id = e.crew_id
		WHERE e.workspace_id = ?
		ORDER BY e.created_at DESC LIMIT 20
	`, workspaceID)
	if err != nil {
		h.logger.Error("list activity: escalations", "error", err)
	} else {
		defer eRows.Close()
		for eRows.Next() {
			var item activityItem
			if err := eRows.Scan(
				&item.ID, &item.Summary, &item.Status, &item.Detail, &item.CreatedAt,
				&item.FromName, &item.FromSlug,
				&item.CrewName, &item.CrewSlug, &item.CrewColor,
			); err != nil {
				h.logger.Error("scan activity: escalation", "error", err)
				continue
			}
			item.Type = "escalation"
			items = append(items, item)
		}
	}

	// Sort all items by created_at DESC
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt > items[j].CreatedAt
	})

	// Truncate to limit
	if len(items) > limit {
		items = items[:limit]
	}

	if items == nil {
		items = []activityItem{}
	}

	writeJSON(w, http.StatusOK, items)
}

// ListEscalations handles GET /api/v1/crews/{crewId}/escalations.
func (h *QueryHandler) ListEscalations(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	type escalationItem struct {
		ID                 string  `json:"id"`
		Type               string  `json:"type"`
		FromName           string  `json:"from_name"`
		FromSlug           string  `json:"from_slug"`
		Reason             string  `json:"reason"`
		Context            *string `json:"context"`
		Metadata           *string `json:"metadata"`
		PeerConversationID *string `json:"peer_conversation_id"`
		Status             string  `json:"status"`
		Resolution         *string `json:"resolution"`
		ResolvedBy         *string `json:"resolved_by"`
		ResolvedAt         *string `json:"resolved_at"`
		CreatedAt          string  `json:"created_at"`
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT e.id, e.type, e.reason, e.context, e.metadata, e.peer_conversation_id, e.status,
		       e.resolution, e.resolved_by, e.resolved_at, e.created_at,
		       from_a.name, from_a.slug
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		WHERE e.crew_id = ? AND e.workspace_id = ?
		ORDER BY e.created_at DESC
		LIMIT ? OFFSET ?
	`, crewID, workspaceID, limit, offset)
	if err != nil {
		h.logger.Error("list escalations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	items := make([]escalationItem, 0)
	for rows.Next() {
		var item escalationItem
		if err := rows.Scan(
			&item.ID, &item.Type, &item.Reason, &item.Context, &item.Metadata,
			&item.PeerConversationID, &item.Status, &item.Resolution, &item.ResolvedBy,
			&item.ResolvedAt, &item.CreatedAt, &item.FromName, &item.FromSlug,
		); err != nil {
			h.logger.Error("scan escalation", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		// Never expose plaintext credential values to the list response
		if item.Type == "CREDENTIAL" && item.Resolution != nil {
			masked := "[credential submitted]"
			item.Resolution = &masked
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, items)
}
