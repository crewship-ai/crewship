package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Not routed yet: until the engine's xlsx writer keeps pivot table
// fields. The export is fidelity-checked (--fidelity): a Sheet that would lose any part stays
// native with code fidelity_loss and the parts named, in the dry run too. Planned
// as POST /api/v1/admin/craft/migrate-workbooks (instance administrators; CLI `crewship craft
// migrate-workbooks`). A Sheet saved before workbook files has a native document as its head; this
// adds one version that is the same Sheet as an xlsx (the engine's export), on the head by CAS,
// audited as artifact.sheet_saved with source migration. Native versions are never removed: they
// stay readable as format "native" and are refused as a restore (409 legacy_native).
//
// Safeguards: a dry run converts in memory and writes nothing; a
// real run names a complete instance backup made within craftWorkbookBackupWindow; it runs in
// batches, skips a Sheet open for editing (busy) and leaves a Sheet the engine cannot convert
// native (failed), naming a code and never the engine's text. Running it again continues.

const (
	craftWorkbookBackupWindow    = 2 * time.Hour
	craftWorkbookMigrationBatch  = 50
	craftWorkbookMigrationMax    = 500
	craftWorkbookMigrationActor  = "system:sheet-migration"
	craftSheetSavedByMigration   = "migration"
	craftWorkbookMigrationTarget = "workbook.xlsx"
)

type craftWorkbookMigrationRequest struct {
	DryRun   bool   `json:"dry_run"`
	BackupID string `json:"backup_id"`
	Limit    int    `json:"limit"`
}

type craftWorkbookMigrationPage struct {
	WorkspaceID   string `json:"workspaceId"`
	PageID        string `json:"pageId"`
	Version       string `json:"version"`
	NativeBytes   string `json:"nativeBytes"`
	WorkbookBytes string `json:"workbookBytes,omitempty"`
	// would_migrate (dry run), migrated, busy (open for editing or changed meanwhile), failed
	Status string `json:"status"`
	// failed: read, convert, fidelity_loss, not_workbook, store
	Code string `json:"code,omitempty"`
	// fidelity_loss: what the workbook would lose (part kind and count, never names or content).
	Losses     []craftsession.FidelityPart `json:"losses,omitempty"`
	NewVersion string                      `json:"newVersion,omitempty"`
}

type craftWorkbookMigrationReport struct {
	DryRun bool `json:"dryRun"`
	// Pending is how many native Sheets were waiting when the run started.
	Pending  int                          `json:"pending"`
	Migrated int                          `json:"migrated"`
	Busy     int                          `json:"busy"`
	Failed   int                          `json:"failed"`
	BackupID string                       `json:"backupId,omitempty"`
	Pages    []craftWorkbookMigrationPage `json:"pages"`
	// More: native Sheets remain beyond this batch; run again.
	More bool `json:"more"`
}

// craftNativeSheetsPending counts the Sheets whose head is still a native document.
func craftNativeSheetsPending(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM artifact_version_heads h JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=h.latest_version
		WHERE h.content_type='gridcraft' AND json_extract(v.manifest_json,'$.entry_point')='document.json'`).Scan(&n)
	return n, err
}

func (d *craftNativeDocumentsHTTP) migrateWorkbooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req craftWorkbookMigrationRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) || req.Limit < 0 || req.Limit > craftWorkbookMigrationMax || len(req.BackupID) > 128 {
		replyError(w, 400, "invalid migration request")
		return
	}
	if req.Limit == 0 {
		req.Limit = craftWorkbookMigrationBatch
	}
	user := UserFromContext(r.Context())
	if d == nil || d.human == nil || d.human.pages == nil || user == nil {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	db := d.human.pages.db
	profile, ok := d.application("gridcraft")
	if !ok {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	if !req.DryRun {
		if profile.Store.Format != mooring.NativeFormatWorkbook {
			replyCode(w, 409, "workbook_off", "this server keeps Sheets as native documents; install a Sheet package that keeps workbook files first")
			return
		}
		if !craftWorkbookBackupFresh(r.Context(), db, req.BackupID) {
			replyCode(w, 412, "backup_required", "name a complete instance backup made within the last 2 hours (backup_id) before migrating")
			return
		}
	}
	convert, err := d.faithfulExporter()
	if err != nil {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	pending, err := craftNativeSheetsPending(r.Context(), db)
	if err != nil {
		replyError(w, 500, "the native Sheets could not be listed")
		return
	}
	rows, err := db.QueryContext(r.Context(), `SELECT h.workspace_id, h.page_id, h.latest_version FROM artifact_version_heads h JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=h.latest_version
		WHERE h.content_type='gridcraft' AND json_extract(v.manifest_json,'$.entry_point')='document.json' ORDER BY h.workspace_id, h.page_id LIMIT ?`, req.Limit+1)
	if err != nil {
		replyError(w, 500, "the native Sheets could not be listed")
		return
	}
	type target struct {
		key  artifacts.VersionKey
		head int64
	}
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.key.WorkspaceID, &t.key.PageID, &t.head); err != nil {
			break
		}
		targets = append(targets, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		replyError(w, 500, "the native Sheets could not be listed")
		return
	}
	report := craftWorkbookMigrationReport{DryRun: req.DryRun, Pending: pending, Pages: []craftWorkbookMigrationPage{}}
	if !req.DryRun {
		report.BackupID = req.BackupID
	}
	if len(targets) > req.Limit {
		targets, report.More = targets[:req.Limit], true
	}
	cfg := profile.Store
	store := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft", MaxHistoryBytes: cfg.MaxHistoryBytes, MaxWorkspaceHistoryBytes: cfg.MaxWorkspaceHistoryBytes, MaxVersionBytes: mooring.MaxWorkbookBytes})
	system := func(context.Context, *sql.Tx) (string, error) { return craftWorkbookMigrationActor, nil }
	for _, t := range targets {
		if r.Context().Err() != nil {
			break
		}
		page := craftWorkbookMigrationPage{WorkspaceID: t.key.WorkspaceID, PageID: t.key.PageID, Version: strconv.FormatInt(t.head, 10)}
		fail := func(code string) { page.Status, page.Code = "failed", code; report.Failed++ }
		if !req.DryRun && d.craftSheetBusy(r.Context(), t.key) {
			page.Status = "busy"
			report.Busy++
			report.Pages = append(report.Pages, page)
			continue
		}
		m, err := store.ReadVersion(r.Context(), t.key, t.head, system)
		file, ok := m.Files["document.json"]
		if err != nil || !ok {
			fail("read")
			report.Pages = append(report.Pages, page)
			continue
		}
		page.NativeBytes = strconv.Itoa(len(file.Content))
		ctx, cancel := context.WithTimeout(r.Context(), craftNativeExportDeadline)
		data, err := convert(ctx, file.Content)
		cancel()
		var loss *craftsession.FidelityLossError
		switch {
		case errors.As(err, &loss):
			fail("fidelity_loss")
			page.Losses = loss.Parts
		case err != nil:
			fail("convert")
		case craftWorkbookCheck(data) != nil:
			fail("not_workbook")
		}
		if page.Status == "failed" {
			slog.Default().Warn("native Sheet not migrated", "workspace", t.key.WorkspaceID, "page", t.key.PageID, "code", page.Code)
			report.Pages = append(report.Pages, page)
			continue
		}
		page.WorkbookBytes = strconv.Itoa(len(data))
		if req.DryRun {
			page.Status = "would_migrate"
			report.Pages = append(report.Pages, page)
			continue
		}
		receipt, err := store.Apply(r.Context(), t.key, artifacts.VersionRequest{BaseVersion: t.head, EntryPoint: craftWorkbookMigrationTarget, Message: "Converted to a workbook file",
			Files: map[string]artifacts.VersionFile{craftWorkbookMigrationTarget: {Content: data, MediaType: mooring.WorkbookMediaTypeXLSX}},
			Record: func(ctx context.Context, tx *sql.Tx, v artifacts.VersionReceipt) error {
				return recordCraftSheetSaved(ctx, tx, craftSheetSaved{WorkspaceID: t.key.WorkspaceID, PageID: t.key.PageID, Version: v.Version, Source: craftSheetSavedByMigration, UserID: user.ID, Application: "gridcraft", Native: data})
			}}, system)
		switch {
		case errors.Is(err, artifacts.ErrVersionConflict):
			page.Status = "busy" // saved meanwhile: the next run converts the new head
			report.Busy++
		case err != nil:
			fail("store")
		default:
			page.Status, page.NewVersion = "migrated", strconv.FormatInt(receipt.Version, 10)
			report.Migrated++
		}
		report.Pages = append(report.Pages, page)
	}
	writeJSON(w, 200, report)
}

// craftWorkbookCheck: the export is an xlsx this store keeps.
func craftWorkbookCheck(data []byte) error {
	format, err := mooring.DetectWorkbook(data)
	if err != nil {
		return err
	}
	if format != "xlsx" {
		return mooring.ErrWorkbookUnsupported
	}
	return craftWorkbookValidate(context.Background(), data)
}

// craftWorkbookBackupFresh: id names a complete instance backup made within the window.
func craftWorkbookBackupFresh(ctx context.Context, db *sql.DB, id string) bool {
	if id == "" {
		return false
	}
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM backup_catalog WHERE id=? AND scope='instance' AND incomplete IS NULL AND created_at>=?`, id, time.Now().Add(-craftWorkbookBackupWindow).UTC().Format(time.RFC3339)).Scan(&n)
	return err == nil && n == 1
}

// craftSheetBusy: someone holds the Sheet's edit lease on this boot and it has not expired. A lease
// from an earlier boot is dead.
func (d *craftNativeDocumentsHTTP) craftSheetBusy(ctx context.Context, k artifacts.VersionKey) bool {
	boot, now, err := d.human.clock.Now()
	if err != nil {
		return true
	}
	var n int
	if err = d.human.pages.db.QueryRowContext(ctx, `SELECT count(*) FROM mooring_edit_leases WHERE workspace_id=? AND page_id=? AND boot_id=? AND expires_ns>?`, k.WorkspaceID, k.PageID, boot, int64(now)).Scan(&n); err != nil {
		return true
	}
	return n > 0
}

// craftWorkbookPendingNotice warns, once at start, when this server keeps workbook Sheets and some
// Sheets are still native documents (they open read-only as legacy until migrated). It only reads.
func craftWorkbookPendingNotice(ctx context.Context, db *sql.DB, applications map[string]craftNativeApplication, logger *slog.Logger) {
	if p, ok := applications["gridcraft"]; !ok || p.Store.Format != mooring.NativeFormatWorkbook || db == nil || logger == nil {
		return
	}
	n, err := craftNativeSheetsPending(ctx, db)
	if err != nil || n == 0 {
		return
	}
	logger.Warn("native Sheets wait for migration to workbook files", "pending", n, "next", "crewship craft migrate-workbooks --dry-run")
}
