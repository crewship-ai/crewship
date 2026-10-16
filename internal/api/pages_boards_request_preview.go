package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const maxBoardRequestPreviewInput = 1 << 20
const maxBoardRequestPreviewOutput = 2 << 20
const boardRequestBodyLimit = (maxBoardRequestPreviewInput * 4 / 3) + 65536 + 16

type boardRequestPreview struct {
	SHA                 string
	Width, Height, Size int
}
type boardPreviewBuffer struct{ bytes.Buffer }

func (b *boardPreviewBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > maxBoardRequestPreviewOutput {
		return 0, errors.New("preview exceeds 2 MiB canonical limit")
	}
	return b.Buffer.Write(data)
}
func canonicalBoardRequestPreview(raw []byte) ([]byte, int, int, error) {
	if len(raw) == 0 || len(raw) > maxBoardRequestPreviewInput {
		return nil, 0, 0, errors.New("preview exceeds 1 MiB PNG input limit")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != "png" || config.Width < 1 || config.Height < 1 || config.Width > 1024 || config.Height > 1024 || config.Width*config.Height > 1024*1024 {
		return nil, 0, 0, errors.New("preview requires bounded PNG pixels, at most 1024 by 1024")
	}
	pixels, err := png.Decode(bytes.NewReader(raw))
	if err != nil || pixels.Bounds().Dx() != config.Width || pixels.Bounds().Dy() != config.Height {
		return nil, 0, 0, errors.New("invalid preview pixels")
	}
	out := &boardPreviewBuffer{}
	if err = png.Encode(out, pixels); err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), config.Width, config.Height, nil
}
func (h *PageHandler) prepareBoardRequestPreview(w http.ResponseWriter, r *http.Request, raw []byte) (*boardRequestPreview, func(), bool) {
	if raw == nil {
		return nil, func() {}, true
	}
	data, width, height, err := canonicalBoardRequestPreview(raw)
	if err != nil {
		replyError(w, 422, err.Error())
		return nil, nil, false
	}
	if h.artifactStoragePath == "" {
		replyError(w, 503, "preview storage unavailable")
		return nil, nil, false
	}
	sha := attachmentDigest(data)
	unlock := lockAttachmentBlob(WorkspaceIDFromContext(r.Context()), sha)
	if _, _, err = storeAttachmentBlob(h.artifactStoragePath, WorkspaceIDFromContext(r.Context()), data); err != nil {
		unlock()
		h.boardError(w, err)
		return nil, nil, false
	}
	return &boardRequestPreview{SHA: sha, Width: width, Height: height, Size: len(data)}, unlock, true
}
func (h *PageHandler) saveBoardRequestPreview(ctx context.Context, tx *sql.Tx, id, workspace string, preview *boardRequestPreview) error {
	if preview == nil {
		return nil
	}
	var size int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(p.byte_size),0) FROM page_board_request_previews p JOIN page_board_requests r ON r.id=p.request_id WHERE r.workspace_id=?`, workspace).Scan(&size); err != nil {
		return err
	}
	if excess := size + int64(preview.Size) - (128 << 20); excess > 0 {
		if err := compactTerminalBoardPreviews(ctx, tx, workspace, excess, time.Now()); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(p.byte_size),0) FROM page_board_request_previews p JOIN page_board_requests r ON r.id=p.request_id WHERE r.workspace_id=?`, workspace).Scan(&size); err != nil {
			return err
		}
		if size+int64(preview.Size) > 128<<20 {
			return errBoardPreviewQuota
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO page_board_request_previews(request_id,sha256,width,height,byte_size,created_at) VALUES(?,?,?,?,?,?)`, id, preview.SHA, preview.Width, preview.Height, preview.Size, tsformat.Format(time.Now()))
	return err
}

var errBoardPreviewQuota = errors.New("retained Board request preview quota reached")

func (h *PageHandler) GetBoardRequestPreviewAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	r, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	select {
	case boardFileReadSlots <- struct{}{}:
		defer func() { <-boardFileReadSlots }()
	case <-r.Context().Done():
		replyError(w, 408, "preview read cancelled")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	id := r.PathValue("requestId")
	var kind string
	if tx.QueryRowContext(r.Context(), `SELECT kind FROM page_board_requests WHERE id=?`, id).Scan(&kind) != nil {
		replyError(w, 403, "job authority required")
		return
	}
	board, err := h.checkBoardRequestRun(r.Context(), id, a, kind)
	if err != nil || !h.boardRequestPreviewReferencesCurrent(r.Context(), board, id, a) {
		replyError(w, 403, "preview source authority changed")
		return
	}
	var sha string
	var width, height, size int
	if tx.QueryRowContext(r.Context(), `SELECT sha256,width,height,byte_size FROM page_board_request_previews WHERE request_id=?`, id).Scan(&sha, &width, &height, &size) != nil {
		replyError(w, 404, "request preview unavailable")
		return
	}
	data, actualWidth, actualHeight, err := readBoardRaster(h.artifactStoragePath, a.WorkspaceID, sha, "image/png")
	if err != nil || len(data) != size || size > maxBoardRequestPreviewOutput || actualWidth != width || actualHeight != height || width > 1024 || height > 1024 {
		replyError(w, 503, "request preview unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"kind": "untrusted-human-illustration", "authority": "canonical-snapshot-only", "content_type": "image/png", "data": data, "width": width, "height": height, "sha256": sha})
}
func (h *PageHandler) boardRequestPreviewReferencesCurrent(ctx context.Context, board, id string, a *artifactRunAuthority) bool {
	rec, err := h.loadBoardRequestPage(ctx, board)
	if err != nil {
		return false
	}
	viewer, err := h.loadViewer(ctx, a.WorkspaceID, a.HumanUserID)
	if err != nil {
		return false
	}
	visible, err := h.canSeePage(ctx, a.WorkspaceID, rec, nil, viewer)
	if err != nil || !visible {
		return false
	}
	q := h.pageAuthReader(ctx)
	var missing int
	if q.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_board_request_files refs LEFT JOIN page_board_files f ON f.id=refs.file_id AND f.board_page_id=? WHERE refs.request_id=? AND f.id IS NULL`, board, id).Scan(&missing) != nil || missing != 0 {
		return false
	}
	var snapshotRaw string
	if q.QueryRowContext(ctx, `SELECT snapshot_json FROM page_board_requests WHERE id=?`, id).Scan(&snapshotRaw) != nil {
		return false
	}
	var snapshot boardRequestSnapshot
	if json.Unmarshal([]byte(snapshotRaw), &snapshot) != nil {
		return false
	}
	for _, raw := range snapshot.Elements {
		element, err := pages.ValidateBoardElement(raw)
		if err != nil {
			return false
		}
		if element.FileID != nil {
			var present int
			if q.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_board_request_files refs JOIN page_board_files f ON f.id=refs.file_id AND f.board_page_id=? WHERE refs.request_id=? AND refs.file_id=?`, board, id, *element.FileID).Scan(&present) != nil || present != 1 {
				return false
			}
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(source_page_id,''),COALESCE(source_revision,0) FROM page_board_request_sources WHERE request_id=?`, id)
	if err != nil {
		return false
	}
	type source struct {
		page     string
		revision int64
	}
	sources := []source{}
	for rows.Next() {
		var s source
		if err = rows.Scan(&s.page, &s.revision); err != nil {
			break
		}
		sources = append(sources, s)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false
	}
	for _, s := range sources {
		if s.page == "" || !h.boardJobSourceVisible(ctx, s.page, a.AgentID) {
			return false
		}
		var present int
		if q.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=?`, s.page, s.revision).Scan(&present) != nil || present != 1 {
			return false
		}
	}
	return true
}
