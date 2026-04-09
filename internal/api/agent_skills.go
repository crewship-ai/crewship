package api

import (
	"database/sql"
	"net/http"
	"time"
)

type agentSkillSkillData struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
	Category    *string `json:"category"`
	Source      string  `json:"source"`
	Icon        *string `json:"icon"`
	Version     *string `json:"version"`
}

type agentSkillResponse struct {
	ID      string              `json:"id"`
	AgentID string              `json:"agent_id"`
	SkillID string              `json:"skill_id"`
	Enabled bool                `json:"enabled"`
	Config  *string             `json:"config"`
	Skill   agentSkillSkillData `json:"skill"`
}

func (h *AgentHandler) ListSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	if err := agentExists(r.Context(), h.db, agentID, workspaceID); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT as2.id, as2.agent_id, as2.skill_id, as2.enabled, as2.config,
			s.id, s.name, s.slug, s.display_name, s.description,
			s.category, s.source, s.icon, s.version
		FROM agent_skills as2
		JOIN skills s ON s.id = as2.skill_id
		WHERE as2.agent_id = ?
	`, agentID)
	if err != nil {
		h.logger.Error("list agent skills", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []agentSkillResponse
	for rows.Next() {
		var s agentSkillResponse
		var enabled int
		if err := rows.Scan(&s.ID, &s.AgentID, &s.SkillID, &enabled, &s.Config,
			&s.Skill.ID, &s.Skill.Name, &s.Skill.Slug, &s.Skill.DisplayName,
			&s.Skill.Description, &s.Skill.Category, &s.Skill.Source,
			&s.Skill.Icon, &s.Skill.Version); err != nil {
			h.logger.Error("scan agent skill", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		s.Enabled = enabled == 1
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agent skills)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []agentSkillResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

type addAgentSkillRequest struct {
	SkillID string  `json:"skill_id"`
	Config  *string `json:"config"`
}

func (h *AgentHandler) AddSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if err := agentExists(r.Context(), h.db, agentID, workspaceID); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var req addAgentSkillRequest
	if err := readJSON(r, &req); err != nil || req.SkillID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill_id is required"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err := h.db.ExecContext(r.Context(),
		"INSERT INTO agent_skills (id, agent_id, skill_id, config, enabled, created_at) VALUES (?, ?, ?, ?, 1, ?)",
		id, agentID, req.SkillID, req.Config, now)
	if err != nil {
		h.logger.Error("add agent skill", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Skill already assigned to agent"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *AgentHandler) RemoveSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	skillID := r.PathValue("skillId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if err := agentExists(r.Context(), h.db, agentID, workspaceID); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		"DELETE FROM agent_skills WHERE agent_id = ? AND skill_id = ?",
		agentID, skillID)
	if err != nil {
		h.logger.Error("remove agent skill", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Skill not assigned to agent"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
