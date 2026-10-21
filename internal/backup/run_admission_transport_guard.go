package backup

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Probe main explicitly: TEMP shadows cannot hide a modern or partial target.
func runAdmissionSchemaIn(ctx context.Context, tx *sql.Tx) (modern bool, err error) {
	if tx == nil {
		return false, ErrRunAdmissionTransportClosed
	}
	rows, err := tx.QueryContext(ctx, `SELECT type,name FROM main.sqlite_master WHERE type IN ('table','view') AND lower(name) GLOB 'pipeline_run_admission*'`)
	if err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	count := 0
	for rows.Next() {
		var kind, name string
		if err = rows.Scan(&kind, &name); err != nil {
			break
		}
		if kind != "table" || name != "pipeline_run_admissions" {
			err = ErrRunAdmissionTransportClosed
			break
		}
		count++
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	rows, err = tx.QueryContext(ctx, `PRAGMA main.table_xinfo(pipeline_runs)`)
	if err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	fields := 0
	for rows.Next() {
		var cid, nn, pk, hidden int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &nn, &def, &pk, &hidden); err != nil {
			break
		}
		if strings.HasPrefix(strings.ToLower(name), "admission_") {
			if hidden != 0 || name != "admission_id" && name != "admission_required_format" {
				err = ErrRunAdmissionTransportClosed
				break
			}
			fields++
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	if count == 0 && fields == 0 {
		return false, nil
	}
	if count != 1 || fields != 2 {
		return false, ErrRunAdmissionTransportClosed
	}
	rows, err = tx.QueryContext(ctx, `PRAGMA main.table_xinfo(pipeline_run_admissions)`)
	if err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	columns := map[string]bool{"id": true, "workspace_id": true, "run_id": true, "format": true, "payload": true, "created_at": true}
	seen := 0
	for rows.Next() {
		var cid, nn, pk, hidden int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &nn, &def, &pk, &hidden); err != nil {
			break
		}
		if hidden != 0 || !columns[name] {
			err = ErrRunAdmissionTransportClosed
			break
		}
		seen++
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	if seen != len(columns) {
		return false, ErrRunAdmissionTransportClosed
	}
	return true, nil
}
func rejectRunAdmissionTargetsIn(ctx context.Context, tx *sql.Tx, ids []string) error {
	modern, err := runAdmissionSchemaIn(ctx, tx)
	if err != nil || !modern {
		return err
	}
	for _, id := range ids {
		var protected bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main.pipeline_runs r LEFT JOIN main.pipelines p ON p.id=r.pipeline_id WHERE (r.workspace_id=? OR p.workspace_id=?) AND (r.admission_required_format IS NOT 0 OR r.admission_id IS NOT NULL)) OR EXISTS(SELECT 1 FROM main.pipeline_run_admissions a LEFT JOIN main.pipeline_runs r ON r.id=a.run_id LEFT JOIN main.pipelines p ON p.id=r.pipeline_id WHERE a.workspace_id=? OR r.workspace_id=? OR p.workspace_id=?)`, id, id, id, id, id).Scan(&protected)
		if err != nil {
			return errors.Join(ErrRunAdmissionTransportClosed, err)
		}
		if protected {
			return ErrRunAdmissionTransportClosed
		}
	}
	return nil
}
func runAdmissionSourceModernIn(ctx context.Context, tx *sql.Tx, versions []int) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name='_migrations')`).Scan(&exists); err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	if !exists {
		return false, nil
	}
	var rawFrontier any
	err := tx.QueryRowContext(ctx, `SELECT version FROM main._migrations WHERE name='pipeline_run_admissions'`).Scan(&rawFrontier)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	frontier, ok := rawFrontier.(int64)
	if !ok || frontier <= 0 {
		return false, ErrRunAdmissionTransportClosed
	}
	for _, v := range versions {
		if int64(v) >= frontier {
			return true, nil
		}
	}
	return false, nil
}
func runAdmissionRestorePreflightIn(ctx context.Context, tx *sql.Tx, d *DBDump, versions []int) error {
	if err := rejectRunAdmissionInput(d, false); err != nil {
		return err
	}
	modern, err := runAdmissionSchemaIn(ctx, tx)
	if err != nil {
		return err
	}
	claimedModern, err := runAdmissionSourceModernIn(ctx, tx, versions)
	if err != nil {
		return err
	}
	if err := rejectRunAdmissionInput(d, claimedModern); err != nil {
		return err
	}
	if (claimedModern || admissionInputModern(d)) && !modern {
		return ErrRunAdmissionTransportClosed
	}
	if d == nil || !modern {
		return nil
	}
	ids := map[string]bool{}
	if d.WorkspaceID != "" {
		ids[d.WorkspaceID] = true
	}
	for _, row := range d.Tables["workspaces"] {
		id, _ := row["id"].(string)
		slug, _ := row["slug"].(string)
		if id == "" && slug == "" {
			continue
		}
		resolved, err := runAdmissionMainWorkspaceIDsIn(ctx, tx, id, slug)
		if err != nil {
			return errors.Join(ErrRunAdmissionTransportClosed, err)
		}
		for _, id := range resolved {
			ids[id] = true
		}
	}
	for _, row := range d.Tables["pipelines"] {
		if id, ok := row["id"]; ok && id != nil {
			var rawOwner any
			err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM main.pipelines WHERE id=?`, id).Scan(&rawOwner)
			if err == nil {
				owner, ok := rawOwner.(string)
				if !ok || owner == "" {
					return ErrRunAdmissionTransportClosed
				}
				ids[owner] = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return errors.Join(ErrRunAdmissionTransportClosed, err)
			}
		}
	}
	for _, row := range d.Tables["pipeline_runs"] {
		if id, ok := row["id"]; ok && id != nil {
			var rawOwner any
			err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM main.pipeline_runs WHERE id=?`, id).Scan(&rawOwner)
			if err == nil {
				owner, ok := rawOwner.(string)
				if !ok || owner == "" {
					return ErrRunAdmissionTransportClosed
				}
				ids[owner] = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return errors.Join(ErrRunAdmissionTransportClosed, err)
			}
		}
	}
	for id := range ids {
		if err := rejectRunAdmissionTargetsIn(ctx, tx, []string{id}); err != nil {
			return err
		}
	}
	return nil
}

// Check raw payload and local target before service planning, remapping, or callbacks.
// Manifest versions can strengthen a refusal; they never authenticate the source.
func runAdmissionBundlePreflight(ctx context.Context, db *sql.DB, d *DBDump, versions []int, resumeWorkspaceID string) (err error) {
	if err = rejectRunAdmissionInput(d, false); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	defer func() {
		if cleanup := tx.Rollback(); cleanup != nil && !errors.Is(cleanup, sql.ErrTxDone) {
			err = errors.Join(err, ErrRunAdmissionTransportClosed, cleanup)
		}
	}()
	if err := runAdmissionRestorePreflightIn(ctx, tx, d, versions); err != nil {
		return err
	}
	if resumeWorkspaceID != "" {
		return rejectRunAdmissionTargetsIn(ctx, tx, []string{resumeWorkspaceID})
	}
	return nil
}

// Negative-only discovery: TEMP workspaces must not hide a protected main target.
// Existing restore provenance, destination validation and deletion resolution stay separate.
func runAdmissionMainWorkspaceIDsIn(ctx context.Context, tx *sql.Tx, id, slug string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM main.workspaces WHERE id=? OR (?<>'' AND slug=?)`, id, slug, slug)
	if err != nil {
		return nil, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	ids := []string{}
	for rows.Next() {
		var raw any
		if err = rows.Scan(&raw); err != nil {
			break
		}
		v, ok := raw.(string)
		if !ok || v == "" {
			err = ErrRunAdmissionTransportClosed
			break
		}
		ids = append(ids, v)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, errors.Join(ErrRunAdmissionTransportClosed, err)
	}
	return ids, nil
}
func rejectRunAdmissionReplaceIn(ctx context.Context, tx *sql.Tx, id, slug string) error {
	ids, err := runAdmissionMainWorkspaceIDsIn(ctx, tx, id, slug)
	if err != nil {
		return err
	}
	return rejectRunAdmissionTargetsIn(ctx, tx, ids)
}
