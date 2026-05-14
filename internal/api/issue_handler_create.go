package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ── Create — POST /api/v1/issues ────────────────────────────────────────────

func (h *IssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
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
		// RoutineID binds the issue to a saved routine (pipeline). When
		// set, /run-routine on this issue invokes the bound pipeline
		// with RoutineInputs as the inputs payload. Stored as the
		// pipeline_id (UUID, not slug) so renames don't break the
		// link. Optional — most issues won't have one.
		RoutineID     *string                `json:"routine_id"`
		RoutineInputs map[string]interface{} `json:"routine_inputs"`
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

	// If a routine binding was supplied, validate the pipeline_id exists
	// in this workspace before INSERT. Catching it here gives the user
	// a 400 instead of a foreign-key-style failure later when /run-routine
	// is hit. routine_inputs piggybacks on the same null-check: an empty
	// inputs map serializes to '{}', matching the column default.
	//
	// We split the err vs. not-found cases — a bad SQL state shouldn't
	// be reported to the user as "routine doesn't exist."
	var routineInputsJSON sql.NullString
	// Normalize empty string to nil so a UI that posts "" doesn't
	// trip the existence check.
	if req.RoutineID != nil && *req.RoutineID == "" {
		req.RoutineID = nil
	}
	// Reject orphan inputs — the SQL COALESCE would silently swallow
	// them when routine_id is NULL, and storing inputs without a
	// routine to drive them is meaningless. Surface the mistake so
	// callers don't think their inputs landed.
	if req.RoutineID == nil && req.RoutineInputs != nil {
		writeProblem(w, r, http.StatusBadRequest, "routine_inputs provided without routine_id")
		return
	}
	if req.RoutineID != nil {
		var exists int
		err = tx.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM pipelines WHERE id = ? AND workspace_id = ?`,
			*req.RoutineID, wsID).Scan(&exists)
		if err != nil {
			h.logger.Error("validate routine_id", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if exists == 0 {
			writeProblem(w, r, http.StatusBadRequest, "routine_id does not exist in this workspace")
			return
		}
		if req.RoutineInputs == nil {
			routineInputsJSON = sql.NullString{String: "{}", Valid: true}
		} else {
			b, mErr := json.Marshal(req.RoutineInputs)
			if mErr != nil {
				writeProblem(w, r, http.StatusBadRequest, "routine_inputs is not valid JSON")
				return
			}
			routineInputsJSON = sql.NullString{String: string(b), Valid: true}
		}
	}

	// Same workspace-scoping for parent_issue_id. Pre-fix the field was
	// inserted verbatim from the request — a workspace-A user could
	// POST a new issue under their own crew with parent_issue_id pointing
	// at a workspace-B issue, silently linking unrelated tenants together.
	// Either side reading the parent later would hit the wrong row or get
	// a confusing 403 because the referenced issue wasn't in their visible
	// set. We reject the cross-workspace parent at insert time.
	if req.ParentIssueID != nil && *req.ParentIssueID != "" {
		var parentExists int
		err = tx.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM missions WHERE id = ? AND workspace_id = ?`,
			*req.ParentIssueID, wsID).Scan(&parentExists)
		if err != nil {
			h.logger.Error("validate parent_issue_id", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if parentExists == 0 {
			writeProblem(w, r, http.StatusBadRequest, "parent_issue_id does not exist in this workspace")
			return
		}
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id,
		    title, description, status, number, identifier, priority,
		    assignee_type, assignee_id, due_date, project_id, estimate,
		    parent_issue_id, milestone_id, sort_order, mission_type,
		    routine_id, routine_inputs_json,
		    created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'issue', ?, COALESCE(?, '{}'), ?, ?)`,
		id, wsID, crewID, leadAgentID, traceID,
		req.Title, req.Description, issueNumber, identifier, req.Priority,
		req.AssigneeType, req.AssigneeID, req.DueDate, req.ProjectID,
		req.Estimate, req.ParentIssueID, req.MilestoneID,
		req.RoutineID, routineInputsJSON,
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
		// Surface the binding back so the client doesn't have to
		// re-fetch immediately. Slug+name come from the next Get
		// (handlers that JOIN pipelines); for the create path the
		// client typically already has the routine in its picker
		// list and can resolve them locally.
		RoutineID: req.RoutineID,
		CreatedAt: now,
		UpdatedAt: now,
		Labels:    []labelResponse{},
	}

	h.broadcastIssueEvent(wsID, "issue.created", map[string]string{"id": id})

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Get — GET /api/v1/crews/{crewId}/issues/{identifier} ────────────────
