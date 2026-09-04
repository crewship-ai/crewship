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
	// storagePath powers the F4.5 mission-outcomes-to-crew-memory hook
	// fired from the review-approve (→ DONE) and cancel (→ CANCELLED)
	// paths in issue_handler_workflow.go. Set via SetStoragePath after
	// construction; unset means the hook no-ops (status transition still
	// works fine).
	storagePath string
	// mentionDispatch wakes an agent named by an @mention in a comment.
	// Wired by the router (SetMentionDispatcher) to the AssignmentHandler, so
	// the mention inherits the delegation caps rather than getting its own.
	// nil = mentions are recorded and audited but dispatch nothing.
	mentionDispatch mentionDispatcher
}

// NewIssueHandler creates a new IssueHandler.
func NewIssueHandler(db *sql.DB, hub *ws.Hub, me MissionStarter, logger *slog.Logger) *IssueHandler {
	return &IssueHandler{db: db, hub: hub, missionEngine: me, logger: logger, journal: noopEmitter{}}
}

// rowQuerier is the minimal interface shared by *sql.DB and *sql.Tx that
// validateAssigneeWorkspace needs — it lets Create (which validates inside
// its transaction) and Update (which validates directly against h.db)
// share one implementation.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// validateAssigneeWorkspace confirms assignee_id belongs to wsID before a
// caller is allowed to persist it on an issue. Mirrors the existing
// parent_issue_id / routine_id workspace-scoping checks in
// issue_handler_create.go and issue_handler_update.go — same shape, same
// "does not exist in this workspace" framing — extended to assignee_id,
// which previously went straight into the INSERT/UPDATE unchecked.
//
// Users don't carry a workspace_id column; membership is resolved via
// workspace_members(workspace_id, user_id). Agents carry workspace_id
// directly. assignee_type must be exactly "user" or "agent" — anything
// else (including empty/unset) is treated as invalid so callers can't
// smuggle an assignee_id in under an unrecognized type.
func validateAssigneeWorkspace(ctx context.Context, q rowQuerier, assigneeType, assigneeID, wsID string) (bool, error) {
	var exists int
	var err error
	switch assigneeType {
	case "user":
		err = q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM workspace_members WHERE user_id = ? AND workspace_id = ?`,
			assigneeID, wsID).Scan(&exists)
	case "agent":
		err = q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM agents WHERE id = ? AND workspace_id = ?`,
			assigneeID, wsID).Scan(&exists)
	default:
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

// resolveAssigneeType determines assignee_type for an assignee_id when a
// PATCH sets assignee_id but leaves assignee_type unset — a client
// reassigning without changing the assignee's KIND (user→user, agent→agent)
// shouldn't have to resend the type on every request. It tries "user" then
// "agent" against wsID and returns the one that matches.
//
// This exists because three call sites (issue_handler_update.go,
// issue_handler_bulk.go, recurring_issue_handler.go's Update) used to fall
// back to the ROW'S CURRENT assignee_type instead of resolving it — safe
// when the reassignment keeps the same kind, but a false-reject when it
// doesn't: reassigning an issue currently held by a user to an agent in the
// SAME workspace, sending only assignee_id, looked "agent" up in
// workspace_members (the user table) under the stale "user" type, found no
// match, and rejected a perfectly valid same-workspace target with the
// misleading "assignee_id does not exist in this workspace". It fails safe
// (never a cross-workspace leak, just a wrong-table miss) but is still a
// false reject of a legitimate request.
//
// assignee_id spaces don't overlap between the two tables (CUIDs are
// effectively unique across both), so at most one of the two checks can
// match; returns ("", false, nil) — not an error — when assigneeID belongs
// to neither table in wsID, matching validateAssigneeWorkspace's "not found"
// signal so callers can report the same "does not exist" 400.
func resolveAssigneeType(ctx context.Context, q rowQuerier, assigneeID, wsID string) (assigneeType string, ok bool, err error) {
	if ok, err := validateAssigneeWorkspace(ctx, q, "user", assigneeID, wsID); err != nil {
		return "", false, err
	} else if ok {
		return "user", true, nil
	}
	if ok, err := validateAssigneeWorkspace(ctx, q, "agent", assigneeID, wsID); err != nil {
		return "", false, err
	} else if ok {
		return "agent", true, nil
	}
	return "", false, nil
}

// SetStoragePath wires the host storage root for the F4.5
// mission-outcomes-to-crew-memory hook. See MissionHandler.SetStoragePath
// for the same contract — handlers share the storage path because both
// emit lessons against the same /crews/{crew_id}/shared/.memory tree.
func (h *IssueHandler) SetStoragePath(p string) {
	h.storagePath = p
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

// events builds the shared issue-event emitter (issue_events.go) from the
// fields this handler already holds. Built per call rather than stored so a
// journal wired after construction via SetJournal is picked up.
func (h *IssueHandler) events() issueEvents {
	return issueEvents{db: h.db, hub: h.hub, logger: h.logger, journal: h.journal}
}

// SetMentionDispatcher wires the @mention trigger's dispatch door (#1768
// item 3). Optional, and nil is a supported configuration: every test that
// builds this handler directly leaves it unset, and a mention there is still
// resolved, persisted and audited — it simply wakes nobody. See
// mentionDispatcher's doc for why that is the right degradation.
func (h *IssueHandler) SetMentionDispatcher(d mentionDispatcher) {
	h.mentionDispatch = d
}

// mentionRecorder builds the shared mention write path from the fields this
// handler already holds. Per call, like events(), so a dispatcher wired after
// construction is picked up.
func (h *IssueHandler) mentionRecorder() mentionRecorder {
	return mentionRecorder{db: h.db, logger: h.logger, events: h.events(), dispatcher: h.mentionDispatch}
}

// logActivity is the compatibility shim for the call sites that still pass a
// bare action string (issue_handler_create.go, _workflow.go, _bulk.go). It
// records the mission_activity row and the journal entry via the shared
// emitter but does not broadcast — those call sites own their own broadcast.
// Best-effort: errors are logged, never returned.
func (h *IssueHandler) logActivity(ctx context.Context, missionID, actorType, actorID, action, details string) {
	h.events().log(ctx, issueEvent{
		MissionID: missionID,
		ActorType: actorType,
		ActorID:   actorID,
		Action:    issueAction(action),
		Details:   details,
	})
}

// broadcastIssueEvent sends a workspace-scoped WebSocket event.
// Delegates to the package-level helper; kept for call-site brevity.
func (h *IssueHandler) broadcastIssueEvent(wsID, eventType string, payload map[string]string) {
	broadcastWorkspaceEvent(h.hub, wsID, eventType, payload)
}

// ── Response types ──────────────────────────────────────────────────────────

type issueResponse struct {
	ID           string  `json:"id"`
	WorkspaceID  string  `json:"workspace_id"`
	CrewID       string  `json:"crew_id"`
	CrewName     string  `json:"crew_name,omitempty"`
	CrewSlug     string  `json:"crew_slug,omitempty"`
	Number       *int    `json:"number"`
	Identifier   *string `json:"identifier"`
	Title        string  `json:"title"`
	Description  *string `json:"description"`
	Status       string  `json:"status"`
	Priority     string  `json:"priority"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	AssigneeName *string `json:"assignee_name,omitempty"`
	// AssigneeSlug is set for an agent assignee — what the agent's page is
	// keyed on, so a client can link the assignee instead of naming it.
	AssigneeSlug   *string         `json:"assignee_slug,omitempty"`
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
	// Creator attribution (v129). CreatedBy identifies WHO created the
	// issue — a human (public API / slash command) or an agent (sidecar
	// tool call). AuthoredVia is the v108 channel enum
	// ('agent_tool_call', 'user_api', …). Both omitempty: legacy rows
	// predate the columns and carry no attribution.
	CreatedBy   *issueCreatorResponse `json:"created_by,omitempty"`
	AuthoredVia *string               `json:"authored_via,omitempty"`
	// CodeLinks is populated ONLY on the agent-facing internal read
	// (issues_internal.go). Browsers get the full, unfenced link objects
	// from GET …/issues/{identifier}/code-links instead; agents get this
	// summary, whose free text is run through the untrusted fence.
	CodeLinks []agentCodeLink `json:"code_links,omitempty"`
}

// issueCreatorResponse is the creator object on issue responses.
// Type discriminates the ID namespace: "agent" → agents.id,
// "user" → users.id. Name is resolved at read time (same pattern as
// assignee_name) so a renamed agent/user shows its current name.
type issueCreatorResponse struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// buildIssueCreator assembles the creator object from the raw columns.
// Agent attribution wins if both are somehow set (they never should be —
// the two create paths stamp exactly one).
func buildIssueCreator(authorAgentID, createdByUserID, creatorName sql.NullString) *issueCreatorResponse {
	switch {
	case authorAgentID.Valid && authorAgentID.String != "":
		return &issueCreatorResponse{Type: "agent", ID: authorAgentID.String, Name: creatorName.String}
	case createdByUserID.Valid && createdByUserID.String != "":
		return &issueCreatorResponse{Type: "user", ID: createdByUserID.String, Name: creatorName.String}
	default:
		return nil
	}
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
//
// The assignee-name subqueries below carry the exact same defense-in-depth
// requirement — and, pre-fix, didn't have it: they resolved full_name/name
// for m.assignee_id regardless of workspace, so a cross-workspace
// assignee_id (rejected at write time by Create/Update, but only there)
// would still leak the foreign user's or agent's display name to anyone
// reading the issue. Users don't carry a workspace_id column directly —
// membership goes through workspace_members(workspace_id, user_id) — so
// the user branch joins through that table; agents.workspace_id exists
// directly. Both are scoped to m.workspace_id via correlation, so no
// caller needs to pass the workspace in separately.
func issueSelectQuery() string {
	return `SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		m.number, m.identifier, m.title, m.description, m.status,
		COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		CASE
			WHEN m.assignee_type = 'user' THEN (
				SELECT u.full_name FROM users u
				JOIN workspace_members wm ON wm.user_id = u.id
				WHERE u.id = m.assignee_id AND wm.workspace_id = m.workspace_id)
			WHEN m.assignee_type = 'agent' THEN (
				SELECT name FROM agents WHERE id = m.assignee_id AND workspace_id = m.workspace_id)
		END,
		CASE
			WHEN m.assignee_type = 'agent' THEN (
				SELECT slug FROM agents WHERE id = m.assignee_id AND workspace_id = m.workspace_id)
		END,
		m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
		m.project_id, m.estimate, m.parent_issue_id, m.milestone_id,
		(SELECT COUNT(*) FROM missions sub WHERE sub.parent_issue_id = m.id) AS sub_issues_count,
		m.routine_id, p.slug, p.name,
		m.author_agent_id, m.created_by_user_id, m.authored_via,
		CASE
			WHEN m.author_agent_id IS NOT NULL THEN (SELECT name FROM agents WHERE id = m.author_agent_id)
			WHEN m.created_by_user_id IS NOT NULL THEN (SELECT full_name FROM users WHERE id = m.created_by_user_id)
		END AS creator_name
	FROM missions m
	LEFT JOIN crews c ON m.crew_id = c.id
	LEFT JOIN pipelines p ON m.routine_id = p.id AND p.workspace_id = m.workspace_id`
}

// scanIssueRow scans a row into an issueResponse.
func scanIssueRow(row interface{ Scan(...interface{}) error }) (issueResponse, error) {
	var issue issueResponse
	var authorAgentID, createdByUserID, authoredVia, creatorName sql.NullString
	err := row.Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName, &issue.AssigneeSlug,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
		&issue.ProjectID, &issue.Estimate, &issue.ParentIssueID, &issue.MilestoneID,
		&issue.SubIssuesCount,
		&issue.RoutineID, &issue.RoutineSlug, &issue.RoutineName,
		&authorAgentID, &createdByUserID, &authoredVia, &creatorName,
	)
	if err == nil {
		issue.Labels = []labelResponse{}
		issue.CreatedBy = buildIssueCreator(authorAgentID, createdByUserID, creatorName)
		if authoredVia.Valid && authoredVia.String != "" {
			issue.AuthoredVia = &authoredVia.String
		}
	}
	return issue, err
}
