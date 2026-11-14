package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Folder access for the agents of the folder's owning crew (#5317 D2).
//
// One setting per folder (page_folders.agent_access), not a page_folder_acl
// row, so that table keeps "no subject is an agent" and there is only ever one
// such row. The level is none, read or propose (suggest), never write: a
// folder has no issuer whose standing could be re-checked at every use. It
// takes effect only with the workspace flag artifact_agent_access_modes on,
// only for agents whose live crew owns the folder, only on non-private pages,
// and only through a chat run's standing path, where the run's human must
// still read the page as a member (pages_artifact_run_enforce.go).
//
// Who may change it is who may change the folder's permissions: a workspace
// admin or a MANAGER+ of the owning crew. The change is fenced on acl_version,
// bumps it, and is audited in the same transaction.

const entryPageFolderAgentAccessChanged journal.EntryType = "page.folder_agent_access_changed"

const (
	folderAgentAccessNone    = "none"
	folderAgentAccessRead    = "read"
	folderAgentAccessPropose = "propose"
)

type folderAgentAccessDocument struct {
	Folder      string `json:"folder"`
	AgentAccess string `json:"agent_access"`
	ACLVersion  int64  `json:"acl_version"`
	// Available is true where the setting takes effect (flag on).
	Available bool `json:"agent_access_available"`
}

func (h *PageHandler) folderAgentAccess(ctx context.Context, folderID string) (string, error) {
	var level string
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT agent_access FROM page_folders WHERE id=?`, folderID).Scan(&level)
	return level, err
}

// PutFolderAgentAccess — PUT /api/v1/page-folders/{slug}/agent-access.
func (h *PageHandler) PutFolderAgentAccess(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, _, ok := h.folderACLOrNotFound(w, r, wsID, viewer, "changing")
	if !ok {
		return
	}
	body, ok := readCapped(w, r, 4<<10, "folder agent access request")
	if !ok {
		return
	}
	var req struct {
		Level      string `json:"level"`
		ACLVersion *int64 `json:"acl_version"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	level := strings.ToLower(strings.TrimSpace(req.Level))
	switch level {
	case folderAgentAccessNone, folderAgentAccessRead, folderAgentAccessPropose:
	default:
		replyError(w, http.StatusBadRequest, `level must be "none", "read" or "propose"; a folder never gives its agents edit access, which takes a grant on the page from its owner`)
		return
	}
	if req.ACLVersion == nil {
		replyError(w, http.StatusBadRequest, "acl_version is required: read the folder's permissions first (crewship page folder acl)")
		return
	}
	if level != folderAgentAccessNone && !h.artifactRunModesOn(r.Context(), wsID) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "folder access for agents needs the workspace feature flag artifact_agent_access_modes; only level none can be set without it",
			"code":  "artifact_agent_access_modes_off",
		})
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin folder agent access change", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	f, _, err = h.folderACLMutationIn(r.Context(), tx, wsID, user.ID, f.ID)
	if errors.Is(err, access.ErrDenied) {
		replyError(w, http.StatusForbidden, "current folder administrator required")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "authorize folder agent access change", err)
		return
	}
	if f.ACLVersion != *req.ACLVersion {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":       fmt.Sprintf("folder %q changed since acl_version %d; read its permissions again", f.Slug, *req.ACLVersion),
			"conflict":    "acl_version",
			"acl_version": f.ACLVersion,
		})
		return
	}
	var previous string
	if err := tx.QueryRowContext(r.Context(), `SELECT agent_access FROM page_folders WHERE id=?`, f.ID).Scan(&previous); err != nil {
		replyInternalError(w, h.logger, "read folder agent access", err)
		return
	}
	now := h.evaluator().Now().UTC().Format("2006-01-02T15:04:05Z07:00")
	res, err := tx.ExecContext(r.Context(), `UPDATE page_folders SET agent_access=?, acl_version=acl_version+1, updated_at=? WHERE id=? AND acl_version=?`, level, now, f.ID, f.ACLVersion)
	if err != nil {
		replyInternalError(w, h.logger, "set folder agent access", err)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "folder changed concurrently; read its permissions again", "conflict": "acl_version"})
		return
	}
	f.ACLVersion++
	f.UpdatedAt = now
	notify, err := appendAuthorizationAudit(r.Context(), tx, h.journal, journal.Entry{
		WorkspaceID: wsID,
		Type:        entryPageFolderAgentAccessChanged,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     user.ID,
		Summary:     fmt.Sprintf("%s set access for the agents of crew/%s on page folder %s: %s (was %s)", user.Email, f.OwnerCrewSlug, f.Slug, level, previous),
		Payload: map[string]any{
			"folder": f.Slug, "folder_id": f.ID, "owner_crew_id": f.OwnerCrewID,
			"agent_access": level, "previous_agent_access": previous,
			"acl_version": f.ACLVersion, "previous_acl_version": f.ACLVersion - 1, "actor_user_id": user.ID,
		},
	})
	if err != nil {
		replyInternalError(w, h.logger, "audit folder agent access change", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit folder agent access change", err)
		return
	}
	if notify != nil {
		notify()
	}
	broadcastWorkspaceEvent(h.hub, wsID, pageFolderEvent, map[string]any{"slug": f.Slug, "folder_id": f.ID})
	writeJSON(w, http.StatusOK, folderAgentAccessDocument{Folder: f.Slug, AgentAccess: level, ACLVersion: f.ACLVersion, Available: h.artifactRunModesOn(r.Context(), wsID)})
}
