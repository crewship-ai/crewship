package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/crewship-ai/crewship/internal/pageassets"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

var pageAssetDecodeSlots = make(chan struct{}, 2)
var pageAssetReadSlots = make(chan struct{}, 4)
var pageAssetMutation = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

const maxPageAssets = 64
const maxPageAssetBytes = 64 << 20
const pageAssetTTL = 24 * time.Hour

type pageAssetMetadata struct {
	ID          string `json:"id"`
	PageID      string `json:"page_id"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ByteSize    int64  `json:"byte_size"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at"`
}

// Asset authority is the live owning Page, never digest knowledge or URL shape.
func (h *PageHandler) assetPage(w http.ResponseWriter, r *http.Request, write bool) (*pageRecord, bool) {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return nil, false
	}
	// The existing human asset routes are scopeSelf: retain their Page ACL
	// authority while refreshing the exact admitted credential, including
	// again through the upload's writer transaction after decoding.
	if err := validateContentHumanCredential(r, h.pageAuthReader(r.Context()), scopeSelf); err != nil {
		replyError(w, 401, "current human credential required")
		return nil, false
	}
	ws := WorkspaceIDFromContext(r.Context())
	rec, err := h.loadPage(r.Context(), ws, r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load asset page", err)
		return nil, false
	}
	role, err := h.liveContentHumanRole(r.Context(), ws, user.ID)
	if err != nil {
		replyError(w, 403, "active trusted workspace membership required")
		return nil, false
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, role))
	viewer, err := h.loadViewer(r.Context(), ws, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "authorize asset page", err)
		return nil, false
	}
	panels, err := h.assetPagePanels(r.Context(), rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load asset page panels", err)
		return nil, false
	}
	visible, err := h.canSeePage(r.Context(), ws, rec, panels, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "authorize asset page", err)
		return nil, false
	}
	if !visible {
		replyError(w, 404, "page not found")
		return nil, false
	}
	if write && !h.mayEditSpec(r.Context(), ws, user.ID, role, rec) {
		replyError(w, 403, "page write permission required")
		return nil, false
	}
	if h.artifactStoragePath == "" {
		replyError(w, 503, "page asset storage unavailable")
		return nil, false
	}
	return rec, true
}
func (h *PageHandler) UploadPageAsset(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.assetPage(w, r, true)
	if !ok {
		return
	}
	mutation := r.URL.Query().Get("mutation_id")
	if !pageAssetMutation.MatchString(mutation) {
		replyError(w, 400, "mutation_id must contain 1..128 letters, digits, underscores or hyphens")
		return
	}
	select {
	case pageAssetDecodeSlots <- struct{}{}:
		defer func() { <-pageAssetDecodeSlots }()
	case <-r.Context().Done():
		replyError(w, 408, "asset upload cancelled")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pageassets.MaxInput))
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			replyError(w, 413, "image exceeds 5 MiB")
		} else {
			replyError(w, 400, "cannot read asset")
		}
		return
	}
	fingerprint := attachmentDigest(raw)
	raster, err := pageassets.Canonical(raw)
	if err != nil {
		replyError(w, 422, err.Error())
		return
	}
	if r.Context().Err() != nil {
		replyError(w, 408, "asset upload cancelled")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	sha := attachmentDigest(raster.Bytes)
	unlock := lockAttachmentBlob(ws, sha)
	defer unlock()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin page asset", err)
		return
	}
	defer tx.Rollback()
	fenced, err := tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, rec.ID, ws)
	if err != nil {
		replyInternalError(w, h.logger, "fence page asset", err)
		return
	}
	if n, _ := fenced.RowsAffected(); n != 1 {
		replyError(w, 404, "page not found")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	checked, ok := h.assetPage(w, r, true)
	if !ok {
		return
	}
	if checked.ID != rec.ID {
		replyError(w, 409, "page identity changed")
		return
	}
	now := tsformat.Format(time.Now().UTC())
	var existing, author, digest string
	err = tx.QueryRowContext(r.Context(), `SELECT id,COALESCE(author_user_id,''),request_digest FROM page_assets WHERE page_id=? AND mutation_id=?`, rec.ID, mutation).Scan(&existing, &author, &digest)
	if err == nil {
		if author != UserFromContext(r.Context()).ID || digest != fingerprint {
			replyError(w, 409, "mutation_id already used for a different upload")
			return
		}
		asset, err := readPageAssetMetadata(r.Context(), tx, rec.ID, existing, now)
		if err != nil {
			replyError(w, 410, "asset upload receipt expired")
			return
		}
		writeJSON(w, 200, asset)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read asset receipt", err)
		return
	}
	// Staging expiry never deletes retained rows. Future typed revision integration
	// must retain and attach within its own durable revision transaction.
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM page_assets WHERE page_id=? AND state='staged' AND expires_at<=?`, rec.ID, now); err != nil {
		replyInternalError(w, h.logger, "expire staged assets", err)
		return
	}
	var count, total int64
	if err = tx.QueryRowContext(r.Context(), `SELECT count(*),COALESCE(sum(byte_size),0) FROM page_assets WHERE page_id=?`, rec.ID).Scan(&count, &total); err != nil {
		replyInternalError(w, h.logger, "count page assets", err)
		return
	}
	if count >= maxPageAssets || int64(len(raster.Bytes)) > maxPageAssetBytes-total {
		replyError(w, 409, "page asset count or byte quota reached")
		return
	}
	if _, _, err = storeAttachmentBlob(h.artifactStoragePath, ws, raster.Bytes); err != nil {
		replyInternalError(w, h.logger, "store page asset", err)
		return
	}
	asset := pageAssetMetadata{ID: generateCUID(), PageID: rec.ID, SHA256: sha, ContentType: raster.MIME, Width: raster.Width, Height: raster.Height, ByteSize: int64(len(raster.Bytes)), State: "staged", CreatedAt: now, ExpiresAt: tsformat.Format(time.Now().UTC().Add(pageAssetTTL))}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO page_assets(id,page_id,sha256,content_type,width,height,byte_size,author_user_id,mutation_id,request_digest,state,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,'staged',?,?)`, asset.ID, rec.ID, sha, asset.ContentType, asset.Width, asset.Height, asset.ByteSize, UserFromContext(r.Context()).ID, mutation, fingerprint, asset.CreatedAt, asset.ExpiresAt)
	if err != nil {
		replyInternalError(w, h.logger, "insert page asset", err)
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit page asset", err)
		return
	}
	writeJSON(w, 201, asset)
}
func readPageAssetMetadata(ctx context.Context, q pageAuthQueryer, page, id, now string) (pageAssetMetadata, error) {
	var a pageAssetMetadata
	err := q.QueryRowContext(ctx, `SELECT id,page_id,sha256,content_type,width,height,byte_size,state,created_at,expires_at FROM page_assets WHERE page_id=? AND id=? AND (state='retained' OR expires_at>?)`, page, id, now).Scan(&a.ID, &a.PageID, &a.SHA256, &a.ContentType, &a.Width, &a.Height, &a.ByteSize, &a.State, &a.CreatedAt, &a.ExpiresAt)
	return a, err
}
func (h *PageHandler) GetPageAssetBytes(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("assetBytes", "bytes")
	h.GetPageAsset(w, r)
}

func (h *PageHandler) GetPageAsset(w http.ResponseWriter, r *http.Request) {
	select {
	case pageAssetReadSlots <- struct{}{}:
		defer func() { <-pageAssetReadSlots }()
	case <-r.Context().Done():
		replyError(w, 408, "asset read cancelled")
		return
	}
	artifactCommentHeaders(w)
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "read page asset", err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, ok := h.assetPage(w, r, false)
	if !ok {
		return
	}
	a, err := readPageAssetMetadata(r.Context(), tx, rec.ID, r.PathValue("assetId"), tsformat.Format(time.Now().UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page asset not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read asset metadata", err)
		return
	}
	if r.PathValue("assetBytes") == "" {
		writeJSON(w, 200, a)
		return
	}
	data, err := readPageAssetBlob(h.artifactStoragePath, WorkspaceIDFromContext(r.Context()), a.SHA256)
	if err != nil || attachmentDigest(data) != a.SHA256 || int64(len(data)) != a.ByteSize {
		replyError(w, 503, "asset content is missing or corrupt")
		return
	}
	w.Header().Set("Content-Type", a.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Disposition", `inline; filename="page-image"`)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

// Only panel ownership is needed for Page reach; no panel payload is loaded or
// exposed. Use the same authority reader as Page/ACL checks, including write tx.
func (h *PageHandler) assetPagePanels(ctx context.Context, page string) ([]*panelRecord, error) {
	rows, err := h.pageAuthReader(ctx).QueryContext(ctx, `SELECT owner_crew_id FROM page_panels WHERE page_id=?`, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var panels []*panelRecord
	for rows.Next() {
		p := new(panelRecord)
		if err := rows.Scan(&p.OwnerCrewID); err != nil {
			return nil, err
		}
		panels = append(panels, p)
	}
	return panels, rows.Err()
}

func readPageAssetBlob(root, workspace, sha string) ([]byte, error) {
	path, err := attachmentBlobPath(root, workspace, sha)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, pageassets.MaxOutput+1))
}
