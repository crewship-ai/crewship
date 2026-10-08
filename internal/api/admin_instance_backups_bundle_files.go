package api

// Reading one catalogued bundle as an instance admin — any scope, the
// instance bundles included:
//
//	GET /api/v1/admin/instance/backups/bundles/inspect?path=   the manifest (plaintext; nothing is decrypted)
//	GET /api/v1/admin/instance/backups/bundles/verify?path=    the sealed payload's checksum against the manifest
//	GET /api/v1/admin/instance/backups/bundles/download?path=  the bundle's bytes
//
// The legacy /api/v1/admin/backups/{inspect,verify,download} routes answer
// for the workspace in context only, so an instance bundle — which belongs
// to no workspace — was a 404 there. These resolve the path through the
// catalog (catalogued), so no file outside it is ever opened.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// InspectBundle is GET /api/v1/admin/instance/backups/bundles/inspect.
func (h *InstanceBackupsHandler) InspectBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entry, ok := h.catalogued(w, ctx, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	m, err := backup.Inspect(ctx, entry.FilePath)
	if err != nil {
		h.logger.Warn("instance backups: inspect", "path", entry.FilePath, "error", err)
		replyError(w, http.StatusUnprocessableEntity, "the backup's manifest could not be read")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// VerifyBundle is GET /api/v1/admin/instance/backups/bundles/verify: proof
// level 1, without a key. The body is the legacy verify's.
func (h *InstanceBackupsHandler) VerifyBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entry, ok := h.catalogued(w, ctx, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	res, err := backup.Verify(ctx, entry.FilePath)
	if err != nil {
		h.fail(w, "verify bundle", err)
		return
	}
	errStr := ""
	if res.Err != nil {
		errStr = res.Err.Error()
	}
	writeJSON(w, http.StatusOK, backupVerifyResponse{
		Valid:                   res.Valid,
		SizeBytes:               res.Size,
		Manifest:                res.Manifest,
		Error:                   errStr,
		CompletenessChecked:     res.CompletenessChecked,
		CompletenessSkipReason:  res.CompletenessSkipReason,
		TableRowCountMismatches: res.TableRowCountMismatches,
		ScopeShortfalls:         res.ScopeShortfalls,
	})
}

// DownloadBundle is GET /api/v1/admin/instance/backups/bundles/download. It
// streams the bundle as stored — encrypted, as every new bundle is — and
// records the download in the instance audit log.
func (h *InstanceBackupsHandler) DownloadBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entry, ok := h.catalogued(w, ctx, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	f, err := os.Open(entry.FilePath)
	if err != nil {
		h.logger.Warn("instance backups: download open", "path", entry.FilePath, "error", err)
		replyError(w, http.StatusNotFound, "the backup file is no longer on disk")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		h.fail(w, "stat bundle", err)
		return
	}
	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(entry.FilePath)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	n, copyErr := io.Copy(w, f)

	// The audit entry is a write, and this is a GET the quiet window's gate
	// does not count: enter it on its own (waiting out a window), on a
	// context a disconnected client cannot cancel.
	actx := context.WithoutCancel(ctx)
	if err := quiesce.Do(actx, func(actx context.Context) error {
		tx, err := h.db.BeginTx(actx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := auditInstance(actx, r, tx, "instance.backup_downloaded", "backup", entry.FilePath, entry.WorkspaceID, map[string]any{
			"scope": entry.Scope, "bytes": n, "complete": copyErr == nil && n == info.Size(),
		}); err != nil {
			return err
		}
		return tx.Commit()
	}); err != nil {
		h.logger.Warn("instance backups: download audit", "path", entry.FilePath, "error", err)
	}
}
