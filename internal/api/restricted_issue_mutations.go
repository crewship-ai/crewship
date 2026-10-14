package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mentions"
)

func readGuestIssueJSON(w http.ResponseWriter, req *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		replyError(w, 400, "Invalid issue request")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		replyError(w, 400, "Invalid issue request")
		return false
	}
	return true
}

// The grant and active account are read under the mutation's transaction.
// Contribute supplements Read; it never authorizes project-source file writes.
func (r *Router) guestIssueAuthority(ctx context.Context, tx *sql.Tx, user, workspace, project string, write bool) error {
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT access_mode FROM workspace_members WHERE user_id=? AND workspace_id=?`, user, workspace).Scan(&mode); err != nil {
		return err
	}
	if mode != "restricted" {
		return access.ErrDenied
	}
	store := access.Store{DB: r.db}
	if err := store.CheckIn(ctx, tx, user, workspace, access.Right{Kind: "project", ID: project, Operation: "read"}); err != nil {
		return err
	}
	if write {
		return store.CheckIn(ctx, tx, user, workspace, access.Right{Kind: "project", ID: project, Operation: "contribute"})
	}
	return nil
}

func guestIssueError(w http.ResponseWriter, err error) {
	if errors.Is(err, access.ErrDenied) || errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Issue unavailable")
		return
	}
	replyError(w, 500, "Issue unavailable")
}

func (r *Router) appendGuestIssueAudit(ctx context.Context, tx *sql.Tx, entry journal.Entry) (func(), error) {
	if r.journal == nil {
		return journal.AppendTx(ctx, tx, entry)
	}
	emitter, ok := r.journal.(journal.TransactionEmitter)
	if !ok {
		return nil, errors.New("issue journal cannot write atomically")
	}
	return emitter.AppendTx(ctx, tx, entry)
}

func finishGuestIssueMutation(w http.ResponseWriter, tx *sql.Tx, notify func(), status int, body any) {
	if err := tx.Commit(); err != nil {
		guestIssueError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, body)
}

type guestIssueIntake struct {
	ProjectID string `json:"project_id"`
	CrewID    string `json:"crew_id"`
	CrewName  string `json:"crew_name"`
	Revision  int64  `json:"revision"`
}

// This administrative surface is deliberately absent from the Guest allowlist.
func (r *Router) guestIssueIntake(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, project := WorkspaceIDFromContext(req.Context()), req.PathValue("projectId")
	if user == nil {
		replyError(w, 404, "Project unavailable")
		return
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	defer tx.Rollback()
	var allowed bool
	err = tx.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members wm JOIN users u ON u.id=wm.user_id JOIN workspaces w ON w.id=wm.workspace_id JOIN projects p ON p.workspace_id=wm.workspace_id
 WHERE wm.user_id=? AND wm.workspace_id=? AND p.id=? AND wm.role IN ('OWNER','ADMIN') AND wm.access_mode='trusted' AND u.suspended_at IS NULL AND w.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM workspace_members other WHERE other.user_id=wm.user_id AND other.access_mode='restricted'))`, user.ID, workspace, project).Scan(&allowed)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	if !allowed {
		replyError(w, 404, "Project unavailable")
		return
	}
	current := guestIssueIntake{ProjectID: project}
	err = tx.QueryRowContext(req.Context(), `SELECT i.crew_id,COALESCE(c.name,''),i.revision FROM project_guest_issue_intake i LEFT JOIN crews c ON c.id=i.crew_id AND c.workspace_id=i.workspace_id AND c.deleted_at IS NULL WHERE i.project_id=? AND i.workspace_id=?`, project, workspace).Scan(&current.CrewID, &current.CrewName, &current.Revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		guestIssueError(w, err)
		return
	}
	if req.Method == "GET" {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, current)
		return
	}
	var body struct {
		CrewID   string `json:"crew_id"`
		Revision *int64 `json:"revision"`
	}
	if !readGuestIssueJSON(w, req, &body) {
		return
	}
	if body.Revision == nil || *body.Revision < 0 || strings.TrimSpace(body.CrewID) == "" {
		replyError(w, 400, "crew_id and revision are required")
		return
	}
	if *body.Revision != current.Revision {
		replyError(w, 409, "Project intake changed. Reload before saving.")
		return
	}
	var crewName string
	err = tx.QueryRowContext(req.Context(), `SELECT c.name FROM crews c WHERE c.id=? AND c.workspace_id=? AND c.deleted_at IS NULL AND EXISTS(SELECT 1 FROM agents a WHERE a.crew_id=c.id AND a.workspace_id=c.workspace_id AND a.agent_role='LEAD' AND a.deleted_at IS NULL)`, body.CrewID, workspace).Scan(&crewName)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 400, "Choose an active workspace crew with a lead agent")
		return
	}
	if err != nil {
		guestIssueError(w, err)
		return
	}
	_, err = tx.ExecContext(req.Context(), `INSERT INTO project_guest_issue_intake(project_id,workspace_id,crew_id,revision,updated_by,updated_at) VALUES(?,?,?,1,?,?)
 ON CONFLICT(project_id) DO UPDATE SET crew_id=excluded.crew_id,revision=project_guest_issue_intake.revision+1,updated_by=excluded.updated_by,updated_at=excluded.updated_at`, project, workspace, body.CrewID, user.ID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		guestIssueError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryAuditEntityUpdated, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Project Guest issue intake changed", Payload: map[string]any{"entity_type": "project_guest_issue_intake", "project_id": project, "previous_crew_id": current.CrewID, "crew_id": body.CrewID, "revision": current.Revision + 1}})
	if err != nil {
		guestIssueError(w, err)
		return
	}
	finishGuestIssueMutation(w, tx, notify, 200, guestIssueIntake{ProjectID: project, CrewID: body.CrewID, CrewName: crewName, Revision: current.Revision + 1})
}

func (r *Router) createRestrictedIssue(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, 404, "Issue unavailable")
		return
	}
	var body struct {
		ProjectID   string  `json:"project_id"`
		Title       string  `json:"title"`
		Description *string `json:"description"`
	}
	if !readGuestIssueJSON(w, req, &body) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if body.Title == "" || len(body.Title) > 4096 || (body.Description != nil && len(*body.Description) > 65536) {
		replyError(w, 400, "Supply a title up to 4096 bytes and a description up to 65536 bytes")
		return
	}
	workspace := WorkspaceIDFromContext(req.Context())
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	defer tx.Rollback()
	if err = r.guestIssueAuthority(req.Context(), tx, user.ID, workspace, body.ProjectID, true); err != nil {
		guestIssueError(w, err)
		return
	}
	var crew, projectName string
	err = tx.QueryRowContext(req.Context(), `SELECT i.crew_id,p.name FROM project_guest_issue_intake i JOIN projects p ON p.id=i.project_id AND p.workspace_id=i.workspace_id JOIN crews c ON c.id=i.crew_id AND c.workspace_id=i.workspace_id AND c.deleted_at IS NULL WHERE i.project_id=? AND i.workspace_id=?`, body.ProjectID, workspace).Scan(&crew, &projectName)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 409, "Ask your administrator to configure this project's issue intake crew")
		return
	}
	if err != nil {
		guestIssueError(w, err)
		return
	}
	id, identifier, err := insertIssueTx(req.Context(), tx, r.logger, issueSpec{WorkspaceID: workspace, CrewID: crew, Title: body.Title, Description: body.Description, ProjectID: &body.ProjectID, CreatedByUserID: user.ID, AuthoredVia: "user_api"})
	if errors.Is(err, errIssueNoLeadAgent) || errors.Is(err, errIssueCrewNotFound) {
		replyError(w, 409, "Project issue intake is unavailable. Ask your administrator.")
		return
	}
	if err != nil {
		guestIssueError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, MissionID: id, Type: journal.EntryMissionCreated, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest created a project issue", Payload: map[string]any{"project_id": body.ProjectID, "issue_id": id, "source": "guest_contribution"}})
	if err != nil {
		guestIssueError(w, err)
		return
	}
	finishGuestIssueMutation(w, tx, notify, 201, restrictedIssue{ID: id, Identifier: identifier, Title: body.Title, Status: "BACKLOG", ProjectID: body.ProjectID, ProjectName: projectName, CanRead: true, CanContribute: true, Description: body.Description})
}

func (r *Router) restrictedIssueIn(ctx context.Context, tx *sql.Tx, user, workspace, id string, write bool) (restrictedIssue, error) {
	var issue restrictedIssue
	var description sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT m.id,COALESCE(m.identifier,m.id),m.title,m.status,p.id,p.name,m.description FROM missions m JOIN projects p ON p.id=m.project_id AND p.workspace_id=m.workspace_id WHERE m.workspace_id=? AND m.mission_type='issue' AND (m.id=? OR m.identifier=?)`, workspace, id, id).Scan(&issue.ID, &issue.Identifier, &issue.Title, &issue.Status, &issue.ProjectID, &issue.ProjectName, &description)
	if err != nil {
		return issue, err
	}
	if err = r.guestIssueAuthority(ctx, tx, user, workspace, issue.ProjectID, write); err != nil {
		return restrictedIssue{}, err
	}
	issue.CanRead = true
	issue.CanContribute = write
	if description.Valid {
		issue.Description = &description.String
	}
	return issue, nil
}

type guestIssueComment struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
}

type guestIssueProject struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CanContribute bool   `json:"can_contribute"`
	IntakeReady   bool   `json:"intake_ready"`
}

func (r *Router) restrictedIssueProjects(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, 404, "Projects unavailable")
		return
	}
	workspace := WorkspaceIDFromContext(req.Context())
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	defer tx.Rollback()
	var member string
	err = tx.QueryRowContext(req.Context(), `SELECT wm.id FROM workspace_members wm JOIN users u ON u.id=wm.user_id JOIN workspaces w ON w.id=wm.workspace_id WHERE wm.user_id=? AND wm.workspace_id=? AND wm.access_mode='restricted' AND u.suspended_at IS NULL AND w.deleted_at IS NULL`, user.ID, workspace).Scan(&member)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	rows, err := tx.QueryContext(req.Context(), `SELECT p.id,p.name,
 EXISTS(SELECT 1 FROM live_access_grants g WHERE g.member_id=? AND g.resource_kind='project' AND g.project_id=p.id AND g.operation='contribute') AND EXISTS(SELECT 1 FROM live_access_grants g WHERE g.member_id=? AND g.resource_kind='project' AND g.project_id=p.id AND g.operation='read'),
 EXISTS(SELECT 1 FROM project_guest_issue_intake i JOIN crews c ON c.id=i.crew_id AND c.workspace_id=i.workspace_id AND c.deleted_at IS NULL WHERE i.project_id=p.id AND i.workspace_id=p.workspace_id AND EXISTS(SELECT 1 FROM agents a WHERE a.crew_id=c.id AND a.workspace_id=c.workspace_id AND a.agent_role='LEAD' AND a.deleted_at IS NULL))
 FROM projects p JOIN live_access_grants visible ON visible.project_id=p.id AND visible.resource_kind='project' AND visible.member_id=? AND visible.operation='list' WHERE p.workspace_id=? ORDER BY p.name,p.id`, member, member, member, workspace)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	items := []guestIssueProject{}
	for rows.Next() {
		var item guestIssueProject
		if err = rows.Scan(&item.ID, &item.Name, &item.CanContribute, &item.IntakeReady); err != nil {
			break
		}
		items = append(items, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		guestIssueError(w, errors.Join(err, rowErr))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, items)
}

func (r *Router) restrictedIssueComments(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, 404, "Issue unavailable")
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	write := req.Method == "POST"
	if write {
		if !readGuestIssueJSON(w, req, &body) {
			return
		}
		body.Body = strings.TrimSpace(body.Body)
		if body.Body == "" || len(body.Body) > 32768 {
			replyError(w, 400, "Comment must contain 1–32768 bytes")
			return
		}
		if len(mentions.ExtractAgentIDs(body.Body)) > 0 {
			replyError(w, 400, "Shared comments cannot include agent mentions")
			return
		}
	}
	workspace := WorkspaceIDFromContext(req.Context())
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	defer tx.Rollback()
	issue, err := r.restrictedIssueIn(req.Context(), tx, user.ID, workspace, req.PathValue("issueId"), write)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	if !write {
		rows, err := tx.QueryContext(req.Context(), `SELECT id,body,CASE WHEN author_type='user' AND author_id=? THEN 'You' ELSE 'Workspace member' END,created_at FROM mission_comments WHERE mission_id=? AND guest_visible=1 AND guest_project_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, user.ID, issue.ID, issue.ProjectID)
		if err != nil {
			guestIssueError(w, err)
			return
		}
		items := []guestIssueComment{}
		for rows.Next() {
			var item guestIssueComment
			if err = rows.Scan(&item.ID, &item.Body, &item.Author, &item.CreatedAt); err != nil {
				break
			}
			items = append(items, item)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			guestIssueError(w, errors.Join(err, rowErr))
			return
		}
		slices.Reverse(items)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, items)
		return
	}
	comment := guestIssueComment{ID: generateCUID(), Body: body.Body, Author: "You", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	_, err = tx.ExecContext(req.Context(), `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,guest_visible,guest_project_id,created_at,updated_at) VALUES(?,?,'user',?,?,1,?,?,?)`, comment.ID, issue.ID, user.ID, comment.Body, issue.ProjectID, comment.CreatedAt, comment.CreatedAt)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, MissionID: issue.ID, Type: journal.EntryMissionComment, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest commented on a shared issue", Payload: map[string]any{"project_id": issue.ProjectID, "issue_id": issue.ID, "comment_id": comment.ID, "guest_visible": true}})
	if err != nil {
		guestIssueError(w, err)
		return
	}
	finishGuestIssueMutation(w, tx, notify, 201, comment)
}

func (r *Router) updateRestrictedIssue(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, 404, "Issue unavailable")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if !readGuestIssueJSON(w, req, &body) {
		return
	}
	// Clients can arrange intake work; execution, review and terminal outcomes
	// remain with the trusted workflow and its existing review requirements.
	if body.Status != "BACKLOG" && body.Status != "TODO" {
		replyError(w, 400, "Guest contributions may move issues between BACKLOG and TODO")
		return
	}
	workspace := WorkspaceIDFromContext(req.Context())
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	defer tx.Rollback()
	issue, err := r.restrictedIssueIn(req.Context(), tx, user.ID, workspace, req.PathValue("issueId"), true)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	if issue.Status != "BACKLOG" && issue.Status != "TODO" {
		replyError(w, 409, "This issue is already in the trusted execution workflow")
		return
	}
	var activeWork bool
	err = tx.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM assignments WHERE (mission_id=? OR chat_id=? OR group_id=?) AND status NOT IN ('COMPLETED','FAILED','CANCELLED')) OR EXISTS(SELECT 1 FROM issue_executions WHERE mission_id=? AND stage IN ('working','reviewing'))`, issue.ID, issue.ID, issue.ID, issue.ID).Scan(&activeWork)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	if activeWork {
		replyError(w, 409, "This issue is already in the trusted execution workflow")
		return
	}
	_, err = tx.ExecContext(req.Context(), `UPDATE missions SET status=?,updated_at=? WHERE id=? AND workspace_id=?`, body.Status, time.Now().UTC().Format(time.RFC3339), issue.ID, workspace)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, MissionID: issue.ID, Type: journal.EntryMissionStatus, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest arranged a shared issue", Payload: map[string]any{"project_id": issue.ProjectID, "issue_id": issue.ID, "from": issue.Status, "to": body.Status, "source": "guest_contribution"}})
	if err != nil {
		guestIssueError(w, err)
		return
	}
	issue.Status = body.Status
	finishGuestIssueMutation(w, tx, notify, 200, issue)
}
