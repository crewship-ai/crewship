package backup

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrLegacyHeadReceiptExportUnsupported = errors.New("backup: legacy head receipt native metadata export unsupported")

// Transport support only: no authenticity, bootstrap or runtime decision.
func checkLegacyHeadReceiptExportIn(ctx context.Context, tx *sql.Tx, workspace string) error {
	fail := func(err error) error { return errors.Join(ErrLegacyHeadReceiptExportUnsupported, err) }
	rows, err := tx.QueryContext(ctx, `PRAGMA main.table_list`)
	if err != nil {
		return fail(err)
	}
	count := 0
	mainParent := false
	for rows.Next() {
		var schema, name, kind string
		var ncol, wr, strict int
		if err = rows.Scan(&schema, &name, &kind, &ncol, &wr, &strict); err != nil {
			break
		}
		if schema != "main" {
			continue
		}
		if name == "pipelines" && kind == "table" {
			mainParent = true
		}
		if !strings.HasPrefix(strings.ToLower(name), "pipeline_legacy_head_receipt") {
			continue
		}
		if name != "pipeline_legacy_head_receipts" || kind != "table" {
			err = ErrLegacyHeadReceiptExportUnsupported
			break
		}
		count++
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return fail(err)
	}
	if count == 0 {
		var ledgerKind string
		err := tx.QueryRowContext(ctx, `SELECT type FROM main.sqlite_master WHERE name='_migrations'`).Scan(&ledgerKind)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fail(err)
		}
		if ledgerKind != "table" {
			return ErrLegacyHeadReceiptExportUnsupported
		}
		var recorded bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main._migrations WHERE name IN ('pipeline_legacy_head_receipts','legacy_pipeline_zero_heads'))`).Scan(&recorded); err != nil {
			return fail(err)
		}
		if recorded {
			return ErrLegacyHeadReceiptExportUnsupported
		}
		return nil // Structural old absence only; no receipt/key/provenance minted.
	}
	if count != 1 || !mainParent {
		return ErrLegacyHeadReceiptExportUnsupported
	}
	var shadow bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM temp.sqlite_master WHERE lower(name) IN ('pipelines','pipeline_legacy_head_receipts'))`).Scan(&shadow); err != nil {
		return fail(err)
	}
	if shadow {
		return ErrLegacyHeadReceiptExportUnsupported
	}
	expected := map[string]string{"id": "TEXT", "pipeline_id": "TEXT", "head_version": "INTEGER", "definition_json": "TEXT", "definition_hash": "TEXT", "author_crew_id": "TEXT", "author_agent_id": "TEXT", "chat_origin_id": "TEXT", "execution_tier_json": "TEXT", "created_at": "TEXT"}
	rows, err = tx.QueryContext(ctx, `PRAGMA main.table_xinfo(pipeline_legacy_head_receipts)`)
	if err != nil {
		return fail(err)
	}
	seen := 0
	for rows.Next() {
		var cid, nn, pk, hidden int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &nn, &def, &pk, &hidden); err != nil {
			break
		}
		if want, ok := expected[name]; !ok || strings.ToUpper(typ) != want || hidden != 0 {
			err = ErrLegacyHeadReceiptExportUnsupported
			break
		}
		seen++
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return fail(err)
	}
	if seen != len(expected) {
		return ErrLegacyHeadReceiptExportUnsupported
	}
	// Same owning-routine workspace scope as workspaceFilterSQL. No foreign
	// owner/reference scan, scope expansion or binary-parent normalization.
	rows, err = tx.QueryContext(ctx, `SELECT r.id,r.pipeline_id,r.head_version,r.definition_json,r.definition_hash,r.author_crew_id,r.author_agent_id,r.chat_origin_id,r.execution_tier_json,r.created_at,p.id,p.head_version,p.definition_json,p.definition_hash,p.author_crew_id,p.author_agent_id,p.chat_origin_id,p.execution_tier_json,p.workspace_id
 FROM main.pipeline_legacy_head_receipts r JOIN main.pipelines p ON p.id=r.pipeline_id WHERE p.workspace_id=?`, workspace)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		values := make([]any, 19)
		ptr := make([]any, 19)
		for i := range values {
			ptr[i] = &values[i]
		}
		if err = rows.Scan(ptr...); err != nil {
			break
		}
		for i, v := range values {
			switch i {
			case 2, 11:
				n, ok := v.(int64)
				if !ok || (i == 2 && n != 0) {
					err = ErrLegacyHeadReceiptExportUnsupported
				}
			case 5, 6, 7, 8, 14, 15, 16, 17:
				if v == nil {
					continue
				}
				s, ok := v.(string)
				if !ok || !utf8.ValidString(s) {
					err = ErrLegacyHeadReceiptExportUnsupported
				}
			default:
				s, ok := v.(string)
				if !ok || !utf8.ValidString(s) {
					err = ErrLegacyHeadReceiptExportUnsupported
				}
			}
			if err != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return legacyHeadExportError(errors.Join(err, rows.Err(), rows.Close()))
}

func legacyHeadExportError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrLegacyHeadReceiptExportUnsupported, err)
}
