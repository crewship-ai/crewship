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
// dispatcher create issues through, so numbering, identifier format, and
// LEAD-agent resolution can't drift between the two paths.
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
	// CreatedByUserID is the human that created this row (v129 attribution).
	// The recurring dispatcher threads the template's created_by so a fired
	// issue is attributed to whoever set up the schedule; empty on the agent
	// path (that path stamps AuthorAgentID instead). Optional — stored NULL
	// when empty so both-NULL legacy rows keep omitting the creator.
	CreatedByUserID string
}

// Sentinel errors so callers can map to their transport (HTTP status, log).
var (
	errIssueCrewNotFound = errors.New("crew not found")
	errIssueNoLeadAgent  = errors.New("crew has no LEAD agent")
	// errIssueAssigneeTypeInvalid / errIssueAssigneeNotInWorkspace mirror the
	// wording issue_handler_create.go / issue_handler_update.go already use for
	// the same check, so a caller's err.Error() reads identically to the
	// user-facing HTTP surface regardless of which write path it came from.
	errIssueAssigneeTypeInvalid    = errors.New("assignee_type must be 'user' or 'agent' when assignee_id is set")
	errIssueAssigneeNotInWorkspace = errors.New("assignee_id does not exist in this workspace")
)

// crewIssuePrefix is the one definition of a crew's effective issue prefix:
// crews.issue_prefix when it is set, otherwise the first three characters of
// the slug upper-cased. Both write paths and the migration that re-keyed
// issue_counters (20260820125000_issue_counters_prefix_scope.sql, which derives
// the same value with UPPER(SUBSTR(slug, 1, 3))) depend on these lines agreeing.
//
// The byte slice and SQL's character-counting SUBSTR agree because a crew slug
// is validated against `^[a-z0-9][a-z0-9_-]*$` (validSlugFormat) — ASCII only,
// so a byte is a character here.
func crewIssuePrefix(issuePrefix, crewSlug string) string {
	if issuePrefix != "" {
		return issuePrefix
	}
	slug := strings.ToUpper(crewSlug)
	if len(slug) > 3 {
		slug = slug[:3]
	}
	return slug
}

// nextIssueIdentifierTx resolves the crew's effective prefix and allocates the
// next number in that prefix's sequence, returning the human identifier (e.g.
// "ENG-42") and the raw number that goes in missions.number. It is the single
// generator: both the REST create (IssueHandler.Create) and insertIssueTx — the
// agent-tool and recurring-issue path — call it. There were two hand-copied
// copies of it before #1797, spelling the same slug truncation two different
// ways (`len >= 3` slicing against `len > 3` slicing), which is what two copies
// of anything eventually do.
//
// The sequence is keyed on (workspace_id, prefix), NOT on the crew. Identifiers
// are unique per workspace (missions carries UNIQUE(workspace_id, identifier)
// since #1733), so a per-crew counter let two crews whose effective prefix
// collided — `engineering` and `engine` both derive ENG without either ever
// setting issue_prefix — each mint ENG-1. The loser's insert was rejected by
// that index, and because the counter upsert shares the caller's transaction
// with the mission insert, the rejection rolled the increment back too: the
// crew retried the same identifier on every subsequent create and could never
// file an issue again. Keying the counter on the namespace it feeds makes the
// collision impossible rather than merely rarer — two crews sharing a prefix
// share one sequence and interleave.
//
// The allocation is two statements rather than one upsert, and deliberately so.
// The common path is the UPDATE: one indexed write, no scan. Only the FIRST
// allocation for a (workspace, prefix) pair falls through to the seeding INSERT,
// which starts the sequence above the highest number already minted under that
// prefix instead of at 1. That matters because issue_prefix is mutable: a crew
// that changes its prefix strands its old counter row and opens a new one, and
// a new row starting at 1 would hand out identifiers the crew's own history
// already holds. The ON CONFLICT on the seeding INSERT covers the case where
// another writer created the row between the two statements.
func nextIssueIdentifierTx(ctx context.Context, tx *sql.Tx, workspaceID, crewID string) (string, int, error) {
	var issuePrefix sql.NullString
	var crewSlug string
	err := tx.QueryRowContext(ctx,
		`SELECT issue_prefix, slug FROM crews WHERE id = ? AND workspace_id = ?`,
		crewID, workspaceID).Scan(&issuePrefix, &crewSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, errIssueCrewNotFound
		}
		return "", 0, err
	}
	prefix := crewIssuePrefix(issuePrefix.String, crewSlug)

	var number int
	err = tx.QueryRowContext(ctx,
		`UPDATE issue_counters SET next_number = next_number + 1
		 WHERE workspace_id = ? AND prefix = ?
		 RETURNING next_number`, workspaceID, prefix).Scan(&number)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		// First use of this prefix in this workspace. Seed above whatever this
		// prefix already minted here — see the note on prefix changes above.
		//
		// The high-water mark is read out of the identifier TEXT rather than out
		// of missions.number, because the question being asked is literally
		// "does <prefix>-<n> already exist": that is what the unique index
		// checks, and it is the only thing that can reject the insert. The match
		// is an exact SUBSTR compare rather than a LIKE, since issue_prefix is
		// free text and a prefix containing % or _ would turn a LIKE pattern
		// into a wildcard over other crews' identifiers. The GLOB discards a
		// tail that is not all digits, so "ENG-1-hotfix" cannot be read as 1.
		err = tx.QueryRowContext(ctx, `
			INSERT INTO issue_counters (workspace_id, prefix, next_number)
			VALUES (?, ?, COALESCE((
			        SELECT MAX(CAST(SUBSTR(m.identifier, LENGTH(?) + 2) AS INTEGER))
			        FROM missions m
			        WHERE m.workspace_id = ?
			          AND m.identifier IS NOT NULL
			          AND SUBSTR(m.identifier, 1, LENGTH(?) + 1) = ? || '-'
			          AND SUBSTR(m.identifier, LENGTH(?) + 2) <> ''
			          AND SUBSTR(m.identifier, LENGTH(?) + 2) NOT GLOB '*[^0-9]*'
			    ), 0) + 1)
			ON CONFLICT(workspace_id, prefix)
			    DO UPDATE SET next_number = issue_counters.next_number + 1
			RETURNING next_number`,
			workspaceID, prefix, prefix, workspaceID, prefix, prefix, prefix, prefix).Scan(&number)
		if err != nil {
			return "", 0, err
		}
	}

	return prefix + "-" + strconv.Itoa(number), number, nil
}

// insertIssueTx creates an issue within tx: resolves the crew's issue prefix,
// allocates the next number in that prefix's sequence, finds the LEAD agent,
// validates the assignee against the workspace, inserts the mission row, and
// links labels.
// Returns the new id and human identifier (e.g. "ENG-42"). The caller owns tx
// begin/commit and maps the sentinel errors. Priority defaults to "none" and
// AuthoredVia to "agent_tool_call" when empty.
//
// Assignee validation lives HERE rather than in each caller because this is
// the actual chokepoint: every issue insert — agent-tool-call creates
// (InternalIssueHandler.Create) and recurring-issue fires
// (RecurringIssueDispatcher) — flows through this one function. Putting the
// check anywhere else would mean a THIRD caller of insertIssueTx could forget
// it exactly the way InternalIssueHandler.Create did (discovered by
// assignee_write_invariant_test.go, not by review — see that file's history).
//
// The recurring dispatcher's assignee_id is already validated once, when the
// recurring_issues row is created or updated (recurring_issue_handler.go), so
// this re-checks it on every fire rather than trusting the stored row forever
// — cheap (one indexed lookup), and it fails closed instead of silently
// carrying forward an assignee who was removed from the workspace between
// firings. fireOne already treats any insertIssueTx error as "skip this
// occurrence, advance next_run, log loudly" (recurring_issue_dispatcher.go),
// so a newly-invalid assignee degrades to a skipped fire, not a crash or a
// silently-created issue with a foreign assignee.
func insertIssueTx(ctx context.Context, tx *sql.Tx, logger *slog.Logger, s issueSpec) (id, identifier string, err error) {
	priority := s.Priority
	if priority == "" {
		priority = "none"
	}
	authoredVia := s.AuthoredVia
	if authoredVia == "" {
		authoredVia = "agent_tool_call"
	}

	identifier, number, err := nextIssueIdentifierTx(ctx, tx, s.WorkspaceID, s.CrewID)
	if err != nil {
		return "", "", err
	}

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

	if s.AssigneeID != nil && *s.AssigneeID != "" {
		assigneeType := ""
		if s.AssigneeType != nil {
			assigneeType = *s.AssigneeType
		}
		if assigneeType != "user" && assigneeType != "agent" {
			return "", "", errIssueAssigneeTypeInvalid
		}
		ok, vErr := validateAssigneeWorkspace(ctx, tx, assigneeType, *s.AssigneeID, s.WorkspaceID)
		if vErr != nil {
			return "", "", vErr
		}
		if !ok {
			return "", "", errIssueAssigneeNotInWorkspace
		}
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
		                      author_agent_id, author_chat_id, author_run_id, created_by_user_id, authored_via,
		                      sort_order, mission_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'issue', ?, ?)`,
		id, s.WorkspaceID, s.CrewID, leadAgentID, traceID,
		s.Title, s.Description, number, identifier,
		priority, s.AssigneeType, s.AssigneeID, s.ProjectID, s.MilestoneID,
		nullable(s.AuthorAgentID), nullable(s.AuthorChatID), nullable(s.AuthorRunID), nullable(s.CreatedByUserID), authoredVia,
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
