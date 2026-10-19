package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Routine folders (ODCH-RT-01). A folder is a named, coloured group of
// routines in one workspace; a routine is in at most one. They only organise
// the Routines panel ("Group by Folder") — a folder grants nothing: sharing
// stays per routine, and a folder ACL ("access from the folder") is not built.
// A folder may wear an icon from the crew icon registry and sit inside another
// folder, at most maxFolderDepth levels deep (#3768, the folder's own menu).
//
//	GET    /workspaces/{ws}/pipeline-folders         member   list, with routine counts
//	POST   /workspaces/{ws}/pipeline-folders         MANAGER+ create (optionally inside parent_id)
//	PATCH  /workspaces/{ws}/pipeline-folders/{id}    MANAGER+ rename / recolour / icon / move (parent_id)
//	DELETE /workspaces/{ws}/pipeline-folders/{id}    MANAGER+ delete; routines become unfiled, subfolders move up
//	POST   /workspaces/{ws}/pipeline-folders/move    MANAGER+ move routines into a folder, or out (folder_id null)

const (
	maxFolderNameLen  = 80
	maxFolderColorLen = 64
	maxFolderMoveSize = 200
	// maxFolderDepth counts levels: a top-level folder is 1, its subfolder 2.
	maxFolderDepth = 3
	// folderWalkLimit bounds the recursive walks; a chain longer than any
	// legal tree means a corrupt row, and the walk stops instead of spinning.
	folderWalkLimit = 32
)

type routineFolderResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color,omitempty"`
	Icon         string `json:"icon,omitempty"`
	ParentID     string `json:"parent_id,omitempty"`
	RoutineCount int    `json:"routine_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func (h *PipelineHandler) folderRow(ctx context.Context, workspaceID, id string) (*routineFolderResponse, error) {
	var f routineFolderResponse
	var color, icon, parent sql.NullString
	err := h.db.QueryRowContext(ctx, `
		SELECT f.id, f.name, f.color, f.icon, f.parent_id, f.created_at, f.updated_at,
		       (SELECT COUNT(*) FROM routine_folder_members m JOIN pipelines p ON p.id = m.pipeline_id AND p.deleted_at IS NULL WHERE m.folder_id = f.id)
		FROM routine_folders f WHERE f.id = ? AND f.workspace_id = ?`, id, workspaceID).
		Scan(&f.ID, &f.Name, &color, &icon, &parent, &f.CreatedAt, &f.UpdatedAt, &f.RoutineCount)
	f.Color, f.Icon, f.ParentID = color.String, icon.String, parent.String
	return &f, err
}

type folderQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// folderPlacementError is a refused parent: Status is what the handler answers.
type folderPlacementError struct {
	Status  int
	Message string
}

func (e *folderPlacementError) Error() string { return e.Message }

// checkFolderPlacement says whether folder id (empty while creating) may sit
// inside parentID: the parent exists in this workspace, is not the folder or
// one of its subfolders, and the folder with everything under it still fits
// in maxFolderDepth levels.
func checkFolderPlacement(ctx context.Context, q folderQuerier, workspaceID, id, parentID string) error {
	var parentDepth int
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM routine_folders WHERE id = ? AND workspace_id = ?`, parentID, workspaceID).Scan(&exists); err == sql.ErrNoRows {
		return &folderPlacementError{http.StatusNotFound, "parent folder not found"}
	} else if err != nil {
		return err
	}
	// Levels from the top down to the parent, the parent included.
	if err := q.QueryRowContext(ctx, `
		WITH RECURSIVE up(id, parent_id, d) AS (
			SELECT id, parent_id, 1 FROM routine_folders WHERE id = ? AND workspace_id = ?
			UNION ALL
			SELECT f.id, f.parent_id, up.d + 1 FROM routine_folders f JOIN up ON f.id = up.parent_id
			WHERE f.workspace_id = ? AND up.d < ?
		)
		SELECT MAX(d) FROM up`, parentID, workspaceID, workspaceID, folderWalkLimit).Scan(&parentDepth); err != nil {
		return err
	}
	height := 1
	if id != "" {
		var inside int
		if err := q.QueryRowContext(ctx, `
			WITH RECURSIVE down(id, d) AS (
				SELECT id, 1 FROM routine_folders WHERE id = ? AND workspace_id = ?
				UNION ALL
				SELECT f.id, down.d + 1 FROM routine_folders f JOIN down ON f.parent_id = down.id
				WHERE f.workspace_id = ? AND down.d < ?
			)
			SELECT COALESCE(MAX(d), 1), COALESCE(MAX(id = ?), 0) FROM down`, id, workspaceID, workspaceID, folderWalkLimit, parentID).Scan(&height, &inside); err != nil {
			return err
		}
		if inside == 1 {
			return &folderPlacementError{http.StatusBadRequest, "a folder cannot go inside itself or one of its subfolders"}
		}
	}
	if parentDepth+height > maxFolderDepth {
		return &folderPlacementError{http.StatusBadRequest, "folders nest at most " + strconv.Itoa(maxFolderDepth) + " levels deep"}
	}
	return nil
}

// folderParentOf reads parent_id from a request body: absent → (false), null or
// "" → top level, a string → that folder. Anything else is refused.
func folderParentOf(raw json.RawMessage) (set bool, parentID string, ok bool) {
	if len(raw) == 0 {
		return false, "", true
	}
	if string(raw) == "null" {
		return true, "", true
	}
	if err := json.Unmarshal(raw, &parentID); err != nil {
		return false, "", false
	}
	return true, strings.TrimSpace(parentID), true
}

func replyPlacement(w http.ResponseWriter, h *PipelineHandler, err error) {
	if pe, ok := err.(*folderPlacementError); ok {
		replyError(w, pe.Status, pe.Message)
		return
	}
	replyInternalError(w, h.logger, "place routine folder", err)
}

// ListFolders GET /api/v1/workspaces/{workspaceId}/pipeline-folders
func (h *PipelineHandler) ListFolders(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT f.id, f.name, f.color, f.icon, f.parent_id, f.created_at, f.updated_at,
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
		var color, icon, parent sql.NullString
		if err := rows.Scan(&f.ID, &f.Name, &color, &icon, &parent, &f.CreatedAt, &f.UpdatedAt, &f.RoutineCount); err != nil {
			replyInternalError(w, h.logger, "scan routine folder", err)
			return
		}
		f.Color, f.Icon, f.ParentID = color.String, icon.String, parent.String
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
	Icon  *string `json:"icon"`
	// ParentID is raw so an omitted field (keep) differs from null (top level).
	ParentID json.RawMessage `json:"parent_id"`
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
	icon := strings.TrimSpace(derefStr(body.Icon))
	if !validPageFolderIcon(icon) {
		replyError(w, http.StatusBadRequest, pageFolderIconRefusal(icon))
		return
	}
	_, parentID, ok := folderParentOf(body.ParentID)
	if !ok {
		replyError(w, http.StatusBadRequest, "parent_id must be a folder id or null")
		return
	}
	if parentID != "" {
		if err := checkFolderPlacement(r.Context(), h.db, workspaceID, "", parentID); err != nil {
			replyPlacement(w, h, err)
			return
		}
	}
	var createdBy any
	if u := UserFromContext(r.Context()); u != nil && u.ID != "" {
		createdBy = u.ID
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO routine_folders (id, workspace_id, name, color, icon, parent_id, created_by, created_at, updated_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`, id, workspaceID, name, color, icon, parentID, createdBy, now, now); err != nil {
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
// An omitted field is left alone; an empty color or icon clears it; parent_id
// null or "" moves the folder to the top level.
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
	name, color, icon, parentID := cur.Name, cur.Color, cur.Icon, cur.ParentID
	if body.Icon != nil {
		icon = strings.TrimSpace(*body.Icon)
		if !validPageFolderIcon(icon) {
			replyError(w, http.StatusBadRequest, pageFolderIconRefusal(icon))
			return
		}
	}
	setParent, nextParent, ok := folderParentOf(body.ParentID)
	if !ok {
		replyError(w, http.StatusBadRequest, "parent_id must be a folder id or null")
		return
	}
	if setParent {
		parentID = nextParent
	}
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
	// The placement check and the write share a transaction, so two moves
	// cannot each pass on their own and together make a cycle.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "update routine folder", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if setParent && parentID != "" {
		if err := checkFolderPlacement(r.Context(), tx, workspaceID, id, parentID); err != nil {
			replyPlacement(w, h, err)
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE routine_folders SET name = ?, color = NULLIF(?, ''), icon = NULLIF(?, ''), parent_id = NULLIF(?, ''), updated_at = ? WHERE id = ? AND workspace_id = ?`,
		name, color, icon, parentID, time.Now().UTC().Format(time.RFC3339), id, workspaceID); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, "a folder named \""+name+"\" already exists")
			return
		}
		replyInternalError(w, h.logger, "update routine folder", err)
		return
	}
	if err := tx.Commit(); err != nil {
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
// The routines in it are not deleted; they become unfiled. Its subfolders,
// with what they hold, move up to the deleted folder's own parent.
func (h *PipelineHandler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("id")
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "delete routine folder", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var parent sql.NullString
	if err := tx.QueryRowContext(r.Context(), `SELECT parent_id FROM routine_folders WHERE id = ? AND workspace_id = ?`, id, workspaceID).Scan(&parent); err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "folder not found")
		return
	} else if err != nil {
		replyInternalError(w, h.logger, "delete routine folder", err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE routine_folders SET parent_id = ? WHERE parent_id = ? AND workspace_id = ?`, parent, id, workspaceID); err != nil {
		replyInternalError(w, h.logger, "lift subfolders", err)
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
