package api

// HandleRequest — keeper credential-access request handler. The agent
// asks for a credential, the gatekeeper Evaluator decides allow/deny
// based on intent, role, history. Extracted from keeper.go.

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

type keeperRequestBody struct {
	RequestingAgentID string `json:"requesting_agent_id"`
	RequestingCrewID  string `json:"requesting_crew_id"`
	WorkspaceID       string `json:"workspace_id"`
	CredentialID      string `json:"credential_id"`
	CredentialName    string `json:"credential_name"`
	TaskID            string `json:"task_id,omitempty"`
	Intent            string `json:"intent"`
}

// HandleRequest handles POST /api/v1/internal/keeper/request.
// Called by the sidecar bridge when an agent requests a non-API credential.

func (h *KeeperHandler) HandleRequest(w http.ResponseWriter, r *http.Request) {
	var body keeperRequestBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if body.RequestingAgentID == "" || body.RequestingCrewID == "" ||
		body.WorkspaceID == "" || body.Intent == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "requesting_agent_id, requesting_crew_id, workspace_id, intent required",
		})
		return
	}
	if body.CredentialID == "" && body.CredentialName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "credential_id or credential_name required",
		})
		return
	}

	// Resolve credential_name to credential_id if only name provided
	if body.CredentialID == "" && body.CredentialName != "" {
		err := h.db.QueryRowContext(r.Context(), `
			SELECT c.id FROM credentials c
			JOIN agent_credentials ac ON ac.credential_id = c.id
			WHERE ac.agent_id = ? AND ac.env_var_name = ? AND c.workspace_id = ? AND c.deleted_at IS NULL
			LIMIT 1`,
			body.RequestingAgentID, body.CredentialName, body.WorkspaceID).Scan(&body.CredentialID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "credential not found for name: " + body.CredentialName,
			})
			return
		}
	}

	// Validate that the requesting agent exists, is not deleted, and belongs to the
	// claimed crew and workspace. Prevents cross-crew and cross-workspace spoofing.
	var agentName, crewName, agentWorkspaceID string
	var agentCrewID sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(a.name,''), COALESCE(c.name,''), a.workspace_id, a.crew_id
		FROM agents a
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL`, body.RequestingAgentID).Scan(
		&agentName, &crewName, &agentWorkspaceID, &agentCrewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusUnauthorized, "requesting agent not found")
			return
		}
		h.logger.Error("keeper: lookup agent", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Workspace boundary: agent must belong to the workspace claimed in the request
	if agentWorkspaceID != body.WorkspaceID {
		replyError(w, http.StatusForbidden, "workspace boundary violation")
		return
	}
	// Crew boundary: if the agent has a crew assignment, it must match the claimed crew
	if agentCrewID.Valid && agentCrewID.String != body.RequestingCrewID {
		replyError(w, http.StatusForbidden, "crew boundary violation")
		return
	}

	// Look up credential metadata (name, security_level)
	var credName string
	var secLevel int
	err = h.db.QueryRowContext(r.Context(), `
		SELECT name, COALESCE(security_level, 1)
		FROM credentials
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.CredentialID, body.WorkspaceID).Scan(&credName, &secLevel)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "credential not found")
			return
		}
		h.logger.Error("keeper: lookup credential", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Build the keeper request record
	reqID := generateCUID()
	req := keeper.Request{
		ID:                reqID,
		RequestingAgentID: body.RequestingAgentID,
		RequestingCrewID:  body.RequestingCrewID,
		CredentialID:      body.CredentialID,
		CredentialName:    credName,
		SecurityLevel:     keeper.SecurityLevel(secLevel),
		TaskID:            body.TaskID,
		Intent:            body.Intent,
		WorkspaceID:       body.WorkspaceID,
		CreatedAt:         time.Now().UTC(),
	}

	// Persist PENDING request
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO keeper_requests (id, requesting_agent_id, requesting_crew_id, credential_id, task_id, intent, decision, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?,?), ?, 'PENDING', ?)`,
		reqID, body.RequestingAgentID, body.RequestingCrewID, body.CredentialID,
		body.TaskID, "", body.Intent, req.CreatedAt.Format(time.RFC3339)); err != nil {
		h.logger.Error("keeper: insert request", "error", err)
		// Non-fatal — continue with evaluation
	}

	// Emit keeper.request to the journal so a credential access ask
	// shows up in the Timeline even before the gatekeeper has decided.
	// Pairs with keeper.decision below — both share `request_id` in
	// payload so the UI can collapse a request+decision pair into a
	// single visual row when needed.
	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.RequestingCrewID,
		AgentID:     body.RequestingAgentID,
		Type:        journal.EntryKeeperRequest,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorAgent,
		ActorID:     body.RequestingAgentID,
		Summary:     fmt.Sprintf("%s requested credential %s", agentName, credName),
		Payload: map[string]any{
			"request_id":      reqID,
			"credential_id":   body.CredentialID,
			"credential_name": credName,
			"security_level":  secLevel,
			"intent":          body.Intent,
			"task_id":         body.TaskID,
		},
		Refs: map[string]any{"keeper_request_id": reqID, "credential_id": body.CredentialID},
	}); jerr != nil {
		h.logger.Warn("keeper: journal emit request failed", "error", jerr, "request_id", reqID)
	}

	// Load agent's recent conversation history for Keeper context
	convHistory := h.loadConversationHistory(r.Context(), body.RequestingAgentID)

	// Run gatekeeper evaluation
	evalReq := gatekeeper.EvalRequest{
		Request:        req,
		CredentialName: credName,
		SecurityLevel:  keeper.SecurityLevel(secLevel),
		AgentName:      agentName,
		CrewName:       crewName,
		ConvHistory:    convHistory,
	}

	var gkResp keeper.GatekeeperResponse
	if h.gatekeeper != nil {
		var evalErr error
		gkResp, evalErr = h.gatekeeper.Evaluate(r.Context(), evalReq)
		if evalErr != nil {
			h.logger.Error("keeper: gatekeeper evaluate failed", "error", evalErr)
			gkResp = keeper.GatekeeperResponse{
				Decision:  string(keeper.DecisionDeny),
				Reason:    "Keeper evaluation failed — deny by default",
				RiskScore: 10,
			}
		}
	} else {
		gkResp = keeper.GatekeeperResponse{
			Decision:  string(keeper.DecisionDeny),
			Reason:    "Keeper not configured",
			RiskScore: 10,
		}
	}

	// Clamp risk score to valid range [1, 10] regardless of evaluator output
	if gkResp.RiskScore < 1 {
		gkResp.RiskScore = 1
	}
	if gkResp.RiskScore > 10 {
		gkResp.RiskScore = 10
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE keeper_requests SET decision=?, reason=?, risk_score=?, decided_at=?, ollama_prompt=?, ollama_raw_response=? WHERE id=?`,
		gkResp.Decision, gkResp.Reason, gkResp.RiskScore, now,
		nullIfEmpty(gkResp.Prompt), nullIfEmpty(gkResp.RawLLMResponse), reqID); err != nil {
		h.logger.Error("keeper: update request decision", "error", err)
	}

	// Emit keeper.decision so the Timeline shows the verdict alongside
	// the request. Severity escalates to warn for DENY because a denied
	// credential ask is the kind of event an operator wants to see
	// without scrolling — it often means an agent went off the rails.
	decisionSeverity := journal.SeverityNotice
	if gkResp.Decision == string(keeper.DecisionDeny) {
		decisionSeverity = journal.SeverityWarn
	}
	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.RequestingCrewID,
		AgentID:     body.RequestingAgentID,
		Type:        journal.EntryKeeperDecision,
		Severity:    decisionSeverity,
		ActorType:   journal.ActorKeeper,
		ActorID:     "keeper",
		Summary: fmt.Sprintf("keeper %s credential %s for %s (risk %d)",
			gkResp.Decision, credName, agentName, gkResp.RiskScore),
		Payload: map[string]any{
			"request_id":      reqID,
			"credential_id":   body.CredentialID,
			"credential_name": credName,
			"decision":        gkResp.Decision,
			"reason":          gkResp.Reason,
			"risk_score":      gkResp.RiskScore,
			"security_level":  secLevel,
		},
		Refs: map[string]any{"keeper_request_id": reqID, "credential_id": body.CredentialID},
	}); jerr != nil {
		h.logger.Warn("keeper: journal emit decision failed", "error", jerr, "request_id", reqID)
	}

	// PR-Z Z.4: ESCALATE decisions land in the inbox as blocking items so
	// operators see them in the bell badge / unified feed. Prior to Z.4
	// ESCALATE only emitted to the journal — operators had no actionable
	// surface, escalations died silently. F4 endpoints in PR-C reuse the
	// same inbox.Insert plumbing for skill-review / behavior / memory-
	// health / negative-learning escalations.
	//
	// inbox.Insert is intentionally fire-and-forget (see writer.go doc:
	// "Best-effort: a SQL failure is logged and swallowed so the caller's
	// path stays intact. The inbox is a projection; the source table
	// remains the source of truth"). The escalation itself is already
	// persisted to keeper_requests (line 211 above) and journal (line 226)
	// — if the inbox projection write fails the operator misses the bell
	// badge but the data exists and can be re-projected by a backfill.
	// Failing the keeper /request response on a projection error would
	// flip the agent's credential request semantics from "ESCALATE
	// pending operator" to "transient transport error → retry" which is
	// the wrong recovery for the same outcome.
	if gkResp.Decision == string(keeper.DecisionEscalate) {
		inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: body.WorkspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    reqID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Keeper escalation: %s requested %s (risk %d)", agentName, credName, gkResp.RiskScore),
			BodyMD:      gkResp.Reason,
			SenderType:  "system",
			SenderID:    "keeper",
			SenderName:  "Keeper",
			Priority:    "high",
			Blocking:    true,
			Payload: map[string]interface{}{
				"request_id":      reqID,
				"request_type":    "access",
				"agent_id":        body.RequestingAgentID,
				"agent_name":      agentName,
				"credential_id":   body.CredentialID,
				"credential_name": credName,
				"security_level":  secLevel,
				"intent":          body.Intent,
				"reason":          gkResp.Reason,
				"risk_score":      gkResp.RiskScore,
			},
		})
	}

	h.logger.Info("keeper: decision",
		"request_id", reqID,
		"agent", agentName,
		"credential", credName,
		"level", secLevel,
		"decision", gkResp.Decision,
		"risk", gkResp.RiskScore)

	if h.broadcaster != nil {
		h.broadcaster.BroadcastKeeperEvent(body.WorkspaceID, map[string]any{
			"request_id":      reqID,
			"request_type":    "access",
			"agent_name":      agentName,
			"credential_name": credName,
			"intent":          body.Intent,
			"decision":        gkResp.Decision,
			"reason":          gkResp.Reason,
			"risk_score":      gkResp.RiskScore,
			"decided_at":      now,
		})
	}

	result := keeper.RequestResult{
		RequestID: reqID,
		Decision:  keeper.Decision(gkResp.Decision),
		Reason:    gkResp.Reason,
		RiskScore: gkResp.RiskScore,
	}
	writeJSON(w, http.StatusOK, result)
}

// keeperExecuteBody is the body forwarded by the sidecar for /keeper/execute requests.
// container_id and requesting_agent_id are always set by the sidecar (from IPC config),
// never taken from the agent's original request body.
