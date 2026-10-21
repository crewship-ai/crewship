package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
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
// metadata batch or the public document response boundary, outside engine
// callbacks. Document callers retain their captured actual-read custodian.
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "authorize private artifact audit", err)
		return false
	}
	if role != "OWNER" {
		replyError(w, 404, "page not found")
		return false
	}
	metadata, _ := json.Marshal(map[string]string{"owner_user_id": rec.OwnerUserID})
	b, _ := r.Context().Value(privateMetadataReadKey{}).(*privateMetadataRead)
	standalone := b == nil
	if standalone {
		// Document operations supply the custodian captured by their actual fenced
		// read. Never replace that identity with a later owner lookup.
		var err error
		r, b, err = h.startMetadataRead(r, true)
		if err != nil {
			replyInternalError(w, h.logger, "begin private artifact audit", err)
			return false
		}
		defer b.tx.Rollback()
		role, err = h.liveContentHumanRole(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "authorize private artifact audit", err)
			return false
		}
		if role != "OWNER" {
			replyError(w, 404, "page not found")
			return false
		}
	}
	if err := h.appendPrivateReadAudit(r, b, rec, string(metadata)); err != nil {
		replyInternalError(w, h.logger, "audit private artifact access", err)
		return false
	}
	if standalone {
		return h.finishMetadataRead(w, r, b)
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
	// Conversation copies outlive a failed creation or the last private Page.
	// The existing OWNER-only bundle boundary follows retained classification,
	// rather than opening an indirect read when a result disappears.
	err := h.db.QueryRowContext(ctx, `SELECT (
 EXISTS(SELECT 1 FROM pages p WHERE p.workspace_id=? AND p.private_audience=1)
 OR EXISTS(SELECT 1 FROM chats c WHERE c.workspace_id=? AND NOT (`+chataudience.NoPrivateArtifactContextSQL+`)))
 AND NOT EXISTS(SELECT 1 FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
 WHERE m.workspace_id=? AND m.user_id=? AND m.role='OWNER' AND m.access_mode='trusted')`, workspace, workspace, workspace, privateAudienceActorID(ctx)).Scan(&denied)
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

// The caller must be outside any document writer transaction. This small
// transaction commits the existing legacy audit before metadata or bytes are
// released, including by the chat-context consumer which has no HTTP writer.
func (h *PageHandler) recordPrivateOwnerAccess(ctx context.Context, workspace, user string, rec *pageRecord) error {
	if !rec.PrivateAudience || user == rec.OwnerUserID {
		return nil
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const authority = `EXISTS(SELECT 1 FROM workspace_members m
JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL
JOIN pages p ON p.workspace_id=m.workspace_id
WHERE m.workspace_id=? AND m.user_id=? AND m.role='OWNER' AND m.access_mode='trusted'
AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=m.user_id AND restricted.access_mode='restricted')
AND p.id=? AND p.private_audience=1 AND p.owner_user_id=?)`
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT `+authority, workspace, user, rec.ID, rec.OwnerUserID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return sql.ErrNoRows
	}
	metadata, _ := json.Marshal(map[string]string{"owner_user_id": rec.OwnerUserID})
	id, created := generateCUID(), tsformat.Format(time.Now())
	args := []any{id, workspace, user, "artifact.private_owner_accessed", "page", rec.ID, string(metadata), created}
	res, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("private access audit insert was not acknowledged")
	}
	// Check the exact native tuple and NULL fields after all INSERT triggers,
	// along with the same live authority and captured custodian, before commit.
	var acknowledged bool
	ackArgs := append(args, workspace, user, rec.ID, rec.OwnerUserID)
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE
 typeof(id)='text' AND id COLLATE BINARY=?
 AND typeof(workspace_id)='text' AND workspace_id COLLATE BINARY=?
 AND typeof(user_id)='text' AND user_id COLLATE BINARY=?
 AND typeof(action)='text' AND action COLLATE BINARY=?
 AND typeof(entity_type)='text' AND entity_type COLLATE BINARY=?
 AND typeof(entity_id)='text' AND entity_id COLLATE BINARY=?
 AND typeof(metadata)='text' AND metadata COLLATE BINARY=?
 AND typeof(created_at)='text' AND created_at COLLATE BINARY=?
 AND ip_address IS NULL AND user_agent IS NULL) AND `+authority, ackArgs...).Scan(&acknowledged)
	if err != nil {
		return err
	}
	if !acknowledged {
		return errors.New("private access audit or authority changed before commit")
	}
	return tx.Commit()
}
