package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const maxArtifactHTMLBytes = 10 << 20
const maxArtifactRevisions = 200
const maxArtifactHourlyRevisions = 30

// The existing Pages ACL readers can use the mutation's transaction. No shared
// handler state changes: ownership, live grants and folder ACLs are evaluated
// after the SQLite writer fence, so revocation and publication have one order.
type pageAuthReaderKey struct{}
type pageAuthQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (h *PageHandler) pageAuthReader(ctx context.Context) pageAuthQueryer {
	if tx, ok := ctx.Value(pageAuthReaderKey{}).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return h.db
}

func (h *PageHandler) artifactPage(w http.ResponseWriter, r *http.Request, write bool) (*pageRecord, bool) {
	user := UserFromContext(r.Context())
	agentCaller, _ := pageGrantCallerIsAgent(r)
	authority, delegated := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if user == nil || (!delegated && (agentCaller || projectAgentFrom(r.Context()) != nil)) {
		replyError(w, 401, "human authentication required")
		return nil, false
	}
	var active int
	if h.pageAuthReader(r.Context()).QueryRowContext(r.Context(), `SELECT 1 FROM users WHERE id=? AND suspended_at IS NULL`, user.ID).Scan(&active) != nil {
		replyError(w, 403, "active human account required")
		return nil, false
	}
	ws := WorkspaceIDFromContext(r.Context())
	rec, err := h.loadPage(r.Context(), ws, r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load artifact page", err)
		return nil, false
	}
	viewer, err := h.loadViewer(r.Context(), ws, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "authorize artifact page", err)
		return nil, false
	}
	visible, err := h.canSeePage(r.Context(), ws, rec, nil, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "authorize artifact page", err)
		return nil, false
	}
	if !visible {
		replyError(w, 404, "page not found")
		return nil, false
	}
	if write && !h.mayEditSpec(r.Context(), ws, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, 403, "page write permission required")
		return nil, false
	}
	if rec.HasBoard {
		replyError(w, 409, "board pages cannot contain artifacts")
		return nil, false
	}
	if write && (rec.OwnerUserID == "" || rec.OwnerCrewID != "") {
		replyError(w, 409, "artifacts require a human-owned page")
		return nil, false
	}
	if h.artifactStoragePath == "" {
		replyError(w, 503, "artifact storage is not configured")
		return nil, false
	}
	if delegated {
		if write && !h.artifactAgentPolicyAllows(r.Context(), ws, authority.CrewID) {
			artifactCommentError(w, 403, "artifact_policy_disabled", "Agent artifact publication is disabled for this crew")
			return nil, false
		}
		if authority.HumanUserID != user.ID || authority.WorkspaceID != ws {
			replyError(w, 403, "artifact principal mismatch")
			return nil, false
		}
		if err := h.checkArtifactRun(r.Context(), rec.ID, authority); err != nil {
			replyError(w, 403, err.Error())
			return nil, false
		}
	}
	return rec, true
}

type pageArtifactRevision struct {
	Revision      int64  `json:"revision"`
	CreatedAt     string `json:"created_at"`
	AuthorUserID  string `json:"author_user_id"`
	AuthorAgentID string `json:"author_agent_id,omitempty"`
	AuthorRunID   string `json:"author_run_id,omitempty"`
	RestoredFrom  *int64 `json:"restored_from,omitempty"`
}

func (h *PageHandler) GetArtifact(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.artifactPage(w, r, false)
	if !ok {
		return
	}
	var head int64
	err := h.db.QueryRowContext(r.Context(), `SELECT head_rev FROM page_artifacts WHERE page_id=?`, rec.ID).Scan(&head)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read artifact head", err)
		return
	}
	selected := head
	if raw := r.URL.Query().Get("revision"); raw != "" {
		selected, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || selected < 1 {
			replyError(w, 400, "revision must be positive")
			return
		}
	}
	html := ""
	var manifest *artifactManifest
	manifestStatus := "unpublished"
	if selected > 0 {
		var sha string
		var recorded sql.NullString
		err := h.db.QueryRowContext(r.Context(), `SELECT sha256,manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, selected).Scan(&sha, &recorded)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "artifact revision not found")
			return
		}
		if err != nil {
			replyInternalError(w, h.logger, "read artifact revision", err)
			return
		}
		data, err := readAttachmentBlob(h.artifactStoragePath, WorkspaceIDFromContext(r.Context()), sha)
		if err != nil || attachmentDigest(data) != sha {
			replyError(w, 503, "artifact content is missing or failed integrity verification")
			return
		}
		resolved, status, err := resolveArtifactManifest(recorded, data, sha)
		if err != nil {
			replyError(w, 503, "artifact manifest is unsupported or failed integrity verification")
			return
		}
		manifest, manifestStatus = &resolved, status
		html = string(data)
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT revision,created_at,COALESCE(author_user_id,''),restored_from,COALESCE(author_agent_id,''),COALESCE(author_run_id,'') FROM page_artifact_revisions WHERE page_id=? ORDER BY revision DESC LIMIT 200`, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read artifact history", err)
		return
	}
	revisions := make([]pageArtifactRevision, 0)
	for rows.Next() {
		var v pageArtifactRevision
		if err := rows.Scan(&v.Revision, &v.CreatedAt, &v.AuthorUserID, &v.RestoredFrom, &v.AuthorAgentID, &v.AuthorRunID); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "read artifact history", err)
			return
		}
		revisions = append(revisions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		replyInternalError(w, h.logger, "read artifact history", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	agentCaller, _ := pageGrantCallerIsAgent(r)
	canComment := !agentCaller && projectAgentFrom(r.Context()) == nil && h.mayComment(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec)
	writeJSON(w, 200, map[string]any{"head_rev": head, "revision": selected, "html": html, "manifest": manifest, "manifest_status": manifestStatus, "revisions": revisions, "can_copy": head > 0 && h.canCopyArtifact(r), "copy_provenance": h.artifactCopyProvenance(r.Context(), WorkspaceIDFromContext(r.Context()), rec.ID, UserFromContext(r.Context()).ID), "can_comment": canComment, "can_write": rec.OwnerUserID != "" && h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec)})
}

type artifactMutation struct {
	artifactFormatRequest
	RequestID  string `json:"request_id,omitempty"`
	HTML       string `json:"html"`
	BaseRev    *int64 `json:"base_rev"`
	MutationID string `json:"mutation_id"`
	Revision   int64  `json:"revision"`
}

func (h *PageHandler) PublishArtifact(w http.ResponseWriter, r *http.Request) {
	h.writeArtifact(w, r, false)
}
func (h *PageHandler) RestoreArtifact(w http.ResponseWriter, r *http.Request) {
	h.writeArtifact(w, r, true)
}
func (h *PageHandler) writeArtifact(w http.ResponseWriter, r *http.Request, restore bool) {
	rec, ok := h.artifactPage(w, r, true)
	if !ok {
		return
	}
	// JSON escaping can expand a 10 MiB document up to sixfold. Bound both the
	// transport and the decoded UTF-8 source; the source limit is authoritative.
	release, ok := acquireArtifactUpload(w, r)
	if !ok {
		return
	}
	defer release()
	var req artifactMutation
	if !decodeArtifactJSON(w, r, &req) {
		return
	}
	if err := normalizeArtifactFormat(req.artifactFormatRequest); err != nil {
		replyError(w, 422, err.Error())
		return
	}
	if req.BaseRev == nil || *req.BaseRev < 0 || len(req.MutationID) < 1 || len(req.MutationID) > 128 || strings.TrimSpace(req.MutationID) != req.MutationID {
		replyError(w, 400, "base_rev and mutation_id are required")
		return
	}
	authority, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if (authority == nil && req.RequestID != "") || (authority != nil && authority.RequestID != req.RequestID) {
		replyError(w, 403, "artifact request binding mismatch")
		return
	}
	var restoredFrom *int64
	if restore {
		if req.Revision < 1 || req.HTML != "" {
			replyError(w, 400, "revision is required for restore")
			return
		}
		var sha string
		var recorded sql.NullString
		err := h.db.QueryRowContext(r.Context(), `SELECT sha256,manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, req.Revision).Scan(&sha, &recorded)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "artifact revision not found")
			return
		}
		if err != nil {
			replyInternalError(w, h.logger, "read restore revision", err)
			return
		}
		data, err := readAttachmentBlob(h.artifactStoragePath, WorkspaceIDFromContext(r.Context()), sha)
		if err != nil || attachmentDigest(data) != sha {
			replyError(w, 503, "artifact content is missing or failed integrity verification")
			return
		}
		if _, _, err := resolveArtifactManifest(recorded, data, sha); err != nil {
			replyError(w, 503, "artifact manifest is unsupported or failed integrity verification")
			return
		}
		req.HTML = string(data)
		restoredFrom = &req.Revision
	} else if req.Revision != 0 {
		replyError(w, 400, "revision is only valid for restore")
		return
	}
	if len(req.HTML) == 0 || !utf8.ValidString(req.HTML) {
		replyError(w, 422, "HTML must be nonempty UTF-8")
		return
	}
	if len(req.HTML) > maxArtifactHTMLBytes {
		replyError(w, 413, "HTML exceeds 10 MiB")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	manifest, err := newArtifactManifest([]byte(req.HTML))
	if err != nil {
		replyError(w, 422, err.Error())
		return
	}
	manifestJSON, err := artifactManifestJSON(manifest)
	if err != nil {
		replyInternalError(w, h.logger, "encode artifact manifest", err)
		return
	}
	sha := manifest.Files[0].SHA256
	fingerprintInput := fmt.Sprintf("%d/%t/%d/%s", *req.BaseRev, restore, req.Revision, sha)
	if req.RequestID != "" {
		fingerprintInput += "/" + req.RequestID
	}
	fingerprint := attachmentDigest([]byte(fingerprintInput))
	unlock := lockAttachmentBlob(ws, sha)
	defer unlock()
	if _, _, err := storeAttachmentBlob(h.artifactStoragePath, ws, []byte(req.HTML)); err != nil {
		replyInternalError(w, h.logger, "store artifact source", err)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin artifact revision", err)
		return
	}
	defer tx.Rollback()
	// First statement takes the writer fence BEFORE reading ACLs or head.
	result, err := tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, rec.ID, ws)
	if err != nil {
		replyInternalError(w, h.logger, "fence artifact revision", err)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		replyError(w, 404, "page not found")
		return
	}
	var role string
	if err := tx.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user.ID).Scan(&role); err != nil {
		replyError(w, 403, "workspace membership required")
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	ctx = context.WithValue(ctx, ctxRole, role)
	checked, ok := h.artifactPage(w, r.WithContext(ctx), true)
	if !ok {
		return
	}
	rec = checked
	var head int64
	err = tx.QueryRowContext(ctx, `SELECT head_rev FROM page_artifacts WHERE page_id=?`, rec.ID).Scan(&head)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read artifact head", err)
		return
	}
	var previous int64
	var oldDigest, oldAuthor, oldAgent, oldRun string
	err = tx.QueryRowContext(ctx, `SELECT revision,request_digest,COALESCE(author_user_id,''),COALESCE(author_agent_id,''),COALESCE(author_run_id,'') FROM page_artifact_mutation_receipts WHERE page_id=? AND mutation_id=?`, rec.ID, req.MutationID).Scan(&previous, &oldDigest, &oldAuthor, &oldAgent, &oldRun)
	if err == nil {
		if oldDigest != fingerprint || oldAuthor != user.ID || oldAgent != artifactAgentID(r) || oldRun != artifactRunID(r) {
			replyError(w, 409, "mutation_id was already used for a different request")
			return
		}
		writeJSON(w, 200, map[string]any{"revision": previous, "head_rev": head})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read artifact retry", err)
		return
	}
	if req.RequestID != "" {
		var base int64
		var state string
		if tx.QueryRowContext(ctx, `SELECT base_rev,status FROM page_artifact_requests WHERE id=? AND page_id=? AND run_id=?`, req.RequestID, rec.ID, artifactRunID(r)).Scan(&base, &state) != nil || base != *req.BaseRev || state != "running" {
			replyError(w, 409, "artifact request is no longer publishable")
			return
		}
	}
	if head != *req.BaseRev {
		replyError(w, 409, "artifact revision changed; reload before saving")
		return
	}
	if err := reserveArtifactRevisionAt(ctx, tx, rec.ID, h.clock.Now(), req.Revision); err != nil {
		if errors.Is(err, errArtifactRetentionPinned) {
			writeJSON(w, 409, map[string]any{"error": err.Error(), "code": "retention_pinned"})
		} else if errors.Is(err, errArtifactReceiptLimit) {
			writeJSON(w, 409, map[string]any{"error": err.Error(), "code": "mutation_limit"})
		} else {
			replyInternalError(w, h.logger, "reserve artifact retention", err)
		}
		return
	}
	var incompatible int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM page_panels WHERE page_id=?)+(SELECT COUNT(*) FROM page_project_drafts WHERE page_id=?)+(SELECT COUNT(*) FROM page_project_live WHERE page_id=?)`, rec.ID, rec.ID, rec.ID).Scan(&incompatible); err != nil {
		replyInternalError(w, h.logger, "check artifact page type", err)
		return
	}
	if incompatible > 0 {
		replyError(w, 409, "only an empty page can become an artifact")
		return
	}
	now := h.clock.Now().UTC()
	var recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_mutation_receipts WHERE page_id=? AND created_at>?`, rec.ID, tsformat.Format(now.Add(-time.Hour))).Scan(&recent); err != nil {
		replyInternalError(w, h.logger, "check artifact revision rate", err)
		return
	}
	if recent >= maxArtifactHourlyRevisions {
		w.Header().Set("Retry-After", "3600")
		writeJSON(w, 429, map[string]any{"error": "artifact revision rate limit reached", "code": "rate_limited"})
		return
	}
	if !h.enforceArtifactWorkspaceRate(w, r, tx, ws, now) {
		return
	}
	revision := head + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,restored_from,created_at,author_agent_id,author_run_id,source_chat_id,manifest_json) VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?)`, generateCUID(), rec.ID, revision, sha, user.ID, req.MutationID, fingerprint, restoredFrom, tsformat.Format(now), artifactAgentID(r), artifactRunID(r), artifactSourceChatID(r), manifestJSON); err != nil {
		replyInternalError(w, h.logger, "save artifact revision", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,author_user_id,author_agent_id,author_run_id,created_at,restored_from) VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?)`, rec.ID, req.MutationID, revision, fingerprint, user.ID, artifactAgentID(r), artifactRunID(r), tsformat.Format(now), restoredFrom); err != nil {
		replyInternalError(w, h.logger, "save artifact retry receipt", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifacts(page_id,head_rev) VALUES (?,?) ON CONFLICT(page_id) DO UPDATE SET head_rev=excluded.head_rev`, rec.ID, revision); err != nil {
		replyInternalError(w, h.logger, "save artifact head", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pages SET updated_at=? WHERE id=?`, tsformat.Format(now), rec.ID); err != nil {
		replyInternalError(w, h.logger, "update artifact page", err)
		return
	}
	if req.RequestID != "" {
		result, e := tx.ExecContext(ctx, `UPDATE page_artifact_requests SET status='succeeded',result_revision=?,updated_at=? WHERE id=? AND page_id=? AND run_id=? AND status='running'`, revision, tsformat.Format(now), req.RequestID, rec.ID, artifactRunID(r))
		if e != nil {
			replyInternalError(w, h.logger, "complete artifact request", e)
			return
		}
		if n, _ := result.RowsAffected(); n != 1 {
			replyError(w, 409, "artifact request changed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit artifact revision", err)
		return
	}
	if h.journal != nil {
		_, err := h.journal.Emit(r.Context(), journal.Entry{WorkspaceID: ws, Type: journal.EntryPageSpecChanged, Severity: journal.SeverityInfo, ActorType: artifactActorType(r), ActorID: artifactActorID(r, user.ID), Summary: fmt.Sprintf("Artifact %s revision %d saved", rec.Slug, revision), Payload: map[string]any{"page_id": rec.ID, "page": rec.Slug, "artifact_revision": revision, "sha256": sha, "restored_from": restoredFrom, "on_behalf_of_user_id": user.ID, "agent_id": artifactAgentID(r), "run_id": artifactRunID(r), "request_id": req.RequestID}})
		if err != nil && h.logger != nil {
			h.logger.Warn("artifact revision audit failed", "error", err)
		}
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"page_id": rec.ID})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{"revision": revision, "head_rev": revision})
}
