package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
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

// ListSkills returns all skills assigned to the specified agent.
// GET /api/v1/agents/{agentId}/skills
func (h *AgentHandler) ListSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
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

// AddSkill assigns a skill to an agent.
// POST /api/v1/agents/{agentId}/skills
func (h *AgentHandler) AddSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	var req addAgentSkillRequest
	if err := readJSON(r, &req); err != nil || req.SkillID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill_id is required"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	// Idempotent assign: if the skill is already on this agent, don't
	// fail the second call. Earlier behaviour returned 409 on every
	// retry, which broke "fan out --to-crew" for crews with one
	// already-installed agent and forced CLI users to special-case
	// network retries. Distinguish a UNIQUE-constraint hit (treat as
	// success, return existing id) from any other DB error (real
	// failure).
	_, err = h.db.ExecContext(r.Context(),
		"INSERT INTO agent_skills (id, agent_id, skill_id, config, enabled, created_at) VALUES (?, ?, ?, ?, 1, ?)",
		id, agentID, req.SkillID, req.Config, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			var existingID string
			if qErr := h.db.QueryRowContext(r.Context(),
				"SELECT id FROM agent_skills WHERE agent_id = ? AND skill_id = ?",
				agentID, req.SkillID).Scan(&existingID); qErr == nil {
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"id":               existingID,
					"already_assigned": true,
				})
				return
			}
			// Lookup itself failed — fall through to the conflict path
			// rather than returning a misleading "ok".
		}
		h.logger.Error("add agent skill", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Skill already assigned to agent"})
		return
	}

	user := UserFromContext(r.Context())
	var actorID string
	if user != nil {
		actorID = user.ID
	}
	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		Type:        journal.EntrySkillAssigned,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     "skill assigned to agent",
		Payload: map[string]any{
			"skill_id": req.SkillID,
		},
	}); jerr != nil {
		h.logger.Warn("skill assigned journal emit failed", "error", jerr)
	}
	// WS broadcast lets the Skills page (and any open agent detail
	// drawer) refresh the installed-on chips without polling. Cheap
	// (no payload beyond ids) and consistent with how credential
	// changes already announce themselves elsewhere.
	h.broadcastAgentEvent("agent.skill_assigned", workspaceID, map[string]string{
		"agent_id": agentID,
		"skill_id": req.SkillID,
	})

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// RemoveSkill unassigns a skill from an agent.
// DELETE /api/v1/agents/{agentId}/skills/{skillId}
func (h *AgentHandler) RemoveSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	skillID := r.PathValue("skillId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
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

	user := UserFromContext(r.Context())
	var actorID string
	if user != nil {
		actorID = user.ID
	}
	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		Type:        journal.EntrySkillUnassigned,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     "skill unassigned from agent",
		Payload: map[string]any{
			"skill_id": skillID,
		},
	}); jerr != nil {
		h.logger.Warn("skill unassigned journal emit failed", "error", jerr)
	}
	h.broadcastAgentEvent("agent.skill_unassigned", workspaceID, map[string]string{
		"agent_id": agentID,
		"skill_id": skillID,
	})

	w.WriteHeader(http.StatusNoContent)
}
