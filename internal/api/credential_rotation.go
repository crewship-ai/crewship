package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// Credential rotation with grace overlap — the biggest enterprise
// differentiator on this surface (CONNECTIONS.md §7.1, MUST-add #1).
//
// GitLab's pattern: new token issued, original becomes inactive
// immediately. Crewship goes one better: configurable grace overlap
// (default 24h, range 0..7d) so dependent crews can drain in-flight
// runs that cached the old value at start.
//
// Lifecycle of a credential_rotations row:
//
//	ACTIVE   ─ rotated_at..expires_at ─→ EXPIRED (cron scrubs old_value)
//	   │
//	   └─ user clicks "End grace early" ─→ CANCELLED (handler scrubs old_value)
//
// During the ACTIVE window:
//   - credentials.encrypted_value already holds the NEW value (so all
//     fresh agent starts pick up the new key from injection time)
//   - the old value is reachable via credential_rotations.old_value
//     for the sidecar fallback path on 401 (see // TODO sidecar in
//     this file — wired by a follow-up ticket; the data layer is
//     ready)

const (
	// Default grace window matches the CONNECTIONS.md §7.1 wireframe
	// recommendation. Custom grace must be 0..maxGraceSeconds — a 7d
	// ceiling is a security sanity check; longer overlaps shouldn't
	// happen in practice and would defeat the point of rotation.
	defaultGraceSeconds = 24 * 60 * 60
	maxGraceSeconds     = 7 * 24 * 60 * 60

	// rotationExpiryInterval is how often the background worker
	// scans for ACTIVE rotations whose expires_at has passed and
	// scrubs old_value. 1h matches the Doppler/GitLab cron cadence
	// and is forgiving of clock skew.
	rotationExpiryInterval = 1 * time.Hour
)

type credentialRotateRequest struct {
	Value        string `json:"value"`
	GraceSeconds *int   `json:"grace_seconds"`
}

type rotationResponse struct {
	ID           string  `json:"id"`
	CredentialID string  `json:"credential_id"`
	GraceSeconds int     `json:"grace_seconds"`
	RotatedAt    string  `json:"rotated_at"`
	ExpiresAt    string  `json:"expires_at"`
	RotatedBy    string  `json:"rotated_by"`
	Status       string  `json:"status"`
	OldValueGone bool    `json:"old_value_gone"`
	CancelledAt  *string `json:"cancelled_at,omitempty"`
}

// Rotate issues a new value for a credential and starts a grace
// overlap window. The old encrypted value is moved to
// credential_rotations.old_value so the sidecar can fall back during
// in-flight runs.
//
// POST /api/v1/credentials/{credentialId}/rotate
func (h *CredentialHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var req credentialRotateRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Value) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "value required"})
		return
	}

	graceSec := defaultGraceSeconds
	if req.GraceSeconds != nil {
		if *req.GraceSeconds < 0 || *req.GraceSeconds > maxGraceSeconds {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("grace_seconds must be 0..%d", maxGraceSeconds),
			})
			return
		}
		graceSec = *req.GraceSeconds
	}

	// Read the current encrypted value inside the same workspace
	// scope check so cross-workspace rotation attempts 404. The
	// existing soft-delete filter applies — rotating a deleted
	// credential makes no sense.
	var oldEncrypted string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT encrypted_value FROM credentials
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, workspaceID).Scan(&oldEncrypted)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
			return
		}
		h.logger.Error("read credential for rotate", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	newEncrypted, err := encryption.Encrypt(req.Value)
	if err != nil {
		h.logger.Error("encrypt rotated value", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt credential"})
		return
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	expiresAt := now.Add(time.Duration(graceSec) * time.Second).Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin rotate tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			h.logger.Warn("rotate tx rollback", "error", rbErr)
		}
	}()

	rotationID := generateCUID()
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO credential_rotations (id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'ACTIVE')`,
		rotationID, credID, oldEncrypted, graceSec, nowStr, expiresAt, user.ID); err != nil {
		h.logger.Error("insert rotation row", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE credentials SET encrypted_value = ?, status = 'ACTIVE', last_error = NULL,
		                       updated_at = ?
		WHERE id = ?`,
		newEncrypted, nowStr, credID); err != nil {
		h.logger.Error("update credential value", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit rotate tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Audit goes outside the rotate tx so a slow audit insert can't
	// roll back the rotation itself. Failures here are logged but
	// don't 5xx the rotation.
	if err := RecordCredentialEvent(r.Context(), h.db, h.logger, credID, AuditEventRotate, "", clientIP(r),
		map[string]any{"rotation_id": rotationID, "grace_seconds": graceSec, "rotated_by": user.ID}); err != nil {
		h.logger.Warn("record rotation audit event", "error", err, "credential_id", credID)
	}

	// TODO(sidecar-fallback): a follow-up ticket wires the sidecar's
	// 401-fallback path. The data layer is ready: during the ACTIVE
	// window, sidecar can `SELECT old_value FROM credential_rotations
	// WHERE credential_id = ? AND status = 'ACTIVE' AND expires_at > now()`
	// and retry the upstream call once with the previous value.

	writeJSON(w, http.StatusOK, rotationResponse{
		ID:           rotationID,
		CredentialID: credID,
		GraceSeconds: graceSec,
		RotatedAt:    nowStr,
		ExpiresAt:    expiresAt,
		RotatedBy:    user.ID,
		Status:       "ACTIVE",
	})
}

// ListRotations returns the rotation history for a credential —
// powers the Settings tab in the detail Sheet.
//
// GET /api/v1/credentials/{credentialId}/rotations
func (h *CredentialHandler) ListRotations(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT id FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credID, workspaceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
			return
		}
		h.logger.Error("rotations: check credential exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, credential_id, grace_seconds, rotated_at, expires_at, rotated_by, status,
		       (CASE WHEN status IN ('EXPIRED','CANCELLED') THEN 1 ELSE 0 END) AS old_value_gone
		FROM credential_rotations
		WHERE credential_id = ?
		ORDER BY rotated_at DESC`, credID)
	if err != nil {
		h.logger.Error("list rotations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	out := []rotationResponse{}
	for rows.Next() {
		var rot rotationResponse
		var oldGone int
		if err := rows.Scan(&rot.ID, &rot.CredentialID, &rot.GraceSeconds, &rot.RotatedAt,
			&rot.ExpiresAt, &rot.RotatedBy, &rot.Status, &oldGone); err != nil {
			h.logger.Error("scan rotation", "error", err)
			continue
		}
		rot.OldValueGone = oldGone != 0
		out = append(out, rot)
	}
	writeJSON(w, http.StatusOK, out)
}

// CancelRotation ends an ACTIVE grace overlap immediately, scrubbing
// old_value. EXPIRED/CANCELLED rotations are no-ops (idempotent 200).
//
// DELETE /api/v1/credential-rotations/{rotationId}
func (h *CredentialHandler) CancelRotation(w http.ResponseWriter, r *http.Request) {
	rotationID := r.PathValue("rotationId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	// Workspace isolation: walk credential_id back to the workspace
	// before we touch anything. A 404 here also covers the case
	// where rotationID belongs to another workspace.
	var status string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT cr.status FROM credential_rotations cr
		JOIN credentials c ON c.id = cr.credential_id
		WHERE cr.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`,
		rotationID, workspaceID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Rotation not found"})
			return
		}
		h.logger.Error("read rotation status", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Already terminal — no-op. Returning 200 here matches the GitLab
	// "already revoked" semantics and avoids racing two cancels.
	if status != "ACTIVE" {
		writeJSON(w, http.StatusOK, map[string]string{"status": status, "message": "rotation already terminal"})
		return
	}

	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE credential_rotations
		SET status = 'CANCELLED', old_value = ''
		WHERE id = ? AND status = 'ACTIVE'`, rotationID); err != nil {
		h.logger.Error("cancel rotation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "CANCELLED"})
}

// ExpireGracedRotations scans for ACTIVE rotations whose expires_at
// has passed and transitions them to EXPIRED, scrubbing old_value.
// Called both by the background worker and (for tests) directly.
//
// Returns the number of rotations transitioned.
func ExpireGracedRotations(ctx context.Context, db *sql.DB, logger *slog.Logger) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.ExecContext(ctx, `
		UPDATE credential_rotations
		SET status = 'EXPIRED', old_value = ''
		WHERE status = 'ACTIVE' AND expires_at <= ?`, now)
	if err != nil {
		return 0, fmt.Errorf("expire graced rotations: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 && logger != nil {
		logger.Info("expired graced rotations", "count", n)
	}
	return int(n), nil
}

// StartCredentialRotationExpiryWorker runs ExpireGracedRotations
// every rotationExpiryInterval. Mirrors the StartRegistrySyncWorker
// pattern: graceful shutdown via stop channel + WaitGroup.
//
// Run an immediate pass on startup so any rotations whose grace
// expired while the server was down are scrubbed before we begin
// accepting traffic.
func StartCredentialRotationExpiryWorker(db *sql.DB, logger *slog.Logger, stop <-chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := context.Background()
		if _, err := ExpireGracedRotations(ctx, db, logger); err != nil {
			logger.Warn("initial rotation expiry pass", "error", err)
		}
		ticker := time.NewTicker(rotationExpiryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if _, err := ExpireGracedRotations(ctx, db, logger); err != nil {
					logger.Warn("rotation expiry tick", "error", err)
				}
			}
		}
	}()
}

// (clientIP lives in nextauth.go; reused here for the audit IP field)
