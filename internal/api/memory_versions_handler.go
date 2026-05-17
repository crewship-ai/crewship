package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/memory"
)

// restoreCanonicalPathSafe enforces server-side containment: the
// restore target must land inside the memory root that contains
// blobRoot. blobRoot is {memoryRoot}/versions, so the allowed root is
// filepath.Dir(blobRoot). Empty blobRoot is treated as "no
// containment configured" → reject all restores; this fails closed
// rather than letting a misconfigured server become an arbitrary
// file-overwrite primitive.
func restoreCanonicalPathSafe(canonicalPath, blobRoot string) bool {
	if strings.TrimSpace(canonicalPath) == "" || strings.Contains(canonicalPath, "..") {
		return false
	}
	if blobRoot == "" {
		return false
	}
	memRoot := filepath.Dir(blobRoot)
	absP, err := filepath.Abs(canonicalPath)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(memRoot)
	if err != nil {
		return false
	}
	rootWithSep := filepath.Clean(absRoot) + string(os.PathSeparator)
	return strings.HasPrefix(filepath.Clean(absP)+string(os.PathSeparator), rootWithSep)
}

// MemoryVersionsHandler serves the v90 audit-trail surface over HTTP.
// Same operations the CLI `crewship memory log/show/restore` exposes
// — distinct from MemoryHandler (FTS search over markdown) because
// the audit trail is SQL-on-DB, not filesystem.
//
// All endpoints require workspace context. Restore additionally
// requires OWNER/ADMIN role because it mutates canonical state.
//
// URL layout is deliberately flat to avoid path-traversal noise:
//
//	GET  /api/v1/memory/versions?path=...&limit=...
//	GET  /api/v1/memory/versions/{sha}?path=...
//	POST /api/v1/memory/versions/{sha}/restore
//
// Workspace id is pulled from auth context, NOT the URL — cross-
// workspace probes can't smuggle a foreign id through query params
// because the handler always anchors on the caller's auth ws id.
type MemoryVersionsHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	blobRoot string
}

func NewMemoryVersionsHandler(db *sql.DB, logger *slog.Logger) *MemoryVersionsHandler {
	return &MemoryVersionsHandler{db: db, logger: logger}
}

// SetBlobRoot wires the content-addressed blob directory for the
// restore path — same value the consolidate runner + ProposedHandler
// receive. Empty disables restore (returns 503) and content reads
// only succeed when payload_ref resolves to a still-on-disk blob.
func (h *MemoryVersionsHandler) SetBlobRoot(root string) {
	h.blobRoot = root
}

// List serves GET /api/v1/memory/versions?path=...&limit=...
// Returns the audit chain newest-first for the auth-context workspace.
func (h *MemoryVersionsHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		replyError(w, http.StatusBadRequest, "path query param required")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := memory.LogVersions(r.Context(), h.db, wsID, path, limit)
	if err != nil {
		h.logger.Error("memory versions list failed", "err", err, "workspace_id", wsID, "path", path)
		replyError(w, http.StatusInternalServerError, "list versions failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    path,
		"count":   len(entries),
		"entries": entries,
	})
}

// Show serves GET /api/v1/memory/versions/{sha}?path=... — returns
// the raw blob bytes as application/octet-stream. Pipe-friendly:
// the body IS the historical content; metadata stays in headers
// (X-Memory-Version-Sha, X-Memory-Version-Bytes).
func (h *MemoryVersionsHandler) Show(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	sha := r.PathValue("sha")
	if sha == "" {
		replyError(w, http.StatusBadRequest, "sha required")
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		replyError(w, http.StatusBadRequest, "path query param required")
		return
	}
	content, err := memory.ReadVersion(r.Context(), h.db, wsID, path, sha)
	if err != nil {
		if errors.Is(err, memory.ErrVersionNotFound) {
			replyError(w, http.StatusNotFound, "memory version not found")
			return
		}
		h.logger.Error("memory version show failed", "err", err, "sha", sha)
		replyError(w, http.StatusInternalServerError, "read version failed")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Memory-Version-Sha", sha)
	w.Header().Set("X-Memory-Version-Bytes", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// Restore serves POST /api/v1/memory/versions/{sha}/restore.
// Body: {"path": "...", "canonical_path": "...", "tier": "learned"}.
// OWNER/ADMIN only.
func (h *MemoryVersionsHandler) Restore(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "user required")
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		replyError(w, http.StatusForbidden, "restore requires OWNER or ADMIN role")
		return
	}
	if h.blobRoot == "" {
		replyError(w, http.StatusServiceUnavailable, "memory versioning is not configured on this server")
		return
	}
	sha := r.PathValue("sha")
	if sha == "" {
		replyError(w, http.StatusBadRequest, "sha required")
		return
	}

	var body struct {
		Path          string `json:"path"`
		CanonicalPath string `json:"canonical_path"`
		Tier          string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Path == "" || body.CanonicalPath == "" || body.Tier == "" {
		replyError(w, http.StatusBadRequest, "path, canonical_path, tier required in body")
		return
	}
	tier := memory.Tier(body.Tier)
	if !memory.ValidTier(tier) {
		replyError(w, http.StatusBadRequest, "invalid tier (allowed: agent|crew|workspace|pins|learned)")
		return
	}
	// Server-side path confinement: the HTTP surface lets an OWNER/ADMIN
	// pick the canonical target, but the target must land inside the
	// configured memory root. Without this check a credential leak (or
	// CSRF on the admin endpoint) lets the attacker overwrite any file
	// the server process can write to. The CLI has its own --force
	// bypass; the HTTP API has none — there is no operator intent to
	// "force into /etc" that justifies the risk surface.
	if !restoreCanonicalPathSafe(body.CanonicalPath, h.blobRoot) {
		h.logger.Warn("memory restore rejected: canonical path outside allowed root",
			"workspace_id", wsID, "canonical_path", body.CanonicalPath, "blob_root", h.blobRoot)
		replyError(w, http.StatusBadRequest, "canonical_path must resolve inside the configured memory root")
		return
	}

	res, err := memory.Restore(r.Context(), h.db, body.CanonicalPath, wsID, body.Path, sha, user.ID, h.blobRoot, tier)
	if err != nil {
		if errors.Is(err, memory.ErrVersionNotFound) {
			replyError(w, http.StatusNotFound, "memory version not found")
			return
		}
		h.logger.Error("memory restore failed", "err", err, "sha", sha, "path", body.Path)
		replyError(w, http.StatusInternalServerError, "restore failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id":   wsID,
		"path":           body.Path,
		"canonical_path": body.CanonicalPath,
		"restored_sha":   res.Sha256,
		"new_version_id": res.VersionID,
		"bytes":          res.Bytes,
		"restored_by":    user.ID,
	})
}
