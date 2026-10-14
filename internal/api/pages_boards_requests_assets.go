package api

import (
	"context"
	"database/sql"
	"net/http"
)

// Job assets are admitted normalized references, not a route to enumerate the
// Board or any source Page. Every read rechecks the current human Board ACL.
func (h *PageHandler) GetBoardRequestAsset(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	r, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	select {
	case boardFileReadSlots <- struct{}{}:
		defer func() { <-boardFileReadSlots }()
	case <-r.Context().Done():
		replyError(w, 408, "selected asset read cancelled")
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
	if err != nil {
		replyError(w, 403, "job authority required")
		return
	}
	rec, err := h.loadBoardRequestPage(r.Context(), board)
	if err != nil {
		replyError(w, 403, "source board unavailable")
		return
	}
	viewer, err := h.loadViewer(r.Context(), a.WorkspaceID, a.HumanUserID)
	if err != nil {
		replyError(w, 403, "source authority unavailable")
		return
	}
	visible, err := h.canSeePage(r.Context(), a.WorkspaceID, rec, nil, viewer)
	if err != nil || !visible {
		replyError(w, 403, "source board authority revoked")
		return
	}
	if file := r.PathValue("fileId"); file != "" {
		var sha, mime string
		if tx.QueryRowContext(r.Context(), `SELECT f.sha256,f.content_type FROM page_board_request_files allowed JOIN page_board_files f ON f.id=allowed.file_id WHERE allowed.request_id=? AND allowed.file_id=? AND f.board_page_id=?`, id, file, board).Scan(&sha, &mime) != nil {
			replyError(w, 404, "selected file unavailable")
			return
		}
		data, width, height, err := readBoardRaster(h.artifactStoragePath, a.WorkspaceID, sha, mime)
		if err != nil {
			replyError(w, 503, "selected file unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"id": file, "content_type": mime, "data": data, "width": width, "height": height})
		return
	}
	var source string
	var revision int64
	if tx.QueryRowContext(r.Context(), `SELECT source_page_id,source_revision FROM page_board_request_sources WHERE id=? AND request_id=?`, r.PathValue("sourceId"), id).Scan(&source, &revision) != nil || !h.boardJobSourceVisible(r.Context(), source, a.AgentID) {
		replyError(w, 404, "selected source unavailable")
		return
	}
	var sha string
	if tx.QueryRowContext(r.Context(), `SELECT sha256 FROM page_artifact_revisions WHERE page_id=? AND revision=?`, source, revision).Scan(&sha) != nil {
		replyError(w, 404, "selected revision unavailable")
		return
	}
	data, err := readAttachmentBlob(h.artifactStoragePath, a.WorkspaceID, sha)
	if err != nil {
		replyError(w, 503, "selected revision unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("sourceId"), "revision": revision, "html": string(data)})
}
