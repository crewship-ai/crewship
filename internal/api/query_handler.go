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

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/hooks"
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
	provisioner   crewProvisioner
	// resolver funnels the peer-query run through the one request-builder
	// (#810). nil → the legacy raw-SQL build (system_prompt_legacy, no MCP).
	resolver agentConfigResolver

	escalationMu      sync.Mutex
	escalationWaiters map[string][]chan escalationResult

	// askJudge gates a credential ASK before it is staged (#2392). nil leaves
	// ask gating off — every ask is staged and routed to a human, the
	// pre-#2392 behaviour. Wired via SetCredentialAskJudge at router setup.
	askJudge CredentialAskJudge
}

// SetResolver wires the agent-config resolver so a peer query builds its run
// through the one request-builder (#810). nil leaves the legacy path.
func (h *QueryHandler) SetResolver(rz agentConfigResolver) {
	h.resolver = rz
}

// SetProvisioner wires the dispatch-time provisioning gate (same as
// AssignmentHandler) so a peer query against a cold crew builds the
// devcontainer image before running instead of booting the bare base image.
func (h *QueryHandler) SetProvisioner(p crewProvisioner) { h.provisioner = p }

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
		escalationWaiters: make(map[string][]chan escalationResult),
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
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &body.CrewID) {
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
		replyInternalError(w, h.logger, "lookup target agent", err)
		return
	}

	// pre_peer_conversation: fires before the peer_conversations row exists
	// and before the target agent runs — this is the one place every
	// `curl localhost:9119/query` call converges on (Create is the
	// synchronous handler behind the sidecar's /query, and there is no
	// second entry point the way task delegation has three). A blocking
	// hook can refuse the whole peer question here.
	//
	// Both error kinds fail closed — a gate that cannot be evaluated must
	// not read as passed — but they are different answers and get different
	// statuses. A *hooks.BlockedError is a policy decision: 403, and the
	// handler's message goes back so the asking agent learns why. A
	// *hooks.DispatchError means the registry could not be read or a handler
	// is broken: that is our fault, not the caller's, so it is a 500 with a
	// generic body (hookErr wraps the raw DB error, which does not belong in
	// a response) and the detail stays in the log.
	//
	// DispatchError is checked FIRST, and that order is load-bearing.
	// Dispatch returns errors.Join(dispatchErrs..., blocked) when one
	// blocking hook fails to run and a LATER one blocks, so the joined error
	// satisfies errors.As for both. Asking about BlockedError first would
	// answer 403 — a clean policy refusal — while silently swallowing the
	// fact that a hook never ran at all, which is the louder of the two
	// facts and the one the operator has to fix.
	if hookErr := hooks.Dispatch(r.Context(), h.db, h.journal, hooks.EventPrePeerConversation, hooks.EventContext{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     target.ID,
		Payload: map[string]any{
			"from_slug":   body.FromSlug,
			"target_slug": body.TargetSlug,
			"chat_id":     body.ChatID,
		},
	}); hookErr != nil {
		var dispatchErr *hooks.DispatchError
		var blocked *hooks.BlockedError
		if !errors.As(hookErr, &dispatchErr) && errors.As(hookErr, &blocked) {
			h.logger.Info("peer query refused: pre_peer_conversation hook blocked",
				"from_slug", body.FromSlug, "target_slug", body.TargetSlug, "error", hookErr)
			writeJSON(w, http.StatusForbidden, map[string]string{"error": hookErr.Error()})
			return
		}
		h.logger.Error("peer query refused: pre_peer_conversation hook could not be evaluated",
			"from_slug", body.FromSlug, "target_slug", body.TargetSlug, "error", hookErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "pre_peer_conversation hook could not be evaluated",
		})
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
		replyInternalError(w, h.logger, "create peer_conversation", err)
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

	// Session provenance for the terminal run record, filled in once the CLI
	// stream starts (below). Declared before the early bails so every
	// finishQuery call site can pass it: those pass it still nil, the getters
	// are nil-safe, and a query that never reached a CLI records nothing
	// rather than blank fields.
	var acc *orchestrator.Accumulator

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
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "", "orchestrator not available", startTime, acc)
		replyError(w, http.StatusServiceUnavailable, "orchestrator not available")
		return
	}

	// Ensure the target crew is provisioned and run the agent from its
	// PROVISIONED image (with claude + tools), not the bare runtime base.
	// A first peer query against a cold crew (devcontainer config, no cached
	// image) would otherwise boot the base image and exit 127, so gate on the
	// same provisioning step assignments use. Fail closed on resolution error
	// (deleted/misconfigured crew) rather than degrading to the base image.
	if h.provisioner != nil {
		if perr := h.provisioner.EnsureProvisioned(r.Context(), body.CrewID, body.WorkspaceID, 0); perr != nil {
			h.logger.Error("ensure provisioned for query", "error", perr, "query_id", convID, "crew_id", body.CrewID)
			h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "",
				fmt.Sprintf("preparing the crew container failed: %v", perr), startTime, acc)
			replyError(w, http.StatusServiceUnavailable, "crew not ready")
			return
		}
	}
	var containerID string
	crewCfg, cfgErr := buildCrewRuntimeConfig(r.Context(), h.db, body.CrewID, body.WorkspaceID)
	if cfgErr != nil {
		h.logger.Error("resolve crew runtime config for query", "error", cfgErr, "crew_id", body.CrewID, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "",
			fmt.Sprintf("resolve crew runtime config: %v", cfgErr), startTime, acc)
		replyError(w, http.StatusInternalServerError, "container error")
		return
	}
	// Audit the runtime container-prep steps for this agent-run path too.
	if h.provisioner != nil {
		crewCfg.ProvisionSink = h.provisioner.RuntimeProvisionSink(r.Context(), body.CrewID, body.WorkspaceID)
	}
	containerID, err = h.orch.GetOrCreateContainerCfg(r.Context(), crewCfg, body.WorkspaceID)
	if err != nil {
		h.logger.Error("get container for query", "error", err, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "",
			fmt.Sprintf("container error: %v", err), startTime, acc)
		replyError(w, http.StatusInternalServerError, "container error")
		return
	}

	// Collect agent output. The shared buffering handler runs underneath for
	// its session-init capture only; the answer text keeps its own collection
	// (it is truncated below before it becomes the peer response).
	var outputParts []string
	base, bufAcc := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
		CaptureResultMeta: true,
	})
	acc = bufAcc
	handler := func(event orchestrator.AgentEvent) {
		if event.Type == "text" && event.Content != "" {
			outputParts = append(outputParts, event.Content)
		}
		base(event)
	}

	// The [PEER QUERY] block is prepended to the ASSEMBLED system prompt so
	// the answering agent keeps its skills/persona/MCP context but knows this
	// is a quick question, not a task.
	peerQueryBlock := fmt.Sprintf(`[PEER QUERY from @%s]
Answer concisely. This is a quick question, not a task.
Question: %s`, body.FromSlug, body.Question)

	req, buildErr := h.buildPeerQueryRequest(r.Context(), body, target, containerID, peerQueryBlock)
	if buildErr != nil {
		// Fail closed: the single builder could not assemble the request (no
		// resolver / resolve failure). Fail the query loudly rather than answer
		// with an MCP-blind, unassembled-prompt degraded run.
		h.logger.Error("peer query dispatch build failed", "error", buildErr, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "", buildErr.Error(), startTime, acc)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to dispatch peer query", "query_id": convID})
		return
	}

	// Guard against running while a backup holds the workspace lock.
	guardRelease, guardErr := refuseIfBackupInProgress(r.Context(), h.db, body.WorkspaceID)
	if guardErr != nil {
		h.logger.Warn("peer query refused — backup in progress", "query_id", convID, "workspace_id", body.WorkspaceID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "", guardErr.Error(), startTime, acc)
		writeJSON(w, http.StatusConflict, map[string]string{"error": guardErr.Error(), "query_id": convID})
		return
	}
	defer guardRelease()

	if err := h.orch.RunAgentForAssignment(r.Context(), req, handler); err != nil {
		h.logger.Error("peer query execution failed", "error", err, "query_id", convID)
		h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, "",
			fmt.Sprintf("execution error: %v", err), startTime, acc)
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

	h.finishQuery(r.Context(), convID, runID, body.ChatID, body.FromSlug, body.TargetSlug, body.WorkspaceID, body.CrewID, target.ID, result, "", startTime, acc)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"query_id": convID,
		"from":     body.FromSlug,
		"target":   body.TargetSlug,
		"question": body.Question,
		"response": result,
		"status":   "COMPLETED",
	})
}

// buildPeerQueryRequest constructs the AgentRunRequest for a peer query. It
// funnels EXCLUSIVELY through the ONE request-builder (ChatInfo.ToAgentRunRequest),
// so the answering agent gets its ASSEMBLED system prompt (skills, persona,
// connected integrations), MCP servers, installed skills, and the crew-policy
// ApprovalMode. peerQueryBlock is prepended to the assembled prompt.
//
// There is NO legacy raw-system_prompt fallback. A missing resolver or a
// resolve failure returns an ERROR — the peer query fails LOUDLY rather than
// answering with an MCP-blind, unassembled-prompt degraded run (the peer path
// used to read raw system_prompt_legacy with no MCP, and the earlier #810 cut
// kept it as a silent-degrade fallback on resolve error). Production always
// wires the resolver.
func (h *QueryHandler) buildPeerQueryRequest(
	ctx context.Context,
	body createQueryBody,
	target targetAgentInfo,
	containerID, peerQueryBlock string,
) (orchestrator.AgentRunRequest, error) {
	if h.resolver == nil {
		return orchestrator.AgentRunRequest{}, fmt.Errorf("peer query dispatch: no agent resolver wired")
	}
	info, rerr := h.resolver.ResolveAgent(ctx, target.ID, body.WorkspaceID)
	if rerr != nil {
		return orchestrator.AgentRunRequest{}, fmt.Errorf("peer query dispatch: resolve agent %s: %w", target.ID, rerr)
	}
	if info == nil {
		return orchestrator.AgentRunRequest{}, fmt.Errorf("peer query dispatch: resolver returned no config for agent %s", target.ID)
	}
	// Prepend the peer-query block to the assembled prompt.
	if info.SystemPrompt != "" {
		info.SystemPrompt += "\n\n"
	}
	info.SystemPrompt += peerQueryBlock
	req := info.ToAgentRunRequest(chatbridge.AgentRunOverrides{
		ChatID:      body.ChatID,
		ContainerID: containerID,
		UserMessage: body.Question,
		LLMModel:    info.LLMModel,
		TimeoutSecs: info.TimeoutSecs,
		MemoryMB:    info.MemoryMB,
		CPUs:        info.CPUs,
	})
	req.AgentRole = "AGENT"
	req.SkipSidecar = true     // Sidecar already running on 9119 in this container
	req.SkipConvHistory = true // Fresh context for peer queries
	return req, nil
}

// finishQuery updates peer_conversations and agent_runs records.
// crewID + targetAgentID are threaded through so the closing answer
// journal entry carries the same scope as the opening question entry —
// without them, crew/agent-filtered journal views see the running row
// but never the completion, which makes the UI look like every peer
// query is permanently running.
//
// acc carries the CLI's session-init provenance onto the terminal run entry;
// it is nil at the call sites that ended the query before an agent ran, and
// nothing is recorded for those.
func (h *QueryHandler) finishQuery(
	ctx context.Context,
	convID, runID, chatID, fromSlug, targetSlug, workspaceID, crewID, targetAgentID, result, errMsg string,
	startTime time.Time,
	acc *orchestrator.Accumulator,
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
	conversationUpdated := true
	if _, err := h.db.ExecContext(ctx,
		`UPDATE peer_conversations SET status=?, response=?, duration_ms=?, finished_at=? WHERE id=?`,
		status, responseVal, durationMs, now, convID); err != nil {
		conversationUpdated = false
		h.logger.Error("update peer_conversation", "error", err, "id", convID)
	}

	// post_peer_conversation is an observation, so publish it only after the
	// terminal state it describes is durable. A background context keeps a
	// request cancellation racing the response from dropping the lookup; the
	// dispatcher itself runs observation handlers asynchronously.
	if conversationUpdated {
		_ = hooks.Dispatch(context.Background(), h.db, h.journal, hooks.EventPostPeerConversation, hooks.EventContext{
			WorkspaceID: workspaceID,
			CrewID:      crewID,
			AgentID:     targetAgentID,
			Payload: map[string]any{
				"from_slug":   fromSlug,
				"target_slug": targetSlug,
				"status":      status,
				"duration_ms": durationMs,
			},
		})
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
		// Unconditional on status — the failed peer query is the one whose
		// provenance gets asked about.
		if md := runSessionProvenance(acc); md != nil {
			runPayload["metadata"] = md
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

// loadAgentCredentials queries and decrypts all credentials for an agent, for
// the peer-query run.
//
// The set is defined once, in loadDeliveredCredentials: explicit
// agent_credentials grants UNION credentials linked to the agent's own crew via
// credential_crews. Three filters here are load-bearing, and every one of them
// arrived late to THIS loader specifically:
//
//   - status = 'ACTIVE' (#1051): PENDING rows (manifest slots, OAuth
//     mid-handshake, rotation in progress) carry sentinel encrypted bodies.
//     resolveAgentCredentials and the delegation loader were both fixed for this;
//     this third loader was not, so a peer query decrypted and injected
//     "pending_oauth" as a real env value. The in-code sentinel guard below
//     mirrors theirs as defence in depth.
//   - the #1373 lease gate: a grant may be a short-lived lease, and a peer query
//     is a full agent run — delivering a lapsed lease here reintroduces exactly
//     the standing credential the TTL was meant to remove.
//   - the crew fanout (PRD-CREDENTIALS-V2 §1.2): a peer query is a full agent
//     run, so an agent answering one needs what it boots with. Fixing the other
//     two loaders and leaving this one would have made a crew-assigned secret
//     present at boot and at the sub-agent boundary and absent when a peer asks
//     — a difference nobody could explain from the outside.
//
// Three arrivals, three near-misses, one shared definition now. Do not spell the
// query out here again.
func (h *QueryHandler) loadAgentCredentials(ctx context.Context, agentID string) ([]orchestrator.Credential, error) {
	delivered, slotNotices, err := loadDeliveredCredentials(ctx, h.db, agentID)
	if err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	logDeliveredCredentialNotices(h.logger, agentID, delivered, slotNotices)

	var creds []orchestrator.Credential
	for _, d := range delivered {
		c := orchestrator.Credential{
			ID:             d.ID,
			EnvVarName:     d.EnvVar,
			Priority:       d.Priority,
			Type:           d.Type,
			Provider:       d.Provider,
			LeaseExpiresAt: d.LeaseExpiresAt,
			AgentIDs:       d.GrantedAgentIDs,
			HandleOnly:     d.HandleOnly,
		}
		// A handle-only credential is delivered as its NAME and nothing else
		// (#2376): the value is never decrypted on a delivery path, so there is
		// nothing for a later gate to forget to withhold.
		if d.HandleOnly {
			logHandleOnlyWithheld(h.logger, agentID, d.EnvVar)
			creds = append(creds, c)
			continue
		}
		dec, err := encryption.Decrypt(d.EncryptedValue)
		if err != nil {
			return nil, fmt.Errorf("decrypt credential %s: %w", c.ID, err)
		}
		if isPendingSentinel(dec) {
			continue
		}
		c.PlainValue = dec
		// A provider whose upstream lives in the credential (OPENAI_COMPAT)
		// stores {baseURL,apiKey,headers} as one object. PlainValue becomes the
		// sidecar's bearer token, so the object has to be split here too — this
		// loader carries the provider column exactly like the boot path, and
		// without the split the base URL and every custom header would be sent
		// upstream AS the secret.
		//
		// A malformed endpoint value FAILS the query, matching the decrypt
		// branch above and the parts branch below rather than the `continue`
		// used by the boot loader in assignments.go. The two loaders differ on
		// purpose and this one is not free to choose: it already fails the whole
		// peer query on a failed decrypt, so continuing here would start a run
		// without its provider credential, and that run dies at the first model
		// call with an error about something else. One policy per function, and
		// this function's is stated three lines below.
		if providerNeedsEndpointValue(d.Provider) {
			token, baseURL, headers, perr := providerEndpointFromValue(d.Provider, dec)
			if perr != nil {
				return nil, fmt.Errorf("endpoint credential %s (provider %s): %w", c.ID, d.Provider, perr)
			}
			c.PlainValue, c.BaseURL, c.Headers = token, baseURL, headers
		}
		// The credential's parts (PRD §2.2), through the same opener. This
		// loader's policy for a failed decrypt is to fail the whole peer query
		// rather than to run one credential short, and a part is no different:
		// a run that answers with a broken tool call is worse than one that
		// says it could not start.
		fields, err := decryptDeliveredFields(d, encryption.Decrypt)
		if err != nil {
			return nil, err
		}
		for _, f := range fields {
			c.Fields = append(c.Fields, orchestrator.CredentialField{
				EnvVar: f.EnvVar, Value: f.Value, IsSecret: f.IsSecret,
			})
		}
		creds = append(creds, c)
	}
	return creds, nil
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
		replyInternalError(w, h.logger, "list peer conversations", err)
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
			replyInternalError(w, h.logger, "scan peer conversation", err)
			return
		}
		item.Escalated = escalatedInt != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration", err)
		return
	}

	writeJSON(w, http.StatusOK, items)
}
