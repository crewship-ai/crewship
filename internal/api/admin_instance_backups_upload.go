package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

type backupUploadResponse struct {
	Path               string `json:"path"`
	Scope              string `json:"scope"`
	SizeBytes          int64  `json:"size_bytes"`
	FormatVersion      int    `json:"format_version"`
	ProofLevel         int    `json:"proof_level"`
	Duplicate          bool   `json:"duplicate"`
	ConversionRequired bool   `json:"conversion_required"`
}

// instanceBackupUploadPath must match the route literal in router_admin.go,
// which gen-openapi reads; TestInstanceBackupUploadIsExemptFromTheAPIBodyCap
// fails with 404 when they drift.
const instanceBackupUploadPath = "/api/v1/admin/instance/backups/bundles/upload"

// uploadIdleTimeout is how long the upload may make no progress. The server's
// ReadTimeout covers a whole request and would cut every large upload short,
// so the read deadline moves forward with each chunk instead.
var uploadIdleTimeout = 2 * time.Minute

// progressDeadlineReader extends the connection's read deadline before every
// read: a slow but moving upload continues, a stalled one ends.
type progressDeadlineReader struct {
	reader     io.Reader
	controller *http.ResponseController
	idle       time.Duration
}

func (r progressDeadlineReader) Read(p []byte) (int, error) {
	if err := r.controller.SetReadDeadline(time.Now().Add(r.idle)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return r.reader.Read(p)
}

// UploadBundle accepts an encrypted archive, never a decryption key. Validation
// records checksum proof only; contents checks and restores remain separate.
// The route is exempt from the API body cap (bodyCapExempt); the backup
// package bounds the stream.
func (h *InstanceBackupsHandler) UploadBundle(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (media != "application/octet-stream" && media != "application/zstd") {
		replyError(w, http.StatusUnsupportedMediaType, "send the encrypted archive as application/octet-stream")
		return
	}
	if !h.uploadBusy.CompareAndSwap(false, true) {
		replyError(w, http.StatusConflict, "another backup upload is in progress")
		return
	}
	defer h.uploadBusy.Store(false)
	body := progressDeadlineReader{reader: r.Body, controller: http.NewResponseController(w), idle: uploadIdleTimeout}
	result, err := backup.ImportUploadedBundle(r.Context(), h.db, body, backup.UploadOptions{
		Directory:     h.recoveryConfig().OutputDir,
		ContentLength: r.ContentLength,
		Finalize: func(ctx context.Context, tx *sql.Tx, result *backup.ImportedBundle) error {
			return auditInstance(ctx, r, tx, "instance.backup_uploaded", "backup", result.Entry.FilePath, result.Entry.WorkspaceID,
				map[string]any{"scope": result.Entry.Scope, "bytes": result.Entry.Size, "duplicate": result.Duplicate, "proof_level": result.Entry.ProofLevel})
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, backup.ErrUploadLimit), errors.As(err, new(*http.MaxBytesError)):
			replyError(w, http.StatusRequestEntityTooLarge, "backup exceeds the 64 GiB upload limit")
		case errors.Is(err, backup.ErrUploadSpace), errors.Is(err, syscall.ENOSPC):
			replyError(w, http.StatusInsufficientStorage, "not enough free disk space for backup upload")
		case errors.Is(err, backup.ErrUploadInvalid):
			replyError(w, http.StatusUnprocessableEntity, "not a supported encrypted backup archive")
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), uploadInterrupted(err):
			replyError(w, http.StatusBadRequest, "backup upload was interrupted")
		default:
			h.fail(w, "upload bundle", err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, backupUploadResponse{
		Path: result.Entry.FilePath, Scope: result.Entry.Scope, SizeBytes: result.Entry.Size,
		FormatVersion: result.Entry.FormatVersion, ProofLevel: result.Entry.ProofLevel,
		Duplicate: result.Duplicate, ConversionRequired: result.ConversionRequired,
	})
}

// uploadInterrupted reports a client that stopped sending: a closed
// connection or the idle read deadline. Neither is a server failure.
func uploadInterrupted(err error) bool {
	var netErr net.Error
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &netErr) && netErr.Timeout()
}
