package api

// HandleRequest — keeper credential-access request handler. The agent
// asks for a credential, the gatekeeper Evaluator decides allow/deny
// based on intent, role, history. Extracted from keeper.go.

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
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
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "requesting agent not found"})
			return
		}
		h.logger.Error("keeper: lookup agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	// Workspace boundary: agent must belong to the workspace claimed in the request
	if agentWorkspaceID != body.WorkspaceID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "workspace boundary violation"})
		return
	}
	// Crew boundary: if the agent has a crew assignment, it must match the claimed crew
	if agentCrewID.Valid && agentCrewID.String != body.RequestingCrewID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "crew boundary violation"})
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
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential not found"})
			return
		}
		h.logger.Error("keeper: lookup credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
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
