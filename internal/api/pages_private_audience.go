package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Private artifacts have an explicit audience; role, folder, crew and ordinary
// grants cannot silently widen it. Ownership succession changes the custodian.
func privatePageViewer(rec *pageRecord, viewer *pageViewer) bool {
	return viewer != nil && (rec.OwnerUserID == viewer.UserID || viewer.Role == "OWNER")
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
	_, err = h.db.ExecContext(r.Context(), `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), WorkspaceIDFromContext(r.Context()), user.ID, "artifact.private_owner_accessed", "page", rec.ID, string(metadata), time.Now().UTC().Format(time.RFC3339Nano))
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
func (h *BackupHandler) canAccessWorkspaceBackups(ctx context.Context) bool {
	if !canAdministerInstance(ctx) {
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
		return false
	}
	var denied bool
	err := h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pages p WHERE p.workspace_id=? AND p.private_audience=1 AND NOT EXISTS(SELECT 1 FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL WHERE m.workspace_id=p.workspace_id AND m.user_id=? AND m.role='OWNER' AND m.access_mode='trusted'))`, workspace, privateAudienceActorID(ctx)).Scan(&denied)
	return err == nil && !denied
}
