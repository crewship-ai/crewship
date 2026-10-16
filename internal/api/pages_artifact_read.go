package api

import (
	"context"
	"net/http"
	"net/url"
)

func validArtifactPageID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Validate private read authority again after blob IO. Historical content is
// immutable, but retained source, exact credentials, Page identity and ACLs
// must still be live before returning any HTML or private revision metadata.
func (h *PageHandler) finalArtifactRead(w http.ResponseWriter, r *http.Request, pageID string, revision int64, html string) (bool, bool) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "fence artifact read", err)
		return false, false
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, pageID, WorkspaceIDFromContext(r.Context())); err != nil {
		replyInternalError(w, h.logger, "fence artifact read", err)
		return false, false
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	rec, ok := h.artifactPage(w, r.WithContext(ctx), false)
	if !ok {
		return false, false
	}
	if rec.ID != pageID {
		replyError(w, 409, "page identity changed; reopen the original Page")
		return false, false
	}
	if revision > 0 {
		var sha string
		if err = tx.QueryRowContext(ctx, `SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=?`, pageID, revision).Scan(&sha); err != nil || sha != attachmentDigest([]byte(html)) {
			replyError(w, 409, "artifact revision is no longer retained")
			return false, false
		}
	}
	role, err := h.liveContentHumanRole(ctx, WorkspaceIDFromContext(ctx), UserFromContext(ctx).ID)
	if err != nil {
		replyError(w, 403, "active trusted workspace membership required")
		return false, false
	}
	canWrite := rec.OwnerUserID != "" && h.mayEditSpec(ctx, WorkspaceIDFromContext(ctx), UserFromContext(ctx).ID, role, rec)
	canWrite = canWrite && validateContentHumanCredential(r.WithContext(ctx), tx, "pages:write") == nil
	return canWrite, true
}

// GET queries are closed and identity-bound too, so a stale native view never
// silently reads a recreated Page that happens to share a slug.
func validArtifactReadQuery(r *http.Request) bool {
	if len(r.URL.RawQuery) > 2048 || r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
		return false
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	for k, v := range q {
		if len(v) != 1 || !(k == "page_id" || k == "revision" || k == "workspace_id") {
			return false
		}
	}
	return validArtifactPageID(q.Get("page_id"))
}
