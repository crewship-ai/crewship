package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// issueSpec is the full set of fields needed to create an issue (a mission row
// with mission_type='issue'). It is the single chokepoint that both the
// internal agent-tool path (InternalIssueHandler.Create) and the recurring-issue
// dispatcher create issues through, so per-crew numbering, identifier format,
// and LEAD-agent resolution can't drift between the two paths.
type issueSpec struct {
	WorkspaceID  string
	CrewID       string
	Title        string
	Description  *string
	Priority     string
	AssigneeType *string
	AssigneeID   *string
	ProjectID    *string
	MilestoneID  *string
	Labels       []string
	// AuthoredVia is the provenance channel: "agent_tool_call" (internal agent
	// path) or "recurring" (dispatcher). Both are allowed by the missions
	// authored_via CHECK (v108).
	AuthoredVia string
	// Optional provenance (agent path threads these; the dispatcher leaves them
	// empty since no chat/run originates a scheduled issue).
	AuthorAgentID string
	AuthorChatID  string
	AuthorRunID   string
}

// Sentinel errors so callers can map to their transport (HTTP status, log).
var (
	errIssueCrewNotFound = errors.New("crew not found")
	errIssueNoLeadAgent  = errors.New("crew has no LEAD agent")
)

// insertIssueTx creates an issue within tx: resolves the crew's issue prefix,
// allocates the atomic per-crew number, finds the LEAD agent, inserts the
// mission row, and links labels. Returns the new id and human identifier
// (e.g. "ENG-42"). The caller owns tx begin/commit and maps the sentinel
// errors. Priority defaults to "none" and AuthoredVia to "agent_tool_call" when
// empty.
func insertIssueTx(ctx context.Context, tx *sql.Tx, logger *slog.Logger, s issueSpec) (id, identifier string, err error) {
	priority := s.Priority
	if priority == "" {
		priority = "none"
	}
	authoredVia := s.AuthoredVia
	if authoredVia == "" {
		authoredVia = "agent_tool_call"
	}

	var issuePrefix sql.NullString
	var crewSlug string
	err = tx.QueryRowContext(ctx,
		`SELECT issue_prefix, slug FROM crews WHERE id = ? AND workspace_id = ?`,
		s.CrewID, s.WorkspaceID).Scan(&issuePrefix, &crewSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", errIssueCrewNotFound
		}
		return "", "", err
	}

	prefix := issuePrefix.String
	if prefix == "" {
		slug := strings.ToUpper(crewSlug)
		if len(slug) > 3 {
			slug = slug[:3]
		}
		prefix = slug
	}

	// Atomic per-crew counter.
	var number int
	err = tx.QueryRowContext(ctx,
		`INSERT INTO issue_counters(crew_id, next_number) VALUES(?, 1)
		 ON CONFLICT(crew_id) DO UPDATE SET next_number = issue_counters.next_number + 1
		 RETURNING next_number`, s.CrewID).Scan(&number)
	if err != nil {
		return "", "", err
	}
	identifier = prefix + "-" + strconv.Itoa(number)

	var leadAgentID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL LIMIT 1`,
		s.CrewID).Scan(&leadAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", errIssueNoLeadAgent
		}
		return "", "", err
	}

	id = generateCUID()
	traceID := "issue-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	nullable := func(str string) sql.NullString {
		return sql.NullString{String: str, Valid: str != ""}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id,
		                      title, description, status, number, identifier,
		                      priority, assignee_type, assignee_id, project_id, milestone_id,
		                      author_agent_id, author_chat_id, author_run_id, authored_via,
		                      sort_order, mission_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'issue', ?, ?)`,
		id, s.WorkspaceID, s.CrewID, leadAgentID, traceID,
		s.Title, s.Description, number, identifier,
		priority, s.AssigneeType, s.AssigneeID, s.ProjectID, s.MilestoneID,
		nullable(s.AuthorAgentID), nullable(s.AuthorChatID), nullable(s.AuthorRunID), authoredVia,
		now, now)
	if err != nil {
		return "", "", err
	}

	for _, labelID := range s.Labels {
		if _, lerr := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO mission_labels(mission_id, label_id) VALUES(?, ?)`,
			id, labelID); lerr != nil && logger != nil {
			logger.Error("insert issue label", "issue_id", id, "error", lerr)
		}
	}

	return id, identifier, nil
}
