package api

// Memory version content endpoint — Iter 8 of the
// memory-hardening series. Pairs with the list endpoint
// (Iter 7) for the complete operator drill-down:
//
//   - stats     (Iter 2): "how much memory per tenant?"
//   - list      (Iter 7): "which rows specifically?"
//   - content   (Iter 8): "what's inside row X?"
//
// Without this endpoint the operator can see that a row
// exists, see its sha256, but cannot inspect what was actually
// written. That gap matters for two workflows: (1) verifying
// the scrubber caught a PII leak (the memory.write_rejected
// journal entry says "PII detected", but the operator needs to
// see the offending bytes to confirm), and (2) compliance
// audits ("show me exactly what version X said on day Y").
//
// Endpoint: GET /api/v1/admin/memory/versions/{id}/content
// Auth:     authed + wsCtx + manage role.
//
// Response: raw blob bytes (NOT JSON-wrapped). Content-Type is
// set from the path extension when recognisable (.md →
// text/markdown), falls back to application/octet-stream so
// the client can't misinterpret bytes. The response includes
// integrity headers:
//
//   X-Memory-Sha256:       <sha256 from memory_versions row>
//   X-Memory-Bytes:        <length from memory_versions row>
//   X-Memory-Tier:         <tier>
//   X-Memory-Path:         <canonical path>
//   X-Memory-Written-At:   <RFC3339>
//   X-Memory-Written-By:   <writer identifier>
//
// Failure modes:
//   - 401 missing workspace
//   - 403 role gate
//   - 400 missing/invalid id
//   - 404 unknown id OR cross-workspace probe (no leak)
//   - 410 row exists, blob file is missing on disk (deleted
//         out-of-band by retention sweep, container rebuild,
//         restore from backup pre-dating the row, etc.)
//   - 413 stored bytes exceed memVersionsContentMaxBytes (10 MB)
//   - 500 sha mismatch (data integrity issue) OR blob path
//         outside the configured blob root (defence-in-depth
//         against a path-traversal write)

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// memVersionsContentMaxBytes caps the response size at 10 MB.
// memory_versions rows are conventionally small (AGENT.md,
// CREW.md, pins.md) and the watcher path enforces its own caps,
// but a defensive ceiling here protects against a corrupted
// row claiming a huge size and stalling the operator's
// browser. 10 MB is well above the largest legitimate file
// (~64 KB) and trivial to download.
const memVersionsContentMaxBytes = 10 * 1024 * 1024

type MemoryVersionsContentHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	blobRoot string
}

// NewMemoryVersionsContentHandler builds the handler against the
// supplied DB + the on-disk blob root. The handler reads files
// directly from blobRoot via os.ReadFile; pass the same root
// the audit watcher / RecordVersion use so the read sees the
// same data the writers wrote.
//
// blobRoot="" disables the handler (every request returns 503).
// Production wires {DataDir.Root}/memory/versions in cmd_start.
func NewMemoryVersionsContentHandler(db *sql.DB, logger *slog.Logger, blobRoot string) *MemoryVersionsContentHandler {
	return &MemoryVersionsContentHandler{db: db, logger: logger, blobRoot: blobRoot}
}

// Content serves GET /api/v1/admin/memory/versions/{id}/content.
func (h *MemoryVersionsContentHandler) Content(w http.ResponseWriter, r *http.Request) {
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
	if h.blobRoot == "" {
		// Versioning was never wired in this deployment. Refuse
		// the request rather than 500 on a nil read — operators
		// running the lite mode get a clear "not configured"
		// signal rather than a confusing 404.
		replyError(w, http.StatusServiceUnavailable, "memory versioning is not configured on this server")
		return
	}
	versionID := strings.TrimSpace(r.PathValue("id"))
	if versionID == "" {
		replyError(w, http.StatusBadRequest, "version id required")
		return
	}

	// Look up the row first — this is the workspace-boundary
	// + existence check. A cross-workspace probe gets the same
	// 404 as a missing id (no existence leak).
	var (
		rowWorkspaceID string
		path           string
		tier           string
		shaStored      string
		bytesStored    int64
		writtenAt      string
		writtenBy      sql.NullString
		payloadRef     string
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT workspace_id, path, tier, sha256, bytes, written_at,
		       COALESCE(written_by, ''), payload_ref
		  FROM memory_versions
		 WHERE id = ?`, versionID,
	).Scan(&rowWorkspaceID, &path, &tier, &shaStored, &bytesStored,
		&writtenAt, &writtenBy, &payloadRef)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "memory version not found")
		return
	}
	if err != nil {
		h.logger.Error("memory content: select", "version_id", versionID, "error", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if rowWorkspaceID != workspaceID {
		// Same 404 as unknown id — no existence leak.
		replyError(w, http.StatusNotFound, "memory version not found")
		return
	}
	if bytesStored > memVersionsContentMaxBytes {
		replyError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("blob size %d exceeds cap %d", bytesStored, memVersionsContentMaxBytes))
		return
	}

	// payload_ref MUST resolve to a real path under blobRoot.
	// A row whose payload_ref points outside that root is a
	// data-integrity bug (the writer should always land
	// under blobRoot/<sha[:2]>/<sha>). Refuse to read in
	// that case — defence-in-depth against a malicious or
	// corrupted INSERT that tries to use payload_ref as a
	// path-traversal vector.
	cleanRoot, err := filepath.Abs(h.blobRoot)
	if err != nil {
		h.logger.Error("memory content: blob root abs", "blob_root", h.blobRoot, "error", err)
		replyError(w, http.StatusInternalServerError, "blob root resolution failed")
		return
	}
	cleanPath, err := filepath.Abs(payloadRef)
	if err != nil {
		h.logger.Error("memory content: payload abs", "payload_ref", payloadRef, "error", err)
		replyError(w, http.StatusInternalServerError, "payload path resolution failed")
		return
	}
	// Use a separator-aware prefix check to defend against
	// /var/blobs vs /var/blobs2 partial matches.
	if !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) && cleanPath != cleanRoot {
		h.logger.Error("memory content: payload outside blob root",
			"version_id", versionID, "payload_ref", payloadRef, "blob_root", cleanRoot)
		replyError(w, http.StatusInternalServerError, "payload path violates blob root boundary")
		return
	}

	content, err := os.ReadFile(cleanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Row outlived its blob — retention sweep deleted
			// the file, container rebuild lost the volume,
			// restore from backup landed without this row's
			// blob, etc. Distinct from 404 (row missing) so
			// operators can tell "we have the audit metadata
			// but not the content" apart from "we have
			// neither".
			replyError(w, http.StatusGone, "blob is missing on disk")
			return
		}
		h.logger.Error("memory content: read blob",
			"version_id", versionID, "payload_ref", cleanPath, "error", err)
		replyError(w, http.StatusInternalServerError, "blob read failed")
		return
	}

	// Verify the sha — if the blob was tampered with after
	// recording, refuse to serve corrupted bytes. The 500
	// (not 200 with a warning header) makes the integrity
	// failure loud; a downstream auditor cannot accidentally
	// quote tampered content.
	sum := sha256.Sum256(content)
	gotSha := hex.EncodeToString(sum[:])
	if gotSha != shaStored {
		h.logger.Error("memory content: sha mismatch",
			"version_id", versionID, "stored", shaStored, "got", gotSha,
			"payload_ref", cleanPath)
		replyError(w, http.StatusInternalServerError, "blob integrity check failed (sha mismatch)")
		return
	}

	// Headers carry the audit metadata so the client can
	// render the content alongside provenance without a
	// second round trip.
	w.Header().Set("X-Memory-Sha256", shaStored)
	w.Header().Set("X-Memory-Bytes", strconv.FormatInt(bytesStored, 10))
	w.Header().Set("X-Memory-Tier", tier)
	w.Header().Set("X-Memory-Path", path)
	w.Header().Set("X-Memory-Written-At", writtenAt)
	if writtenBy.Valid {
		w.Header().Set("X-Memory-Written-By", writtenBy.String)
	}
	// Content-Type from the canonical-path extension — .md
	// gets text/markdown; everything else falls back to
	// octet-stream so the client never auto-renders untrusted
	// bytes as HTML.
	if strings.HasSuffix(strings.ToLower(path), ".md") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// Cache-Control: blobs are content-addressed (sha256 == path),
	// so they're effectively immutable. immutable + 1y is the
	// canonical cache hint for content-addressed assets; the row
	// id in the URL ensures we never collide on a different
	// content+id pair.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(content); err != nil {
		// Body write errors are post-headers — can't change the
		// status. Log so operators can spot client disconnects
		// or write timeouts in aggregate.
		h.logger.Warn("memory content: response write",
			"version_id", versionID, "bytes", len(content), "error", err)
	}
}
