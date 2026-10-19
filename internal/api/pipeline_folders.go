package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"
)

// Routine folders (ODCH-RT-01). A folder is a named, coloured group of
// routines in one workspace; a routine is in at most one. They only organise
// the Routines panel ("Group by Folder") — a folder grants nothing: sharing
// stays per routine, and a folder ACL ("access from the folder") is not built.
//
//	GET    /workspaces/{ws}/pipeline-folders         member   list, with routine counts
//	POST   /workspaces/{ws}/pipeline-folders         MANAGER+ create
//	PATCH  /workspaces/{ws}/pipeline-folders/{id}    MANAGER+ rename / recolour
//	DELETE /workspaces/{ws}/pipeline-folders/{id}    MANAGER+ delete; routines become unfiled
//	POST   /workspaces/{ws}/pipeline-folders/move    MANAGER+ move routines into a folder, or out (folder_id null)

const (
	maxFolderNameLen  = 80
	maxFolderColorLen = 64
	maxFolderMoveSize = 200
)

type routineFolderResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color,omitempty"`
	RoutineCount int    `json:"routine_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func (h *PipelineHandler) folderRow(ctx context.Context, workspaceID, id string) (*routineFolderResponse, error) {
	var f routineFolderResponse
	var color sql.NullString
	err := h.db.QueryRowContext(ctx, `
		SELECT f.id, f.name, f.color, f.created_at, f.updated_at,
		       (SELECT COUNT(*) FROM routine_folder_members m JOIN pipelines p ON p.id = m.pipeline_id AND p.deleted_at IS NULL WHERE m.folder_id = f.id)
		FROM routine_folders f WHERE f.id = ? AND f.workspace_id = ?`, id, workspaceID).
		Scan(&f.ID, &f.Name, &color, &f.CreatedAt, &f.UpdatedAt, &f.RoutineCount)
	f.Color = color.String
	return &f, err
}

// ListFolders GET /api/v1/workspaces/{workspaceId}/pipeline-folders
func (h *PipelineHandler) ListFolders(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT f.id, f.name, f.color, f.created_at, f.updated_at,
		       (SELECT COUNT(*) FROM routine_folder_members m JOIN pipelines p ON p.id = m.pipeline_id AND p.deleted_at IS NULL WHERE m.folder_id = f.id)
		FROM routine_folders f WHERE f.workspace_id = ?
		ORDER BY lower(f.name), f.id`, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "list routine folders", err)
		return
	}
	defer rows.Close()
	out := []routineFolderResponse{}
	for rows.Next() {
		var f routineFolderResponse
		var color sql.NullString
		if err := rows.Scan(&f.ID, &f.Name, &color, &f.CreatedAt, &f.UpdatedAt, &f.RoutineCount); err != nil {
			replyInternalError(w, h.logger, "scan routine folder", err)
			return
		}
		f.Color = color.String
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "list routine folders", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type routineFolderBody struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
}

func validFolderName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && len([]rune(name)) <= maxFolderNameLen
}

// CreateFolder POST /api/v1/workspaces/{workspaceId}/pipeline-folders
func (h *PipelineHandler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	var body routineFolderBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name, ok := validFolderName(derefStr(body.Name))
	if !ok {
		replyError(w, http.StatusBadRequest, "name is required and must be 80 characters or fewer")
		return
	}
	color := strings.TrimSpace(derefStr(body.Color))
	if len(color) > maxFolderColorLen {
		replyError(w, http.StatusBadRequest, "color must be 64 characters or fewer")
		return
	}
	var createdBy any
	if u := UserFromContext(r.Context()); u != nil && u.ID != "" {
		createdBy = u.ID
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO routine_folders (id, workspace_id, name, color, created_by, created_at, updated_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?)`, id, workspaceID, name, color, createdBy, now, now); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, "a folder named \""+name+"\" already exists")
			return
		}
		replyInternalError(w, h.logger, "create routine folder", err)
		return
	}
	f, err := h.folderRow(r.Context(), workspaceID, id)
	if err != nil {
		replyInternalError(w, h.logger, "load routine folder", err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

// UpdateFolder PATCH /api/v1/workspaces/{workspaceId}/pipeline-folders/{id}
// An omitted field is left alone; an empty color clears it.
func (h *PipelineHandler) UpdateFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("id")
	cur, err := h.folderRow(r.Context(), workspaceID, id)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "folder not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load routine folder", err)
		return
	}
	var body routineFolderBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name, color := cur.Name, cur.Color
	if body.Name != nil {
		var ok bool
		if name, ok = validFolderName(*body.Name); !ok {
			replyError(w, http.StatusBadRequest, "name must be 1 to 80 characters")
			return
		}
	}
	if body.Color != nil {
		color = strings.TrimSpace(*body.Color)
		if len(color) > maxFolderColorLen {
			replyError(w, http.StatusBadRequest, "color must be 64 characters or fewer")
			return
		}
	}
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE routine_folders SET name = ?, color = NULLIF(?, ''), updated_at = ? WHERE id = ? AND workspace_id = ?`,
		name, color, time.Now().UTC().Format(time.RFC3339), id, workspaceID); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, "a folder named \""+name+"\" already exists")
			return
		}
		replyInternalError(w, h.logger, "update routine folder", err)
		return
	}
	f, err := h.folderRow(r.Context(), workspaceID, id)
	if err != nil {
		replyInternalError(w, h.logger, "load routine folder", err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// DeleteFolder DELETE /api/v1/workspaces/{workspaceId}/pipeline-folders/{id}
// The routines in it are not deleted; they become unfiled.
func (h *PipelineHandler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("id")
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "delete routine folder", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM routine_folders WHERE id = ? AND workspace_id = ?`, id, workspaceID).Scan(&exists); err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "folder not found")
		return
	} else if err != nil {
		replyInternalError(w, h.logger, "delete routine folder", err)
		return
	}
	// Explicit rather than relying on ON DELETE CASCADE: foreign keys are a
	// per-connection pragma and the unfiling must not depend on it.
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM routine_folder_members WHERE folder_id = ? AND workspace_id = ?`, id, workspaceID); err != nil {
		replyInternalError(w, h.logger, "unfile routines", err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM routine_folders WHERE id = ? AND workspace_id = ?`, id, workspaceID); err != nil {
		replyInternalError(w, h.logger, "delete routine folder", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "delete routine folder", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// routineFolderMoveResult is the body of POST .../pipeline-folders/move.
type routineFolderMoveResult struct {
	Moved    int     `json:"moved"`
	FolderID *string `json:"folder_id"`
}

type moveToFolderBody struct {
	Slugs    []string `json:"slugs"`
	FolderID *string  `json:"folder_id"`
}

// MoveToFolder POST /api/v1/workspaces/{workspaceId}/pipeline-folders/move
// {"slugs":["a","b"],"folder_id":"…"|null}. All-or-nothing: an unknown
// routine or folder changes nothing.
func (h *PipelineHandler) MoveToFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	var body moveToFolderBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Slugs) == 0 || len(body.Slugs) > maxFolderMoveSize {
		replyError(w, http.StatusBadRequest, "slugs must list 1 to 200 routines")
		return
	}
	var folder any
	var folderID *string
	if body.FolderID != nil && *body.FolderID != "" {
		var exists int
		if err := h.db.QueryRowContext(r.Context(), `SELECT 1 FROM routine_folders WHERE id = ? AND workspace_id = ?`, *body.FolderID, workspaceID).Scan(&exists); err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "folder not found")
			return
		} else if err != nil {
			replyInternalError(w, h.logger, "move routines", err)
			return
		}
		folder, folderID = *body.FolderID, body.FolderID
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "move routines", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	moved := 0
	seen := map[string]bool{}
	for _, slug := range body.Slugs {
		if seen[slug] {
			continue
		}
		seen[slug] = true
		var pipelineID string
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM pipelines WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, workspaceID, slug).Scan(&pipelineID); err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "routine not found: "+slug)
			return
		} else if err != nil {
			replyInternalError(w, h.logger, "move routines", err)
			return
		}
		if folder == nil {
			_, err = tx.ExecContext(r.Context(), `DELETE FROM routine_folder_members WHERE pipeline_id = ?`, pipelineID)
		} else {
			_, err = tx.ExecContext(r.Context(), `
				INSERT INTO routine_folder_members (pipeline_id, folder_id, workspace_id) VALUES (?, ?, ?)
				ON CONFLICT (pipeline_id) DO UPDATE SET folder_id = excluded.folder_id`, pipelineID, folder, workspaceID)
		}
		if err != nil {
			replyInternalError(w, h.logger, "move routines", err)
			return
		}
		moved++
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "move routines", err)
		return
	}
	writeJSON(w, http.StatusOK, routineFolderMoveResult{Moved: moved, FolderID: folderID})
}

// enrichPipelineFolders sets folder_id on list rows. Best effort, like the
// other list enrichments: a failed lookup leaves the routines unfiled in the
// response rather than failing the list.
func enrichPipelineFolders(ctx context.Context, db *sql.DB, workspaceID string, rows []pipelineResponse) {
	if len(rows) == 0 || db == nil {
		return
	}
	res, err := db.QueryContext(ctx, `SELECT pipeline_id, folder_id FROM routine_folder_members WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return
	}
	defer res.Close()
	byID := map[string]string{}
	for res.Next() {
		var id, folder string
		if res.Scan(&id, &folder) == nil {
			byID[id] = folder
		}
	}
	for i := range rows {
		rows[i].FolderID = byID[rows[i].ID]
	}
}
