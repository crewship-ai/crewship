package backup

// Persisted restore reports (restore_reports). RestoreBackup builds a
// detailed report — rows inserted, dropped crew files, clamped credential
// tiers, dropped columns, missing attachment files, row-count shortfalls —
// and the admin UI used to show it once and throw it away. Every restore,
// dry run and drill is now written down with that report, so "what did that
// restore skip?" has an answer after the dialog is closed.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Restore report kinds (restore_reports.kind).
const (
	RestoreKindRestore = "restore"
	RestoreKindDryRun  = "dry_run"
	RestoreKindDrill   = "drill"
)

// Restore results (restore_reports.result, backup_catalog.drill_result).
const (
	RestoreResultOK      = "ok"
	RestoreResultPartial = "partial"
	RestoreResultFailed  = "failed"
)

// RestoreReport is one row of restore_reports.
type RestoreReport struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	ActorUserID string          `json:"actor_user_id"`
	BundlePath  string          `json:"bundle_path"`
	Target      string          `json:"target"`
	Result      string          `json:"result"`
	Report      json.RawMessage `json:"report"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ClassifyRestore reduces a RestoreBackup outcome to ok | partial | failed.
// failed: the restore returned an error (including a no-op restore, which
// landed nothing). partial: it committed (or, on a dry run, would commit)
// but the report names something skipped, missing or lowered — crew files
// not landed, credential tiers clamped, columns dropped, payload or insert
// row counts short, attachment files missing or in conflict, or any gap the
// bundle itself recorded. Everything else is ok.
func ClassifyRestore(res *RestoreResult, err error) string {
	if err != nil || res == nil {
		return RestoreResultFailed
	}
	switch {
	case len(res.DroppedCrewFilesystems) > 0,
		res.SecurityLevelClamped > 0,
		res.ColumnsDropped > 0,
		len(res.PayloadRowCountMismatches) > 0,
		len(res.RowsInsertedShortfalls) > 0,
		res.AttachmentsMissing > 0,
		res.AttachmentsConflicts > 0,
		len(res.Incomplete) > 0:
		return RestoreResultPartial
	}
	// An environment rebuilt or skipped did not come back as captured.
	for _, e := range res.Environments {
		if e.Result != EnvRestored {
			return RestoreResultPartial
		}
	}
	return RestoreResultOK
}

// RecordRestoreReport inserts r. ID and CreatedAt are filled when empty; an
// empty Report is stored as {}.
func RecordRestoreReport(ctx context.Context, db *sql.DB, r RestoreReport) (RestoreReport, error) {
	if db == nil {
		return r, errors.New("backup: record restore report: no database")
	}
	switch r.Kind {
	case RestoreKindRestore, RestoreKindDryRun, RestoreKindDrill:
	default:
		return r, fmt.Errorf("backup: restore report kind %q is not restore, dry_run or drill", r.Kind)
	}
	switch r.Result {
	case RestoreResultOK, RestoreResultPartial, RestoreResultFailed:
	default:
		return r, fmt.Errorf("backup: restore report result %q is not ok, partial or failed", r.Result)
	}
	if r.ID == "" {
		id, err := newCatalogID()
		if err != nil {
			return r, err
		}
		r.ID = "rr_" + id[len("bk_"):]
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if len(r.Report) == 0 {
		r.Report = json.RawMessage("{}")
	}
	if !json.Valid(r.Report) {
		return r, errors.New("backup: restore report is not valid JSON")
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO restore_reports (id, kind, actor_user_id, bundle_path, target, result, report, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Kind, nullableString(r.ActorUserID), r.BundlePath, r.Target, r.Result, string(r.Report),
		tsformat.Format(r.CreatedAt)); err != nil {
		return r, fmt.Errorf("backup: record restore report: %w", err)
	}
	return r, nil
}

// ListRestoreReports returns the newest reports first, at most limit
// (1..500; out of range means 100).
func ListRestoreReports(ctx context.Context, db *sql.DB, limit int) ([]RestoreReport, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, kind, COALESCE(actor_user_id, ''), bundle_path, target, result, report, created_at
FROM restore_reports ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("backup: list restore reports: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []RestoreReport{}
	for rows.Next() {
		var r RestoreReport
		var report, created string
		if err := rows.Scan(&r.ID, &r.Kind, &r.ActorUserID, &r.BundlePath, &r.Target, &r.Result, &report, &created); err != nil {
			return nil, fmt.Errorf("backup: scan restore report: %w", err)
		}
		r.Report = json.RawMessage(report)
		t, err := time.Parse(time.RFC3339Nano, created) // tsformat:allow: parses tsformat.Format output and legacy rows
		if err != nil {
			return nil, fmt.Errorf("backup: parse restore report created_at %q: %w", created, err)
		}
		r.CreatedAt = t
		out = append(out, r)
	}
	return out, rows.Err()
}
