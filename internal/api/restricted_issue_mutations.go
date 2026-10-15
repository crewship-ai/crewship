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
	"github.com/crewship-ai/crewship/internal/chatentities"
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
	return appendProjectIssueAudit(ctx, tx, r.journal, entry)
}

func appendProjectIssueAudit(ctx context.Context, tx *sql.Tx, writer journal.Emitter, entry journal.Entry) (func(), error) {
	_, noop := writer.(noopEmitter)
	if writer == nil || noop {
		return journal.AppendTx(ctx, tx, entry)
	}
	emitter, ok := writer.(journal.TransactionEmitter)
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
	workspace, matches := memberAccessWorkspace(req)
	project := req.PathValue("projectId")
	if user == nil || !matches {
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

// The caller must first check fresh Guest Read/Contribute in this transaction.
// Stored results never replace project authorization, including creation replay.
func (r *Router) beginGuestIssueOperation(w http.ResponseWriter, req *http.Request, tx *sql.Tx, verb, entityID, project string, payload any) (*chatentities.Operation, bool) {
	key := req.Header.Get("Idempotency-Key")
	if key == "" {
		return nil, false
	}
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		replyError(w, 400, "operation_invalid")
		return nil, true
	}
	hash, err := chatIssueRequestHash(verb, entityID, payload)
	if err != nil {
		guestIssueError(w, err)
		return nil, true
	}
	op, replay, err := chatentities.BeginOperation(req.Context(), tx, chatentities.OperationSpec{ID: generateCUID(), WorkspaceID: WorkspaceIDFromContext(req.Context()), Key: key, RequestHash: hash, ActorType: "human", ActorID: UserFromContext(req.Context()).ID, Provenance: "human", Kind: "issue", Verb: verb})
	if err != nil {
		writeChatIssueError(w, err)
		return nil, true
	}
	if !replay {
		return op, false
	}
	issue, err := r.restrictedIssueIn(req.Context(), tx, UserFromContext(req.Context()).ID, WorkspaceIDFromContext(req.Context()), op.EntityID, true)
	if err != nil {
		guestIssueError(w, err)
		return nil, true
	}
	if issue.ProjectID != project || (entityID != "" && op.EntityID != entityID) {
		replyError(w, 409, "The issue project changed. Review before retrying.")
		return nil, true
	}
	_ = tx.Rollback()
	w.Header().Set("Idempotency-Replayed", "true")
	w.Header().Set("Operation-ID", op.ID)
	w.Header().Set("Cache-Control", "no-store")
	status := 201
	if verb == "updated" {
		status = 200
	}
	writeJSON(w, status, json.RawMessage(op.Result))
	return nil, true
}

// Guest intake is human-authored but carries no agent execution grant. An
// explicit unavailable origin prevents schedulers interpreting NULL as autonomous.
func guestIssueOrigin(ctx context.Context, tx *sql.Tx, workspace, root string) (string, error) {
	id := generateCUID()
	_, err := tx.ExecContext(ctx, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES(?,?,?,'','','{}',?)`, id, workspace, root, time.Now().UTC().Format(time.RFC3339))
	return id, err
}

func (r *Router) finishGuestContentMutation(w http.ResponseWriter, req *http.Request, tx *sql.Tx, op *chatentities.Operation, ev issueEvent, status int, body any) {
	if _, err := persistIssueActivityTx(req.Context(), tx, ev); err != nil {
		guestIssueError(w, err)
		return
	}
	if err := completeHumanIssueOperation(req, tx, op, ev.MissionID, body); err != nil {
		guestIssueError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		guestIssueError(w, err)
		return
	}
	if op != nil {
		w.Header().Set("Operation-ID", op.ID)
	}
	delivery := issueEvents{db: r.db, hub: r.hub, logger: r.logger, journal: r.journal}.mutationDelivery()
	delivery.drainCommittedIssueMutation(WorkspaceIDFromContext(req.Context()))
	delivery.broadcastIssueEvent(WorkspaceIDFromContext(req.Context()), "issue.updated", map[string]string{"id": ev.MissionID})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, body)
}

func (r *Router) createRestrictedIssue(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
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
	op, handled := r.beginGuestIssueOperation(w, req, tx, "created", "", body.ProjectID, body)
	if handled {
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
	origin, err := guestIssueOrigin(req.Context(), tx, workspace, "guest-intake:"+id)
	if err == nil {
		_, err = tx.ExecContext(req.Context(), `UPDATE missions SET chat_origin_id=? WHERE id=? AND workspace_id=?`, origin, id, workspace)
	}
	if err != nil {
		guestIssueError(w, err)
		return
	}
	r.finishGuestContentMutation(w, req, tx, op, issueEvent{MissionID: id, ActorType: "user", ActorID: user.ID, Action: actionCreated, Details: "Guest created a project issue"}, 201, restrictedIssue{ID: id, Identifier: identifier, Title: body.Title, Status: "BACKLOG", ProjectID: body.ProjectID, ProjectName: projectName, CanRead: true, CanContribute: true, Description: body.Description})
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
	CanManage     bool   `json:"can_manage"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	CanContribute bool   `json:"can_contribute"`
	IntakeReady   bool   `json:"intake_ready"`
}

func (r *Router) restrictedIssueProjects(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Projects unavailable")
		return
	}
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
 EXISTS(SELECT 1 FROM live_access_grants mg WHERE mg.member_id=? AND mg.resource_kind='project' AND mg.project_id=p.id AND mg.operation='manage') AND EXISTS(SELECT 1 FROM live_access_grants rg WHERE rg.member_id=? AND rg.resource_kind='project' AND rg.project_id=p.id AND rg.operation='read'),
 EXISTS(SELECT 1 FROM project_guest_issue_intake i JOIN crews c ON c.id=i.crew_id AND c.workspace_id=i.workspace_id AND c.deleted_at IS NULL WHERE i.project_id=p.id AND i.workspace_id=p.workspace_id AND EXISTS(SELECT 1 FROM agents a WHERE a.crew_id=c.id AND a.workspace_id=c.workspace_id AND a.agent_role='LEAD' AND a.deleted_at IS NULL))
 FROM projects p JOIN live_access_grants visible ON visible.project_id=p.id AND visible.resource_kind='project' AND visible.member_id=? AND visible.operation='list' WHERE p.workspace_id=? ORDER BY p.name,p.id`, member, member, member, member, member, workspace)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	items := []guestIssueProject{}
	for rows.Next() {
		var item guestIssueProject
		if err = rows.Scan(&item.ID, &item.Name, &item.CanContribute, &item.CanManage, &item.IntakeReady); err != nil {
			break
		}
		item.CanContribute = item.CanContribute && canScope(req.Context(), "workspace:admin")
		item.CanManage = item.CanManage && canScope(req.Context(), "workspace:admin")
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
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Issue unavailable")
		return
	}
	var body struct {
		Body      string `json:"body"`
		ProjectID string `json:"project_id"`
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
	if body.ProjectID == "" {
		replyError(w, 400, "project_id is required to confirm the comment audience")
		return
	}
	if body.ProjectID != issue.ProjectID {
		replyError(w, 409, "The issue project changed. Review the audience before sharing your draft.")
		return
	}
	op, handled := r.beginGuestIssueOperation(w, req, tx, "commented", issue.ID, issue.ProjectID, body)
	if handled {
		return
	}
	comment := guestIssueComment{ID: generateCUID(), Body: body.Body, Author: "You", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	origin, err := guestIssueOrigin(req.Context(), tx, workspace, "guest-comment:"+comment.ID)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	_, err = tx.ExecContext(req.Context(), `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,guest_visible,guest_project_id,created_at,updated_at,chat_origin_id) VALUES(?,?,'user',?,?,1,?,?,?,?)`, comment.ID, issue.ID, user.ID, comment.Body, issue.ProjectID, comment.CreatedAt, comment.CreatedAt, origin)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	r.finishGuestContentMutation(w, req, tx, op, issueEvent{MissionID: issue.ID, ActorType: "user", ActorID: user.ID, Action: actionCommented, Details: "Guest commented on a shared issue"}, 201, comment)
}

func (r *Router) updateRestrictedIssue(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
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
	op, handled := r.beginGuestIssueOperation(w, req, tx, "updated", issue.ID, issue.ProjectID, body)
	if handled {
		return
	}
	if issue.Status != "BACKLOG" && issue.Status != "TODO" {
		replyError(w, 409, "This issue is already in the trusted execution workflow")
		return
	}
	activeWork, err := guestIssueActiveWorkIn(req.Context(), tx, issue.ID)
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
	previous := issue.Status
	issue.Status = body.Status
	r.finishGuestContentMutation(w, req, tx, op, issueEvent{MissionID: issue.ID, ActorType: "user", ActorID: user.ID, Action: actionStatusChanged, Details: previous + " → " + body.Status, From: previous, To: body.Status}, 200, issue)
}
