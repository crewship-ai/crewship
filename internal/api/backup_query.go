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
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}

	type outEntry struct {
		Path          string    `json:"path"`
		FileName      string    `json:"file_name"`
		Size          int64     `json:"size_bytes"`
		Scope         string    `json:"scope"`
		Encrypted     bool      `json:"encrypted"`
		CreatedAt     time.Time `json:"created_at,omitempty"`
		FormatVersion int       `json:"format_version,omitempty"`
	}

	// Prefer the backup_catalog index — since CRE-128 every create/
	// delete keeps it in sync, so a workspace-scoped SELECT is O(rows)
	// instead of O(bundles) filesystem scan + per-file Inspect. Fall
	// back to the filesystem when the catalog is empty (fresh install,
	// pre-migration data, or a startup backfill that hasn't run yet).
	cat, err := backup.ListCatalog(ctx, h.db, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	entries, err := backup.ListBackups(ctx, dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		// Either the bundle doesn't exist, failed to inspect, or
		// belongs to a different workspace. Return 404 rather than
		// 403 so we don't confirm the existence of a bundle the
		// caller is not meant to see.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup not found"})
		return
	}
	m, err := backup.Inspect(ctx, path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace context required"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	res, err := backup.Verify(ctx, path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	if !backup.IsInstanceOwner(user.Email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "instance owner required"})
		return
	}
	writeJSON(w, http.StatusOK, backup.Snapshot())
}

// statusForBackupError maps a backup package error to the right HTTP
// status using sentinel errors (errors.Is) rather than substring
// matching on the message. When the backup package reworks its
// wording the status codes stay stable.
