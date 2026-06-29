package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
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
	journal       journal.Emitter

	escalationMu      sync.Mutex
	escalationWaiters map[string]chan escalationResult
}

// NewQueryHandler creates a QueryHandler with the given orchestrator, hub, and internal token.
// Callers that want journal emits wire them after construction with SetJournal.
// The default is a no-op emitter so tests that don't care about journal
// integration continue to work without touching every test call site.
func NewQueryHandler(db *sql.DB, orch *orchestrator.Orchestrator, hub *ws.Hub, internalToken string, logger *slog.Logger) *QueryHandler {
	return &QueryHandler{
		db:                db,
		orch:              orch,
		hub:               hub,
		logger:            logger,
		internalToken:     internalToken,
		journal:           noopEmitter{},
		escalationWaiters: make(map[string]chan escalationResult),
	}
}

// SetJournal wires a journal emitter. nil is accepted and maps to the
// no-op so callers don't have to branch on whether the server wired one.
func (h *QueryHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// truncate clips s to n runes, appending "…" when cut. Used for journal
// summaries which must fit a single UI line — the raw content stays in
// payload for anyone who wants it.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
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
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if body.TargetSlug == "" || body.Question == "" || body.CrewID == "" || body.WorkspaceID == "" || body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "target_slug, question, crew_id, workspace_id, chat_id required",
		})
		return
	}
	// PR-F24 F-4: a bound token may only run peer queries inside its own
	// workspace (body workspace_id scopes the lookup; the auth middleware
	// can't inspect bodies).
	if !assertInternalTokenWorkspace(w, r, body.WorkspaceID) {
		return
	}
	// PR-F24 foreign-ID closure: crew_id and chat_id are independent of the
	// workspace_id checked above — prove they belong to the bound workspace
	// before running the peer query so a ws-A token can't drive a ws-B crew.
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, body.CrewID) {
		return
	}
	if !assertBoundChatWorkspaceDB(w, r, h.db, h.logger, body.ChatID) {
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
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt_legacy,''),
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
			replyError(w, http.StatusNotFound, "target agent not found")
			return
		}
		h.logger.Error("lookup target agent", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Load target agent credentials
	creds, err := h.loadAgentCredentials(r.Context(), target.ID)
	if err != nil {
		h.logger.Error("load agent credentials", "agent_id", target.ID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Dual-write into the Crew Journal. The old peer_conversations table
	// stays the source of truth for existing UI queries; the journal is
	// the new canonical stream once handlers migrate. State=running is
	// flagged so downstream consumers know a follow-up completed/failed
	// entry is coming on the same thread_id.
	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     fromAgentID,
		Type:        journal.EntryPeerConversation,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorAgent,
		ActorID:     fromAgentID,
		Summary:     fmt.Sprintf("%s asked %s: %s", body.FromSlug, body.TargetSlug, truncate(body.Question, 140)),
		Payload: map[string]any{
			"message_type": "question",
			"question":     body.Question,
			"from_slug":    body.FromSlug,
			"target_slug":  body.TargetSlug,
			"target_id":    target.ID,
			"state":        "running",
			"thread_id":    convID,
		},
		Refs: map[string]any{"peer_conversation_id": convID, "chat_id": body.ChatID},
	})

	// Record the peer-query as an agent run via the journal (single
	// source of truth post Phase J). trace_id == runID groups the
	// query's lifecycle entries.
	runID := generateCUID()
	if _, err := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		AgentID:     target.ID,
		Type:        journal.EntryRunStarted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorAgent,
		Summary:     fmt.Sprintf("run %s started (peer query)", shortRunID(runID)),
		Payload: map[string]any{
			"trigger_type":  "PEER_QUERY",
			"chat_id":       body.ChatID,
			"peer_query_id": convID,
			"from_slug":     body.FromSlug,
			"target_slug":   body.TargetSlug,
			"question":      body.Question,
		},
		Refs:    map[string]any{"peer_query_id": convID, "chat_id": body.ChatID},
		TraceID: runID,
	}); err != nil {
		h.logger.Error("create run record for query", "error", err)
		runID = "" // prevent finishQuery from emitting a terminal entry
	}

	// Thread runID into ctx (for the synchronous part of this handler)
	// AND override r's request context so downstream journal emits
	// during the orchestrator call group under the same trace.
	if runID != "" {
		r = r.WithContext(journal.WithRunID(r.Context(), runID))
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
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "", "orchestrator not available", startTime)
		replyError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	// Ensure crew container is running, created from the crew's PROVISIONED
	// image (with claude + tools) rather than the bare runtime default —
	// otherwise a cold target crew launches from the base image and the peer
	// query exits 127. Peer queries target an already-active crew in the
	// common case (container reused), so there's no provisioning gate here;
	// the image resolution alone fixes the cold-start case.
	var containerID string
	if crewCfg, cfgErr := buildCrewRuntimeConfig(r.Context(), h.db, body.CrewID, body.WorkspaceID); cfgErr != nil {
		h.logger.Warn("resolve crew runtime config for query; using bare container config",
			"error", cfgErr, "crew_id", body.CrewID, "query_id", convID)
		containerID, err = h.orch.GetOrCreateContainer(r.Context(), target.CrewSlug, body.CrewID, body.WorkspaceID)
	} else {
		containerID, err = h.orch.GetOrCreateContainerCfg(r.Context(), crewCfg, body.WorkspaceID)
	}
	if err != nil {
		h.logger.Error("get container for query", "error", err, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "",
			fmt.Sprintf("container error: %v", err), startTime)
		replyError(w, http.StatusInternalServerError, "container error")
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
		SkipSidecar:     true, // Sidecar already running on 9119 in this container
		SkipConvHistory: true, // Fresh context for peer queries
	}

	// Guard against running while a backup holds the workspace lock.
	guardRelease, guardErr := refuseIfBackupInProgress(r.Context(), h.db, body.WorkspaceID)
	if guardErr != nil {
		h.logger.Warn("peer query refused — backup in progress", "query_id", convID, "workspace_id", body.WorkspaceID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "", guardErr.Error(), startTime)
		writeJSON(w, http.StatusConflict, map[string]string{"error": guardErr.Error(), "query_id": convID})
		return
	}
	defer guardRelease()

	if err := h.orch.RunAgentForAssignment(r.Context(), req, handler); err != nil {
		h.logger.Error("peer query execution failed", "error", err, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "",
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

	h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, result, "", startTime)

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
// crewID + targetAgentID are threaded through so the closing answer
// journal entry carries the same scope as the opening question entry —
// without them, crew/agent-filtered journal views see the running row
// but never the completion, which makes the UI look like every peer
// query is permanently running.
func (h *QueryHandler) finishQuery(
	ctx context.Context,
	convID, runID, chatID, fromSlug, targetSlug, workspaceID, crewID, targetAgentID, result, errMsg string,
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

	// Emit the answer entry on the same thread. Severity elevates to
	// error when the call failed so the journal filters surface failed
	// peer queries without having to read payload.state on every row.
	answerSev := journal.SeverityInfo
	if errMsg != "" {
		answerSev = journal.SeverityError
	}
	summary := fmt.Sprintf("%s → %s: %s (%dms)", fromSlug, targetSlug, strings.ToLower(status), durationMs)
	if errMsg != "" {
		summary = fmt.Sprintf("%s → %s: FAILED (%s)", fromSlug, targetSlug, truncate(errMsg, 120))
	}
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		AgentID:     targetAgentID,
		Type:        journal.EntryPeerConversation,
		Severity:    answerSev,
		ActorType:   journal.ActorAgent,
		ActorID:     targetAgentID,
		Summary:     summary,
		Payload: map[string]any{
			"message_type": "answer",
			"response":     result,
			"error":        errMsg,
			"from_slug":    fromSlug,
			"target_slug":  targetSlug,
			"state":        strings.ToLower(status),
			"duration_ms":  durationMs,
			"thread_id":    convID,
		},
		Refs: map[string]any{"peer_conversation_id": convID, "chat_id": chatID},
	})

	// Emit terminal run.* entry — the source of truth post Phase J.
	if runID != "" {
		runStatus := status
		entryType := terminalEntryType(runStatus)
		runSeverity := journal.SeverityInfo
		if runStatus == "FAILED" {
			runSeverity = journal.SeverityError
		}
		runPayload := map[string]any{
			"peer_query_id": convID,
			"duration_ms":   durationMs,
		}
		if errMsg != "" {
			runPayload["error_message"] = errMsg
		}
		if runStatus == "COMPLETED" {
			runPayload["exit_code"] = 0
		}
		if _, err := h.journal.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			CrewID:      crewID,
			AgentID:     targetAgentID,
			Type:        entryType,
			Severity:    runSeverity,
			ActorType:   journal.ActorAgent,
			Summary:     fmt.Sprintf("run %s %s (peer query)", shortRunID(runID), entryType[len("run."):]),
			Payload:     runPayload,
			Refs:        map[string]any{"peer_query_id": convID, "chat_id": chatID},
			TraceID:     runID,
		}); err != nil {
			h.logger.Error("emit terminal run entry for query", "error", err, "run_id", runID)
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
		ID         string  `json:"id"`
		FromName   string  `json:"from_name"`
		FromSlug   string  `json:"from_slug"`
		ToName     string  `json:"to_name"`
		ToSlug     string  `json:"to_slug"`
		Question   string  `json:"question"`
		Response   *string `json:"response"`
		Status     string  `json:"status"`
		DurationMs *int    `json:"duration_ms"`
		Escalated  bool    `json:"escalated"`
		CreatedAt  string  `json:"created_at"`
		FinishedAt *string `json:"finished_at"`
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	items := make([]peerConvItem, 0, capacityHint(limit))
	for rows.Next() {
		var item peerConvItem
		var escalatedInt int
		if err := rows.Scan(
			&item.ID, &item.Question, &item.Response, &item.Status, &item.DurationMs,
			&escalatedInt, &item.CreatedAt, &item.FinishedAt,
			&item.FromName, &item.FromSlug, &item.ToName, &item.ToSlug,
		); err != nil {
			h.logger.Error("scan peer conversation", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		item.Escalated = escalatedInt != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, items)
}
