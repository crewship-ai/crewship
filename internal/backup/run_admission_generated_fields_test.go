package backup

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

// Both native PRAGMAs must close before the next query on this one-connection fixture.
func admissionGeneratedColumnKinds(t *testing.T, db *sql.DB, table string, extended bool) map[string]int {
	t.Helper()
	pragma := "table_info"
	if extended {
		pragma = "table_xinfo"
	}
	rows, err := db.Query(`PRAGMA main.` + pragma + `(` + quoteIdent(table) + `)`)
	if err != nil {
		t.Fatalf("SETUP native column metadata: %v", err)
	}
	columns := map[string]int{}
	for rows.Next() {
		var cid, required, primary, hidden int
		var name, kind string
		var defaultValue any
		if extended {
			err = rows.Scan(&cid, &name, &kind, &required, &defaultValue, &primary, &hidden)
		} else {
			err = rows.Scan(&cid, &name, &kind, &required, &defaultValue, &primary)
		}
		if err != nil {
			break
		}
		columns[name] = hidden
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		t.Fatalf("SETUP native column scan/close: %v", err)
	}
	return columns
}

func TestRunAdmissionGeneratedFieldsBeforeCallback(t *testing.T) {
	for _, kind := range []string{"required-format", "opaque-id", "unknown-marker", "receipt-extra"} {
		t.Run(kind, func(t *testing.T) {
			db := admissionGuardDB(t, kind == "receipt-extra")
			table, column, expression, columnType := "pipeline_runs", "admission_required_format", "1", "INTEGER"
			var expected any = int64(1)
			if kind == "opaque-id" {
				column, expression, expected, columnType = "admission_id", "'opaque'", "opaque", "TEXT"
			}
			if kind == "unknown-marker" {
				column = "admission_future"
			}
			if kind == "receipt-extra" {
				table, column = "pipeline_run_admissions", "future_projection"
			}
			admissionGuardExec(t, db, `ALTER TABLE `+quoteIdent(table)+` ADD COLUMN `+quoteIdent(column)+` `+columnType+` GENERATED ALWAYS AS (`+expression+`) VIRTUAL`)
			extended := admissionGeneratedColumnKinds(t, db, table, true)
			ordinary := admissionGeneratedColumnKinds(t, db, table, false)
			if hidden, ok := extended[column]; !ok || hidden != 2 {
				t.Fatalf("SETUP native VIRTUAL column not visible as hidden=2: %v", extended)
			}
			if _, ok := ordinary[column]; ok {
				t.Fatalf("SETUP ordinary table_info did not omit generated column: %v", ordinary)
			}
			if kind != "receipt-extra" {
				var value any
				var storage string
				if err := db.QueryRow(`SELECT `+quoteIdent(column)+`,typeof(`+quoteIdent(column)+`) FROM main.pipeline_runs WHERE id='admission-r'`).Scan(&value, &storage); err != nil || !reflect.DeepEqual(value, expected) || storage != map[bool]string{false: "integer", true: "text"}[kind == "opaque-id"] {
					t.Fatalf("SETUP native generated marker: %v %T/%v %q", err, value, value, storage)
				}
			} else {
				// Keep receipts empty: a real row would independently refuse the target
				// and mask the generated-column schema counterexample.
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM main.pipeline_run_admissions`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("SETUP receipt schema control not empty: %d %v", count, err)
				}
			}
			before := admissionGuardSnapshot(t, db)
			called := false
			dump := &DBDump{WorkspaceID: "admission-w", Tables: map[string][]map[string]any{"workspaces": {{"id": "admission-w", "slug": "admission"}}}}
			_, err := RestoreDumpTxHooks(t.Context(), db, dump, &RestoreDumpHooks{PreInsert: func(_ context.Context, _ *sql.Tx) error { called = true; return nil }})
			if err != nil && !errors.Is(err, ErrRunAdmissionTransportClosed) {
				t.Fatalf("SETUP unexpected restore failure before intended refusal: %v", err)
			}
			if !errors.Is(err, ErrRunAdmissionTransportClosed) {
				if !called {
					t.Fatal("SETUP control callback path was not reached")
				}
				t.Fatalf("missing generated-field refusal before callback: err=%v called=%v", err, called)
			}
			if called || admissionGuardSnapshot(t, db) != before {
				t.Fatal("generated-field refusal changed state or reached callback")
			}
		})
	}
}

func TestRunAdmissionUnrelatedGeneratedFieldCompatibility(t *testing.T) {
	db := admissionGuardDB(t, false)
	admissionGuardExec(t, db, `ALTER TABLE pipeline_runs ADD COLUMN harmless_projection INTEGER GENERATED ALWAYS AS (1) VIRTUAL`)
	if hidden := admissionGeneratedColumnKinds(t, db, "pipeline_runs", true)["harmless_projection"]; hidden != 2 {
		t.Fatalf("SETUP unrelated VIRTUAL column: %d", hidden)
	}
	if _, ok := admissionGeneratedColumnKinds(t, db, "pipeline_runs", false)["harmless_projection"]; ok {
		t.Fatal("SETUP ordinary table_info unexpectedly exposes unrelated generated column")
	}
	before := admissionGuardSnapshot(t, db)
	called := false
	dump := &DBDump{WorkspaceID: "admission-w", Tables: map[string][]map[string]any{"workspaces": {{"id": "admission-w", "slug": "admission"}}}}
	_, err := RestoreDumpTxHooks(t.Context(), db, dump, &RestoreDumpHooks{PreInsert: func(_ context.Context, _ *sql.Tx) error { called = true; return nil }})
	if err != nil || !called || admissionGuardSnapshot(t, db) != before {
		t.Fatalf("unrelated generated column changed legacy compatibility: %v called=%v", err, called)
	}
}
