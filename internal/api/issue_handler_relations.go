package api

import (
	"net/http"
	"strings"
)

// ── 12. ListRelations — GET /api/v1/crews/{crewId}/issues/{identifier}/relations

func (h *IssueHandler) ListRelations(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Issue not found")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT mr.id, mr.source_id, mr.target_id, mr.relation_type, mr.created_at,
		       m.identifier, m.title, m.status
		FROM mission_relations mr
		JOIN missions m ON m.id = CASE WHEN mr.source_id = ? THEN mr.target_id ELSE mr.source_id END
		WHERE mr.source_id = ? OR mr.target_id = ?`,
		missionID, missionID, missionID)
	if err != nil {
		internalError(w, r, h.logger, "list relations", err)
		return
	}
	defer rows.Close()

	var result []relationResponse
	for rows.Next() {
		var rel relationResponse
		if err := rows.Scan(&rel.ID, &rel.SourceID, &rel.TargetID, &rel.RelationType, &rel.CreatedAt,
			&rel.TargetIdentifier, &rel.TargetTitle, &rel.TargetStatus); err != nil {
			h.logger.Error("scan relation", "error", err)
			continue
		}
		if rel.TargetID == missionID {
			switch rel.RelationType {
			case "blocks":
				rel.RelationType = "blocked_by"
			case "blocked_by":
				rel.RelationType = "blocks"
			}
		}
		result = append(result, rel)
	}
	if result == nil {
		result = []relationResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 13. CreateRelation — POST /api/v1/crews/{crewId}/issues/{identifier}/relations

func (h *IssueHandler) CreateRelation(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		TargetIdentifier string `json:"target_identifier"`
		RelationType     string `json:"relation_type"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	validTypes := map[string]bool{"blocks": true, "blocked_by": true, "relates_to": true, "duplicate_of": true}
	if !validTypes[req.RelationType] {
		writeProblem(w, r, http.StatusBadRequest, "relation_type must be: blocks, blocked_by, relates_to, duplicate_of")
		return
	}

	sourceID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Source issue not found")
		return
	}

	var targetID string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND workspace_id = ?`,
		req.TargetIdentifier, wsID).Scan(&targetID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Target issue not found: "+req.TargetIdentifier)
		return
	}

	if sourceID == targetID {
		writeProblem(w, r, http.StatusBadRequest, "Cannot relate an issue to itself")
		return
	}

	actualSource, actualTarget, actualType := sourceID, targetID, req.RelationType
	if req.RelationType == "blocked_by" {
		actualSource, actualTarget, actualType = targetID, sourceID, "blocks"
	}

	id := generateCUID()
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_relations (id, source_id, target_id, relation_type) VALUES (?, ?, ?, ?)`,
		id, actualSource, actualTarget, actualType)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeProblem(w, r, http.StatusConflict, "Relation already exists")
			return
		}
		internalError(w, r, h.logger, "create relation", err)
		return
	}

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": sourceID})

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "ok"})
}

// ── 14. DeleteRelation — DELETE /api/v1/relations/{relationId}

func (h *IssueHandler) DeleteRelation(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	relID := r.PathValue("relationId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Pre-fix the DELETE was unscoped: any authenticated user could
	// guess a relation ID and delete a relationship between two issues
	// in any workspace. The fix is a join-bound delete — the relation
	// only goes if BOTH endpoints live in the caller's workspace.
	// CreateRelation already enforces same-workspace endpoints, so this
	// rejects no legitimate request.
	res, err := h.db.ExecContext(r.Context(), `
		DELETE FROM mission_relations
		WHERE id = ?
		  AND source_id IN (SELECT id FROM missions WHERE workspace_id = ?)
		  AND target_id IN (SELECT id FROM missions WHERE workspace_id = ?)`,
		relID, wsID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete relation", err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Don't distinguish "not found in this workspace" from "doesn't
		// exist anywhere" — that would be a cross-workspace existence
		// oracle.
		writeProblem(w, r, http.StatusNotFound, "Relation not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
