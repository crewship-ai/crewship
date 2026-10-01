package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/chataudience"
)

type ProjectInputOption struct {
	access.ProjectFileVersion
	ProjectName string `json:"project_name"`
}

type ProjectInputOptionsResponse struct {
	Files   []ProjectInputOption `json:"files"`
	HasMore bool                 `json:"has_more"`
}

// This is a read-only resource projection: it never admits an attempt or imports
// legacy storage, and a project grant is checked independently of chat access.
func (r *Router) projectInputOptions(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, 401, "Authentication required")
		return
	}
	workspace, chat := WorkspaceIDFromContext(req.Context()), req.PathValue("chatId")
	search := strings.TrimSpace(req.URL.Query().Get("search"))
	if len(search) > 128 {
		replyError(w, 400, "Search is too long")
		return
	}
	member, err := r.projectInputAudience(req.Context(), user.ID, workspace, chat)
	if err != nil {
		replyError(w, 404, "Project inputs unavailable")
		return
	}
	rows, err := r.db.QueryContext(req.Context(), `SELECT v.id,v.file_id,v.project_id,v.name,v.revision,v.size_bytes,v.sha256,v.created_at,p.name
 FROM project_file_versions v JOIN project_files f ON f.head_version_id=v.id AND f.id=v.file_id AND f.project_id=v.project_id AND f.workspace_id=v.workspace_id
 JOIN project_file_blobs b ON b.version_id=v.id AND b.workspace_id=v.workspace_id AND b.project_id=v.project_id
 JOIN projects p ON p.id=v.project_id AND p.workspace_id=v.workspace_id
 JOIN access_grants g ON g.member_id=? AND g.resource_kind='project' AND g.project_id=p.id AND g.operation='read'
 WHERE v.workspace_id=? AND v.retired_at IS NULL AND f.retired_at IS NULL
 AND (?='' OR instr(lower(v.name),lower(?))>0 OR instr(lower(p.name),lower(?))>0)
 ORDER BY p.name,v.name,v.id LIMIT 101`, member.ID, workspace, search, search, search)
	if err != nil {
		replyError(w, 503, "Project inputs unavailable")
		return
	}
	result := ProjectInputOptionsResponse{Files: []ProjectInputOption{}}
	for rows.Next() {
		var option ProjectInputOption
		if err = rows.Scan(&option.ID, &option.FileID, &option.ProjectID, &option.Name, &option.Revision, &option.Size, &option.SHA256, &option.CreatedAt, &option.ProjectName); err != nil {
			break
		}
		result.Files = append(result.Files, option)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		replyError(w, 503, "Project inputs unavailable")
		return
	}
	current, err := r.projectInputAudience(req.Context(), user.ID, workspace, chat)
	if err != nil || current.ID != member.ID || current.Revision != member.Revision {
		replyError(w, 404, "Project inputs unavailable")
		return
	}
	store := access.Store{DB: r.db}
	for _, option := range result.Files {
		if store.CheckProjectFile(req.Context(), user.ID, workspace, option.ProjectID, option.ID) != nil {
			replyError(w, 404, "Project inputs unavailable")
			return
		}
	}
	result.HasMore = len(result.Files) > 100
	if result.HasMore {
		result.Files = result.Files[:100]
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (r *Router) projectInputAudience(ctx context.Context, user, workspace, chat string) (access.Membership, error) {
	store := access.Store{DB: r.db}
	member, err := store.Membership(ctx, user, workspace)
	if err != nil || member.Mode != "restricted" {
		return member, access.ErrDenied
	}
	ok, err := chataudience.CanReadInWorkspace(ctx, r.db, chat, user, workspace)
	if err != nil || !ok {
		return member, access.ErrDenied
	}
	var agent string
	err = r.db.QueryRowContext(ctx, `SELECT c.agent_id FROM chats c JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id
 WHERE c.id=? AND c.workspace_id=? AND c.created_by=? AND c.visibility='private' AND COALESCE(c.origin,'CHAT')<>'ROUTINE' AND a.deleted_at IS NULL AND a.restricted_execution_profile='native_api_key'`, chat, workspace, user).Scan(&agent)
	if err != nil || store.Check(ctx, user, workspace, access.Right{Kind: "agent", ID: agent, Operation: "chat"}) != nil {
		return member, access.ErrDenied
	}
	return member, nil
}
