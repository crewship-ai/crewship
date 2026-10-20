package api

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/chataudience"
)

// A download is a long-lived read. Middleware's admission role alone must
// not authorize later chunks after revocation, suspension or demotion.
func backupDownloadAuthority(r *http.Request, db *sql.DB, instanceOnly bool) error {
	if !auth.StreamCredentialCurrent(r.Context()) {
		return access.ErrDenied
	}
	u := UserFromContext(r.Context())
	if u == nil {
		return access.ErrDenied
	}
	var email, role string
	var suspended, restricted bool
	err := db.QueryRowContext(r.Context(), `SELECT email,COALESCE(instance_role,''),suspended_at IS NOT NULL,
		EXISTS(SELECT 1 FROM workspace_members WHERE user_id=users.id AND access_mode='restricted')
		FROM users WHERE id=?`, u.ID).Scan(&email, &role, &suspended, &restricted)
	if errors.Is(err, sql.ErrNoRows) {
		return access.ErrDenied
	}
	if err != nil {
		return err
	}
	if suspended || restricted {
		return access.ErrDenied
	}
	instance := backup.IsInstanceOwner(email) || role == "ADMIN"
	if instanceOnly {
		if !instance || !canScope(r.Context(), scopeInstanceAdmin) {
			return access.ErrDenied
		}
		return nil
	}
	workspace := WorkspaceIDFromContext(r.Context())
	var workspaceRole string
	var private bool
	err = db.QueryRowContext(r.Context(), `SELECT
		COALESCE((SELECT m.role FROM workspace_members m WHERE m.workspace_id=w.id AND m.user_id=? AND m.access_mode='trusted'),''),
		EXISTS(SELECT 1 FROM pages p WHERE p.workspace_id=w.id AND p.private_audience=1)
		OR EXISTS(SELECT 1 FROM chats c WHERE c.workspace_id=w.id AND NOT (`+chataudience.NoPrivateArtifactContextSQL+`))
		FROM workspaces w WHERE w.id=? AND w.deleted_at IS NULL`, u.ID, workspace).Scan(&workspaceRole, &private)
	if errors.Is(err, sql.ErrNoRows) {
		return access.ErrDenied
	}
	if err != nil {
		return err
	}
	if !instance && (workspaceRole != "OWNER" && workspaceRole != "ADMIN" || private && workspaceRole != "OWNER") {
		return access.ErrDenied
	}
	return nil
}

func copyBackupDownload(w http.ResponseWriter, r *http.Request, db *sql.DB, source io.Reader, instanceOnly bool) (int64, error) {
	// Keep arbitrary bundle bytes inert even when called without endpoint headers.
	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	buffer := make([]byte, 16384)
	controller := http.NewResponseController(w)
	var copied int64
	for {
		n, readErr := source.Read(buffer)
		if n > 0 {
			if err := backupDownloadAuthority(r, db, instanceOnly); err != nil {
				return copied, err
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			written, err := w.Write(buffer[:n])
			copied += int64(written)
			if err != nil {
				return copied, err
			}
			if written != n {
				return copied, io.ErrShortWrite
			}
			if err := controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return copied, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return copied, nil
			}
			return copied, readErr
		}
	}
}
