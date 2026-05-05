package api

// Backup admin lifecycle operations — Unlock (release stuck advisory
// locks), Rotate (apply retention), Delete, Download, and the
// SelfTest endpoint that validates the whole flow end-to-end.
// Extracted from backup.go for readability.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/backup"
)

func (h *BackupHandler) Unlock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}
	if user == nil || workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	if err := backup.ForceReleaseLock(ctx, h.db, workspaceID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	WriteAuditLog(ctx, h.db, "backup.unlock", "backup", workspaceID, user.ID, workspaceID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// rotateRequest is the body of POST /api/v1/admin/backups/rotate.

type rotateRequest struct {
	KeepLast int  `json:"keep_last,omitempty"`
	KeepDays int  `json:"keep_days,omitempty"`
	DryRun   bool `json:"dry_run,omitempty"`
}

// Rotate handles POST /api/v1/admin/backups/rotate. Applies retention
// policy (by count and/or age) to the caller's workspace bundles.
// DryRun returns the list of paths that WOULD be deleted without
// touching disk.

func (h *BackupHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}
	if user == nil || workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	var req rotateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	// Reject negatives explicitly. 0 disables a rule (documented);
	// a caller passing -1 otherwise slipped past the "at least one
	// positive" gate with a positive counterpart and then fed the
	// negative into Rotate, producing undefined behaviour.
	if req.KeepLast < 0 || req.KeepDays < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keep_last and keep_days must be >= 0 (0 disables the rule)"})
		return
	}
	if req.KeepLast == 0 && req.KeepDays == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one of keep_last or keep_days must be positive"})
		return
	}
	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	deleted, err := backup.Rotate(ctx, dir, workspaceID, req.KeepLast, req.KeepDays, req.DryRun)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !req.DryRun {
		for _, p := range deleted {
			// Sync the catalog with the rotation. Without this the
			// admin UI's list view (which prefers backup_catalog over
			// a fresh filesystem walk) keeps showing rotated bundles
			// that 404 on Verify/Download/Restore. Best-effort: a
			// failed catalog delete does not undo the file removal,
			// the next List call's reconcile pass picks up the slack.
			if cerr := backup.DeleteCatalogEntry(ctx, h.db, p); cerr != nil {
				h.logger.Warn("backup catalog delete after rotate failed", "error", cerr, "path", p)
			}
			WriteAuditLog(ctx, h.db, "backup.rotate", "backup", p, user.ID, workspaceID, map[string]interface{}{
				"keep_last": req.KeepLast,
				"keep_days": req.KeepDays,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
		"dry_run": req.DryRun,
	})
}

// Delete handles DELETE /api/v1/admin/backups?path=…

func (h *BackupHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query param required"})
		return
	}
	if err := validateBackupPath(path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !bundleBelongsToWorkspace(ctx, path, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup not found"})
		return
	}
	if err := backup.Delete(ctx, path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Drop the catalog row too so the admin UI list view refreshes
	// cleanly. A best-effort failure here is not fatal — the bundle is
	// gone from disk; a stale row would surface on the next refresh
	// (ListCatalog) and either get ignored by the UI or re-resolved by
	// the startup backfill scan.
	if err := backup.DeleteCatalogEntry(ctx, h.db, path); err != nil {
		h.logger.Warn("backup catalog delete failed", "error", err, "path", path)
	}
	WriteAuditLog(ctx, h.db, "backup.delete", "backup", path, user.ID, workspaceID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// Download handles GET /api/v1/admin/backups/download?path=…. Streams
// the raw bundle bytes so the admin can `scp`-like pull from a remote
// Crewship install.

func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path query param required"})
		return
	}
	if err := validateBackupPath(path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !bundleBelongsToWorkspace(ctx, path, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup not found"})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Bundle bytes contain sensitive workspace contents (even encrypted,
	// the metadata is plaintext). Disable proxy / browser caching so a
	// compromised intermediary does not retain a copy after download.
	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, f)

	if user != nil {
		WriteAuditLog(ctx, h.db, "backup.download", "backup", path, user.ID, workspaceID, nil)
	}
}

// bundleBelongsToWorkspace reports whether the bundle at path was
// produced for (or contains) the given workspace. Used by the
// per-endpoint authZ check so admin of workspace A cannot inspect,
// download, restore or delete bundles of workspace B even though
// every bundle lives in the shared DefaultBackupsDir.
//
// A bundle with no workspace in its manifest (e.g. a future
// instance-scope bundle) returns false — instance admin endpoints
// will handle those separately once CRE-129 lands.

type selfTestRequest struct {
	CrewID string `json:"crew_id"`
}

// SelfTest handles POST /api/v1/admin/backups/self-test. Runs the canary
// round-trip server-side so the seed CLI (and future CI harness) can
// validate the backup pipeline end-to-end without depending on a
// bundle-on-disk CLI roundtrip. Lightweight: no encryption, no DB dump,
// just collect → destroy canary → restore → verify → cleanup.
//
// Admin-only. The BackupHandler's DockerOps must be wired; otherwise
// the endpoint 503s since there's nothing to talk to.

func (h *BackupHandler) SelfTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	if h.dockerOps == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "backup self-test unavailable: docker not configured",
		})
		return
	}

	var req selfTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	crewID := strings.TrimSpace(req.CrewID)
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_id required"})
		return
	}

	// Resolve crew. The row must belong to the caller's workspace so an
	// ADMIN in workspace A can't self-test crews in workspace B.
	var crewSlug string
	err := h.db.QueryRowContext(ctx, `
		SELECT slug FROM crews
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL
	`, crewID, workspaceID).Scan(&crewSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
			return
		}
		h.logger.Error("backup self-test: lookup crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	containerID := h.resolveCrewContainerName()(crewSlug)
	result, err := backup.BackupSelfTest(ctx, h.dockerOps, backup.SelfTestOpts{
		ContainerID: containerID,
		Crew: backup.CrewTarget{
			ID:          crewID,
			Slug:        crewSlug,
			ContainerID: containerID,
		},
	})
	if err != nil {
		h.logger.Error("backup self-test: pipeline", "crew_id", crewID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Happy and content-mismatch paths both return 200 with the result
	// body — the "ok" field tells the caller what happened.
	writeJSON(w, http.StatusOK, result)
}
