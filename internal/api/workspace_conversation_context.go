package api

import (
	"context"
	"database/sql"
	"net/url"
	"regexp"
	"strings"
)

// This is a bounded read-only snapshot, not another orchestration source. Never
// include issue descriptions, private routine definitions, outputs or secrets.
type conversationWorkContext struct {
	CapturedAt        string                     `json:"captured_at"`
	Issues            []conversationIssueState   `json:"issues"`
	IssuesTruncated   bool                       `json:"issues_truncated"`
	Routines          []conversationRoutineState `json:"routines"`
	RoutinesTruncated bool                       `json:"routines_truncated"`
}

type conversationIssueState struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	UpdatedAt  string `json:"updated_at"`
	URL        string `json:"url"`
}

type conversationRoutineState struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	RunID     string `json:"latest_run_id,omitempty"`
	Status    string `json:"latest_run_status,omitempty"`
	StartedAt string `json:"latest_run_started_at,omitempty"`
	URL       string `json:"url"`
}

var conversationIssueReference = regexp.MustCompile(`(?i)\b[a-z][a-z0-9]{1,15}-[0-9]+\b`)

func conversationWorkSnapshot(ctx context.Context, conn *sql.Conn, workspace, latest string) (conversationWorkContext, error) {
	out := conversationWorkContext{CapturedAt: isoMillisNow(), Issues: []conversationIssueState{}, Routines: []conversationRoutineState{}}
	// Named references take precedence over the recent sample, including an old
	// issue which would otherwise disappear beyond the page boundary.
	refs := conversationIssueReference.FindAllString(latest, 10)
	args := []any{workspace}
	order := ""
	if len(refs) > 0 {
		marks := make([]string, len(refs))
		for i, ref := range refs {
			marks[i] = "?"
			args = append(args, strings.ToUpper(ref))
		}
		order = "CASE WHEN UPPER(m.identifier) IN (" + strings.Join(marks, ",") + ") THEN 0 ELSE 1 END, "
	}
	rows, err := conn.QueryContext(ctx, `SELECT m.id,COALESCE(m.identifier,''),m.title,m.status,COALESCE(m.priority,'none'),m.updated_at
 FROM missions m JOIN crews c ON c.id=m.crew_id AND c.workspace_id=m.workspace_id
 WHERE m.workspace_id=? AND m.mission_type='issue' AND c.deleted_at IS NULL
 ORDER BY `+order+`m.updated_at DESC,m.id DESC LIMIT 21`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var issue conversationIssueState
		if err := rows.Scan(&issue.ID, &issue.Identifier, &issue.Title, &issue.Status, &issue.Priority, &issue.UpdatedAt); err != nil {
			rows.Close()
			return out, err
		}
		if len(out.Issues) == 20 {
			out.IssuesTruncated = true
			break
		}
		issue.Title = conversationContextLabel(issue.Title)
		identifier := issue.Identifier
		if identifier == "" {
			identifier = issue.ID
		}
		issue.URL = "/issues/" + url.PathEscape(identifier) + "?workspace_id=" + url.QueryEscape(workspace)
		out.Issues = append(out.Issues, issue)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = conn.QueryContext(ctx, `SELECT p.id,p.slug,p.name,COALESCE(r.id,''),COALESCE(r.status,''),COALESCE(r.started_at,'')
 FROM pipelines p LEFT JOIN pipeline_runs r ON r.id=(SELECT pr.id FROM pipeline_runs pr WHERE pr.pipeline_id=p.id AND pr.workspace_id=p.workspace_id ORDER BY pr.started_at DESC,pr.id DESC LIMIT 1)
 WHERE p.workspace_id=? AND p.deleted_at IS NULL AND p.workspace_visible=1 AND p.ephemeral=0
 ORDER BY p.updated_at DESC,p.id DESC LIMIT 21`, workspace)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var routine conversationRoutineState
		if err := rows.Scan(&routine.ID, &routine.Slug, &routine.Name, &routine.RunID, &routine.Status, &routine.StartedAt); err != nil {
			return out, err
		}
		if len(out.Routines) == 20 {
			out.RoutinesTruncated = true
			break
		}
		routine.Name = conversationContextLabel(routine.Name)
		routine.URL = "/routines?slug=" + url.QueryEscape(routine.Slug) + "&workspace_id=" + url.QueryEscape(workspace)
		out.Routines = append(out.Routines, routine)
	}
	return out, rows.Err()
}

func conversationContextLabel(value string) string {
	runes := []rune(value)
	if len(runes) > 256 {
		return string(runes[:256]) + "…"
	}
	return value
}
