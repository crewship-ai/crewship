package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// BackupHandler serves the /api/v1/admin/backups endpoints. All routes
// require workspace role OWNER or ADMIN; the router wires authed() +
// wsCtx() in front, but each handler double-checks via canRole to
// avoid accidental downgrades when a caller supplies a stale token.
//
// The handler depends on backup.DockerOps (an abstraction implemented
// by backup.MobyDockerOps) rather than the concrete *docker.Client,
// keeping the HTTP layer honest about the provider pattern and
// trivially mockable from unit tests.
type BackupHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	dockerOps backup.DockerOps // nil-safe: Create/Restore fall back to pure-DB mode
	// crewshipVersion is stamped into created manifests so restores on
	// future binaries can report what produced each bundle. Injected by
	// the router from main's build-info; empty string when unknown.
	crewshipVersion string
}

// NewBackupHandler constructs a BackupHandler. dockerOps may be nil
// in test setups; Create/Restore then run in pure-DB mode (useful for
// restoring a workspace that has no crews with containers).
func NewBackupHandler(db *sql.DB, logger *slog.Logger, dockerOps backup.DockerOps, crewshipVersion string) *BackupHandler {
	return &BackupHandler{db: db, logger: logger, dockerOps: dockerOps, crewshipVersion: crewshipVersion}
}

// createRequest is the JSON body of POST /api/v1/admin/backups.
//
// Exactly one of Passphrase, Recipient or NoEncrypt must be set (the
// CLI enforces this before calling). Recipient is an `age1…` X25519
// public key; Passphrase is a user-supplied secret run through scrypt.
type createRequest struct {
	Scope      string `json:"scope"` // "crew" or "workspace"
	CrewID     string `json:"crew_id,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Recipient  string `json:"recipient,omitempty"`
	NoEncrypt  bool   `json:"no_encrypt,omitempty"`
	OutputDir  string `json:"output_dir,omitempty"`
}

type createResponse struct {
	Path          string    `json:"path"`
	Size          int64     `json:"size_bytes"`
	SHA256        string    `json:"payload_sha256"`
	FormatVersion int       `json:"format_version"`
	Scope         string    `json:"scope"`
	CreatedAt     time.Time `json:"created_at"`
	Encrypted     bool      `json:"encrypted"`
}

// Create handles POST /api/v1/admin/backups. Runs the backup inline;
// typical durations are seconds-to-minute so no async job queue yet.
func (h *BackupHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	role := RoleFromContext(ctx)

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}
	if user == nil || workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	scope := backup.Scope(req.Scope)
	if !scope.Valid() || scope == backup.ScopeInstance {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope must be 'crew' or 'workspace'"})
		return
	}
	if scope == backup.ScopeCrew && req.CrewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_id required for crew scope"})
		return
	}
	// Exactly-one encryption selector. Passphrase, Recipient, or
	// NoEncrypt — never multiple.
	encryptionSelectors := 0
	if req.Passphrase != "" {
		encryptionSelectors++
	}
	if req.Recipient != "" {
		encryptionSelectors++
	}
	if req.NoEncrypt {
		encryptionSelectors++
	}
	if encryptionSelectors == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "passphrase, recipient, or no_encrypt=true required"})
		return
	}
	if encryptionSelectors > 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "exactly one of passphrase / recipient / no_encrypt may be supplied"})
		return
	}

	ops := h.dockerOps

	// Explicit passphrase vs recipient wire — no prefix sniffing. CLI
	// and UI send the mode they picked, server parses the matching
	// field. An accidental age1-shaped passphrase no longer gets
	// silently reinterpreted as an X25519 recipient.
	var passphrase string
	var recipients []age.Recipient
	if req.Recipient != "" {
		rec, err := age.ParseX25519Recipient(req.Recipient)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid age1 recipient: " + err.Error()})
			return
		}
		recipients = []age.Recipient{rec}
	} else {
		passphrase = req.Passphrase
	}

	// Constrain custom output directory to live under the default so an
	// admin cannot use POST /backups as a write primitive to arbitrary
	// host paths. validateBackupPath handles the symlink + prefix
	// checks we already use for Inspect / Restore / Download.
	outputDir := req.OutputDir
	if outputDir != "" {
		if err := validateBackupPath(outputDir); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid output_dir: " + err.Error()})
			return
		}
	}

	result, err := backup.CreateBackup(ctx, h.db, backup.CreateOptions{
		Scope:             scope,
		WorkspaceID:       workspaceID,
		CrewID:            req.CrewID,
		OutputDir:         outputDir,
		CrewshipVersion:   h.crewshipVersion,
		Actor:             backup.Actor{UserID: user.ID, Email: user.Email, Role: role},
		Passphrase:        passphrase,
		Recipients:        recipients,
		NoEncrypt:         req.NoEncrypt,
		CrewContainerName: crewContainerNameFunc(),
		DockerOps:         ops,
	})
	if err != nil {
		h.logger.Warn("backup create failed", "error", err, "workspace", workspaceID, "user", user.ID)
		status := statusForBackupError(err)
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	WriteAuditLog(ctx, h.db, "backup.create", "backup", result.Path, user.ID, workspaceID, map[string]interface{}{
		"scope":          string(scope),
		"size_bytes":     result.Size,
		"payload_sha256": result.SHA256,
		"encrypted":      result.Manifest.Encryption.Enabled,
	})

	writeJSON(w, http.StatusCreated, createResponse{
		Path:          result.Path,
		Size:          result.Size,
		SHA256:        result.SHA256,
		FormatVersion: result.Manifest.FormatVersion,
		Scope:         string(result.Manifest.Scope),
		CreatedAt:     result.Manifest.CreatedAt,
		Encrypted:     result.Manifest.Encryption.Enabled,
	})
}

// List handles GET /api/v1/admin/backups.
func (h *BackupHandler) List(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
		return
	}

	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	entries, err := backup.ListBackups(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Filter entries to just this caller's workspace. DefaultBackupsDir
	// is host-shared, so without this filter an admin of workspace A
	// would see bundles from every other workspace on the host.
	filtered := entries[:0]
	for _, e := range entries {
		if bundleBelongsToWorkspace(e.Path, workspaceID) {
			filtered = append(filtered, e)
		}
	}
	entries = filtered

	type outEntry struct {
		Path          string    `json:"path"`
		FileName      string    `json:"file_name"`
		Size          int64     `json:"size_bytes"`
		Scope         string    `json:"scope"`
		Encrypted     bool      `json:"encrypted"`
		CreatedAt     time.Time `json:"created_at,omitempty"`
		FormatVersion int       `json:"format_version,omitempty"`
	}
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
	role := RoleFromContext(r.Context())
	workspaceID := WorkspaceIDFromContext(r.Context())
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
	if !bundleBelongsToWorkspace(path, workspaceID) {
		// Either the bundle doesn't exist, failed to inspect, or
		// belongs to a different workspace. Return 404 rather than
		// 403 so we don't confirm the existence of a bundle the
		// caller is not meant to see.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup not found"})
		return
	}
	m, err := backup.Inspect(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// restoreRequest is the body of POST /api/v1/admin/backups/restore.
type restoreRequest struct {
	Path        string `json:"path"`
	Passphrase  string `json:"passphrase,omitempty"`
	AsWorkspace string `json:"as_workspace,omitempty"`
	AsCrew      string `json:"as_crew,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
}

// Restore handles POST /api/v1/admin/backups/restore.
func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
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
	var req restoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path required"})
		return
	}
	if err := validateBackupPath(req.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !bundleBelongsToWorkspace(req.Path, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup not found"})
		return
	}

	ops := h.dockerOps

	result, err := backup.RestoreBackup(ctx, h.db, backup.RestoreOptions{
		Path:         req.Path,
		Passphrase:   req.Passphrase,
		AsWorkspace:  req.AsWorkspace,
		AsCrew:       req.AsCrew,
		DryRun:       req.DryRun,
		Actor:        backup.Actor{UserID: user.ID, Email: user.Email, Role: role},
		DockerOps:    ops,
		ContainerFor: crewContainerNameFunc(),
	})
	if err != nil {
		h.logger.Warn("backup restore failed", "error", err, "path", req.Path, "user", user.ID)
		writeJSON(w, statusForBackupError(err), map[string]string{"error": err.Error()})
		return
	}

	WriteAuditLog(ctx, h.db, "backup.restore", "backup", req.Path, user.ID, workspaceID, map[string]interface{}{
		"scope":         string(result.Manifest.Scope),
		"crews_count":   result.CrewsCount,
		"rows_inserted": result.RowsInserted,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"manifest":              result.Manifest,
		"restored_ws":           result.RestoredWs,
		"restored_workspace_id": result.RestoredWorkspaceID,
		"crews_count":           result.CrewsCount,
		"rows_inserted":         result.RowsInserted,
		"docker_phase_skipped":  result.DockerPhaseSkipped,
	})
}

// Status handles GET /api/v1/admin/backups/status. Reports whether an
// advisory backup_lock row is currently held for the caller's workspace.
// Useful when Create returns 409 "another backup is already in progress"
// and the admin wants to know who has the lock and when it expires.
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
	role := RoleFromContext(r.Context())
	workspaceID := WorkspaceIDFromContext(r.Context())
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
	if !bundleBelongsToWorkspace(path, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup not found"})
		return
	}
	res, err := backup.Verify(path)
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
	if req.KeepLast <= 0 && req.KeepDays <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one of keep_last or keep_days must be positive"})
		return
	}
	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	deleted, err := backup.Rotate(dir, workspaceID, req.KeepLast, req.KeepDays, req.DryRun)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !req.DryRun {
		for _, p := range deleted {
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
	if !bundleBelongsToWorkspace(path, workspaceID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "backup not found"})
		return
	}
	if err := backup.Delete(path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
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
	if !bundleBelongsToWorkspace(path, workspaceID) {
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
func bundleBelongsToWorkspace(path, workspaceID string) bool {
	if workspaceID == "" {
		return false
	}
	m, err := backup.Inspect(path)
	if err != nil || m == nil {
		return false
	}
	if m.Contents.Workspace != nil && m.Contents.Workspace.ID == workspaceID {
		return true
	}
	// Crew-scope bundles embed the parent workspace in the single
	// crew summary (CrewSummary doesn't carry the ws id directly, so
	// we fall through to the workspace summary match above for MVP).
	return false
}

// validateBackupPath refuses paths outside DefaultBackupsDir so these
// admin endpoints cannot be coerced into arbitrary-file read/write
// primitives. Symlinks are resolved before the prefix check so a
// malicious link under the backups dir (e.g.
// ~/.crewship/backups/evil -> /etc/passwd) cannot bypass the
// boundary once os.Open follows it. A future --allow-external-dir
// flag can relax this once a real use case appears.
func validateBackupPath(path string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("path must not contain '..'")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		return fmt.Errorf("resolve default dir: %w", err)
	}
	absDefault, err := filepath.Abs(defaultDir)
	if err != nil {
		return fmt.Errorf("resolve default dir: %w", err)
	}
	// Reject symlinks outright: a Crewship backup file is always a
	// regular file written by us, never a symlink. Lstat probes the
	// link itself, not its target.
	if info, err := os.Lstat(absPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path must not be a symlink")
		}
	}
	// Resolve symlinks on both sides so the prefix check compares the
	// canonical paths. For a path that does not yet exist (e.g. the
	// final file name during Create), EvalSymlinks errors — walk up
	// the ancestry to the nearest existing directory, resolve that,
	// and re-append the trailing segments. Apple's /var → /private/var
	// redirect is the most common reason this matters.
	resolvedPath := resolveExistingAncestor(absPath)
	resolvedDefault := absDefault
	if rd, err := filepath.EvalSymlinks(absDefault); err == nil {
		resolvedDefault = rd
	}
	if !strings.HasPrefix(resolvedPath, resolvedDefault+string(filepath.Separator)) &&
		resolvedPath != resolvedDefault {
		return fmt.Errorf("path must live under %s", resolvedDefault)
	}
	return nil
}

// resolveExistingAncestor returns the symlink-resolved form of p using
// the closest existing directory on its way up. A leaf that does not
// yet exist still gets a canonical prefix so validateBackupPath can
// compare against a resolved default directory. When every ancestor
// fails EvalSymlinks we return the original absolute path unchanged.
func resolveExistingAncestor(p string) string {
	for dir := p; dir != "/" && dir != "." && dir != ""; dir = filepath.Dir(dir) {
		if rp, err := filepath.EvalSymlinks(dir); err == nil {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return p
			}
			return filepath.Join(rp, rel)
		}
	}
	return p
}

// statusForBackupError maps a backup package error to the right HTTP
// status using sentinel errors (errors.Is) rather than substring
// matching on the message. When the backup package reworks its
// wording the status codes stay stable.
func statusForBackupError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch {
	case errors.Is(err, backup.ErrAdminRequired):
		return http.StatusForbidden
	case errors.Is(err, backup.ErrAgentRunning),
		errors.Is(err, backup.ErrLockHeld):
		return http.StatusConflict
	case errors.Is(err, backup.ErrFormatTooNew),
		errors.Is(err, backup.ErrFormatTooOld),
		errors.Is(err, backup.ErrSchemaTooOld),
		errors.Is(err, backup.ErrInvalidManifest),
		errors.Is(err, backup.ErrInvalidScope):
		return http.StatusBadRequest
	case errors.Is(err, backup.ErrDecryption),
		errors.Is(err, backup.ErrInvalidChecksum):
		return http.StatusBadRequest
	case errors.Is(err, backup.ErrNoOpRestore):
		// The DB was never mutated; surface as 409 so the client sees
		// "nothing to do" rather than "internal error".
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// crewContainerNameFunc returns the slug→container-name mapping used
// by the Docker provider. Hard-coded here to avoid importing
// internal/provider/docker (which would create a dependency cycle).
func crewContainerNameFunc() func(slug string) string {
	return func(slug string) string { return "crewship-team-" + slug }
}
