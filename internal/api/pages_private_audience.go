package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Private artifacts have an explicit audience; role, folder, crew and ordinary
// grants cannot silently widen it. Ownership succession changes the custodian.
func privatePageViewer(rec *pageRecord, viewer *pageViewer) bool {
	return viewer != nil && (rec.OwnerUserID == viewer.UserID || viewer.Role == "OWNER")
}

// Custody is a read audience, not a grant of trusted workspace power. A
// restricted membership suppresses the OWNER override, while the live human
// custodian can still read their own output. Missing membership is a denial;
// operational database failures remain errors for the caller.
func (h *PageHandler) livePrivatePageReader(ctx context.Context, workspace, user string) (*pageViewer, error) {
	var role string
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=m.user_id AND restricted.access_mode='restricted') THEN '' ELSE m.role END
FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL
WHERE m.workspace_id=? AND m.user_id=?`, workspace, user).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pageViewer{UserID: user, Role: role}, nil
}

// Audit before releasing another human's private metadata or content. Use the
// public request boundary, outside the engine's read/transaction callbacks, so
// a rollback or a busy SQLite writer cannot discard or deadlock the audit.
func (h *PageHandler) auditPrivateOwnerAccess(w http.ResponseWriter, r *http.Request, rec *pageRecord) bool {
	if !rec.PrivateAudience {
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 404, "page not found")
		return false
	}
	if user.ID == rec.OwnerUserID {
		return true
	}
	role, err := h.liveContentHumanRole(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
	if err != nil || role != "OWNER" {
		replyError(w, 404, "page not found")
		return false
	}
	metadata, _ := json.Marshal(map[string]string{"owner_user_id": rec.OwnerUserID})
	_, err = h.db.ExecContext(r.Context(), `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), WorkspaceIDFromContext(r.Context()), user.ID, "artifact.private_owner_accessed", "page", rec.ID, string(metadata), tsformat.Format(time.Now()))
	if err != nil {
		replyInternalError(w, h.logger, "audit private artifact access", err)
		return false
	}
	return true
}

func privateAudienceActorID(ctx context.Context) string {
	if user := UserFromContext(ctx); user != nil {
		return user.ID
	}
	return ""
}

// Whole-workspace bundles contain private bytes. A workspace ADMIN must not
// obtain those bytes indirectly through backup discovery, download or restore.
// Existing instance-admin recovery remains its separate audited authority.
func (h *BackupHandler) authorizeWorkspaceBackups(w http.ResponseWriter, r *http.Request) bool {
	ctx := r.Context()
	if !canAdministerInstance(ctx) {
		replyError(w, http.StatusForbidden, "admin role required")
		return false
	}
	if instance, _ := ctx.Value(ctxInstanceAdmin).(bool); instance {
		return true
	}
	workspace := WorkspaceIDFromContext(ctx)
	if workspace == "" {
		return true
	} // the operation returns its existing missing-context error
	if h.db == nil {
		replyError(w, http.StatusInternalServerError, "backup authorization unavailable")
		return false
	}
	var denied bool
	err := h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pages p WHERE p.workspace_id=? AND p.private_audience=1 AND NOT EXISTS(SELECT 1 FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL WHERE m.workspace_id=p.workspace_id AND m.user_id=? AND m.role='OWNER' AND m.access_mode='trusted'))`, workspace, privateAudienceActorID(ctx)).Scan(&denied)
	if err != nil {
		replyInternalError(w, h.logger, "authorize workspace backup", err)
		return false
	}
	if denied {
		replyError(w, http.StatusForbidden, "workspace owner role required")
		return false
	}
	return true
}
