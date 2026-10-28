package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// Native workbook snapshots and receipts are UTF-8 JSON, so the existing TEXT
// normalizer preserves their bytes. Live intents are excluded separately.
func safeGridCraftRestoreRow(table string, row map[string]any) (map[string]any, error) {
	if table != "gridcraft_heads" && table != "gridcraft_operations" && table != "gridcraft_revisions" && table != "gridcraft_audit" && table != "gridcraft_outbox" {
		return row, nil
	}
	copy := make(map[string]any, len(row))
	for key, value := range row {
		copy[key] = value
	}
	keys := []string{"page_id"}
	if table == "gridcraft_heads" || table == "gridcraft_operations" {
		keys = append(keys, "workspace_id")
	}
	for _, key := range keys {
		if v, ok := row[key].(string); !ok || v == "" {
			return nil, fmt.Errorf("backup: invalid workbook %s", key)
		}
	}
	if table != "gridcraft_operations" {
		column := "rev"
		if table == "gridcraft_heads" {
			column = "head_rev"
		}
		revision, err := gridcraftPositiveInteger(row[column])
		if err != nil {
			return nil, fmt.Errorf("backup: invalid workbook revision")
		}
		copy[column] = revision
	}
	if table == "gridcraft_heads" || table == "gridcraft_revisions" {
		snapshot, ok := row["snapshot"].(string)
		if !ok || len(snapshot) > appengine.MaxSnapshotBytes || !json.Valid([]byte(snapshot)) {
			return nil, fmt.Errorf("backup: invalid workbook snapshot")
		}
	}
	if table == "gridcraft_revisions" || table == "gridcraft_audit" || table == "gridcraft_outbox" {
		return copy, nil
	}
	if table == "gridcraft_heads" {
		generation, err := gridcraftPositiveInteger(row["generation"])
		if err != nil || generation == math.MaxInt64 {
			return nil, fmt.Errorf("backup: invalid workbook generation")
		}
		copy["generation"] = generation + 1
	} else {
		token, err := gridcraftPositiveInteger(row["token"])
		if err != nil {
			return nil, err
		}
		if row["state"] == "applied" {
			raw, ok := row["receipt"].(string)
			if !ok {
				return nil, fmt.Errorf("backup: workbook receipt required")
			}
			var receipt appengine.Receipt
			if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.OperationKey.MutationID != row["mutation_id"] || receipt.OperationKey.AuthorityID != row["authority_id"] || receipt.PayloadHash != row["payload_hash"] || receipt.Outcome != "applied" || receipt.Rev < 1 {
				return nil, fmt.Errorf("backup: invalid workbook receipt")
			}
		} else {
			if token == math.MaxInt64 {
				return nil, fmt.Errorf("backup: invalid workbook fencing token")
			}
			copy["state"], copy["token"], copy["lease_until"], copy["receipt"] = "failed", token+1, int64(0), nil
		}
	}
	return copy, nil
}
func gridcraftPositiveInteger(value any) (int64, error) {
	var n int64
	switch v := value.(type) {
	case int64:
		n = v
	case int:
		n = int64(v)
	case float64:
		// Generic portable JSON decoding produces float64. At this boundary
		// a larger original integer may already have rounded down, so adding
		// one could reissue its old fencing counter. Exact int64/json.Number
		// inputs remain supported; ambiguous floating counters fail closed.
		if v < 1 || v >= 1<<53 || math.Trunc(v) != v {
			return 0, fmt.Errorf("backup: invalid workbook integer")
		}
		n = int64(v)
	case json.Number:
		var err error
		n, err = v.Int64()
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("backup: missing workbook integer")
	}
	if n < 1 {
		return 0, fmt.Errorf("backup: invalid workbook integer")
	}
	return n, nil
}
func remapGridCraft(dump *DBDump, idMap map[string]map[string]string) {
	for _, table := range []string{"gridcraft_heads", "gridcraft_revisions", "gridcraft_operations", "gridcraft_audit", "gridcraft_outbox"} {
		for _, row := range dump.Tables[table] {
			for column, anchor := range map[string]string{"workspace_id": "workspaces", "page_id": "pages"} {
				if old, ok := row[column].(string); ok {
					if mapped := idMap[anchor][old]; mapped != "" {
						row[column] = mapped
					}
				}
			}
		}
	}
	// Receipts remain immutable source provenance. Their original payload hash
	// binds the old workspace/Page, so replay in the fork conflicts. Status must
	// also refuse presenting a source-Page receipt as a fork operation.
}

// A recovered process must never accept a candidate prepared by its source.
// Advance counters atomically and fail closed before SQLite promotes an
// overflowing INTEGER counter to REAL.
func resetRecoveredGridCraft(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='gridcraft_heads')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := fenceGridCraftTarget(ctx, tx, "", "", true); err != nil {
		return err
	}
	return tx.Commit()
}

func fenceGridCraftTarget(ctx context.Context, tx *sql.Tx, workspace, page string, all bool) error {
	filter, args := ` WHERE workspace_id=? AND page_id=?`, []any{workspace, page}
	if all {
		filter, args = "", nil
	}
	for _, entry := range []struct{ table, counter, extra string }{
		{"gridcraft_heads", "generation", ""},
		{"gridcraft_operations", "token", `state!='applied'`},
	} {
		where := filter
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		if entry.extra != "" {
			where += entry.extra + " AND "
		}
		where += `(` + entry.counter + `>=9223372036854775807 OR typeof(` + entry.counter + `)!='integer')`
		var exhausted bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+entry.table+where+`)`, args...).Scan(&exhausted); err != nil {
			return err
		}
		if exhausted {
			return fmt.Errorf("backup: workbook %s counter exhausted", entry.counter)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gridcraft_heads SET generation=generation+1`+filter, args...); err != nil {
		return err
	}
	where := filter
	if where == "" {
		where = " WHERE "
	} else {
		where += " AND "
	}
	_, err := tx.ExecContext(ctx, `UPDATE gridcraft_operations SET state='failed',token=token+1,lease_until=0`+where+`state!='applied'`, args...)
	return err
}

// A portable bundle is a coherent source history, not a patch to a target
// workbook. Refuse collisions before a replace hook can erase target fences.
func rejectGridCraftHistoryCollision(ctx context.Context, q collabRestoreQuerier, dump *DBDump) error {
	if dump == nil || len(dump.Tables["gridcraft_heads"]) == 0 {
		return nil
	}
	for _, row := range dump.Tables["gridcraft_heads"] {
		page, ok := row["page_id"].(string)
		if !ok || page == "" {
			return fmt.Errorf("backup: malformed workbook scope")
		}
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pages WHERE id=?)`, page).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("backup: workbook history merge is not qualified; restore into an empty Page destination or fork")
		}
	}
	return nil
}

// Published workbooks were created privately and published afterward. The
// shipped insertion trigger intentionally still requires a private personal
// Page for ordinary initialization. During authorized restore only, satisfy
// that initial-state guard and put the original audience back in the SAME
// transaction before hooks or commit can expose the Page. Any failure rolls
// back the whole change. Authority triggers remain enabled and monotonic.
func prepareRestoredGridCraftAudience(ctx context.Context, tx *sql.Tx, row map[string]any) (bool, error) {
	var private int
	if err := tx.QueryRowContext(ctx, `SELECT private_audience FROM pages WHERE id=? AND workspace_id=? AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND folder_id IS NULL`, row["page_id"], row["workspace_id"]).Scan(&private); err != nil {
		return false, fmt.Errorf("backup: workbook requires a personal restore Page: %w", err)
	}
	if private == 1 {
		return false, nil
	}
	if private != 0 {
		return false, fmt.Errorf("backup: invalid workbook audience")
	}
	// Both Page updates fire old/new authority triggers: reserve all four
	// increments rather than letting SQLite promote an exhausted epoch to REAL.
	var epoch int64
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT authority_revision,typeof(authority_revision) FROM workspaces WHERE id=?`, row["workspace_id"]).Scan(&epoch, &kind); err != nil {
		return false, err
	}
	if kind != "integer" || epoch > math.MaxInt64-4 {
		return false, fmt.Errorf("backup: workbook restore authority epoch exhausted")
	}
	var exhausted bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind='page' AND scope_id=? AND (revision>? OR typeof(revision)!='integer'))`, row["workspace_id"], row["page_id"], int64(math.MaxInt64-4)).Scan(&exhausted); err != nil {
		return false, err
	}
	if exhausted {
		return false, fmt.Errorf("backup: workbook restore Page authority exhausted")
	}
	res, err := tx.ExecContext(ctx, `UPDATE pages SET private_audience=1 WHERE id=? AND workspace_id=? AND private_audience=0`, row["page_id"], row["workspace_id"])
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, fmt.Errorf("backup: workbook audience preparation did not update one Page")
	}
	return true, nil
}

func finishRestoredGridCraftAudience(ctx context.Context, tx *sql.Tx, row map[string]any) error {
	res, err := tx.ExecContext(ctx, `UPDATE pages SET private_audience=0 WHERE id=? AND workspace_id=? AND private_audience=1`, row["page_id"], row["workspace_id"])
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("backup: original workbook audience was not restored")
	}
	return nil
}
