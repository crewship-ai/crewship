package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type guestProjectMilestone struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TargetDate  string `json:"target_date"`
	Status      string `json:"status"`
	Position    int    `json:"position"`
	Version     string `json:"version"`
	CanManage   bool   `json:"can_manage"`
	CanDelete   bool   `json:"can_delete"`
}

func guestMilestoneVersion(item guestProjectMilestone, updated string) string {
	raw, _ := json.Marshal([]any{item.ID, item.ProjectID, item.Name, item.Description, item.TargetDate, item.Status, item.Position, updated})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *Router) guestProjectManageAuthority(ctx context.Context, tx *sql.Tx, user, workspace, project string) error {
	if err := r.guestIssueAuthority(ctx, tx, user, workspace, project, false); err != nil {
		return err
	}
	return (access.Store{DB: r.db}).CheckIn(ctx, tx, user, workspace, access.Right{Kind: "project", ID: project, Operation: "manage"})
}

func scanGuestMilestone(row interface{ Scan(...any) error }, canManage bool) (guestProjectMilestone, error) {
	var item guestProjectMilestone
	var updated string
	var empty bool
	err := row.Scan(&item.ID, &item.ProjectID, &item.Name, &item.Description, &item.TargetDate, &item.Status, &item.Position, &updated, &empty)
	item.CanManage = canManage
	item.CanDelete = canManage && empty
	item.Version = guestMilestoneVersion(item, updated)
	return item, err
}

const guestMilestoneColumns = `id,project_id,name,COALESCE(description,''),COALESCE(target_date,''),status,position,updated_at,NOT EXISTS(SELECT 1 FROM missions linked WHERE linked.milestone_id=milestones.id)`

// GET /api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}/milestones
// GET /api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}/milestones/{milestoneId}
// POST /api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}/milestones
// PUT /api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}/milestones/{milestoneId}
// DELETE /api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}/milestones/{milestoneId}
func (r *Router) restrictedProjectMilestones(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Project unavailable")
		return
	}
	project, id := req.PathValue("projectId"), req.PathValue("milestoneId")
	var body struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		TargetDate  *string `json:"target_date"`
		Status      string  `json:"status"`
		Position    *int    `json:"position"`
		Version     string  `json:"version"`
	}
	if req.Method == "DELETE" {
		var deletion struct {
			Version string `json:"version"`
		}
		if !readGuestIssueJSON(w, req, &deletion) {
			return
		}
		body.Version = deletion.Version
		if len(body.Version) != 64 {
			replyError(w, 400, "Supply the reviewed milestone version")
			return
		}
	} else if req.Method != "GET" {
		if !readGuestIssueJSON(w, req, &body) {
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" || len(body.Name) > 4096 || body.Description == nil || len(*body.Description) > 65536 || body.TargetDate == nil || len(body.Status) > 64 {
			replyError(w, 400, "Supply milestone name, description and target_date")
			return
		}
		if *body.TargetDate != "" {
			if _, err := time.Parse("2006-01-02", *body.TargetDate); err != nil {
				replyError(w, 400, "target_date must be empty or YYYY-MM-DD")
				return
			}
		}
		if req.Method == "POST" {
			if body.Version != "" || body.Position != nil {
				replyError(w, 400, "New milestones are ordered by the server")
				return
			}
			if body.Status == "" {
				body.Status = "active"
			}
		} else if len(body.Version) != 64 || body.Position == nil || *body.Position < 0 || *body.Position > 1_000_000 {
			replyError(w, 400, "Supply the reviewed version and a valid position")
			return
		}
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	defer tx.Rollback()
	if err = r.guestProjectManageAuthority(req.Context(), tx, user.ID, workspace, project); err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	canManage := canScope(req.Context(), "workspace:admin")
	if req.Method == "GET" && id == "" {
		limit, offset := 100, 0
		if v := req.URL.Query().Get("limit"); v != "" {
			limit, err = strconv.Atoi(v)
			if err != nil || limit < 1 || limit > 500 {
				replyError(w, 400, "limit must be 1–500")
				return
			}
		}
		if v := req.URL.Query().Get("offset"); v != "" {
			offset, err = strconv.Atoi(v)
			if err != nil || offset < 0 {
				replyError(w, 400, "offset must be nonnegative")
				return
			}
		}
		var total int
		if err = tx.QueryRowContext(req.Context(), `SELECT COUNT(*) FROM milestones WHERE project_id=?`, project).Scan(&total); err != nil {
			guestProjectDetailsError(w, err)
			return
		}
		rows, err := tx.QueryContext(req.Context(), `SELECT `+guestMilestoneColumns+` FROM milestones WHERE project_id=? ORDER BY position,created_at,id LIMIT ? OFFSET ?`, project, limit, offset)
		if err != nil {
			guestProjectDetailsError(w, err)
			return
		}
		items := []guestProjectMilestone{}
		for rows.Next() {
			item, scanErr := scanGuestMilestone(rows, canManage)
			if scanErr != nil {
				err = scanErr
				break
			}
			items = append(items, item)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			guestProjectDetailsError(w, err)
			return
		}
		if rowErr != nil {
			guestProjectDetailsError(w, rowErr)
			return
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
		w.Header().Set("X-Can-Manage", strconv.FormatBool(canManage))
		writeJSON(w, 200, items)
		return
	}
	var current guestProjectMilestone
	if id != "" {
		current, err = scanGuestMilestone(tx.QueryRowContext(req.Context(), `SELECT `+guestMilestoneColumns+` FROM milestones WHERE id=? AND project_id=?`, id, project), canManage)
		if err != nil {
			guestProjectDetailsError(w, err)
			return
		}
		if req.Method == "GET" {
			writeJSON(w, 200, current)
			return
		}
		if body.Version != current.Version {
			replyError(w, 409, "Milestone changed. Review its current version before saving.")
			return
		}
	}
	if req.Method != "DELETE" && body.Status != "active" && body.Status != "completed" && body.Status != "cancelled" && (id == "" || body.Status != current.Status) {
		replyError(w, 400, "Choose active, completed or cancelled status")
		return
	}
	now := tsformat.Format(time.Now())
	entryType, summary := journal.EntryAuditEntityUpdated, "Guest updated a project milestone"
	status := 200
	switch req.Method {
	case "POST":
		var position int
		if err = tx.QueryRowContext(req.Context(), `SELECT COALESCE(MAX(position),0)+1 FROM milestones WHERE project_id=?`, project).Scan(&position); err != nil {
			guestProjectDetailsError(w, err)
			return
		}
		id = generateCUID()
		_, err = tx.ExecContext(req.Context(), `INSERT INTO milestones(id,project_id,name,description,target_date,status,position,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, project, body.Name, *body.Description, *body.TargetDate, body.Status, position, now, now)
		current = guestProjectMilestone{ID: id, ProjectID: project, Name: body.Name, Description: *body.Description, TargetDate: *body.TargetDate, Status: body.Status, Position: position, CanManage: canManage, CanDelete: canManage}
		entryType, summary, status = journal.EntryAuditEntityCreated, "Guest created a project milestone", 201
	case "PUT":
		_, err = tx.ExecContext(req.Context(), `UPDATE milestones SET name=?,description=?,target_date=?,status=?,position=?,updated_at=? WHERE id=? AND project_id=?`, body.Name, *body.Description, *body.TargetDate, body.Status, *body.Position, now, id, project)
		current.Name, current.Description, current.TargetDate, current.Status, current.Position = body.Name, *body.Description, *body.TargetDate, body.Status, *body.Position
	case "DELETE":
		// An empty-only delete never modifies linked issue work, including
		// legacy references left in a different project after an issue move.
		var linked bool
		err = tx.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM missions WHERE milestone_id=?)`, id).Scan(&linked)
		if err != nil {
			guestProjectDetailsError(w, err)
			return
		}
		if linked {
			replyError(w, 409, "This milestone has linked work and cannot be removed here. Ask a workspace administrator.")
			return
		}
		_, err = tx.ExecContext(req.Context(), `DELETE FROM milestones WHERE id=? AND project_id=?`, id, project)
		entryType, summary, status = journal.EntryAuditEntityDeleted, "Guest deleted a project milestone", 204
	}
	if err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, Type: entryType, ActorType: journal.ActorUser, ActorID: user.ID, Summary: summary, Payload: map[string]any{"entity_type": "milestone", "project_id": project, "milestone_id": id}})
	if err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	if status == 204 {
		if err = tx.Commit(); err != nil {
			guestProjectDetailsError(w, err)
			return
		}
		if notify != nil {
			notify()
		}
		w.WriteHeader(204)
		return
	}
	current.Version = guestMilestoneVersion(current, now)
	finishGuestIssueMutation(w, tx, notify, status, current)
}
