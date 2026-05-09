package api

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/statuses"
	"github.com/crewship-ai/crewship/internal/ws"
)

// MissionStarter starts a mission that has been inserted in PLANNING/IN_PROGRESS
// state and approves task completions. *orchestrator.MissionEngine satisfies it.
type MissionStarter interface {
	StartMission(ctx context.Context, missionID string) error
	ApproveTask(ctx context.Context, taskID, userID string, approved bool, notes string) error
}

// IssueHandler implements endpoints for the issue tracker (Linear-like).
type IssueHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine MissionStarter
	logger        *slog.Logger
	journal       journal.Emitter
}

// NewIssueHandler creates a new IssueHandler.
func NewIssueHandler(db *sql.DB, hub *ws.Hub, me MissionStarter, logger *slog.Logger) *IssueHandler {
	return &IssueHandler{db: db, hub: hub, missionEngine: me, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires a journal emitter after construction so the router can
// pass its shared emitter in without breaking existing test call sites.
func (h *IssueHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// logActivity inserts a row into mission_activity AND emits a journal entry.
// Errors are logged but not returned — activity logging is best-effort and
// should not fail the caller. The journal emit is fire-and-forget via the
// batched writer so the caller never waits on a DB round-trip here.
func (h *IssueHandler) logActivity(ctx context.Context, missionID, actorType, actorID, action, details string) {
	actID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actID, missionID, actorType, actorID, action, details, now); err != nil {
		h.logger.Error("insert mission activity", "action", action, "mission_id", missionID, "error", err)
	}

	// The mission row carries workspace_id; we grab it here rather than
	// threading it through every logActivity caller. One extra light
	// query per activity is cheap compared with the benefit of a
	// single-argument signature at every call site.
	var workspaceID, crewID string
	_ = h.db.QueryRowContext(ctx, `SELECT workspace_id, crew_id FROM missions WHERE id = ?`, missionID).
		Scan(&workspaceID, &crewID)
	if workspaceID == "" {
		// Mission may not exist yet (some callers pass chatID as missionID
		// legacy). Journal write needs workspace_id, so skip silently.
		return
	}
	actor := journal.ActorType(actorType)
	if actor != journal.ActorAgent && actor != journal.ActorUser && actor != journal.ActorSystem {
		actor = journal.ActorSystem
	}
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		MissionID:   missionID,
		Type:        journal.EntryMissionStatus,
		Severity:    journal.SeverityInfo,
		ActorType:   actor,
		ActorID:     actorID,
		Summary:     action + ": " + truncate(details, 120),
		Payload:     map[string]any{"action": action, "details": details},
		Refs:        map[string]any{"mission_id": missionID, "activity_id": actID},
	})
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
	// Routine binding — when set, /run-routine on this issue invokes
	// the bound pipeline. RoutineSlug is denormalized in the response
	// so the UI doesn't have to round-trip the pipelines list to
	// label the chip ("Run with: triage-classifier"). Both omitempty
	// so unbound issues don't carry empty fields.
	RoutineID   *string `json:"routine_id,omitempty"`
	RoutineSlug *string `json:"routine_slug,omitempty"`
	RoutineName *string `json:"routine_name,omitempty"`
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

// validIssueTransitions references the canonical transition map from the
// statuses package so there is a single source of truth.
var validIssueTransitions = statuses.ValidIssueTransitions

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

// broadcastIssueEvent and logActivity are defined earlier in this file (the
// merge pulled in main's modernized versions that delegate to the package-level
// broadcastWorkspaceEvent helper). The duplicates that lived here in the
// pre-merge feat/code-quality version were removed to avoid redeclaration.

// validateStatusTransition checks if a status transition is allowed.
func (h *IssueHandler) validateStatusTransition(currentStatus, newStatus string) bool {
	allowed := validIssueTransitions[currentStatus]
	for _, s := range allowed {
		if s == newStatus {
			return true
		}
	}
	return false
}

// addIssueComment inserts a comment on an issue (used by best-effort flows
// like auto-posted review notes; distinct from the public CreateComment
// handler in issue_handler_comments.go).
func (h *IssueHandler) addIssueComment(ctx context.Context, missionID, authorType, authorID, body string) {
	commentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = h.db.ExecContext(ctx,
		`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		commentID, missionID, authorType, authorID, body, now, now)
}

// issueSelectQuery returns the base SELECT query for fetching issues.
// LEFT JOINs into pipelines on routine_id so the response can include
// routine slug + name without a second round-trip — the Issue UI
// renders a "Run with: <slug>" chip and would otherwise have to map
// every routine_id against a prefetched pipeline list.
//
// The pipelines join is workspace-scoped: a stale or cross-tenant
// routine_id would otherwise leak the foreign workspace's slug+name
// into the response. The handler-level validation already prevents
// cross-workspace IDs from being persisted, but defense-in-depth here
// keeps the surface safe even if a row sneaks through (manual SQL,
// imported backup, etc.).
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
		(SELECT COUNT(*) FROM missions sub WHERE sub.parent_issue_id = m.id) AS sub_issues_count,
		m.routine_id, p.slug, p.name
	FROM missions m
	LEFT JOIN crews c ON m.crew_id = c.id
	LEFT JOIN pipelines p ON m.routine_id = p.id AND p.workspace_id = m.workspace_id`
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
		&issue.RoutineID, &issue.RoutineSlug, &issue.RoutineName,
	)
	if err == nil {
		issue.Labels = []labelResponse{}
	}
	return issue, err
}
