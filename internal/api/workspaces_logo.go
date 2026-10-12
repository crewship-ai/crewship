package api

// Workspace logo upload / serve / clear (#3005), the workspace's counterpart
// of the profile picture (users_avatar.go) and held to the same rules: PNG,
// JPEG or WebP, at most 2MB and maxAvatarDimension a side.
//
// Storage: one file per workspace at <storageRoot>/workspace-logos/<id> (no
// extension; the type is sniffed again on serve). logo_url points at the
// serve route with a ?v=<nanos> cache buster so a replaced logo refreshes.
// Setting or clearing it is ADMIN+ (the same tier as renaming the
// workspace); the serve route is authed only, because a workspace is drawn
// to people outside it too (an instance admin's lists, the switcher).

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SetStorageRoot sets where uploaded workspace logos are kept (the router's
// storagePath). Empty leaves the logo endpoints failing closed.
func (h *WorkspaceHandler) SetStorageRoot(root string) { h.storageRoot = root }

func (h *WorkspaceHandler) logoFilePath(workspaceID string) (string, bool) {
	if h.storageRoot == "" || workspaceID == "" {
		return "", false
	}
	if strings.ContainsAny(workspaceID, `/\`) || strings.Contains(workspaceID, "..") {
		return "", false
	}
	return filepath.Join(h.storageRoot, "workspace-logos", workspaceID), true
}

// UploadLogo stores the workspace's logo and points logo_url at it.
// POST /api/v1/workspaces/{workspaceId}/logo (multipart, field "file").
func (h *WorkspaceHandler) UploadLogo(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	path, ok := h.logoFilePath(workspaceID)
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "logo storage is not configured")
		return
	}
	data, ok := readUploadedImage(w, r)
	if !ok {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		replyInternalError(w, h.logger, "workspace logo mkdir", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		replyInternalError(w, h.logger, "workspace logo write", err)
		return
	}
	url := fmt.Sprintf("/api/v1/workspaces/%s/logo?v=%d", workspaceID, time.Now().UnixNano())
	h.setLogoURL(w, r, workspaceID, &url, "workspace.logo.update")
}

// DeleteLogo clears the workspace's logo back to initials.
// DELETE /api/v1/workspaces/{workspaceId}/logo
func (h *WorkspaceHandler) DeleteLogo(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if path, ok := h.logoFilePath(workspaceID); ok {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			h.logger.Warn("workspace logo remove file", "error", err, "workspace_id", workspaceID)
		}
	}
	h.setLogoURL(w, r, workspaceID, nil, "workspace.logo.remove")
}

func (h *WorkspaceHandler) setLogoURL(w http.ResponseWriter, r *http.Request, workspaceID string, url *string, action string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE workspaces SET logo_url = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL", url, now, workspaceID); err != nil {
		replyInternalError(w, h.logger, "workspace logo update url", err)
		return
	}
	ws, err := h.workspaceByID(r.Context(), workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "get workspace after logo change", err)
		return
	}
	auditFromRequest(r, h.db, action, "WORKSPACE", workspaceID, map[string]interface{}{"fields": []string{"logo_url"}})
	broadcastWorkspaceEvent(h.hub, workspaceID, "workspace.updated", map[string]string{"workspace_id": workspaceID})
	writeJSON(w, http.StatusOK, ws)
}

// ServeLogo streams a workspace's stored logo.
// GET /api/v1/workspaces/{workspaceId}/logo — authed; ?v= is ignored.
func (h *WorkspaceHandler) ServeLogo(w http.ResponseWriter, r *http.Request) {
	path, ok := h.logoFilePath(r.PathValue("workspaceId"))
	if !ok {
		replyError(w, http.StatusNotFound, "logo not found")
		return
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		replyError(w, http.StatusNotFound, "logo not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "workspace logo read", err)
		return
	}
	ct := http.DetectContentType(data)
	if !allowedAvatarContentType(ct) {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
