package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

const maxFolderTreeDepth = 8
const maxFolderMoveNodes = 256

// Every tree mutation takes this workspace writer fence before evaluating live
// membership or graph shape. Parent ACL is never inherited by descendants.
func (h *PageHandler) beginFolderTree(w http.ResponseWriter, r *http.Request) (*sql.Tx, *http.Request, *pageViewer, int64, bool) {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return nil, r, nil, 0, false
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin folder tree", err)
		return nil, r, nil, 0, false
	}
	fail := func() { _ = tx.Rollback() }
	ws := WorkspaceIDFromContext(r.Context())
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_folder_structure(workspace_id,version) VALUES(?,0) ON CONFLICT(workspace_id) DO UPDATE SET version=version`, ws); err != nil {
		fail()
		replyInternalError(w, h.logger, "fence folder tree", err)
		return nil, r, nil, 0, false
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	role, err := h.liveContentHumanRole(ctx, ws, user.ID)
	if err != nil {
		fail()
		replyError(w, 403, "active trusted workspace membership required")
		return nil, r, nil, 0, false
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	r = r.WithContext(ctx)
	viewer, err := h.loadViewer(ctx, ws, user.ID)
	if err != nil {
		fail()
		replyInternalError(w, h.logger, "authorize folder tree", err)
		return nil, r, nil, 0, false
	}
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM page_folder_structure WHERE workspace_id=?`, ws).Scan(&version); err != nil {
		fail()
		replyInternalError(w, h.logger, "read folder tree fence", err)
		return nil, r, nil, 0, false
	}
	return tx, r, viewer, version, true
}
func folderTreeConflict(w http.ResponseWriter, version int64) {
	writeJSON(w, 409, map[string]any{"error": "folder structure changed; refresh and confirm again", "tree_version": version})
}
func (h *PageHandler) folderTreeParent(w http.ResponseWriter, r *http.Request, viewer *pageViewer, slug string, aclVersion *int64) (*pageFolderRecord, bool) {
	f, err := h.loadFolder(r.Context(), WorkspaceIDFromContext(r.Context()), slug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "parent folder not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load parent folder", err)
		return nil, false
	}
	acl, err := h.loadFolderACL(r.Context(), f.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read parent permissions", err)
		return nil, false
	}
	// Arrangement implies visibility; do not enumerate inaccessible parent rows.
	if !folderStanding(viewer, f, acl) {
		replyError(w, 404, "parent folder not found")
		return nil, false
	}
	if !mayArrangeFolder(viewer, f, acl) {
		replyError(w, 403, "parent folder arrangement permission required")
		return nil, false
	}
	if aclVersion == nil || *aclVersion < 0 {
		replyError(w, 400, "parent acl_version is required")
		return nil, false
	}
	if *aclVersion != f.ACLVersion {
		writeJSON(w, 409, map[string]any{"error": "parent permissions changed; refresh and confirm again", "acl_version": f.ACLVersion})
		return nil, false
	}
	return f, true
}

// Limits use all descendants, including those not visible to the caller. Errors
// contain no ancestor IDs, labels, paths or hidden-child counts.
func (h *PageHandler) validateFolderTreeMove(ctx context.Context, source, parent string) error {
	q := h.pageAuthReader(ctx)
	ws := WorkspaceIDFromContext(ctx)
	parentDepth := 0
	if parent != "" {
		rows, err := q.QueryContext(ctx, `WITH RECURSIVE chain(id,parent_id,depth) AS (SELECT id,parent_id,1 FROM page_folders WHERE id=? AND workspace_id=? UNION ALL SELECT f.id,f.parent_id,c.depth+1 FROM page_folders f JOIN chain c ON f.id=c.parent_id WHERE c.depth<9 AND f.workspace_id=?) SELECT id,depth FROM chain LIMIT 10`, parent, ws, ws)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for rows.Next() {
			var id string
			var depth int
			if err = rows.Scan(&id, &depth); err != nil {
				rows.Close()
				return err
			}
			if id == source || seen[id] {
				rows.Close()
				return errors.New("folder parent would create a cycle")
			}
			seen[id] = true
			if depth > parentDepth {
				parentDepth = depth
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if parentDepth == 0 {
			return errors.New("parent folder unavailable")
		}
		if parentDepth >= maxFolderTreeDepth {
			return errors.New("folder tree depth exceeds supported limit")
		}
	}
	height := 1
	if source != "" {
		rows, err := q.QueryContext(ctx, `WITH RECURSIVE subtree(id,depth) AS (SELECT id,1 FROM page_folders WHERE id=? AND workspace_id=? UNION ALL SELECT f.id,s.depth+1 FROM page_folders f JOIN subtree s ON f.parent_id=s.id WHERE s.depth<9 AND f.workspace_id=?) SELECT id,depth FROM subtree LIMIT 257`, source, ws, ws)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for rows.Next() {
			var id string
			var depth int
			if err = rows.Scan(&id, &depth); err != nil {
				rows.Close()
				return err
			}
			if seen[id] {
				rows.Close()
				return errors.New("folder tree is invalid")
			}
			seen[id] = true
			if len(seen) > maxFolderMoveNodes {
				rows.Close()
				return errors.New("folder subtree exceeds supported move budget")
			}
			if depth > height {
				height = depth
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if parentDepth+height > maxFolderTreeDepth {
		return errors.New("folder tree depth exceeds supported limit")
	}
	return nil
}
func bumpFolderTree(ctx context.Context, tx *sql.Tx, ws string) error {
	_, err := tx.ExecContext(ctx, `UPDATE page_folder_structure SET version=version+1 WHERE workspace_id=?`, ws)
	return err
}

type folderParentRequest struct {
	Parent      json.RawMessage `json:"parent"`
	TreeVersion *int64          `json:"tree_version"`
	ACLVersion  *int64          `json:"acl_version"`
}

func (h *PageHandler) ReparentFolder(w http.ResponseWriter, r *http.Request) {
	var req folderParentRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || len(req.Parent) == 0 || req.TreeVersion == nil || *req.TreeVersion < 0 {
		replyError(w, 400, "parent (slug or null) and nonnegative tree_version are required")
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		replyError(w, 400, "one parent request object is required")
		return
	}
	var parentSlug string
	if strings.TrimSpace(string(req.Parent)) != "null" {
		if err := json.Unmarshal(req.Parent, &parentSlug); err != nil || strings.TrimSpace(parentSlug) == "" {
			replyError(w, 400, "parent must be a folder slug or null")
			return
		}
		parentSlug = strings.TrimSpace(parentSlug)
	}
	tx, r, viewer, version, ok := h.beginFolderTree(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	ws := WorkspaceIDFromContext(r.Context())
	source, err := h.loadFolder(r.Context(), ws, r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "folder not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load moved folder", err)
		return
	}
	acl, err := h.loadFolderACL(r.Context(), source.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read moved folder permissions", err)
		return
	}
	if !folderStanding(viewer, source, acl) {
		replyError(w, 404, "folder not found")
		return
	}
	if !mayAdministerFolder(viewer, source) {
		replyError(w, 403, "moving a folder requires its crew manager or workspace administrator")
		return
	}
	var parentID string
	if parentSlug != "" {
		parent, ok := h.folderTreeParent(w, r, viewer, parentSlug, req.ACLVersion)
		if !ok {
			return
		}
		parentID = parent.ID
	}
	if version != *req.TreeVersion {
		folderTreeConflict(w, version)
		return
	}
	if err = h.validateFolderTreeMove(r.Context(), source.ID, parentID); err != nil {
		replyError(w, 409, err.Error())
		return
	}
	changed := source.ParentID != parentID
	if changed {
		if _, err = tx.ExecContext(r.Context(), `UPDATE page_folders SET parent_id=NULLIF(?,'') WHERE id=?`, parentID, source.ID); err != nil {
			replyInternalError(w, h.logger, "move folder parent", err)
			return
		}
		if err = bumpFolderTree(r.Context(), tx, ws); err != nil {
			replyInternalError(w, h.logger, "bump folder structure", err)
			return
		}
		version++
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit folder parent", err)
		return
	}
	source.ParentID = parentID
	source.TreeVersion = version
	if changed {
		// Both activity and realtime invalidation are deliberately content-free:
		// workspace subscriptions do not imply access to a private folder.
		if h.journal != nil {
			_, err := h.journal.Emit(r.Context(), journal.Entry{WorkspaceID: ws, Type: entryPageFolderTreeChanged, Severity: journal.SeverityInfo, ActorType: journal.ActorUser, ActorID: UserFromContext(r.Context()).ID, Summary: "Page folder organization changed", Payload: map[string]any{"tree_changed": true}})
			if err != nil {
				h.logger.Warn("journal folder tree change", "error", err)
			}
		}
		broadcastWorkspaceEvent(h.hub, ws, pageFolderEvent, folderTreeInvalidation())
	}
	writeJSON(w, 200, map[string]any{"tree_version": version})
}

const entryPageFolderTreeChanged journal.EntryType = "page.folder_tree_changed"

func folderTreeInvalidation() map[string]any { return map[string]any{"tree_changed": true} }
