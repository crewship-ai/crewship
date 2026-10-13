package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type artifactCopyRequest struct {
	SourceRevision int64  `json:"source_revision"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	MutationID     string `json:"mutation_id"`
}

// CopyArtifact creates an independent private Page with its own human author.
// The source grants, agent credentials, comments and jobs are never copied.
func (h *PageHandler) CopyArtifact(w http.ResponseWriter, r *http.Request) {
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(r.Context()) != nil || artifactAgentID(r) != "" {
		replyError(w, 401, "human authentication required")
		return
	}
	source, ok := h.artifactPage(w, r, false)
	if !ok {
		return
	}
	var req artifactCopyRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeArtifactJSON(w, r, &req) {
		return
	}
	if req.SourceRevision < 1 || len(req.MutationID) < 1 || len(req.MutationID) > 128 || strings.TrimSpace(req.MutationID) != req.MutationID {
		replyError(w, 400, "source_revision and mutation_id are required")
		return
	}
	doc := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Slug: req.Slug, Name: req.Name}}
	if err := doc.ValidateEmpty(); err != nil {
		writeSpecError(w, err)
		return
	}
	spec, err := json.Marshal(doc)
	if err != nil {
		replyInternalError(w, h.logger, "encode artifact copy", err)
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if !h.artifactPrincipalMayCreate(r.Context(), ws, user.ID) {
		replyError(w, 403, "page creation permission required")
		return
	}
	// Stable source identity and request body, not the current mutable head.
	encoded, _ := json.Marshal(struct {
		Source  string
		Request artifactCopyRequest
	}{source.ID, req})
	fingerprint := attachmentDigest(encoded)
	// A committed copy remains retryable after its source revision is pruned.
	// Current source Page read authority is still required, including on retry.
	var existing int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_artifact_copies WHERE workspace_id=? AND human_user_id=? AND mutation_id=?`, ws, user.ID, req.MutationID).Scan(&existing); err != nil {
		replyInternalError(w, h.logger, "check artifact copy retry", err)
		return
	}
	var sha, manifestJSON string
	if existing == 0 {
		var recorded sql.NullString
		if err := h.db.QueryRowContext(r.Context(), `SELECT sha256,manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=?`, source.ID, req.SourceRevision).Scan(&sha, &recorded); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, 404, "artifact revision not found")
			} else {
				replyInternalError(w, h.logger, "read copy source", err)
			}
			return
		}
		release, ok := acquireArtifactUpload(w, r)
		if !ok {
			return
		}
		defer release()
		unlock := lockAttachmentBlob(ws, sha)
		defer unlock()
		data, err := readArtifactCopySource(h.artifactStoragePath, ws, sha)
		if err != nil || len(data) == 0 || len(data) > maxArtifactHTMLBytes || !utf8.Valid(data) || attachmentDigest(data) != sha {
			replyError(w, 503, "artifact content is missing or failed integrity verification")
			return
		}
		manifest, _, err := resolveArtifactManifest(recorded, data, sha)
		if err != nil {
			replyError(w, 503, "artifact manifest is unsupported or failed integrity verification")
			return
		}
		manifestJSON, err = artifactManifestJSON(manifest)
		if err != nil {
			replyInternalError(w, h.logger, "encode copied artifact manifest", err)
			return
		}
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin artifact copy", err)
		return
	}
	defer tx.Rollback()
	// Writer fence orders revocation/pruning with this copy's final authorization.
	result, err := tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, source.ID, ws)
	if err != nil {
		replyInternalError(w, h.logger, "fence artifact copy", err)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		replyError(w, 404, "page not found")
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	var role string
	if tx.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user.ID).Scan(&role) != nil {
		replyError(w, 403, "workspace membership required")
		return
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	r = r.WithContext(ctx)
	checked, ok := h.artifactPage(w, r, false)
	if !ok {
		return
	}
	if checked.ID != source.ID || !h.artifactPrincipalMayCreate(ctx, ws, user.ID) {
		replyError(w, 403, "page creation permission required")
		return
	}
	// Retry fences survive target deletion. Never resurrect a deleted copy.
	var previousDigest string
	var target sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT request_digest,target_page_id FROM page_artifact_copies WHERE workspace_id=? AND human_user_id=? AND mutation_id=?`, ws, user.ID, req.MutationID).Scan(&previousDigest, &target)
	if err == nil {
		if previousDigest != fingerprint {
			artifactCommentError(w, 409, "mutation_conflict", "mutation_id was already used for a different copy")
			return
		}
		if !target.Valid {
			artifactCommentError(w, 409, "copy_deleted", "the original copy was deleted")
			return
		}
		var slug string
		if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=? AND owner_user_id=?`, target.String, ws, user.ID).Scan(&slug) != nil {
			replyError(w, 404, "copy is no longer available")
			return
		}
		writeJSON(w, 200, map[string]any{"page_slug": slug, "revision": 1})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read artifact copy retry", err)
		return
	}
	var currentSHA string
	if tx.QueryRowContext(ctx, `SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=?`, source.ID, req.SourceRevision).Scan(&currentSHA) != nil || currentSHA != sha {
		replyError(w, 404, "artifact revision not found")
		return
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pages WHERE workspace_id=?`, ws).Scan(&count); err != nil {
		replyInternalError(w, h.logger, "count copied pages", err)
		return
	}
	if count >= pages.MaxPagesPerWorkspace {
		artifactCommentError(w, 409, "page_limit", "workspace page limit reached")
		return
	}
	if !h.enforceArtifactWorkspaceRate(w, r, tx, ws, h.clock.Now().UTC()) {
		return
	}
	targetID := generateCUID()
	stamp := tsformat.Format(h.clock.Now().UTC())
	if _, err = tx.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, targetID, ws, req.Slug, req.Name, user.ID, string(spec), stamp, stamp); err != nil {
		if isUniqueViolation(err) {
			artifactCommentError(w, 409, "slug_conflict", "a page with this slug already exists")
		} else {
			replyInternalError(w, h.logger, "create copied page", err)
		}
		return
	}
	if err := insertPageVersion(ctx, tx, targetID, 1, string(spec), user.ID, stamp); err != nil {
		replyInternalError(w, h.logger, "record copied page version", err)
		return
	}
	revisionDigest := attachmentDigest([]byte(fmt.Sprintf("%d/%t/%d/%s", 0, false, 0, sha)))
	if _, err = tx.ExecContext(ctx, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,manifest_json) VALUES(?,?,1,?,?,?,?,?,?)`, generateCUID(), targetID, sha, user.ID, req.MutationID, revisionDigest, stamp, manifestJSON); err != nil {
		replyInternalError(w, h.logger, "save copied revision", err)
		return
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,author_user_id,created_at) VALUES(?,?,1,?,?,?)`, targetID, req.MutationID, revisionDigest, user.ID, stamp); err != nil {
		replyInternalError(w, h.logger, "save copied revision receipt", err)
		return
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`, targetID); err != nil {
		replyInternalError(w, h.logger, "save copied artifact", err)
		return
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO page_artifact_copies(id,workspace_id,human_user_id,source_page_id,source_revision,source_sha256,target_page_id,mutation_id,request_digest,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, generateCUID(), ws, user.ID, source.ID, req.SourceRevision, sha, targetID, req.MutationID, fingerprint, stamp); err != nil {
		replyInternalError(w, h.logger, "record artifact copy provenance", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit artifact copy", err)
		return
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"page_id": targetID})
	writeJSON(w, 201, map[string]any{"page_slug": req.Slug, "revision": 1})
}

// Origin links are not capabilities: resolve source visibility again on every
// authorized copy read, and preserve only the revision audit when inaccessible.
func (h *PageHandler) artifactCopyProvenance(ctx context.Context, ws, targetID, userID string) map[string]any {
	var source sql.NullString
	var revision int64
	if h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT source_page_id,source_revision FROM page_artifact_copies WHERE target_page_id=? AND workspace_id=?`, targetID, ws).Scan(&source, &revision) != nil {
		return nil
	}
	result := map[string]any{"source_revision": revision}
	if !source.Valid {
		return result
	}
	var slug string
	if h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT p.slug FROM pages p JOIN page_artifact_revisions v ON v.page_id=p.id AND v.revision=? WHERE p.id=? AND p.workspace_id=?`, revision, source.String, ws).Scan(&slug) != nil {
		return result
	}
	rec, err := h.loadPage(ctx, ws, slug)
	if err != nil {
		return result
	}
	viewer, err := h.loadViewer(ctx, ws, userID)
	if err != nil {
		return result
	}
	if visible, err := h.canSeePage(ctx, ws, rec, nil, viewer); err == nil && visible {
		result["source"] = map[string]any{"page_slug": slug}
	}
	return result
}

func readArtifactCopySource(root, workspace, sha string) ([]byte, error) {
	path, err := attachmentBlobPath(root, workspace, sha)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxArtifactHTMLBytes+1))
}

func (h *PageHandler) canCopyArtifact(r *http.Request) bool {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	return user != nil && !agent && projectAgentFrom(r.Context()) == nil && artifactAgentID(r) == "" && h.artifactPrincipalMayCreate(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
}
