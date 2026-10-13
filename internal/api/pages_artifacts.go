package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/journal"
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
	if tx, ok := ctx.Value(pageAuthReaderKey{}).(*sql.Tx); ok {
		return tx
	}
	return h.db
}

func (h *PageHandler) artifactPage(w http.ResponseWriter, r *http.Request, write bool) (*pageRecord, bool) {
	user := UserFromContext(r.Context())
	agentCaller, _ := pageGrantCallerIsAgent(r)
	if user == nil || agentCaller || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
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
	if write && (rec.OwnerUserID == "" || rec.OwnerCrewID != "") {
		replyError(w, 409, "artifacts require a human-owned page")
		return nil, false
	}
	if h.artifactStoragePath == "" {
		replyError(w, 503, "artifact storage is not configured")
		return nil, false
	}
	return rec, true
}

type pageArtifactRevision struct {
	Revision     int64  `json:"revision"`
	CreatedAt    string `json:"created_at"`
	AuthorUserID string `json:"author_user_id"`
	RestoredFrom *int64 `json:"restored_from,omitempty"`
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
	if selected > 0 {
		var sha string
		err := h.db.QueryRowContext(r.Context(), `SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, selected).Scan(&sha)
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
		html = string(data)
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT revision,created_at,COALESCE(author_user_id,''),restored_from FROM page_artifact_revisions WHERE page_id=? ORDER BY revision DESC LIMIT 200`, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read artifact history", err)
		return
	}
	revisions := make([]pageArtifactRevision, 0)
	for rows.Next() {
		var v pageArtifactRevision
		if err := rows.Scan(&v.Revision, &v.CreatedAt, &v.AuthorUserID, &v.RestoredFrom); err != nil {
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
	writeJSON(w, 200, map[string]any{"head_rev": head, "revision": selected, "html": html, "revisions": revisions, "can_write": rec.OwnerUserID != "" && h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, RoleFromContext(r.Context()), rec)})
}

type artifactMutation struct {
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
	body, ok := readCapped(w, r, 6*maxArtifactHTMLBytes+4096, "artifact")
	if !ok {
		return
	}
	var req artifactMutation
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.BaseRev == nil || *req.BaseRev < 0 || len(req.MutationID) < 1 || len(req.MutationID) > 128 || strings.TrimSpace(req.MutationID) != req.MutationID {
		replyError(w, 400, "base_rev and mutation_id are required")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		replyError(w, 400, "artifact request must contain one JSON object")
		return
	}
	var restoredFrom *int64
	if restore {
		if req.Revision < 1 || req.HTML != "" {
			replyError(w, 400, "revision is required for restore")
			return
		}
		var sha string
		err := h.db.QueryRowContext(r.Context(), `SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, req.Revision).Scan(&sha)
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
	sha := attachmentDigest([]byte(req.HTML))
	fingerprint := attachmentDigest([]byte(fmt.Sprintf("%d/%t/%d/%s", *req.BaseRev, restore, req.Revision, sha)))
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
	var oldDigest, oldAuthor string
	err = tx.QueryRowContext(ctx, `SELECT revision,request_digest,COALESCE(author_user_id,'') FROM page_artifact_revisions WHERE page_id=? AND mutation_id=?`, rec.ID, req.MutationID).Scan(&previous, &oldDigest, &oldAuthor)
	if err == nil {
		if oldDigest != fingerprint || oldAuthor != user.ID {
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
	if head != *req.BaseRev {
		replyError(w, 409, "artifact revision changed; reload before saving")
		return
	}
	if head >= maxArtifactRevisions {
		writeJSON(w, 409, map[string]any{"error": "artifact revision limit reached", "code": "revision_limit"})
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND created_at>?`, rec.ID, now.Add(-time.Hour).Format(time.RFC3339Nano)).Scan(&recent); err != nil {
		replyInternalError(w, h.logger, "check artifact revision rate", err)
		return
	}
	if recent >= maxArtifactHourlyRevisions {
		w.Header().Set("Retry-After", "3600")
		writeJSON(w, 429, map[string]any{"error": "artifact revision rate limit reached", "code": "rate_limited"})
		return
	}
	revision := head + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,restored_from,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, generateCUID(), rec.ID, revision, sha, user.ID, req.MutationID, fingerprint, restoredFrom, now.Format(time.RFC3339Nano)); err != nil {
		replyInternalError(w, h.logger, "save artifact revision", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO page_artifacts(page_id,head_rev) VALUES (?,?) ON CONFLICT(page_id) DO UPDATE SET head_rev=excluded.head_rev`, rec.ID, revision); err != nil {
		replyInternalError(w, h.logger, "save artifact head", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pages SET updated_at=? WHERE id=?`, now.Format(time.RFC3339), rec.ID); err != nil {
		replyInternalError(w, h.logger, "update artifact page", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit artifact revision", err)
		return
	}
	if h.journal != nil {
		_, err := h.journal.Emit(r.Context(), journal.Entry{WorkspaceID: ws, Type: journal.EntryPageSpecChanged, Severity: journal.SeverityInfo, ActorType: journal.ActorUser, ActorID: user.ID, Summary: fmt.Sprintf("Artifact %s revision %d saved", rec.Slug, revision), Payload: map[string]any{"page_id": rec.ID, "page": rec.Slug, "artifact_revision": revision, "sha256": sha, "restored_from": restoredFrom}})
		if err != nil && h.logger != nil {
			h.logger.Warn("artifact revision audit failed", "error", err)
		}
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"page_id": rec.ID})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{"revision": revision, "head_rev": revision})
}
