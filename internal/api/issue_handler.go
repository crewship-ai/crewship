package api

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// IssueHandler implements endpoints for the issue tracker (Linear-like).
// Uses MissionStarter interface defined in captain.go.
type IssueHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine MissionStarter
	logger        *slog.Logger
}

// NewIssueHandler creates a new IssueHandler.
func NewIssueHandler(db *sql.DB, hub *ws.Hub, me MissionStarter, logger *slog.Logger) *IssueHandler {
	return &IssueHandler{db: db, hub: hub, missionEngine: me, logger: logger}
}

// issueSelectColumns is the shared SELECT clause for fetching issues/missions.
const issueSelectColumns = `
	SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
	       m.number, m.identifier, m.title, m.description, m.status,
	       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
	       COALESCE(u.full_name, ag.name),
	       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
	       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
	       m.project_id, m.estimate, m.parent_issue_id, m.milestone_id,
	       COALESCE(sub_cnt.cnt, 0)
	FROM missions m
	LEFT JOIN crews c ON m.crew_id = c.id
	LEFT JOIN users u ON m.assignee_type = 'user' AND u.id = m.assignee_id
	LEFT JOIN agents ag ON m.assignee_type = 'agent' AND ag.id = m.assignee_id
	LEFT JOIN (SELECT parent_issue_id, COUNT(*) AS cnt FROM missions WHERE parent_issue_id IS NOT NULL GROUP BY parent_issue_id) sub_cnt ON sub_cnt.parent_issue_id = m.id`

// scanIssue scans a row into an issueResponse using the standard column order.
func scanIssue(s interface{ Scan(...interface{}) error }) (issueResponse, error) {
	var issue issueResponse
	err := s.Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
		&issue.ProjectID, &issue.Estimate, &issue.ParentIssueID, &issue.MilestoneID,
		&issue.SubIssuesCount,
	)
	if err == nil {
		issue.Labels = []labelResponse{}
	}
	return issue, err
}

// logActivity inserts a row into mission_activity. Errors are logged but not
// returned — activity logging is best-effort and should not fail the caller.
func (h *IssueHandler) logActivity(ctx context.Context, missionID, actorType, actorID, action, details string) {
	actID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actID, missionID, actorType, actorID, action, details, now); err != nil {
		h.logger.Error("insert mission activity", "action", action, "mission_id", missionID, "error", err)
	}
}

// setIssueLabels replaces the label associations for a mission by deleting
// existing rows and inserting new ones. Errors are logged but not returned.
func (h *IssueHandler) setIssueLabels(ctx context.Context, missionID string, labelIDs []string) {
	if _, err := h.db.ExecContext(ctx,
		`DELETE FROM mission_labels WHERE mission_id = ?`, missionID); err != nil {
		h.logger.Error("delete mission labels", "mission_id", missionID, "error", err)
		return
	}
	for _, labelID := range labelIDs {
		if _, err := h.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO mission_labels (mission_id, label_id) VALUES (?, ?)`,
			missionID, labelID); err != nil {
			h.logger.Error("insert mission label", "mission_id", missionID, "error", err)
		}
	}
}

// insertComment inserts a row into mission_comments. Errors are logged but not
// returned — this is used by best-effort comment flows (e.g. review notes).
func (h *IssueHandler) insertComment(ctx context.Context, missionID, authorType, authorID, body string) {
	commentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		commentID, missionID, authorType, authorID, body, now, now); err != nil {
		h.logger.Error("insert mission comment", "mission_id", missionID, "error", err)
	}
}

// broadcastIssueEvent sends a workspace-scoped WebSocket event.
// Delegates to the package-level helper; kept for call-site brevity.
func (h *IssueHandler) broadcastIssueEvent(wsID, eventType string, payload map[string]string) {
	broadcastWorkspaceEvent(h.hub, wsID, eventType, payload)
}

// ── Response types ──────────────────────────────────────────────────────────

type issueResponse struct {
	ID             string          `json:"id"`
	WorkspaceID    string          `json:"workspace_id"`
	CrewID         string          `json:"crew_id"`
	CrewName       string          `json:"crew_name,omitempty"`
	CrewSlug       string          `json:"crew_slug,omitempty"`
	Number         *int            `json:"number"`
	Identifier     *string         `json:"identifier"`
	Title          string          `json:"title"`
	Description    *string         `json:"description"`
	Status         string          `json:"status"`
	Priority       string          `json:"priority"`
	AssigneeType   *string         `json:"assignee_type"`
	AssigneeID     *string         `json:"assignee_id"`
	AssigneeName   *string         `json:"assignee_name,omitempty"`
	DueDate        *string         `json:"due_date"`
	SortOrder      float64         `json:"sort_order"`
	MissionType    string          `json:"mission_type"`
	LeadAgentID    string          `json:"lead_agent_id"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
	CompletedAt    *string         `json:"completed_at"`
	Labels         []labelResponse `json:"labels"`
	ProjectID      *string         `json:"project_id"`
	ProjectName    *string         `json:"project_name,omitempty"`
	Estimate       *int            `json:"estimate"`
	ParentIssueID  *string         `json:"parent_issue_id"`
	MilestoneID    *string         `json:"milestone_id"`
	SubIssuesCount int             `json:"sub_issues_count"`
	CommentCount   int             `json:"comment_count"`
}

type labelResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Color      string  `json:"color"`
	LabelGroup *string `json:"label_group"`
}

type relationResponse struct {
	ID           string `json:"id"`
	SourceID     string `json:"source_id"`
	TargetID     string `json:"target_id"`
	RelationType string `json:"relation_type"`
	// Resolved target info
	TargetIdentifier *string `json:"target_identifier,omitempty"`
	TargetTitle      string  `json:"target_title,omitempty"`
	TargetStatus     string  `json:"target_status,omitempty"`
	CreatedAt        string  `json:"created_at"`
}

type commentResponse struct {
	ID         string `json:"id"`
	MissionID  string `json:"mission_id"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
	AuthorName string `json:"author_name,omitempty"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type activityResponse struct {
	ID        string  `json:"id"`
	MissionID string  `json:"mission_id"`
	ActorType string  `json:"actor_type"`
	ActorID   string  `json:"actor_id"`
	ActorName *string `json:"actor_name,omitempty"`
	Action    string  `json:"action"`
	Details   *string `json:"details"`
	CreatedAt string  `json:"created_at"`
}

// validIssueTransitions defines allowed status transitions for issues.
// Issue statuses are a superset of the existing mission statuses.
var validIssueTransitions = map[string][]string{
	"BACKLOG":     {"TODO", "IN_PROGRESS", "CANCELLED"},
	"TODO":        {"IN_PROGRESS", "BACKLOG", "CANCELLED"},
	"IN_PROGRESS": {"REVIEW", "DONE", "FAILED", "CANCELLED", "TODO"},
	"REVIEW":      {"DONE", "TODO", "IN_PROGRESS", "FAILED", "CANCELLED"},
	"DONE":        {"BACKLOG"},
	"FAILED":      {"BACKLOG", "TODO", "IN_PROGRESS"},
	"CANCELLED":   {"BACKLOG", "TODO"},
	"DUPLICATE":   {},
}

// ── Helper methods ──────────────────────────────────────────────────────────

// resolveMissionID looks up a mission ID by identifier, crew, and workspace.
func (h *IssueHandler) resolveMissionID(ctx context.Context, identifier, crewID, wsID string) (string, error) {
	var id string
	err := h.db.QueryRowContext(ctx,
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		identifier, crewID, wsID).Scan(&id)
	return id, err
}

// loadIssueLabels loads labels for a single issue.
func (h *IssueHandler) loadIssueLabels(ctx context.Context, missionID string) []labelResponse {
	rows, err := h.db.QueryContext(ctx, `
		SELECT l.id, l.name, l.color, l.label_group
		FROM mission_labels ml JOIN labels l ON ml.label_id = l.id
		WHERE ml.mission_id = ?`, missionID)
	if err != nil {
		return []labelResponse{}
	}
	defer rows.Close()
	var labels []labelResponse
	for rows.Next() {
		var lbl labelResponse
		if err := rows.Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup); err != nil {
			continue
		}
		labels = append(labels, lbl)
	}
	if labels == nil {
		return []labelResponse{}
	}
	return labels
}

// validateStatusTransition checks if a status transition is allowed.
// Unique HEAD contribution — consumed by issue_handler_crud.go and
// issue_handler_bulk.go splits. broadcastIssueEvent/logActivity duplicates
// were removed in favor of the canonical versions near the top of this file.
func (h *IssueHandler) validateStatusTransition(currentStatus, newStatus string) bool {
	allowed := validIssueTransitions[currentStatus]
	for _, s := range allowed {
		if s == newStatus {
			return true
		}
	}
	return false
}

// addIssueComment inserts a comment on an issue.
// Unique HEAD contribution — consumed by issue_handler_workflow.go split.
func (h *IssueHandler) addIssueComment(ctx context.Context, missionID, authorType, authorID, body string) {
	commentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = h.db.ExecContext(ctx,
		`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		commentID, missionID, authorType, authorID, body, now, now)
}

// issueSelectQuery returns the base SELECT query for fetching issues.
func issueSelectQuery() string {
	return `SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		m.number, m.identifier, m.title, m.description, m.status,
		COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		CASE
			WHEN m.assignee_type = 'user' THEN (SELECT full_name FROM users WHERE id = m.assignee_id)
			WHEN m.assignee_type = 'agent' THEN (SELECT name FROM agents WHERE id = m.assignee_id)
		END,
		m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
		m.project_id, m.estimate, m.parent_issue_id, m.milestone_id,
		(SELECT COUNT(*) FROM missions sub WHERE sub.parent_issue_id = m.id) AS sub_issues_count
	FROM missions m
	LEFT JOIN crews c ON m.crew_id = c.id`
}

// scanIssueRow scans a row into an issueResponse.
func scanIssueRow(row interface{ Scan(...interface{}) error }) (issueResponse, error) {
	var issue issueResponse
	err := row.Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
		&issue.ProjectID, &issue.Estimate, &issue.ParentIssueID, &issue.MilestoneID,
		&issue.SubIssuesCount,
	)
	if err == nil {
		issue.Labels = []labelResponse{}
	}
	return issue, err
}
