package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/access"
)

// A guest's Issue view deliberately contains no agent/crew references, comments,
// run history, summaries or cross-project relations from the shared issue DTO.
type restrictedIssue struct {
	CanDeleteIssues bool    `json:"can_delete_issues"`
	ID              string  `json:"id"`
	Identifier      string  `json:"identifier"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	ProjectID       string  `json:"project_id"`
	ProjectName     string  `json:"project_name"`
	CanRead         bool    `json:"can_read"`
	CanContribute   bool    `json:"can_contribute"`
	Description     *string `json:"description,omitempty"`
}

// restrictedIssues serves a single, bulk-filtered project projection. List and
// read are separate operations; a direct URL always rechecks project.read.
func (r *Router) restrictedIssues(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Issues unavailable")
		return
	}
	store := access.Store{DB: r.db}
	member, err := store.Membership(req.Context(), user.ID, workspace)
	if err != nil || member.Mode != "restricted" {
		replyError(w, 404, "Issues unavailable")
		return
	}
	id := req.PathValue("issueId")
	search := req.URL.Query().Get("q")
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 256 {
		replyError(w, http.StatusBadRequest, "Search must contain at most 256 valid Unicode characters")
		return
	}
	search = strings.TrimSpace(search)
	if id != "" && req.URL.Query().Has("q") {
		replyError(w, http.StatusBadRequest, "Search is only available when listing shared issues")
		return
	}
	operation := "list"
	if id != "" {
		operation = "read"
	}
	project := req.URL.Query().Get("project_id")
	if project != "" {
		if err = store.Check(req.Context(), user.ID, workspace, access.Right{Kind: "project", ID: project, Operation: operation}); err != nil {
			if errors.Is(err, access.ErrDenied) {
				replyError(w, 404, "Issues unavailable")
			} else {
				replyError(w, 500, "Issues unavailable")
			}
			return
		}
	}
	limit, offset, ok := routineListPage(w, req.URL.Query().Get("limit"), req.URL.Query().Get("offset"), 100)
	if !ok {
		return
	}
	total := 0
	if id == "" {
		err = r.db.QueryRowContext(req.Context(), `SELECT COUNT(*)
            FROM missions m JOIN projects p ON p.id=m.project_id AND p.workspace_id=m.workspace_id
            JOIN live_access_grants g ON g.project_id=p.id AND g.resource_kind='project' AND g.member_id=? AND g.operation='list'
            WHERE m.workspace_id=? AND m.mission_type='issue' AND (?='' OR p.id=?)
            AND (?='' OR instr(lower(m.title),lower(?))>0 OR instr(lower(COALESCE(m.identifier,m.id)),lower(?))>0)`, member.ID, workspace, project, project, search, search, search).Scan(&total)
		if err != nil {
			replyError(w, 500, "Issues unavailable")
			return
		}
	}
	rows, err := r.db.QueryContext(req.Context(), `SELECT m.id,COALESCE(m.identifier,m.id),m.title,m.status,p.id,p.name,
 EXISTS(SELECT 1 FROM live_access_grants read_grant WHERE read_grant.member_id=? AND read_grant.resource_kind='project' AND read_grant.project_id=p.id AND read_grant.operation='read'),
 EXISTS(SELECT 1 FROM live_access_grants contribution WHERE contribution.member_id=? AND contribution.resource_kind='project' AND contribution.project_id=p.id AND contribution.operation='contribute') AND EXISTS(SELECT 1 FROM live_access_grants read_grant WHERE read_grant.member_id=? AND read_grant.resource_kind='project' AND read_grant.project_id=p.id AND read_grant.operation='read'),
 EXISTS(SELECT 1 FROM live_access_grants d WHERE d.member_id=? AND d.resource_kind='project' AND d.project_id=p.id AND d.operation='delete_issues') AND EXISTS(SELECT 1 FROM live_access_grants mg WHERE mg.member_id=? AND mg.resource_kind='project' AND mg.project_id=p.id AND mg.operation='manage') AND EXISTS(SELECT 1 FROM live_access_grants rd WHERE rd.member_id=? AND rd.resource_kind='project' AND rd.project_id=p.id AND rd.operation='read'),
 CASE WHEN ?='read' THEN m.description ELSE NULL END
 FROM missions m JOIN projects p ON p.id=m.project_id AND p.workspace_id=m.workspace_id
 JOIN live_access_grants g ON g.project_id=p.id AND g.resource_kind='project' AND g.member_id=? AND g.operation=?
 WHERE m.workspace_id=? AND m.mission_type='issue' AND (?='' OR m.id=? OR m.identifier=?) AND (?='' OR p.id=?)
 AND (?='' OR instr(lower(m.title),lower(?))>0 OR instr(lower(COALESCE(m.identifier,m.id)),lower(?))>0)
 ORDER BY m.created_at DESC,m.id LIMIT ? OFFSET ?`, member.ID, member.ID, member.ID, member.ID, member.ID, member.ID, operation, member.ID, operation, workspace, id, id, id, project, project, search, search, search, limit, offset)
	if err != nil {
		replyError(w, 500, "Issues unavailable")
		return
	}
	items := []restrictedIssue{}
	for rows.Next() {
		var item restrictedIssue
		var description sql.NullString
		if err = rows.Scan(&item.ID, &item.Identifier, &item.Title, &item.Status, &item.ProjectID, &item.ProjectName, &item.CanRead, &item.CanContribute, &item.CanDeleteIssues, &description); err != nil {
			break
		}
		if description.Valid {
			item.Description = &description.String
		}
		item.CanDeleteIssues = item.CanDeleteIssues && canScope(req.Context(), "workspace:admin")
		item.CanContribute = item.CanContribute && canScope(req.Context(), "workspace:admin")
		items = append(items, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		replyError(w, 500, "Issues unavailable")
		return
	}
	current, err := store.Membership(req.Context(), user.ID, workspace)
	if err != nil || current != member {
		replyError(w, 404, "Issues unavailable")
		return
	}
	if id != "" {
		if len(items) != 1 {
			replyError(w, 404, "Issue unavailable")
			return
		}
		writeJSON(w, 200, items[0])
		return
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	setRoutineListNextOffset(w, offset, limit, len(items))
	writeJSON(w, 200, items)
}
