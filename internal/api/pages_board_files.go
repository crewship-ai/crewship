package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const maxBoardFileInput = 5 << 20
const maxBoardFileOutput = 8 << 20

type boundedImageBuffer struct{ bytes.Buffer }

func (b *boundedImageBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > maxBoardFileOutput {
		return 0, errors.New("canonical image too large")
	}
	return b.Buffer.Write(data)
}
func canonicalBoardImage(raw []byte) ([]byte, string, int, int, error) {
	if len(raw) == 0 || len(raw) > maxBoardFileInput {
		return nil, "", 0, 0, errors.New("image exceeds input limit")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 || config.Width*config.Height > 4*1024*1024 {
		return nil, "", 0, 0, errors.New("only bounded PNG/JPEG images are supported")
	}
	decoded, actual, err := image.Decode(bytes.NewReader(raw))
	if err != nil || actual != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return nil, "", 0, 0, errors.New("invalid image pixels")
	}
	output := &boundedImageBuffer{}
	mime := "image/png"
	if format == "png" {
		err = png.Encode(output, decoded)
	} else {
		mime = "image/jpeg"
		err = jpeg.Encode(output, decoded, &jpeg.Options{Quality: 90})
	}
	if err != nil {
		return nil, "", 0, 0, err
	}
	return output.Bytes(), mime, config.Width, config.Height, nil
}
func (h *PageHandler) UploadBoardFile(w http.ResponseWriter, r *http.Request) {
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	release, ok := acquireBoardWrite(w, r)
	if !ok {
		return
	}
	defer release()
	// Authorize before spending decode memory, then fence/recheck at commit.
	preflight, ok := h.boardPage(w, r, true)
	if !ok {
		return
	}
	if !preflight.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	mutation := r.URL.Query().Get("mutation_id")
	base, err := strconv.ParseInt(r.URL.Query().Get("base_seq"), 10, 64)
	if err != nil || base < 0 || base >= 9007199254740991 || !boardMutationValid(mutation) {
		replyError(w, 400, "base_seq and mutation_id required")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBoardFileInput))
	if err != nil {
		replyError(w, 413, "image exceeds 5 MiB")
		return
	}
	data, mime, width, height, err := canonicalBoardImage(raw)
	if err != nil {
		replyError(w, 422, err.Error())
		return
	}
	if h.artifactStoragePath == "" {
		replyError(w, 503, "board file storage unavailable")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	sha := attachmentDigest(data)
	digest := boardFingerprint("file-upload", []any{base, attachmentDigest(raw)})
	unlock := lockAttachmentBlob(ws, sha)
	defer unlock()
	if _, _, err = storeAttachmentBlob(h.artifactStoragePath, ws, data); err != nil {
		h.boardError(w, err)
		return
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	var author, old, file string
	var result, head int64
	err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(author_user_id,''),fingerprint,result_seq,COALESCE(result_file_id,'') FROM page_board_mutations WHERE board_page_id=? AND mutation_id=?`, rec.ID, mutation).Scan(&author, &old, &result, &file)
	if err == nil {
		if author != UserFromContext(r.Context()).ID || old != digest {
			replyError(w, 409, "mutation_id was already used for a different request")
			return
		}
		if err = tx.QueryRowContext(r.Context(), `SELECT seq FROM page_boards WHERE page_id=?`, rec.ID).Scan(&head); err != nil {
			h.boardError(w, err)
			return
		}
		h.writeBoardFileResult(w, r, tx, rec.ID, file, head, result, 200)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		h.boardError(w, err)
		return
	}
	if err = tx.QueryRowContext(r.Context(), `SELECT seq FROM page_boards WHERE page_id=?`, rec.ID).Scan(&head); err != nil {
		h.boardError(w, err)
		return
	}
	if head != base {
		replyError(w, 409, "board sequence changed")
		return
	}
	var boardCount, boardBytes, workspaceCount, workspaceBytes int64
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(byte_size),0) FROM page_board_files WHERE board_page_id=?`, rec.ID).Scan(&boardCount, &boardBytes); err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(f.byte_size),0) FROM page_board_files f JOIN pages p ON p.id=f.board_page_id WHERE p.workspace_id=?`, ws).Scan(&workspaceCount, &workspaceBytes); err != nil {
		h.boardError(w, err)
		return
	}
	if boardCount >= 100 || boardBytes+int64(len(data)) > 50<<20 || workspaceCount >= 1000 || workspaceBytes+int64(len(data)) > 500<<20 {
		replyError(w, 409, "board image storage quota reached")
		return
	}
	file = generateCUID()
	head++
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_files(id,board_page_id,sha256,content_type,width,height,byte_size,author_user_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, file, rec.ID, sha, mime, width, height, len(data), UserFromContext(r.Context()).ID, tsformat.Format(h.evaluator().Now())); err != nil {
		h.boardError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_boards SET seq=? WHERE page_id=?`, head, rec.ID); err != nil {
		h.boardError(w, err)
		return
	}
	if err = h.boardReceipt(r, tx, rec.ID, mutation, digest, head, ""); err != nil {
		h.boardError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_board_mutations SET result_file_id=? WHERE board_page_id=? AND mutation_id=?`, file, rec.ID, mutation); err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"page_id": rec.ID})
	h.writeBoardFileResult(w, r, h.db, rec.ID, file, head, head, 201)
}
func (h *PageHandler) writeBoardFileResult(w http.ResponseWriter, r *http.Request, q pageAuthQueryer, page, file string, seq, result int64, status int) {
	var mime string
	var width, height, size int
	if err := q.QueryRowContext(r.Context(), `SELECT content_type,width,height,byte_size FROM page_board_files WHERE id=? AND board_page_id=?`, file, page).Scan(&mime, &width, &height, &size); err != nil {
		h.boardError(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"file_id": file, "content_type": mime, "width": width, "height": height, "bytes": size, "seq": seq, "result_seq": result})
}
func (h *PageHandler) GetBoardFile(w http.ResponseWriter, r *http.Request) {
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	rec, ok := h.boardPage(w, r, false)
	if !ok {
		return
	}
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	rec, ok = h.boardPage(w, r, false)
	if !ok {
		return
	}
	var sha, mime string
	if err = tx.QueryRowContext(ctx, `SELECT sha256,content_type FROM page_board_files WHERE id=? AND board_page_id=?`, r.PathValue("fileId"), rec.ID).Scan(&sha, &mime); errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "board file not found")
		return
	} else if err != nil {
		h.boardError(w, err)
		return
	}
	data, err := readAttachmentBlob(h.artifactStoragePath, WorkspaceIDFromContext(ctx), sha)
	if err != nil || attachmentDigest(data) != sha {
		replyError(w, 503, "board file unavailable")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Content-Disposition", `inline; filename="board-image"`)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
func (h *PageHandler) validateBoardFileReferences(ctx context.Context, q pageAuthQueryer, pageID string, scene pages.BoardScene) error {
	for _, record := range scene.Records {
		if record.Deleted {
			continue
		}
		element, err := pages.ValidateBoardElement(record.Element)
		if err != nil {
			return err
		}
		if element.Type != "image" {
			continue
		}
		var exists int
		if element.FileID == nil || q.QueryRowContext(ctx, `SELECT 1 FROM page_board_files WHERE id=? AND board_page_id=?`, *element.FileID, pageID).Scan(&exists) != nil {
			return fmt.Errorf("image file is not available on this board")
		}
	}
	return nil
}
