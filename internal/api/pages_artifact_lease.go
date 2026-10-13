package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type artifactLeaseMetadata struct {
	PageID         string `json:"page_id"`
	Revision       int64  `json:"revision"`
	ContentSHA256  string `json:"content_sha256"`
	RuntimeProfile string `json:"runtime_profile"`
	ExpiresAt      string `json:"expires_at"`
}

// GetArtifactLease checks current human authority over one retained revision.
// This is metadata, not a transferable capability, runtime activation, or an
// assurance that permission cannot change before the short expiry.
func (h *PageHandler) GetArtifactLease(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	_, delegated := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if user == nil || agent || delegated || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	revisions := r.URL.Query()["revision"]
	if len(revisions) != 1 {
		replyError(w, 400, "one exact revision is required")
		return
	}
	revision, err := strconv.ParseInt(revisions[0], 10, 64)
	if err != nil || revision < 1 {
		replyError(w, 400, "positive revision is required")
		return
	}
	expiresAt := h.clock.Now().UTC().Add(15 * time.Second)
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "begin artifact lease snapshot", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	workspace := WorkspaceIDFromContext(ctx)
	var role string
	err = tx.QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL WHERE m.workspace_id=? AND m.user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=m.user_id AND restricted.access_mode='restricted')`, workspace, user.ID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 403, "active workspace membership required")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "authorize artifact lease membership", err)
		return
	}
	// Never let a stale middleware role preserve an administrator's old access.
	ctx = context.WithValue(ctx, ctxRole, role)
	rec, err := h.loadPage(ctx, workspace, r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load artifact lease page", err)
		return
	}
	viewer, err := h.loadViewer(ctx, workspace, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load artifact lease viewer", err)
		return
	}
	visible, err := h.canSeePage(ctx, workspace, rec, nil, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "authorize artifact lease page", err)
		return
	}
	if !visible {
		replyError(w, 404, "page not found")
		return
	}
	result := artifactLeaseMetadata{PageID: rec.ID, Revision: revision, RuntimeProfile: artifactStaticProfile}
	var manifest sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT sha256,manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, revision).Scan(&result.ContentSHA256, &manifest)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "artifact revision not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read artifact lease revision", err)
		return
	}
	if manifest.Valid {
		var stored artifactManifest
		decoder := json.NewDecoder(bytes.NewBufferString(manifest.String))
		decoder.DisallowUnknownFields()
		invalid := decoder.Decode(&stored) != nil || stored.RuntimeProfile != artifactStaticProfile || stored.FormatVersion != artifactFormatVersion || stored.Entrypoint != "index.html" || len(stored.Files) != 1 || string(stored.Source) != "null"
		if !invalid {
			file := stored.Files[0]
			invalid = file.Path != "index.html" || file.MIME != artifactHTMLMIME || file.SHA256 != result.ContentSHA256 || file.SizeBytes < 1 || file.SizeBytes > maxArtifactHTMLBytes
		}
		canonical, canonicalErr := artifactManifestJSON(stored)
		if invalid || canonicalErr != nil || canonical != manifest.String {
			replyError(w, 409, "artifact runtime profile is unavailable")
			return
		}
		result.RuntimeProfile = stored.RuntimeProfile
	}
	// Calculate before commit so a delayed response never extends the checked
	// snapshot's authority. Every subsequent call repeats all live ACL checks.
	result.ExpiresAt = tsformat.Format(expiresAt)
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "finish artifact lease snapshot", err)
		return
	}
	writeJSON(w, 200, result)
}
