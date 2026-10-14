package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const maxBoardFileInput = 5 << 20
const maxBoardFileOutput = 8 << 20

// Encoding may expand a compressed raster. Refuse an over-limit write before
// it reaches disk, independently of input bytes and decoded pixel dimensions.
type boundedImageWriter struct {
	writer io.Writer
	size   int64
}

func (b *boundedImageWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > maxBoardFileOutput-b.size {
		return 0, errors.New("canonical image too large")
	}
	n, err := b.writer.Write(data)
	b.size += int64(n)
	return n, err
}

func canonicalBoardImage(source io.ReadSeeker, output io.Writer, inputSize int64) (string, int, int, error) {
	if inputSize == 0 || inputSize > maxBoardFileInput {
		return "", 0, 0, errors.New("image exceeds input limit")
	}
	config, format, err := image.DecodeConfig(source)
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 || config.Width*config.Height > 4*1024*1024 {
		return "", 0, 0, errors.New("only bounded PNG/JPEG images are supported")
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return "", 0, 0, err
	}
	decoded, actual, err := image.Decode(source)
	if err != nil || actual != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return "", 0, 0, errors.New("invalid image pixels")
	}
	mime := "image/png"
	if format == "png" {
		err = png.Encode(output, decoded)
	} else {
		mime = "image/jpeg"
		err = jpeg.Encode(output, decoded, &jpeg.Options{Quality: 90})
	}
	if err != nil {
		return "", 0, 0, err
	}
	return mime, config.Width, config.Height, nil
}

type boardImageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r boardImageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Staging files are private (CreateTemp uses 0600) and remain owned by this
// request. They are never database references or publicly readable blobs.
func boardImageTemp(root, workspace string) (*os.File, func(), error) {
	// The fixed shard is only a staging location; final names are still derived
	// from canonical bytes. Using the existing tree also lets attachment GC
	// reclaim crashed uploads with its normal .tmp-* age floor.
	path, err := attachmentBlobPath(root, workspace, "0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		return nil, nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".tmp-board-image-*")
	if err != nil {
		return nil, nil, err
	}
	return file, func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}, nil
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
	if h.artifactStoragePath == "" {
		replyError(w, 503, "board file storage unavailable")
		return
	}
	// Keep compressed input and canonical output on disk. Only the two admitted
	// decoders retain pixels; a 16-bit RGBA PNG can need 32 MiB at 2048².
	ws := WorkspaceIDFromContext(r.Context())
	input, closeInput, err := boardImageTemp(h.artifactStoragePath, ws)
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer closeInput()
	rawDigest := sha256.New()
	inputSize, err := io.Copy(io.MultiWriter(input, rawDigest), boardImageContextReader{r.Context(), http.MaxBytesReader(w, r.Body, maxBoardFileInput)})
	if err != nil {
		var tooLarge *http.MaxBytesError
		if r.Context().Err() != nil {
			replyError(w, 408, "board image upload cancelled")
		} else if errors.As(err, &tooLarge) {
			replyError(w, 413, "image exceeds 5 MiB")
		} else {
			h.boardError(w, err)
		}
		return
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		h.boardError(w, err)
		return
	}
	output, closeOutput, err := boardImageTemp(h.artifactStoragePath, ws)
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer closeOutput()
	canonicalDigest := sha256.New()
	bounded := &boundedImageWriter{writer: io.MultiWriter(output, canonicalDigest)}
	mime, width, height, err := canonicalBoardImage(input, bounded, inputSize)
	if err != nil {
		replyError(w, 422, err.Error())
		return
	}
	if r.Context().Err() != nil {
		replyError(w, 408, "board image upload cancelled")
		return
	}
	sha := hex.EncodeToString(canonicalDigest.Sum(nil))
	digest := boardFingerprint("file-upload", []any{base, hex.EncodeToString(rawDigest.Sum(nil))})
	unlock := lockAttachmentBlob(ws, sha)
	defer unlock()
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		h.boardError(w, err)
		return
	}
	if _, _, err = storeAttachmentBlobStream(h.artifactStoragePath, ws, sha, boardImageContextReader{r.Context(), output}); err != nil {
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
	if expired, e := boardHistoryExpired(r.Context(), tx, rec.ID, mutation); e != nil {
		h.boardError(w, e)
		return
	} else if expired {
		h.boardError(w, errBoardHistoryExpired)
		return
	}
	if err = h.compactBoardHistory(r.Context(), tx, rec.ID); err != nil {
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
	if boardCount >= 100 || boardBytes+bounded.size > 50<<20 || workspaceCount >= 1000 || workspaceBytes+bounded.size > 500<<20 {
		replyError(w, 409, "board image storage quota reached")
		return
	}
	file = generateCUID()
	head++
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_files(id,board_page_id,sha256,content_type,width,height,byte_size,author_user_id,created_at,last_used_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, file, rec.ID, sha, mime, width, height, bounded.size, UserFromContext(r.Context()).ID, tsformat.Format(h.evaluator().Now()), tsformat.Format(h.evaluator().Now())); err != nil {
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

var boardFileReadSlots = make(chan struct{}, 4)

func readBoardRaster(root, workspace, sha, mime string) ([]byte, int, int, error) {
	if root == "" {
		return nil, 0, 0, errors.New("storage unavailable")
	}
	path, err := attachmentBlobPath(root, workspace, sha)
	if err != nil {
		return nil, 0, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBoardFileOutput+1))
	if err != nil || len(data) > maxBoardFileOutput || attachmentDigest(data) != sha {
		return nil, 0, 0, errors.New("invalid image bytes")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 || (format != "png" && format != "jpeg") || mime != "image/"+format {
		return nil, 0, 0, errors.New("invalid stored raster")
	}
	return data, config.Width, config.Height, nil
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
	select {
	case boardFileReadSlots <- struct{}{}:
		defer func() { <-boardFileReadSlots }()
	case <-r.Context().Done():
		replyError(w, 408, "board image read cancelled")
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
	data, width, height, err := readBoardRaster(h.artifactStoragePath, WorkspaceIDFromContext(ctx), sha, mime)
	if err != nil {
		replyError(w, 503, "board file unavailable")
		return
	}
	w.Header().Set("X-Crewship-Image-Width", strconv.Itoa(width))
	w.Header().Set("X-Crewship-Image-Height", strconv.Itoa(height))
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
