package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ── Create — POST /api/v1/issues ────────────────────────────────────────────

func (h *IssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Title         string   `json:"title"`
		Description   *string  `json:"description"`
		Priority      string   `json:"priority"`
		AssigneeType  *string  `json:"assignee_type"`
		AssigneeID    *string  `json:"assignee_id"`
		DueDate       *string  `json:"due_date"`
		ProjectID     *string  `json:"project_id"`
		Estimate      *int     `json:"estimate"`
		ParentIssueID *string  `json:"parent_issue_id"`
		MilestoneID   *string  `json:"milestone_id"`
		Labels        []string `json:"labels"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}
	if req.Priority == "" {
		req.Priority = "none"
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Get crew info
	var issuePrefix sql.NullString
	var crewSlug string
	err = tx.QueryRowContext(r.Context(),
		`SELECT issue_prefix, slug FROM crews WHERE id = ? AND workspace_id = ?`,
		crewID, wsID).Scan(&issuePrefix, &crewSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("get crew for issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Derive prefix from slug if not set
	prefix := issuePrefix.String
	if !issuePrefix.Valid || prefix == "" {
		slugUpper := strings.ToUpper(crewSlug)
		if len(slugUpper) >= 3 {
			prefix = slugUpper[:3]
		} else {
			prefix = slugUpper
		}
	}

	// Atomic counter for issue number
	var issueNumber int
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO issue_counters(crew_id, next_number) VALUES(?, 1)
		ON CONFLICT(crew_id) DO UPDATE SET next_number = issue_counters.next_number + 1
		RETURNING next_number`,
		crewID).Scan(&issueNumber)
	if err != nil {
		h.logger.Error("issue counter upsert", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	identifier := fmt.Sprintf("%s-%d", prefix, issueNumber)

	// Find lead agent for the crew
	var leadAgentID string
	err = tx.QueryRowContext(r.Context(),
		`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL LIMIT 1`,
		crewID).Scan(&leadAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusBadRequest, "Crew has no lead agent")
			return
		}
		h.logger.Error("find lead agent", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	id := generateCUID()
	traceID := "issue-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id,
		    title, description, status, number, identifier, priority,
		    assignee_type, assignee_id, due_date, project_id, estimate,
		    parent_issue_id, milestone_id, sort_order, mission_type,
		    created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'issue', ?, ?)`,
		id, wsID, crewID, leadAgentID, traceID,
		req.Title, req.Description, issueNumber, identifier, req.Priority,
		req.AssigneeType, req.AssigneeID, req.DueDate, req.ProjectID,
		req.Estimate, req.ParentIssueID, req.MilestoneID,
		now, now)
	if err != nil {
		h.logger.Error("insert issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Insert label associations
	for _, labelID := range req.Labels {
		_, err = tx.ExecContext(r.Context(),
			`INSERT OR IGNORE INTO mission_labels (mission_id, label_id) VALUES (?, ?)`,
			id, labelID)
		if err != nil {
			h.logger.Error("insert mission label", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := issueResponse{
		ID:            id,
		WorkspaceID:   wsID,
		CrewID:        crewID,
		Number:        &issueNumber,
		Identifier:    &identifier,
		Title:         req.Title,
		Description:   req.Description,
		Status:        "BACKLOG",
		Priority:      req.Priority,
		AssigneeType:  req.AssigneeType,
		AssigneeID:    req.AssigneeID,
		DueDate:       req.DueDate,
		SortOrder:     0,
		MissionType:   "issue",
		LeadAgentID:   leadAgentID,
		Estimate:      req.Estimate,
		ParentIssueID: req.ParentIssueID,
		MilestoneID:   req.MilestoneID,
		CreatedAt:     now,
		UpdatedAt:     now,
		Labels:        []labelResponse{},
	}

	h.broadcastIssueEvent(wsID, "issue.created", map[string]string{"id": id})

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Get — GET /api/v1/crews/{crewId}/issues/{identifier} ────────────────
