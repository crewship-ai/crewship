package api

import (
	"database/sql"
	"net/http"
	"time"
)

type agentCredentialResponse struct {
	ID           string `json:"id"`
	AgentID      string `json:"agent_id"`
	CredentialID string `json:"credential_id"`
	CredName     string `json:"credential_name"`
	CredType     string `json:"credential_type"`
	CredProvider string `json:"credential_provider"`
	CredStatus   string `json:"credential_status"`
	EnvVarName   string `json:"env_var_name"`
	Priority     int    `json:"priority"`
	CreatedAt    string `json:"created_at"`
}

// ListCredentials returns all credentials assigned to the specified agent.
// GET /api/v1/agents/{agentId}/credentials
func (h *AgentHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "check agent exists", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// COALESCE the nullable text columns: a credential may legitimately have a
	// NULL provider/type/status (e.g. a SECRET with no provider, or a row mid
	// lifecycle), and ac.env_var_name/created_at can be NULL on older rows.
	// Scanning a NULL into a Go string returns "converting NULL to string is
	// unsupported" and 500s the whole list — so normalize to '' in SQL.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ac.id, ac.agent_id, ac.credential_id,
			COALESCE(c.name, ''), COALESCE(c.type, ''), COALESCE(c.provider, ''), COALESCE(c.status, ''),
			COALESCE(ac.env_var_name, ''), ac.priority, COALESCE(ac.created_at, '')
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?
		ORDER BY ac.env_var_name, ac.priority DESC
	`, agentID)
	if err != nil {
		replyInternalError(w, h.logger, "list agent credentials", err)
		return
	}
	defer rows.Close()

	var result []agentCredentialResponse
	for rows.Next() {
		var c agentCredentialResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.CredentialID, &c.CredName,
			&c.CredType, &c.CredProvider, &c.CredStatus,
			&c.EnvVarName, &c.Priority, &c.CreatedAt); err != nil {
			replyInternalError(w, h.logger, "scan agent credential", err)
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (agent credentials)", err)
		return
	}
	if result == nil {
		result = []agentCredentialResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

type addAgentCredentialRequest struct {
	CredentialID string `json:"credential_id"`
	EnvVarName   string `json:"env_var_name"`
	Priority     int    `json:"priority"`
}

// AddCredential assigns an existing credential to an agent with a specified environment variable name.
// POST /api/v1/agents/{agentId}/credentials
func (h *AgentHandler) AddCredential(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	foundAgent, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "check agent exists", err)
		return
	}
	if !foundAgent {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	var req addAgentCredentialRequest
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" || req.EnvVarName == "" {
		replyError(w, http.StatusBadRequest, "credential_id and env_var_name are required")
		return
	}

	// Verify credential exists in this workspace (single query prevents enumeration)
	foundCred, err := credentialExists(r.Context(), h.db, req.CredentialID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "check credential exists", err)
		return
	}
	if !foundCred {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, agentID, req.CredentialID, req.EnvVarName, req.Priority, now)
	if err != nil {
		h.logger.Error("add agent credential", "error", err)
		replyError(w, http.StatusConflict, "Credential already assigned to agent")
		return
	}

	// #1198: a human may grant an agent's credential need by creating +
	// assigning the credential directly instead of using `escalation
	// resolve --action approve` on the specific escalation record. Close
	// out any PENDING escalation this agent raised that clearly named this
	// credential, so the queue doesn't accumulate stale, functionally-done
	// rows. Best-effort — never fails the assignment itself.
	var credName sql.NullString
	if scanErr := h.db.QueryRowContext(r.Context(),
		`SELECT name FROM credentials WHERE id = ? AND workspace_id = ?`,
		req.CredentialID, workspaceID).Scan(&credName); scanErr != nil {
		h.logger.Warn("auto-resolve escalations: lookup credential name", "error", scanErr, "credential_id", req.CredentialID)
	} else if credName.Valid {
		autoResolveEscalationsForCredential(r.Context(), h.db, h.logger, h.hub, h.journal, workspaceID, agentID, credName.String)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// RemoveCredential unassigns a credential from an agent.
// DELETE /api/v1/agents/{agentId}/credentials/{credentialId}
func (h *AgentHandler) RemoveCredential(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("assignmentId")
	agentID := r.PathValue("agentId")
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM agent_credentials WHERE id = ? AND agent_id = ?
		 AND agent_id IN (SELECT id FROM agents WHERE workspace_id = ? AND deleted_at IS NULL)`,
		assignmentID, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "remove agent credential", err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		replyError(w, http.StatusNotFound, "Assignment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
