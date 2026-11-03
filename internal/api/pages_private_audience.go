package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// workspaceArtifactReader is the single read authority of PRD §5 (owner
// decision 30. 10.): a live, trusted workspace OWNER or ADMIN reads every
// artifact in the workspace, of every type, including another member's
// unshared private output. It is READ power only; it never grants write,
// share, publish or custody. Every read through it of an artifact the reader
// does not own and holds no grant for is audited (auditPrivateOwnerAccess,
// recordWorkspaceAdminArtifactAccess). Callers pass a role resolved from live
// membership; liveContentHumanRole and livePrivatePageReader both return an
// empty role for a restricted (Guest) membership, which never qualifies.
func workspaceArtifactReader(role string) bool {
	return role == "OWNER" || role == "ADMIN"
}

// Private artifacts have an explicit audience; folder, crew and ordinary grants
// cannot silently widen it. The read audience is the custodian plus the
// workspace administrators (workspaceArtifactReader), always audited.
// Ownership succession changes the custodian.
func privatePageViewer(rec *pageRecord, viewer *pageViewer) bool {
	return viewer != nil && (rec.OwnerUserID == viewer.UserID || workspaceArtifactReader(viewer.Role))
}

// privatePageCustodian is the WRITE/administration audience of a private
// artifact: its custodian and the workspace OWNER, unchanged by the
// administrator read rule. An ADMIN's read of private output never becomes
// spec editing, production, grant administration or publication.
func privatePageCustodian(rec *pageRecord, viewer *pageViewer) bool {
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
//
// The same boundary audits a workspace administrator's read of an ordinary
// (non-private) artifact it reaches only through its OWNER/ADMIN role
// (auditWorkspaceAdminRead), so every read endpoint that releases artifact
// metadata or content applies one rule to every artifact type.
func (h *PageHandler) auditPrivateOwnerAccess(w http.ResponseWriter, r *http.Request, rec *pageRecord) bool {
	if !rec.PrivateAudience {
		return h.auditWorkspaceAdminRead(w, r, rec)
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
	if !workspaceArtifactReader(role) {
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
		if !workspaceArtifactReader(role) {
			replyError(w, 404, "page not found")
			return false
		}
	}
	if err := h.appendPrivateReadAudit(r, b, rec, auditActionPrivateArtifactRead, string(metadata)); err != nil {
		replyInternalError(w, h.logger, "audit private artifact access", err)
		return false
	}
	if standalone {
		return h.finishMetadataRead(w, r, b)
	}
	return true
}

// Audit actions of the administrator read rule (PRD §5). One rule, named by
// the audience the artifact had when it was read:
//
//   - auditActionPrivateArtifactRead: a private-audience artifact (agent or
//     chat output, a private Project, Sheet, Slides, Word) read by a workspace
//     OWNER or ADMIN who is not its custodian. The established name is kept:
//     backups, chat context and existing compliance queries already key on it.
//   - auditActionWorkspaceAdminArtifactRead: any other artifact read by a
//     workspace OWNER or ADMIN who neither owns it nor reaches it through its
//     owning crew, a panel crew, a grant or a folder ACL.
//   - design.workspace_admin_accessed (pages_design_audit.go) remains the
//     Design engine's own record, unchanged for compatibility.
const (
	auditActionPrivateArtifactRead        = "artifact.private_owner_accessed"
	auditActionWorkspaceAdminArtifactRead = "artifact.workspace_admin_accessed"
)

// auditWorkspaceAdminRead records PRD §5's audit for an ordinary artifact.
// The caller has already decided the read is allowed. Nothing is recorded for
// the owner, for a non-administrator (who reached the artifact some other way)
// or for an administrator who would reach it without the role. A failed audit
// refuses the read; nothing is released unaudited.
func (h *PageHandler) auditWorkspaceAdminRead(w http.ResponseWriter, r *http.Request, rec *pageRecord) bool {
	return h.auditWorkspaceAdminReadBeyond(w, r, rec, func(ctx context.Context, workspace, userID string) (bool, error) {
		return h.reachedOnlyByAdminRole(ctx, workspace, rec, userID)
	})
}

// The administrator read rule for ordinary Pages, by what a read releases.
// PRD §5: a workspace OWNER/ADMIN reads everything, and every read only the
// role makes possible is audited. The comparison is always the same person
// as a plain MEMBER: same crews, grants and folder ACLs, no manage role. A
// read that member would also receive is authorized by that standing and is
// not audited; anything more is audited. Private Pages are audited for every
// non-custodian administrator by auditPrivateOwnerAccess.

// memberViewer is userID as a plain MEMBER: same crews, no manage role.
func (h *PageHandler) memberViewer(ctx context.Context, workspace, userID string) (*pageViewer, error) {
	viewer, err := h.loadViewer(ctx, workspace, userID)
	if err != nil {
		return nil, err
	}
	member := *viewer
	member.Role = "MEMBER"
	return &member, nil
}

// memberSealsPanel reports whether userID as a plain MEMBER would receive any
// of panels sealed (§7.1 rule 2: panel visibility is its owning crew's).
func (h *PageHandler) memberSealsPanel(ctx context.Context, workspace, userID string, panels []*panelRecord) (bool, error) {
	if len(panels) == 0 {
		return false, nil
	}
	member, err := h.memberViewer(ctx, workspace, userID)
	if err != nil {
		return false, err
	}
	for _, p := range panels {
		if !h.canSeePanel(member, p) {
			return true, nil
		}
	}
	return false, nil
}

// memberSealsDefinition reports whether the requesting user as a plain
// MEMBER could not see some panel declared in docs (the visibility
// requireProjectDefinitions and the review surface apply).
func (h *PageHandler) memberSealsDefinition(ctx context.Context, workspace string, docs ...*pages.Document) (bool, error) {
	auth, err := h.reviewDefinitionAuthorizer(ctx, workspace)
	if err != nil {
		return false, err
	}
	member := *auth.viewer
	member.Role = "MEMBER"
	auth.viewer = &member
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		for _, panel := range doc.Spec.Panels {
			if !auth.visible(panel.Owner) {
				return true, nil
			}
		}
	}
	return false, nil
}

// parsedDefinitions decodes stored spec_json values; empty ones are skipped.
// A spec that does not decode is returned as an error, never as "visible".
func parsedDefinitions(specs ...string) ([]*pages.Document, error) {
	out := make([]*pages.Document, 0, len(specs))
	for _, spec := range specs {
		if spec == "" {
			continue
		}
		var doc pages.Document
		if err := json.Unmarshal([]byte(spec), &doc); err != nil {
			return nil, err
		}
		out = append(out, &doc)
	}
	return out, nil
}

// auditWholeArrangementRead is auditPrivateOwnerAccess for a read that
// releases a Page's whole arrangement: its spec history, its export bundle,
// its project sources and their history. Reaching the Page does not authorize
// these; a member needs `write` on it (mayEditSpec), and the spec history of a
// Page holding a workbook is its owner's alone (workbookPageOwnerOnly) even for
// a named Sheet editor. definitions, when given, must also be wholly visible
// to the member (export requires every panel). An administrator whose
// standing without the role does not authorize the read (a Read grant, a
// Sheet editor grant, a panel crew, no grant, a Write grant without the
// panels' crews where every panel is required) is audited.
func (h *PageHandler) auditWholeArrangementRead(w http.ResponseWriter, r *http.Request, rec *pageRecord, workbookHistory bool, definitions ...*pages.Document) bool {
	if rec.PrivateAudience {
		return h.auditPrivateOwnerAccess(w, r, rec)
	}
	return h.auditWorkspaceAdminReadBeyond(w, r, rec, func(ctx context.Context, workspace, userID string) (bool, error) {
		if workbookHistory && !workbookPageOwnerOnly(rec, userID) {
			return true, nil
		}
		// The same member without the manage role; an unresolvable grant
		// counts as none, so the read is audited rather than released quietly.
		if !h.mayEditSpec(ctx, workspace, userID, "MEMBER", rec) {
			return true, nil
		}
		return h.memberSealsDefinition(ctx, workspace, definitions...)
	})
}

// auditDefinitionPanelsRead covers the project reads that also release a
// whole definition (the draft, a retained revision, the review snapshot).
// projectPage has already audited a reader the member standing would refuse
// outright (no `write`) and every private-Page administrator read; this adds
// the reader whose `write` is real but who sees some declared panel only
// through the role.
func (h *PageHandler) auditDefinitionPanelsRead(w http.ResponseWriter, r *http.Request, rec *pageRecord, definitions ...*pages.Document) bool {
	if rec.PrivateAudience {
		return true
	}
	return h.auditWorkspaceAdminReadBeyond(w, r, rec, func(ctx context.Context, workspace, userID string) (bool, error) {
		if !h.mayEditSpec(ctx, workspace, userID, "MEMBER", rec) {
			return false, nil // audited by projectPage
		}
		return h.memberSealsDefinition(ctx, workspace, definitions...)
	})
}

// auditPageDocumentRead is the audit of the Page detail read (Get). An
// OWNER/ADMIN receives every panel unsealed and, through mayEditSpec, the
// authored half (actions, wake gates, on_failure). The same person as a
// plain member would reach the Page only by its non-role arms, receive the
// panels of other crews sealed, and get the authored half only with `write`.
func (h *PageHandler) auditPageDocumentRead(w http.ResponseWriter, r *http.Request, rec *pageRecord, panels []*panelRecord) bool {
	if rec.PrivateAudience {
		return h.auditPrivateOwnerAccess(w, r, rec)
	}
	return h.auditWorkspaceAdminReadBeyond(w, r, rec, func(ctx context.Context, workspace, userID string) (bool, error) {
		if roleOnly, err := h.reachedOnlyByAdminRole(ctx, workspace, rec, userID); err != nil || roleOnly {
			return roleOnly, err
		}
		if sealed, err := h.memberSealsPanel(ctx, workspace, userID, panels); err != nil || sealed {
			return sealed, err
		}
		return len(panels) > 0 && !h.mayEditSpec(ctx, workspace, userID, "MEMBER", rec), nil
	})
}

// auditPanelRead is the audit of a single panel's read (its declared actions,
// its application payload history). resolvePanelForCaller admitted the
// caller by panel visibility; a plain member sees a panel only through its
// owning crew.
func (h *PageHandler) auditPanelRead(w http.ResponseWriter, r *http.Request, rec *pageRecord, panel *panelRecord) bool {
	if rec.PrivateAudience {
		user := UserFromContext(r.Context())
		if user == nil || user.ID == rec.OwnerUserID {
			return true
		}
		role, err := h.liveContentHumanRole(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "authorize private panel audit", err)
			return false
		}
		if !workspaceArtifactReader(role) {
			return true // admitted by its crew, as before; not an administrator read
		}
		return h.auditPrivateOwnerAccess(w, r, rec)
	}
	return h.auditWorkspaceAdminReadBeyond(w, r, rec, func(ctx context.Context, workspace, userID string) (bool, error) {
		return h.memberSealsPanel(ctx, workspace, userID, []*panelRecord{panel})
	})
}

// auditWorkspaceAdminReadBeyond records the administrator read when roleOnly
// reports that the reader's standing without the OWNER/ADMIN role does not
// authorize this read.
func (h *PageHandler) auditWorkspaceAdminReadBeyond(w http.ResponseWriter, r *http.Request, rec *pageRecord, roleOnlyRead func(ctx context.Context, workspace, userID string) (bool, error)) bool {
	user := UserFromContext(r.Context())
	if user == nil || (rec.OwnerUserID != "" && user.ID == rec.OwnerUserID) {
		return true
	}
	workspace := WorkspaceIDFromContext(r.Context())
	role, err := h.liveContentHumanRole(r.Context(), workspace, user.ID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !workspaceArtifactReader(role) {
		return true
	}
	if err != nil {
		replyInternalError(w, h.logger, "authorize administrator artifact audit", err)
		return false
	}
	roleOnly, err := roleOnlyRead(r.Context(), workspace, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve administrator artifact reach", err)
		return false
	}
	if !roleOnly {
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	metadata, _ := json.Marshal(map[string]string{"owner_user_id": rec.OwnerUserID, "owner_crew_id": rec.OwnerCrewID, "reader_role": role})
	b, _ := r.Context().Value(privateMetadataReadKey{}).(*privateMetadataRead)
	standalone := b == nil
	if standalone {
		r, b, err = h.startMetadataRead(r, true)
		if err != nil {
			replyInternalError(w, h.logger, "begin administrator artifact audit", err)
			return false
		}
		defer b.tx.Rollback()
	}
	if err := h.appendPrivateReadAudit(r, b, rec, auditActionWorkspaceAdminArtifactRead, string(metadata)); err != nil {
		replyInternalError(w, h.logger, "audit administrator artifact access", err)
		return false
	}
	if standalone {
		return h.finishMetadataRead(w, r, b)
	}
	return true
}

// reachedOnlyByAdminRole reports whether userID would lose an ordinary
// artifact without its workspace OWNER/ADMIN role: not its owner, not in its
// owning crew or a panel's crew, named by no live grant and no folder ACL.
func (h *PageHandler) reachedOnlyByAdminRole(ctx context.Context, workspace string, rec *pageRecord, userID string) (bool, error) {
	if rec.OwnerUserID != "" && rec.OwnerUserID == userID {
		return false, nil
	}
	viewer, err := h.loadViewer(ctx, workspace, userID)
	if err != nil {
		return false, err
	}
	// The same member without the manage role: every non-role arm still applies,
	// including grants to the whole workspace.
	viewer.Role = "MEMBER"
	panels, err := h.loadPanels(ctx, workspace, rec.ID)
	if err != nil {
		return false, err
	}
	plain := *rec
	plain.PrivateAudience = false
	reached, err := h.canSeePage(ctx, workspace, &plain, panels, viewer)
	return !reached, err
}

// privateReadAuditHook is a test-only seam. It runs between a content read
// that released its transaction and the audit decision for that read, so a
// test can interleave a concurrent change (a custody transfer) deterministically.
var privateReadAuditHook atomic.Pointer[func(pageID string)]

func privateReadAuditSeam(pageID string) {
	if hook := privateReadAuditHook.Load(); hook != nil {
		(*hook)(pageID)
	}
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
	// A project pin does not publish its retained private file history.
	// The existing OWNER-only bundle boundary follows retained classification,
	// rather than opening an indirect read when a result disappears.
	err := h.db.QueryRowContext(ctx, `SELECT (
 EXISTS(SELECT 1 FROM pages p WHERE p.workspace_id=? AND p.private_audience=1)
 OR EXISTS(SELECT 1 FROM artifact_version_heads v WHERE v.workspace_id=? AND v.content_type='artifact_project')
 OR EXISTS(SELECT 1 FROM chats c WHERE c.workspace_id=? AND NOT (`+chataudience.NoPrivateArtifactContextSQL+`)))
 AND NOT EXISTS(SELECT 1 FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
 WHERE m.workspace_id=? AND m.user_id=? AND m.role='OWNER' AND m.access_mode='trusted')`, workspace, workspace, workspace, workspace, privateAudienceActorID(ctx)).Scan(&denied)
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
WHERE m.workspace_id=? AND m.user_id=? AND m.role IN ('OWNER','ADMIN') AND m.access_mode='trusted'
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
