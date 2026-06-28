package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// TriageHandler implements CRUD for triage rules and the process endpoint.
type TriageHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewTriageHandler creates a new TriageHandler.
func NewTriageHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *TriageHandler {
	return &TriageHandler{db: db, hub: hub, logger: logger}
}

// ── Response type ──────────────────────────────────────────────────────────

type triageRuleResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Pattern    string  `json:"pattern"`
	MatchType  string  `json:"match_type"`
	CrewID     *string `json:"crew_id"`
	AssigneeID *string `json:"assignee_id"`
	Priority   *string `json:"priority"`
	ProjectID  *string `json:"project_id"`
	LabelsJSON *string `json:"labels_json"`
	Position   int     `json:"position"`
	Enabled    bool    `json:"enabled"`
	MatchCount int     `json:"match_count"`
	CreatedAt  string  `json:"created_at"`
}

// ── 1. ListRules — GET /api/v1/triage-rules ────────────────────────────────

// ListRules returns all triage rules for the workspace.
// GET /api/v1/triage/rules
func (h *TriageHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, pattern, match_type, crew_id, assignee_id, priority,
		       project_id, labels_json, position, enabled, match_count, created_at
		FROM triage_rules
		WHERE workspace_id = ?
		ORDER BY position ASC, created_at ASC`, wsID)
	if err != nil {
		internalError(w, r, h.logger, "list triage rules", err)
		return
	}
	defer rows.Close()

	var result []triageRuleResponse
	for rows.Next() {
		var tr triageRuleResponse
		if err := rows.Scan(
			&tr.ID, &tr.Name, &tr.Pattern, &tr.MatchType, &tr.CrewID,
			&tr.AssigneeID, &tr.Priority, &tr.ProjectID, &tr.LabelsJSON,
			&tr.Position, &tr.Enabled, &tr.MatchCount, &tr.CreatedAt,
		); err != nil {
			internalError(w, r, h.logger, "scan triage rule", err)
			return
		}
		result = append(result, tr)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (triage rules)", err)
		return
	}

	if result == nil {
		result = []triageRuleResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. CreateRule — POST /api/v1/triage-rules ──────────────────────────────

// CreateRule creates a new triage rule with pattern matching and action configuration.
// POST /api/v1/triage/rules
func (h *TriageHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name       string  `json:"name"`
		Pattern    string  `json:"pattern"`
		MatchType  string  `json:"match_type"`
		CrewID     *string `json:"crew_id"`
		AssigneeID *string `json:"assignee_id"`
		Priority   *string `json:"priority"`
		ProjectID  *string `json:"project_id"`
		LabelsJSON *string `json:"labels_json"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}
	if req.Pattern == "" {
		writeProblem(w, r, http.StatusBadRequest, "pattern is required")
		return
	}
	validMatchTypes := map[string]bool{"contains": true, "regex": true, "exact": true}
	if !validMatchTypes[req.MatchType] {
		writeProblem(w, r, http.StatusBadRequest, "match_type must be: contains, regex, or exact")
		return
	}

	// Validate regex patterns at creation time
	if req.MatchType == "regex" {
		if _, err := regexp.Compile(req.Pattern); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "Invalid regex pattern: "+err.Error())
			return
		}
	}

	// Get next position
	var maxPos int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(position), 0) FROM triage_rules WHERE workspace_id = ?`,
		wsID).Scan(&maxPos); err != nil {
		h.logger.Error("get max triage rule position", "error", err)
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO triage_rules (id, workspace_id, name, pattern, match_type,
		    crew_id, assignee_id, priority, project_id, labels_json,
		    position, enabled, match_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		id, wsID, req.Name, req.Pattern, req.MatchType,
		req.CrewID, req.AssigneeID, req.Priority, req.ProjectID, req.LabelsJSON,
		maxPos+1, now)
	if err != nil {
		internalError(w, r, h.logger, "insert triage rule", err)
		return
	}

	resp := triageRuleResponse{
		ID:         id,
		Name:       req.Name,
		Pattern:    req.Pattern,
		MatchType:  req.MatchType,
		CrewID:     req.CrewID,
		AssigneeID: req.AssigneeID,
		Priority:   req.Priority,
		ProjectID:  req.ProjectID,
		LabelsJSON: req.LabelsJSON,
		Position:   maxPos + 1,
		Enabled:    true,
		MatchCount: 0,
		CreatedAt:  now,
	}

	broadcastWorkspaceEvent(h.hub, wsID, "triage_rule.created", map[string]string{"id": id})

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. UpdateRule — PATCH /api/v1/triage-rules/{id} ────────────────────────

// UpdateRule modifies an existing triage rule.
// PATCH /api/v1/triage-rules/{ruleId}
func (h *TriageHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	ruleID := r.PathValue("ruleId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify rule exists
	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM triage_rules WHERE id = ? AND workspace_id = ?`,
		ruleID, wsID).Scan(&existingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Triage rule not found")
			return
		}
		internalError(w, r, h.logger, "get triage rule for update", err)
		return
	}

	var req struct {
		Name       *string `json:"name"`
		Pattern    *string `json:"pattern"`
		MatchType  *string `json:"match_type"`
		CrewID     *string `json:"crew_id"`
		AssigneeID *string `json:"assignee_id"`
		Priority   *string `json:"priority"`
		ProjectID  *string `json:"project_id"`
		LabelsJSON *string `json:"labels_json"`
		Position   *int    `json:"position"`
		Enabled    *bool   `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()

	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Pattern != nil {
		ub.Set("pattern", *req.Pattern)
	}
	if req.MatchType != nil {
		validMatchTypes := map[string]bool{"contains": true, "regex": true, "exact": true}
		if !validMatchTypes[*req.MatchType] {
			writeProblem(w, r, http.StatusBadRequest, "match_type must be: contains, regex, or exact")
			return
		}
		ub.Set("match_type", *req.MatchType)
	}

	// Validate regex if pattern or match_type changed
	if req.Pattern != nil && req.MatchType != nil && *req.MatchType == "regex" {
		if _, err := regexp.Compile(*req.Pattern); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "Invalid regex pattern: "+err.Error())
			return
		}
	}

	if req.CrewID != nil {
		if *req.CrewID == "" {
			ub.SetNull("crew_id")
		} else {
			ub.Set("crew_id", *req.CrewID)
		}
	}
	if req.AssigneeID != nil {
		if *req.AssigneeID == "" {
			ub.SetNull("assignee_id")
		} else {
			ub.Set("assignee_id", *req.AssigneeID)
		}
	}
	if req.Priority != nil {
		ub.Set("priority", *req.Priority)
	}
	if req.ProjectID != nil {
		if *req.ProjectID == "" {
			ub.SetNull("project_id")
		} else {
			ub.Set("project_id", *req.ProjectID)
		}
	}
	if req.LabelsJSON != nil {
		ub.Set("labels_json", *req.LabelsJSON)
	}
	if req.Position != nil {
		ub.Set("position", *req.Position)
	}
	if req.Enabled != nil {
		ub.Set("enabled", *req.Enabled)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("triage_rules", "id = ? AND workspace_id = ?", ruleID, wsID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		internalError(w, r, h.logger, "update triage rule", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "triage_rule.updated", map[string]string{"id": ruleID})

	// Return updated rule
	var tr triageRuleResponse
	err = h.db.QueryRowContext(r.Context(), `
		SELECT id, name, pattern, match_type, crew_id, assignee_id, priority,
		       project_id, labels_json, position, enabled, match_count, created_at
		FROM triage_rules
		WHERE id = ?`, ruleID).Scan(
		&tr.ID, &tr.Name, &tr.Pattern, &tr.MatchType, &tr.CrewID,
		&tr.AssigneeID, &tr.Priority, &tr.ProjectID, &tr.LabelsJSON,
		&tr.Position, &tr.Enabled, &tr.MatchCount, &tr.CreatedAt,
	)
	if err != nil {
		internalError(w, r, h.logger, "read updated triage rule", err)
		return
	}

	writeJSON(w, http.StatusOK, tr)
}

// ── 4. DeleteRule — DELETE /api/v1/triage-rules/{id} ───────────────────────

// DeleteRule removes a triage rule.
// DELETE /api/v1/triage-rules/{ruleId}
func (h *TriageHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	ruleID := r.PathValue("ruleId")
	wsID := WorkspaceIDFromContext(r.Context())

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM triage_rules WHERE id = ? AND workspace_id = ?`, ruleID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete triage rule", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "delete triage rule rows affected", err)
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Triage rule not found")
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "triage_rule.deleted", map[string]string{"id": ruleID})

	w.WriteHeader(http.StatusNoContent)
}

// ── 5. Process — POST /api/v1/triage/process ───────────────────────────────

// Process evaluates triage rules against an issue and applies the matching actions.
// POST /api/v1/triage/process
func (h *TriageHandler) Process(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	// 1. Load all enabled rules ordered by position
	ruleRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, pattern, match_type, crew_id, assignee_id, priority, project_id, labels_json
		FROM triage_rules
		WHERE workspace_id = ? AND enabled = 1
		ORDER BY position ASC`, wsID)
	if err != nil {
		internalError(w, r, h.logger, "triage: load rules", err)
		return
	}
	defer ruleRows.Close()

	type triageRule struct {
		ID         string
		Pattern    string
		MatchType  string
		CrewID     *string
		AssigneeID *string
		Priority   *string
		ProjectID  *string
		LabelsJSON *string
		compiledRe *regexp.Regexp // pre-compiled for "regex" match type
	}

	var rules []triageRule
	for ruleRows.Next() {
		var tr triageRule
		if err := ruleRows.Scan(&tr.ID, &tr.Pattern, &tr.MatchType,
			&tr.CrewID, &tr.AssigneeID, &tr.Priority, &tr.ProjectID, &tr.LabelsJSON); err != nil {
			h.logger.Error("triage: scan rule", "error", err)
			continue
		}
		if tr.MatchType == "regex" {
			re, err := regexp.Compile(tr.Pattern)
			if err != nil {
				h.logger.Warn("triage: invalid regex pattern, skipping rule", "rule_id", tr.ID, "pattern", tr.Pattern)
				continue
			}
			tr.compiledRe = re
		}
		rules = append(rules, tr)
	}
	if err := ruleRows.Err(); err != nil {
		internalError(w, r, h.logger, "triage: rules iteration", err)
		return
	}

	if len(rules) == 0 {
		writeJSON(w, http.StatusOK, map[string]int{"processed": 0, "matched": 0})
		return
	}

	// 2. Load all BACKLOG issues where assignee_id IS NULL
	issueRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, title FROM missions
		WHERE workspace_id = ? AND status = 'BACKLOG' AND assignee_id IS NULL
		  AND COALESCE(mission_type, 'mission') = 'issue'`, wsID)
	if err != nil {
		internalError(w, r, h.logger, "triage: load issues", err)
		return
	}
	defer issueRows.Close()

	type issueRow struct {
		ID    string
		Title string
	}

	var issues []issueRow
	for issueRows.Next() {
		var iss issueRow
		if err := issueRows.Scan(&iss.ID, &iss.Title); err != nil {
			h.logger.Error("triage: scan issue", "error", err)
			continue
		}
		issues = append(issues, iss)
	}
	if err := issueRows.Err(); err != nil {
		internalError(w, r, h.logger, "triage: issues iteration", err)
		return
	}

	// 3. Match each issue against rules
	processed := len(issues)
	matched := 0
	matchedRules := make(map[string]int) // rule ID -> match increment

	for _, iss := range issues {
		for _, rule := range rules {
			if !triageMatchCompiled(rule.MatchType, rule.Pattern, iss.Title, rule.compiledRe) {
				continue
			}

			// 4. Apply rule updates to issue
			ub := newUpdate()
			if rule.CrewID != nil {
				ub.Set("crew_id", *rule.CrewID)
			}
			if rule.AssigneeID != nil {
				ub.Set("assignee_id", *rule.AssigneeID)
				ub.Set("assignee_type", "agent")
			}
			if rule.Priority != nil {
				ub.Set("priority", *rule.Priority)
			}
			if rule.ProjectID != nil {
				ub.Set("project_id", *rule.ProjectID)
			}

			query, args := ub.Build("missions", "id = ?", iss.ID)
			if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
				h.logger.Error("triage: update issue", "error", err, "issue_id", iss.ID, "rule_id", rule.ID)
				continue
			}

			matchedRules[rule.ID]++
			matched++
			break // first matching rule wins
		}
	}

	// 5. Increment match_count for matched rules
	for ruleID, count := range matchedRules {
		if _, err := h.db.ExecContext(r.Context(),
			`UPDATE triage_rules SET match_count = match_count + ? WHERE id = ?`,
			count, ruleID); err != nil {
			h.logger.Error("update triage rule match_count", "rule_id", ruleID, "error", err)
		}
	}

	if matched > 0 {
		broadcastWorkspaceEvent(h.hub, wsID, "triage.processed",
			map[string]string{
				"processed": strconv.Itoa(processed),
				"matched":   strconv.Itoa(matched),
			})
	}

	writeJSON(w, http.StatusOK, map[string]int{"processed": processed, "matched": matched})
}

// triageMatchCompiled checks if a title matches a rule pattern based on match type.
// For "regex" type, uses the pre-compiled *regexp.Regexp to avoid recompilation per call.
func triageMatchCompiled(matchType, pattern, title string, compiledRe *regexp.Regexp) bool {
	switch matchType {
	case "contains":
		return strings.Contains(strings.ToLower(title), strings.ToLower(pattern))
	case "regex":
		if compiledRe != nil {
			return compiledRe.MatchString(title)
		}
		return false
	case "exact":
		return title == pattern
	default:
		return false
	}
}
