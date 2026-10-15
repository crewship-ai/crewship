package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// This intentionally small projection excludes leads, crews, credentials and
// private issue/run metadata. Version describes the editable content snapshot.
type guestProjectDetails struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	CanManage   bool   `json:"can_manage"`
}

func projectDetailsVersion(id, name, description, updated string) string {
	raw, _ := json.Marshal([]string{id, name, description, updated})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func guestProjectDetailsError(w http.ResponseWriter, err error) {
	if errors.Is(err, access.ErrDenied) || errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Project unavailable")
		return
	}
	replyError(w, 500, "Project unavailable")
}

// GET /api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}
// PUT /api/v1/workspaces/{workspaceId}/restricted-projects/{projectId}
func (r *Router) restrictedProjectDetails(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Project unavailable")
		return
	}
	project := req.PathValue("projectId")
	var body struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Version     string  `json:"version"`
	}
	write := req.Method == "PUT"
	if write {
		if !readGuestIssueJSON(w, req, &body) {
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" || len(body.Name) > 4096 || body.Description == nil || len(*body.Description) > 65536 || len(body.Version) != 64 {
			replyError(w, 400, "Supply name, description and the current project version")
			return
		}
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	defer tx.Rollback()
	if err = r.guestIssueAuthority(req.Context(), tx, user.ID, workspace, project, false); err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	store := access.Store{DB: r.db}
	manageErr := store.CheckIn(req.Context(), tx, user.ID, workspace, access.Right{Kind: "project", ID: project, Operation: "manage"})
	if manageErr != nil {
		guestProjectDetailsError(w, manageErr)
		return
	}
	var current guestProjectDetails
	var updated string
	err = tx.QueryRowContext(req.Context(), `SELECT id,name,COALESCE(description,''),updated_at FROM projects WHERE id=? AND workspace_id=?`, project, workspace).Scan(&current.ID, &current.Name, &current.Description, &updated)
	if err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	current.Version = projectDetailsVersion(current.ID, current.Name, current.Description, updated)
	current.CanManage = manageErr == nil && canScope(req.Context(), "workspace:admin")
	if !write {
		writeJSON(w, 200, current)
		return
	}
	if body.Version != current.Version {
		replyError(w, 409, "Project details changed. Review the current version before saving.")
		return
	}
	now := tsformat.Format(time.Now())
	if _, err = tx.ExecContext(req.Context(), `UPDATE projects SET name=?,description=?,updated_at=? WHERE id=? AND workspace_id=?`, body.Name, *body.Description, now, project, workspace); err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryAuditEntityUpdated, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest updated shared project details", Payload: map[string]any{"entity_type": "project", "project_id": project, "fields": []string{"name", "description"}}})
	if err != nil {
		guestProjectDetailsError(w, err)
		return
	}
	current.Name, current.Description = body.Name, *body.Description
	current.Version = projectDetailsVersion(project, current.Name, current.Description, now)
	finishGuestIssueMutation(w, tx, notify, 200, current)
}
