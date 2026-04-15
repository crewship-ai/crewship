package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	dockerclient "github.com/docker/docker/client"

	"github.com/crewship-ai/crewship/internal/backup"
)

// BackupHandler serves the /api/v1/admin/backups endpoints. All routes
// require workspace role OWNER or ADMIN; the router wires authed() +
// wsCtx() in front, but each handler double-checks via canRole to
// avoid accidental downgrades when a caller supplies a stale token.
type BackupHandler struct {
	db           *sql.DB
	logger       *slog.Logger
	dockerClient *dockerclient.Client
	// crewshipVersion is stamped into created manifests so restores on
	// future binaries can report what produced each bundle. Injected by
	// the router from main's build-info; empty string when unknown.
	crewshipVersion string
}

// NewBackupHandler constructs a BackupHandler. dockerClient may be nil
// in test setups; Create/Restore then run in pure-DB mode (useful for
// restoring a workspace that has no crews with containers).
func NewBackupHandler(db *sql.DB, logger *slog.Logger, dockerClient *dockerclient.Client, crewshipVersion string) *BackupHandler {
	return &BackupHandler{db: db, logger: logger, dockerClient: dockerClient, crewshipVersion: crewshipVersion}
}

// createRequest is the JSON body of POST /api/v1/admin/backups.
type createRequest struct {
	Scope      string `json:"scope"` // "crew" or "workspace"
	CrewID     string `json:"crew_id,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	NoEncrypt  bool   `json:"no_encrypt,omitempty"`
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
	if req.Passphrase == "" && !req.NoEncrypt {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "passphrase required unless no_encrypt=true"})
		return
	}

	var ops backup.DockerOps
	if h.dockerClient != nil {
		ops = &backup.MobyDockerOps{Client: h.dockerClient}
	}

	result, err := backup.CreateBackup(ctx, h.db, backup.CreateOptions{
		Scope:             scope,
		WorkspaceID:       workspaceID,
		CrewID:            req.CrewID,
		CrewshipVersion:   h.crewshipVersion,
		Actor:             backup.Actor{UserID: user.ID, Email: user.Email, Role: role},
		Passphrase:        req.Passphrase,
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
	ctx := r.Context()
	_ = ctx
	role := RoleFromContext(r.Context())
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

	var ops backup.DockerOps
	if h.dockerClient != nil {
		ops = &backup.MobyDockerOps{Client: h.dockerClient}
	}

	result, err := backup.RestoreBackup(ctx, h.db, backup.RestoreOptions{
		Path:         req.Path,
		Passphrase:   req.Passphrase,
		AsWorkspace:  req.AsWorkspace,
		AsCrew:       req.AsCrew,
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
		"manifest":      result.Manifest,
		"restored_ws":   result.RestoredWs,
		"crews_count":   result.CrewsCount,
		"rows_inserted": result.RowsInserted,
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
	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	_, _ = io.Copy(w, f)

	if user != nil {
		WriteAuditLog(ctx, h.db, "backup.download", "backup", path, user.ID, workspaceID, nil)
	}
}

// validateBackupPath refuses paths outside DefaultBackupsDir so these
// admin endpoints cannot be coerced into arbitrary-file read/write
// primitives. A future `--allow-external-dir` flag can relax it.
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
	if !strings.HasPrefix(absPath, absDefault+string(filepath.Separator)) {
		return fmt.Errorf("path must live under %s", absDefault)
	}
	return nil
}

// statusForBackupError maps a backup package error to the right HTTP
// status. RBAC denials → 403; agent / lock conflicts → 409; format
// version issues → 400; everything else → 500.
func statusForBackupError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "admin role required"):
		return http.StatusForbidden
	case strings.Contains(msg, "agent") && strings.Contains(msg, "running"):
		return http.StatusConflict
	case strings.Contains(msg, "another backup is already in progress"):
		return http.StatusConflict
	case strings.Contains(msg, "format version"):
		return http.StatusBadRequest
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
