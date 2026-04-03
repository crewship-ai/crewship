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

func (h *AgentHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ac.id, ac.agent_id, ac.credential_id, c.name, c.type, c.provider, c.status,
			ac.env_var_name, ac.priority, ac.created_at
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?
		ORDER BY ac.env_var_name, ac.priority DESC
	`, agentID)
	if err != nil {
		h.logger.Error("list agent credentials", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []agentCredentialResponse
	for rows.Next() {
		var c agentCredentialResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.CredentialID, &c.CredName,
			&c.CredType, &c.CredProvider, &c.CredStatus,
			&c.EnvVarName, &c.Priority, &c.CreatedAt); err != nil {
			h.logger.Error("scan agent credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agent credentials)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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

func (h *AgentHandler) AddCredential(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var req addAgentCredentialRequest
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" || req.EnvVarName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential_id and env_var_name are required"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, agentID, req.CredentialID, req.EnvVarName, req.Priority, now)
	if err != nil {
		h.logger.Error("add agent credential", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Credential already assigned to agent"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *AgentHandler) RemoveCredential(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("assignmentId")
	agentID := r.PathValue("agentId")
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM agent_credentials WHERE id = ? AND agent_id = ?
		 AND agent_id IN (SELECT id FROM agents WHERE workspace_id = ? AND deleted_at IS NULL)`,
		assignmentID, agentID, workspaceID)
	if err != nil {
		h.logger.Error("remove agent credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Assignment not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
