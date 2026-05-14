package api

// Read-only backup endpoints — list, inspect, status, verify, metrics.
// Extracted from backup.go for readability; the handler struct and
// shared helpers stay in the main file.

import (
	"net/http"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func (h *BackupHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}

	type outEntry struct {
		Path          string    `json:"path"`
		FileName      string    `json:"file_name"`
		Size          int64     `json:"size_bytes"`
		Scope         string    `json:"scope"`
		ScopeLevel    string    `json:"scope_level,omitempty"`
		Encrypted     bool      `json:"encrypted"`
		CreatedAt     time.Time `json:"created_at,omitempty"`
		FormatVersion int       `json:"format_version,omitempty"`
	}

	// Prefer the backup_catalog index — since CRE-128 every create/
	// delete keeps it in sync, so a workspace-scoped SELECT is O(rows)
	// instead of O(bundles) filesystem scan + per-file Inspect. Fall
	// back to the filesystem when the catalog is empty (fresh install,
	// pre-migration data, or a startup backfill that hasn't run yet).
	//
	// Reconcile first: prune rows whose backing file disappeared (out-
	// of-band rm, pre-CRE-159 rotate that forgot to sync). Cheap on a
	// local filesystem (one stat per row) and keeps the admin UI from
	// showing entries that would 404 on Verify/Download/Restore.
	if pruned, rerr := backup.ReconcileCatalog(ctx, h.db, workspaceID); rerr != nil {
		h.logger.Warn("backup catalog reconcile failed", "error", rerr)
	} else if len(pruned) > 0 {
		h.logger.Info("backup catalog reconciled", "pruned_count", len(pruned))
	}
	cat, err := backup.ListCatalog(ctx, h.db, workspaceID)
	if err != nil {
		h.logger.Error("backup list catalog", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to list backup catalog")
		return
	}
	if len(cat) > 0 {
		out := make([]outEntry, 0, len(cat))
		for _, e := range cat {
			out = append(out, outEntry{
				Path:          e.FilePath,
				FileName:      filepath.Base(e.FilePath),
				Size:          e.Size,
				Scope:         e.Scope,
				ScopeLevel:    e.ScopeLevel,
				Encrypted:     e.Encrypted,
				CreatedAt:     e.CreatedAt,
				FormatVersion: e.FormatVersion,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out})
		return
	}

	// Legacy / fallback path — scan the filesystem and filter per
	// workspace. Kept so an admin with pre-catalog bundles can still
	// list without a manual backfill step.
	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		h.logger.Error("backup default-dir resolve", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to resolve backup directory")
		return
	}
	entries, err := backup.ListBackups(ctx, dir)
	if err != nil {
		h.logger.Error("backup list disk", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to list backups on disk")
		return
	}
	filtered := entries[:0]
	for _, e := range entries {
		if bundleBelongsToWorkspace(ctx, e.Path, workspaceID) {
			filtered = append(filtered, e)
		}
	}
	entries = filtered

	out := make([]outEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, outEntry{
			Path:          e.Path,
			FileName:      filepath.Base(e.Path),
			Size:          e.Size,
			Scope:         string(e.Scope),
			ScopeLevel:    string(e.ScopeLevel),
			Encrypted:     e.Encrypted,
			CreatedAt:     e.CreatedAt,
			FormatVersion: e.FormatVersion,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// Inspect handles GET /api/v1/admin/backups/inspect?path=…

func (h *BackupHandler) Inspect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		replyError(w, http.StatusBadRequest, "path query param required")
		return
	}
	if err := validateBackupPath(path); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !bundleBelongsToWorkspace(ctx, path, workspaceID) {
		// Either the bundle doesn't exist, failed to inspect, or
		// belongs to a different workspace. Return 404 rather than
		// 403 so we don't confirm the existence of a bundle the
		// caller is not meant to see.
		replyError(w, http.StatusNotFound, "backup not found")
		return
	}
	m, err := backup.Inspect(ctx, path)
	if err != nil {
		// Inspect can fail because the file is gone (the realistic case
		// after validateBackupPath passed) or because the bundle is
		// malformed. Either way the caller doesn't need the raw error
		// — surface "not found" and keep the detail in logs.
		h.logger.Warn("backup inspect", "error", err)
		replyError(w, http.StatusNotFound, "backup not found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// restoreRequest is the body of POST /api/v1/admin/backups/restore.

func (h *BackupHandler) Status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}

	type statusResp struct {
		Held        bool   `json:"held"`
		WorkspaceID string `json:"workspace_id,omitempty"`
		AcquiredBy  string `json:"acquired_by,omitempty"`
		AcquiredAt  string `json:"acquired_at,omitempty"`
		ExpiresAt   string `json:"expires_at,omitempty"`
	}

	var out statusResp
	out.WorkspaceID = workspaceID
	held, err := backup.IsLockHeld(ctx, h.db, workspaceID, time.Now())
	if err != nil {
		h.logger.Error("backup lock status", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to query backup lock status")
		return
	}
	out.Held = held
	if held {
		// Pull the row detail so the CLI can show who / when. Errors
		// here degrade to "held=true with empty fields" rather than a
		// 500 — the lock detection itself is authoritative.
		_ = h.db.QueryRowContext(ctx,
			`SELECT acquired_by, acquired_at, expires_at FROM backup_locks WHERE workspace_id = ?`,
			workspaceID,
		).Scan(&out.AcquiredBy, &out.AcquiredAt, &out.ExpiresAt)
	}
	writeJSON(w, http.StatusOK, out)
}

// Verify handles GET /api/v1/admin/backups/verify?path=…. Opens the
// bundle, verifies the sealed payload SHA-256 against the manifest,
// and returns a VerifyResult JSON. Does NOT decrypt — checksum
// covers sealed bytes so no key is needed. Handy for periodic
// bundle-rot checks ("is my 3-month-old backup still restorable?").

func (h *BackupHandler) Verify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		replyError(w, http.StatusBadRequest, "path query param required")
		return
	}
	if err := validateBackupPath(path); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !bundleBelongsToWorkspace(ctx, path, workspaceID) {
		replyError(w, http.StatusNotFound, "backup not found")
		return
	}
	res, err := backup.Verify(ctx, path)
	if err != nil {
		h.logger.Error("backup verify", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to verify backup")
		return
	}
	errStr := ""
	if res.Err != nil {
		errStr = res.Err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":      res.Valid,
		"size_bytes": res.Size,
		"manifest":   res.Manifest,
		"error":      errStr,
	})
}

// Unlock handles DELETE /api/v1/admin/backups/status. Force-releases
// the advisory lock for the caller's workspace regardless of owner.
// Emergency escape hatch when a crashed backup left a stale lock
// behind and the 1 h TTL has not yet fired.

func (h *BackupHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	if user == nil {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if !backup.IsInstanceOwner(user.Email) {
		replyError(w, http.StatusForbidden, "instance owner required")
		return
	}
	writeJSON(w, http.StatusOK, backup.Snapshot())
}

// statusForBackupError maps a backup package error to the right HTTP
// status using sentinel errors (errors.Is) rather than substring
// matching on the message. When the backup package reworks its
// wording the status codes stay stable.
