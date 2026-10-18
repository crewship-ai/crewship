package backup_test

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Native PRAGMA metadata must remain distinct from a TEMP SQL homonym.
// Check unsupported native schemas before creating that homonym, then verify
// that synthetic SQL metadata cannot hide them from the export boundary.
func TestSyntheticReceiptExportNativeMetadataCannotBeShadowed(t *testing.T) {
	for _, kind := range []string{"healthy-empty-fake", "receipt-view-hidden", "unknown-prefix-hidden"} {
		t.Run(kind, func(t *testing.T) {
			source, workspace := syntheticReceiptExportFixture(t, nil)
			baseline, err := backup.DumpWorkspace(t.Context(), source, workspace)
			if err != nil || baseline == nil || len(baseline.Tables["pipeline_legacy_head_receipts"]) != 1 {
				t.Fatalf("SETUP healthy native export: %v", err)
			}
			native := syntheticReceiptExportNativeMetadataForTest(t, source)
			if native["pipeline_legacy_head_receipts"] != "table" || native["pipelines"] != "table" {
				t.Fatalf("SETUP genuine main tables: %v", native)
			}
			before := syntheticReceiptExportState(t, source)
			if kind == "receipt-view-hidden" {
				if _, err = source.Exec(`ALTER TABLE main.pipeline_legacy_head_receipts RENAME TO receipt_metadata_rows; CREATE VIEW main.pipeline_legacy_head_receipts AS SELECT * FROM main.receipt_metadata_rows`); err != nil {
					t.Fatalf("SETUP real receipt view: %v", err)
				}
			}
			if kind == "unknown-prefix-hidden" {
				if _, err = source.Exec(`CREATE TABLE main.pipeline_legacy_head_receipt_future(id TEXT)`); err != nil {
					t.Fatalf("SETUP unknown reserved table: %v", err)
				}
			}
			if kind != "healthy-empty-fake" {
				// Require unsupported native schema refusal before creating the SQL homonym.
				denied, denyErr := backup.DumpWorkspace(t.Context(), source, workspace)
				if !errors.Is(denyErr, backup.ErrLegacyHeadReceiptExportUnsupported) || denied != nil {
					t.Fatalf("SETUP native unsupported schema control not refused: %v", denyErr)
				}
			}
			if _, err = source.Exec(`CREATE TEMP TABLE pragma_table_list("schema" TEXT,name TEXT,type TEXT,ncol INTEGER,wr INTEGER,strict INTEGER)`); err != nil {
				t.Fatalf("SETUP legal metadata homonym: %v", err)
			}
			expectedFakeRows := 0
			if kind != "healthy-empty-fake" {
				if _, err = source.Exec(`INSERT INTO temp.pragma_table_list VALUES('main','pipelines','table',32,0,0),('main','pipeline_legacy_head_receipts','table',10,0,0)`); err != nil {
					t.Fatalf("SETUP fake metadata rows: %v", err)
				}
				expectedFakeRows = 2
			}
			var fakeRows int
			// Confirm ordinary SQL resolves to the TEMP fixture while native PRAGMA does not.
			if err = source.QueryRow(`SELECT COUNT(*) FROM pragma_table_list`).Scan(&fakeRows); err != nil || fakeRows != expectedFakeRows {
				t.Fatalf("SETUP SQL homonym did not resolve to fixture: %d/%d %v", fakeRows, expectedFakeRows, err)
			}
			native = syntheticReceiptExportNativeMetadataForTest(t, source)
			if kind == "receipt-view-hidden" && native["pipeline_legacy_head_receipts"] != "view" {
				t.Fatalf("SETUP native view hidden too: %v", native)
			}
			if kind == "unknown-prefix-hidden" && native["pipeline_legacy_head_receipt_future"] != "table" {
				t.Fatalf("SETUP native extra absent: %v", native)
			}
			if kind == "healthy-empty-fake" && native["pipeline_legacy_head_receipts"] != "table" {
				t.Fatalf("SETUP real receipt table absent: %v", native)
			}
			var broken int
			if err = source.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
				t.Fatalf("SETUP native FK: %d %v", broken, err)
			}
			dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
			if kind == "healthy-empty-fake" {
				if err != nil || dump == nil || len(dump.Tables["pipeline_legacy_head_receipts"]) != 1 {
					t.Fatalf("healthy native export was replaced by fake metadata: %v", err)
				}
			} else if !errors.Is(err, backup.ErrLegacyHeadReceiptExportUnsupported) || dump != nil {
				count := 0
				if dump != nil {
					count = len(dump.Tables["pipeline_legacy_head_receipts"])
				}
				t.Fatalf("native unsupported inventory hidden: dump=%v receipts=%d err=%v", dump != nil, count, err)
			}
			if !reflect.DeepEqual(before, syntheticReceiptExportState(t, source)) {
				t.Fatal("metadata support check changed native source/proof")
			}
		})
	}
}

// Actual PRAGMA statement, all six native columns; CLOSE before another query
// on the fixture's single connection. No SQL-table-valued metadata alias.
func syntheticReceiptExportNativeMetadataForTest(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`PRAGMA main.table_list`)
	if err != nil {
		t.Fatalf("SETUP actual PRAGMA: %v", err)
	}
	out := map[string]string{}
	for rows.Next() {
		var schema, name, kind string
		var columns, withoutRowID, strict int
		if err = rows.Scan(&schema, &name, &kind, &columns, &withoutRowID, &strict); err != nil {
			_ = rows.Close()
			t.Fatalf("SETUP full native six columns: %v", err)
		}
		if schema == "main" {
			out[name] = kind
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		t.Fatalf("SETUP metadata cursor: %v / %v", iterationErr, closeErr)
	}
	return out
}
